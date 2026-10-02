/*
Copyright 2026 Ahmad Masry.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/telemetry"
)

const (
	requeueAfterUserFix = 30 * time.Second
	resyncPeriod        = 10 * time.Minute

	indexSecretName     = "spec.environmentSecretRef.name"
	indexConfigMapNames = "spec.runner.configMapNames"
)

// ClaudeEnvironmentReconciler reconciles a ClaudeEnvironment object.
type ClaudeEnvironmentReconciler struct {
	client.Client
	// Reader is an uncached reader for deletion decisions a lagging informer
	// must not make.
	Reader   client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// HookImage is the operator's own image, used by the init container that
	// installs the spawn-runner hook into orchestrator pods.
	HookImage string
	// Clock is the time source; nil means time.Now.
	Clock Clock

	mu sync.RWMutex // guards HookImage after construction
}

func (r *ClaudeEnvironmentReconciler) now() time.Time { return tick(r.Clock) }

// SetHookImage replaces the hook image at runtime; tests use it.
func (r *ClaudeEnvironmentReconciler) SetHookImage(image string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.HookImage = image
}

func (r *ClaudeEnvironmentReconciler) hookImage() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.HookImage
}

// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one ClaudeEnvironment towards its desired state.
func (r *ClaudeEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := telemetry.StartSpan(ctx, "claudeenvironment.reconcile",
		attribute.String("k8s.namespace.name", req.Namespace), attribute.String("environment", req.Name))
	defer span.End()
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			metrics.ForgetEnvironment(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !env.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	before := env.Status.DeepCopy()
	pass := newStatusPass(env)
	res, err := r.reconcile(ctx, env, pass)
	pass.finish()

	if !meta.IsStatusConditionTrue(before.Conditions, selfhostedv1alpha1.ConditionFleetAvailable) &&
		meta.IsStatusConditionTrue(env.Status.Conditions, selfhostedv1alpha1.ConditionFleetAvailable) {
		r.Recorder.Event(env, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonFleetAvailable, "runner fleet is available")
	}

	if !equality.Semantic.DeepEqual(before, &env.Status) {
		if uerr := r.Status().Update(ctx, env); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(uerr)
		}
	}
	pass.emit()
	metrics.RecordEnvironment(env)
	return res, err
}

func (r *ClaudeEnvironmentReconciler) reconcile(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) (ctrl.Result, error) {
	secret, ok, err := r.resolveSecret(ctx, env, pass)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, nil
	}
	configMaps, ok, err := r.resolveConfigMaps(ctx, env, pass)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, nil
	}

	env.Status.ComputedDrainBudgetSeconds = builders.DrainBudgetSeconds(env.Spec.Runner.Settings)
	if _, tooShort := builders.EffectiveGracePeriod(env); tooShort {
		msg := fmt.Sprintf("terminationGracePeriodSeconds %d is below the computed drain budget of %d seconds",
			*env.Spec.Runner.TerminationGracePeriodSeconds, env.Status.ComputedDrainBudgetSeconds)
		pass.degradeOnce(r.Recorder, env, selfhostedv1alpha1.ReasonGracePeriodTooShort, msg)
	}

	if env.Spec.OnDemand != nil {
		env.Status.Mode = "onDemand"
		env.Status.Fixed = nil
		res, err := r.reconcileOnDemand(ctx, env, pass, secret)
		return r.withNetworkPolicy(ctx, env, res, err)
	}
	env.Status.Mode = "fixed"
	env.Status.OnDemand = nil
	res, err := r.reconcileFixed(ctx, env, pass, secret, configMaps)
	return r.withNetworkPolicy(ctx, env, res, err)
}

// withNetworkPolicy reconciles the egress policy once the mode branch has
// succeeded, passing the branch's result through.
func (r *ClaudeEnvironmentReconciler) withNetworkPolicy(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, res ctrl.Result, err error) (ctrl.Result, error) {
	if err != nil {
		return res, err
	}
	return res, r.reconcileNetworkPolicy(ctx, env)
}

func (r *ClaudeEnvironmentReconciler) reconcileNetworkPolicy(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	np := env.Spec.Runner.NetworkPolicy
	obj := builders.EnvironmentNetworkPolicy(env)
	if np == nil || !np.Enabled {
		return r.deleteIfOwned(ctx, env, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: obj.Name, Namespace: obj.Namespace}})
	}
	return r.apply(ctx, env, obj)
}

func (r *ClaudeEnvironmentReconciler) resolveSecret(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) (*corev1.Secret, bool, error) {
	ref := env.Spec.EnvironmentSecretRef
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: env.Namespace}, secret)
	switch {
	case apierrors.IsNotFound(err):
		msg := fmt.Sprintf("Secret %q not found", ref.Name)
		pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonSecretMissing, msg)
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	if len(secret.Data[ref.Key]) == 0 {
		msg := fmt.Sprintf("Secret %q has no key %q", ref.Name, ref.Key)
		pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretKeyMissing, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonSecretKeyMissing, msg)
		return nil, false, nil
	}
	pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionTrue, selfhostedv1alpha1.ReasonSecretFound, "")
	return secret, true, nil
}

// configMapRefs lists the ConfigMaps a runner spec references, with the key a
// wrapper script must contain ("" when any content is fine).
func configMapRefs(r selfhostedv1alpha1.RunnerSpec) map[string]string {
	refs := map[string]string{}
	add := func(name, key string) {
		if existing, ok := refs[name]; ok && existing != "" {
			return
		}
		refs[name] = key
	}
	if r.LifecycleHooks != nil {
		add(r.LifecycleHooks.Name, "")
	}
	if r.WrapperScript != nil {
		add(r.WrapperScript.Name, r.WrapperScript.Key)
	}
	if r.HostConfig != nil {
		add(r.HostConfig.Name, "")
	}
	return refs
}

func (r *ClaudeEnvironmentReconciler) resolveConfigMaps(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) ([]*corev1.ConfigMap, bool, error) {
	var out []*corev1.ConfigMap
	ok := true
	refs := configMapRefs(env.Spec.Runner)
	names := make([]string, 0, len(refs))
	for n := range refs {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		requiredKey := refs[name]
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: env.Namespace}, cm)
		switch {
		case apierrors.IsNotFound(err):
			msg := fmt.Sprintf("ConfigMap %q not found", name)
			pass.degradeOnce(r.Recorder, env, selfhostedv1alpha1.ReasonConfigMapMissing, msg)
			ok = false
			continue
		case err != nil:
			return nil, false, err
		}
		if requiredKey != "" {
			if _, has := cm.Data[requiredKey]; !has {
				if _, hasBin := cm.BinaryData[requiredKey]; !hasBin {
					msg := fmt.Sprintf("ConfigMap %q has no key %q", name, requiredKey)
					pass.degradeOnce(r.Recorder, env, selfhostedv1alpha1.ReasonConfigMapMissing, msg)
					ok = false
					continue
				}
			}
		}
		out = append(out, cm)
	}
	return out, ok, nil
}

func (r *ClaudeEnvironmentReconciler) reconcileFixed(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, secret *corev1.Secret, configMaps []*corev1.ConfigMap) (ctrl.Result, error) {
	hash := builders.ConfigHash(env, secret, configMaps)
	pass.set(selfhostedv1alpha1.ConditionSecretOnRunners, metav1.ConditionTrue, selfhostedv1alpha1.ReasonFixedModeSecretOnPods,
		"fixed mode mounts the environment secret on every runner pod; prefer onDemand for production")

	desired := int32(1)
	if env.Spec.Fixed.Replicas != nil {
		desired = *env.Spec.Fixed.Replicas
	}

	var available bool
	if env.Spec.Fixed.PersistentWorkspace != nil {
		sts := builders.FixedStatefulSet(env, hash)
		if err := r.apply(ctx, env, sts); err != nil {
			env.Status.Fixed = nil
			return ctrl.Result{}, r.applyFailed(env, pass, "StatefulSet", err)
		}
		if err := r.deleteIfOwned(ctx, env, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: sts.Name, Namespace: sts.Namespace}}); err != nil {
			return ctrl.Result{}, err
		}
		available = sts.Status.ObservedGeneration == sts.Generation && sts.Status.ReadyReplicas >= desired
		env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: sts.Status.Replicas, ReadyReplicas: sts.Status.ReadyReplicas, UpdatedReplicas: sts.Status.UpdatedReplicas}
	} else {
		dep := builders.FixedDeployment(env, hash)
		if err := r.apply(ctx, env, dep); err != nil {
			env.Status.Fixed = nil
			return ctrl.Result{}, r.applyFailed(env, pass, "Deployment", err)
		}
		if err := r.deleteIfOwned(ctx, env, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: dep.Name, Namespace: dep.Namespace}}); err != nil {
			return ctrl.Result{}, err
		}
		available = deploymentAvailable(dep)
		env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: dep.Status.Replicas, ReadyReplicas: dep.Status.ReadyReplicas, UpdatedReplicas: dep.Status.UpdatedReplicas}
	}

	if err := r.deleteOrchestratorObjects(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	if available {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionTrue, selfhostedv1alpha1.ReasonWorkloadAvailable, "")
	} else {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkloadUnavailable, "runner workload is not yet available")
	}

	if err := r.checkRunnerPods(ctx, env, pass); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}

func (r *ClaudeEnvironmentReconciler) reconcileOnDemand(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, secret *corev1.Secret) (ctrl.Result, error) {
	pass.set(selfhostedv1alpha1.ConditionSecretOnRunners, metav1.ConditionFalse, selfhostedv1alpha1.ReasonOnDemandSecretOnOrchestrator,
		"the environment secret is mounted only on the orchestrator; runners receive single-use work orders")

	name := builders.FixedWorkloadName(env)
	for _, stale := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace}},
	} {
		if err := r.deleteIfOwned(ctx, env, stale); err != nil {
			return ctrl.Result{}, err
		}
	}

	if r.hookImage() == "" {
		msg := "operator has no hook image configured; set --hook-image or OPERATOR_IMAGE on the manager"
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonHookImageUnset, msg)
		pass.degradeOnce(r.Recorder, env, selfhostedv1alpha1.ReasonHookImageUnset, msg)
		env.Status.OnDemand = nil
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, nil
	}

	if err := r.applyOrchestratorRBAC(ctx, env, pass); err != nil {
		env.Status.OnDemand = nil
		return ctrl.Result{}, err
	}
	dep := builders.OrchestratorDeployment(env, builders.OrchestratorConfigHash(env, secret, r.hookImage()), r.hookImage())
	if err := r.apply(ctx, env, dep); err != nil {
		env.Status.OnDemand = nil
		return ctrl.Result{}, r.applyFailed(env, pass, "Deployment", err)
	}

	if deploymentAvailable(dep) {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionTrue, selfhostedv1alpha1.ReasonWorkloadAvailable, "")
	} else {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonOrchestratorUnavailable, "orchestrator deployment is not yet available")
	}

	counts, err := r.countRunners(ctx, env)
	if err != nil {
		return ctrl.Result{}, err
	}
	counts.OrchestratorReadyReplicas = dep.Status.ReadyReplicas
	env.Status.OnDemand = &counts
	if err := r.sweepOrphanedWorkOrders(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}

// sweepOrphanedWorkOrders deletes work-order Secrets that no ClaudeRunner
// references once the spawn deadline has passed, so a hook crash between its
// two creates cannot leave a live JWT behind. Only Secrets the hook made for
// this environment qualify: both operator labels, the work-order name suffix
// and the environment as controller owner. The environment key Secret is never
// deleted, whatever it is named or labelled.
func (r *ClaudeEnvironmentReconciler) sweepOrphanedWorkOrders(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(env.Namespace),
		client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}, client.HasLabels{selfhostedv1alpha1.LabelOrderID}); err != nil {
		return err
	}
	runners := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := r.List(ctx, runners, client.InNamespace(env.Namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}); err != nil {
		return err
	}
	referenced := map[string]bool{}
	for i := range runners.Items {
		referenced[runners.Items[i].Spec.WorkOrderSecretRef.Name] = true
	}
	deadline := time.Duration(env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds) * time.Second
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if s.Name == env.Spec.EnvironmentSecretRef.Name || !strings.HasSuffix(s.Name, selfhostedv1alpha1.WorkOrderSecretSuffix) ||
			!metav1.IsControlledBy(s, env) || referenced[s.Name] {
			continue
		}
		if r.now().Sub(s.CreationTimestamp.Time) < deadline {
			continue
		}
		// The cached runner list can lag a redelivery that reused this Secret,
		// so confirm against the API server that no ClaudeRunner (named after
		// the order ID) exists before deleting.
		key := types.NamespacedName{Name: strings.TrimSuffix(s.Name, selfhostedv1alpha1.WorkOrderSecretSuffix), Namespace: s.Namespace}
		err := r.Reader.Get(ctx, key, &selfhostedv1alpha1.ClaudeRunner{})
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		// The preconditions keep a stale cached copy from deleting a Secret
		// that was recreated or handed to a ClaudeRunner since.
		if err := r.Delete(ctx, s, client.Preconditions{UID: &s.UID, ResourceVersion: &s.ResourceVersion}); err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		r.Recorder.Event(env, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonOrphanedWorkOrderDeleted, "deleted unreferenced work-order Secret "+s.Name)
	}
	return nil
}

func (r *ClaudeEnvironmentReconciler) applyOrchestratorRBAC(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) error {
	for _, obj := range []client.Object{
		builders.OrchestratorServiceAccount(env), builders.OrchestratorRole(env), builders.OrchestratorRoleBinding(env),
	} {
		if err := r.apply(ctx, env, obj); err != nil {
			return r.applyFailed(env, pass, obj.GetObjectKind().GroupVersionKind().Kind, err)
		}
	}
	return nil
}

// deploymentAvailable reports whether the Deployment's current generation is Available.
func deploymentAvailable(dep *appsv1.Deployment) bool {
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return dep.Status.ObservedGeneration == dep.Generation
		}
	}
	return false
}

func (r *ClaudeEnvironmentReconciler) countRunners(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) (selfhostedv1alpha1.OnDemandStatus, error) {
	list := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := r.List(ctx, list, client.InNamespace(env.Namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}); err != nil {
		return selfhostedv1alpha1.OnDemandStatus{}, err
	}
	var st selfhostedv1alpha1.OnDemandStatus
	for i := range list.Items {
		switch list.Items[i].Status.Phase {
		case selfhostedv1alpha1.RunnerRunning:
			st.RunningRunners++
		case "", selfhostedv1alpha1.RunnerPending:
			st.PendingRunners++
		}
	}
	return st, nil
}

// deleteOrchestratorObjects removes on-demand objects after a switch to fixed mode.
func (r *ClaudeEnvironmentReconciler) deleteOrchestratorObjects(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	name := builders.OrchestratorName(env)
	om := metav1.ObjectMeta{Name: name, Namespace: env.Namespace}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: om}, &rbacv1.RoleBinding{ObjectMeta: om}, &rbacv1.Role{ObjectMeta: om}, &corev1.ServiceAccount{ObjectMeta: om},
	} {
		if err := r.deleteIfOwned(ctx, env, obj); err != nil {
			return err
		}
	}
	return nil
}

// applyFailed marks the fleet unavailable when the runner workload could not be
// applied, so a stale FleetAvailable=True never reports the new generation as
// Ready. The error is returned for a backoff requeue. The message carries the
// API error only: the workload objects reference the Secret by name and never
// hold its value.
func (r *ClaudeEnvironmentReconciler) applyFailed(env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, kind string, err error) error {
	msg := fmt.Sprintf("could not apply %s (%s): %s", kind, apierrors.ReasonForError(err), err.Error())
	pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkloadApplyFailed, msg)
	r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonWorkloadApplyFailed, msg)
	return err
}

// checkRunnerPods flags a fleet whose runners exit right after starting.
func (r *ClaudeEnvironmentReconciler) checkRunnerPods(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) error {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(env.Namespace), client.MatchingLabels(builders.RunnerSelectorLabels(env))); err != nil {
		return err
	}
	if failed, msg := detectFailedStart(pods.Items, r.now()); failed {
		pass.degradeOnce(r.Recorder, env, selfhostedv1alpha1.ReasonRunnerFailedStart, msg)
	}
	return nil
}

// apply server-side-applies obj with the environment as controller owner. The
// object is updated in place with the server's response, including status.
func (r *ClaudeEnvironmentReconciler) apply(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, obj client.Object) error {
	if err := controllerutil.SetControllerReference(env, obj, r.Scheme); err != nil {
		return err
	}
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	existing := obj.DeepCopyObject().(client.Object)
	notFound := apierrors.IsNotFound(r.Get(ctx, client.ObjectKeyFromObject(obj), existing))
	//nolint:staticcheck // typed-object server-side apply; the replacement API needs generated apply configurations
	if err := r.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner(selfhostedv1alpha1.FieldOwner)); err != nil {
		return err
	}
	if notFound {
		r.Recorder.Event(env, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonCreated, fmt.Sprintf("created %s %s", kind, obj.GetName()))
	}
	return nil
}

// deleteIfOwned removes a stale workload left behind by a mode switch.
func (r *ClaudeEnvironmentReconciler) deleteIfOwned(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, obj client.Object) error {
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(obj, env) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

// CacheByObject restricts the manager's Pod informer to pods carrying an
// operator role label, and its ServiceAccount, Role, RoleBinding and NetworkPolicy informers
// to objects carrying the operator's part-of label, so these watches do not
// cache every such object in the cluster. Secrets and ConfigMaps stay
// unfiltered because users name them.
func CacheByObject() (map[client.Object]cache.ByObject, error) {
	role, err := labels.NewRequirement(selfhostedv1alpha1.LabelRole, selection.In,
		[]string{selfhostedv1alpha1.RoleRunner, selfhostedv1alpha1.RoleOrchestrator})
	if err != nil {
		return nil, err
	}
	partOf, err := labels.NewRequirement(selfhostedv1alpha1.LabelPartOf, selection.Equals, []string{selfhostedv1alpha1.PartOfValue})
	if err != nil {
		return nil, err
	}
	operatorOwned := cache.ByObject{Label: labels.NewSelector().Add(*partOf)}
	return map[client.Object]cache.ByObject{
		&corev1.Pod{}:                 {Label: labels.NewSelector().Add(*role)},
		&corev1.ServiceAccount{}:      operatorOwned,
		&rbacv1.Role{}:                operatorOwned,
		&rbacv1.RoleBinding{}:         operatorOwned,
		&networkingv1.NetworkPolicy{}: operatorOwned,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClaudeEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &selfhostedv1alpha1.ClaudeEnvironment{}, indexSecretName, func(o client.Object) []string {
		return []string{o.(*selfhostedv1alpha1.ClaudeEnvironment).Spec.EnvironmentSecretRef.Name}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &selfhostedv1alpha1.ClaudeEnvironment{}, indexConfigMapNames, func(o client.Object) []string {
		refs := configMapRefs(o.(*selfhostedv1alpha1.ClaudeEnvironment).Spec.Runner)
		names := make([]string, 0, len(refs))
		for n := range refs {
			names = append(names, n)
		}
		return names
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfhostedv1alpha1.ClaudeEnvironment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&selfhostedv1alpha1.ClaudeRunner{}, builder.WithPredicates(runnerPhaseChanged())).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.environmentsReferencing(indexSecretName))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.environmentsReferencing(indexConfigMapNames))).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			name, ok := o.GetLabels()[selfhostedv1alpha1.LabelEnvironment]
			if !ok || o.GetLabels()[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: o.GetNamespace()}}}
		}), builder.WithPredicates(podStatusChanged())).
		Named("claudeenvironment").
		Complete(r)
}

func (r *ClaudeEnvironmentReconciler) environmentsReferencing(index string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := &selfhostedv1alpha1.ClaudeEnvironmentList{}
		if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace()), client.MatchingFields{index: obj.GetName()}); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	}
}
