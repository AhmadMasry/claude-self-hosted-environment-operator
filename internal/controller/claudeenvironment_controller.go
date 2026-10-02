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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"
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
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one ClaudeEnvironment towards its desired state.
func (r *ClaudeEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	if !equality.Semantic.DeepEqual(before, &env.Status) {
		if uerr := r.Status().Update(ctx, env); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(uerr)
		}
	}
	metrics.RecordEnvironment(env)
	return res, err
}

func (r *ClaudeEnvironmentReconciler) reconcile(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) (ctrl.Result, error) {
	secret, ok, err := r.resolveSecret(ctx, env, pass)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, err
	}
	configMaps, ok, err := r.resolveConfigMaps(ctx, env, pass)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, err
	}

	env.Status.ComputedDrainBudgetSeconds = builders.DrainBudgetSeconds(env.Spec.Runner.Settings)
	if _, tooShort := builders.EffectiveGracePeriod(env); tooShort {
		msg := fmt.Sprintf("terminationGracePeriodSeconds %d is below the computed drain budget of %d seconds",
			*env.Spec.Runner.TerminationGracePeriodSeconds, env.Status.ComputedDrainBudgetSeconds)
		pass.degrade(selfhostedv1alpha1.ReasonGracePeriodTooShort, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonGracePeriodTooShort, msg)
	}

	if env.Spec.OnDemand != nil {
		env.Status.Mode = "onDemand"
		env.Status.Fixed = nil
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonUnsupportedMode, "")
		pass.degrade(selfhostedv1alpha1.ReasonUnsupportedMode, "onDemand mode is not implemented in this operator version")
		return ctrl.Result{}, nil
	}
	env.Status.Mode = "fixed"
	env.Status.OnDemand = nil
	return r.reconcileFixed(ctx, env, pass, secret, configMaps)
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
	if r.LifecycleHooks != nil {
		refs[r.LifecycleHooks.Name] = ""
	}
	if r.WrapperScript != nil {
		refs[r.WrapperScript.Name] = r.WrapperScript.Key
	}
	if r.HostConfig != nil {
		refs[r.HostConfig.Name] = ""
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
			pass.degrade(selfhostedv1alpha1.ReasonConfigMapMissing, msg)
			r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonConfigMapMissing, msg)
			ok = false
			continue
		case err != nil:
			return nil, false, err
		}
		if requiredKey != "" {
			if _, has := cm.Data[requiredKey]; !has {
				if _, hasBin := cm.BinaryData[requiredKey]; !hasBin {
					msg := fmt.Sprintf("ConfigMap %q has no key %q", name, requiredKey)
					pass.degrade(selfhostedv1alpha1.ReasonConfigMapMissing, msg)
					r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonConfigMapMissing, msg)
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
			return ctrl.Result{}, r.applyFailed(env, pass, "Deployment", err)
		}
		if err := r.deleteIfOwned(ctx, env, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: dep.Name, Namespace: dep.Namespace}}); err != nil {
			return ctrl.Result{}, err
		}
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				available = dep.Status.ObservedGeneration == dep.Generation
			}
		}
		env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: dep.Status.Replicas, ReadyReplicas: dep.Status.ReadyReplicas, UpdatedReplicas: dep.Status.UpdatedReplicas}
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

// applyFailed marks the fleet unavailable when the runner workload could not be
// applied, so a stale FleetAvailable=True never reports the new generation as
// Ready. The error is returned for a backoff requeue. The message carries the
// API error only: the workload objects reference the Secret by name and never
// hold its value.
func (r *ClaudeEnvironmentReconciler) applyFailed(env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, kind string, err error) error {
	msg := fmt.Sprintf("could not apply runner %s (%s): %s", kind, apierrors.ReasonForError(err), err.Error())
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
	if failed, msg := detectFailedStart(pods.Items, time.Now()); failed {
		pass.degrade(selfhostedv1alpha1.ReasonRunnerFailedStart, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonRunnerFailedStart, msg)
	}
	return nil
}

// apply server-side-applies obj with the environment as controller owner. The
// object is updated in place with the server's response, including status.
func (r *ClaudeEnvironmentReconciler) apply(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, obj client.Object) error {
	if err := controllerutil.SetControllerReference(env, obj, r.Scheme); err != nil {
		return err
	}
	//nolint:staticcheck // typed-object server-side apply; the replacement API needs generated apply configurations
	return r.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner(selfhostedv1alpha1.FieldOwner))
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
