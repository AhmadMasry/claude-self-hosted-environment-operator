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
	"time"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/telemetry"
)

const (
	defaultRunnerTTL    = 300 * time.Second
	podDeletionPollWait = 2 * time.Second
	workOrderPollWait   = 2 * time.Second
)

// ClaudeRunnerReconciler turns one ClaudeRunner into one single-session pod.
type ClaudeRunnerReconciler struct {
	client.Client
	// Reader is an uncached reader for decisions a lagging informer must not
	// make: whether the work-order Secret exists and whether the pod is gone.
	Reader   client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// Clock is the time source; nil means time.Now.
	Clock Clock
}

func (r *ClaudeRunnerReconciler) now() time.Time { return tick(r.Clock) }

// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners/finalizers,verbs=update
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one ClaudeRunner: finalizer, pod, phase, spawn timeout, TTL.
func (r *ClaudeRunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	runner := &selfhostedv1alpha1.ClaudeRunner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !runner.Status.Phase.IsTerminal() {
		ctx = telemetry.ContextWithTraceparent(ctx, runner.Annotations[selfhostedv1alpha1.AnnotationTraceparent])
	}
	log := logf.FromContext(ctx).WithValues("orderID", runner.Spec.OrderID, "sessionID", runner.Spec.SessionID)
	ctx = logf.IntoContext(ctx, log)
	ctx, span := telemetry.StartSpan(ctx, "clauderunner.reconcile",
		attribute.String("k8s.namespace.name", req.Namespace), attribute.String("order_id", runner.Spec.OrderID))
	defer span.End()
	if !runner.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, runner)
	}
	if !controllerutil.ContainsFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer) {
		controllerutil.AddFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer)
		if err := r.Update(ctx, runner); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil // one mutation per pass
	}

	before := runner.Status.DeepCopy()
	res, err := r.reconcile(ctx, runner)
	runner.Status.ObservedGeneration = runner.Generation
	if before.Phase != runner.Status.Phase {
		log.Info("Runner phase changed", "from", before.Phase, "to", runner.Status.Phase)
	}
	if !equality.Semantic.DeepEqual(before, &runner.Status) {
		if uerr := r.Status().Update(ctx, runner); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(uerr)
		}
	}
	return res, err
}

func (r *ClaudeRunnerReconciler) reconcile(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if runner.Status.Phase.IsTerminal() {
		if runner.Status.Reason == selfhostedv1alpha1.ReasonSpawnTimeout {
			// The pass that timed the runner out may have failed to delete the pod.
			if _, err := r.deleteOwnPod(ctx, runner); err != nil {
				return ctrl.Result{}, err
			}
		}
		return r.expire(ctx, runner)
	}

	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, types.NamespacedName{Name: runner.Spec.EnvironmentRef.Name, Namespace: runner.Namespace}, env); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(runner, selfhostedv1alpha1.ReasonEnvironmentMissing, fmt.Sprintf("ClaudeEnvironment %q not found", runner.Spec.EnvironmentRef.Name))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	wasRunning := runner.Status.Phase == selfhostedv1alpha1.RunnerRunning
	var pod *corev1.Pod
	var err error
	if runner.Status.PodName == "" {
		if msg := ownerMismatch(runner, env); msg != "" {
			r.fail(runner, selfhostedv1alpha1.ReasonEnvironmentMismatch, msg)
			return ctrl.Result{}, nil
		}
		if res, wait, werr := r.awaitWorkOrder(ctx, env, runner); wait || werr != nil {
			return res, werr
		}
		if pod, err = r.createPod(ctx, env, runner); err != nil || pod == nil {
			return ctrl.Result{}, err
		}
	} else {
		if pod, err = r.existingPod(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		if pod == nil {
			// Never re-create: the pod carried a single-use work order.
			r.fail(runner, lostReason(runner.Status.Reason), fmt.Sprintf("runner pod %s disappeared before it finished", runner.Status.PodName))
			return ctrl.Result{}, nil
		}
	}
	ph := derivePhase(pod, r.now())
	runner.Status.PodName = runner.Name
	if ph.StartedAt != nil {
		runner.Status.StartedAt = ph.StartedAt
	}
	if ph.Phase == selfhostedv1alpha1.RunnerFailed {
		runner.Status.FinishedAt = ph.FinishedAt
		r.fail(runner, ph.Reason, ph.Message)
		return r.expire(ctx, runner)
	}
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = ph.Phase, ph.Reason, ph.Message

	switch ph.Phase {
	case selfhostedv1alpha1.RunnerPending:
		deadline := runner.CreationTimestamp.Add(time.Duration(spawnSeconds(env)) * time.Second)
		now := r.now()
		if !now.After(deadline) {
			r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
			return ctrl.Result{RequeueAfter: deadline.Sub(now) + time.Second}, nil
		}
		msg := fmt.Sprintf("pod did not start within %d seconds (%s: %s)", spawnSeconds(env), ph.Reason, ph.Message)
		logf.FromContext(ctx).Info("Runner spawn timed out", "pod", pod.Name)
		// Store Failed before the pod goes and count it only once stored, so
		// neither a lost update here nor one after the delete counts it twice:
		// every later pass sees a terminal runner.
		r.markFailed(runner, selfhostedv1alpha1.ReasonSpawnTimeout, msg)
		if err := r.Status().Update(ctx, runner); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeFailed)
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeSpawnTimeout)
		r.Recorder.Event(runner, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonSpawnTimeout, msg)
		if _, err := r.deleteOwnPod(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		return r.expire(ctx, runner)
	case selfhostedv1alpha1.RunnerRunning:
		if !wasRunning {
			metrics.ObserveSpawn(runner.Namespace, runner.Spec.EnvironmentRef.Name, spawnDuration(runner, pod, r.now()))
		}
		r.setReady(runner, metav1.ConditionTrue, ph.Reason, "")
		return ctrl.Result{}, nil
	default: // Succeeded; a failed pod went through fail above
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeSucceeded)
		runner.Status.FinishedAt = ph.FinishedAt
		r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
		r.Recorder.Event(runner, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonRunnerSucceeded, "runner finished: "+ph.Reason)
		return r.expire(ctx, runner)
	}
}

// lostReason is the failure reason for a runner whose pod is gone: a runner
// that already timed out keeps SpawnTimeout, any other reports PodLost.
// SpawnTimeout here covers runners persisted by a pre-upgrade manager as
// Pending with Reason SpawnTimeout, which the current code no longer writes.
func lostReason(current string) string {
	if current == selfhostedv1alpha1.ReasonSpawnTimeout {
		return selfhostedv1alpha1.ReasonSpawnTimeout
	}
	return selfhostedv1alpha1.ReasonPodLost
}

// spawnDuration is the time from runner creation to the pod starting, or to
// now when the pod reports no start time.
func spawnDuration(runner *selfhostedv1alpha1.ClaudeRunner, pod *corev1.Pod, now time.Time) time.Duration {
	if pod.Status.StartTime != nil {
		return pod.Status.StartTime.Sub(runner.CreationTimestamp.Time)
	}
	return now.Sub(runner.CreationTimestamp.Time)
}

// ownerMismatch returns why the runner may not start a pod from env, or "".
// The orchestrator identity can create ClaudeRunners naming any environment in
// its namespace; only a runner controlled by the on-demand environment it
// names gets a pod.
func ownerMismatch(runner *selfhostedv1alpha1.ClaudeRunner, env *selfhostedv1alpha1.ClaudeEnvironment) string {
	owner := metav1.GetControllerOf(runner)
	if owner == nil || owner.APIVersion != selfhostedv1alpha1.GroupVersion.String() || owner.Kind != "ClaudeEnvironment" || owner.Name != env.Name {
		return fmt.Sprintf("ClaudeRunner is not controlled by ClaudeEnvironment %q named in spec.environmentRef", env.Name)
	}
	if owner.UID != env.UID {
		return fmt.Sprintf("controller owner UID does not match ClaudeEnvironment %q", env.Name)
	}
	if env.Spec.OnDemand == nil {
		return fmt.Sprintf("ClaudeEnvironment %q is not in on-demand mode", env.Name)
	}
	return ""
}

// awaitWorkOrder confirms, before the pod exists, that the work-order Secret
// does. The hook creates it just before the ClaudeRunner, so an absent Secret is
// read uncached and waited for until the spawn deadline before the runner fails.
// wait is true when the caller must stop and return res.
func (r *ClaudeRunnerReconciler) awaitWorkOrder(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment,
	runner *selfhostedv1alpha1.ClaudeRunner) (res ctrl.Result, wait bool, err error) {
	name := runner.Spec.WorkOrderSecretRef.Name
	secret := &corev1.Secret{}
	err = r.Reader.Get(ctx, types.NamespacedName{Name: name, Namespace: runner.Namespace}, secret)
	if err == nil {
		if !workOrderBelongs(secret, env, runner) {
			r.fail(runner, selfhostedv1alpha1.ReasonWorkOrderMismatch,
				fmt.Sprintf("work-order Secret %q does not belong to environment %q", name, env.Name))
			return ctrl.Result{}, true, nil
		}
		return ctrl.Result{}, false, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, true, err
	}
	deadline := runner.CreationTimestamp.Add(time.Duration(spawnSeconds(env)) * time.Second)
	if r.now().After(deadline) {
		r.fail(runner, selfhostedv1alpha1.ReasonWorkOrderMissing,
			fmt.Sprintf("work-order Secret %q not found within %d seconds", name, spawnSeconds(env)))
		return ctrl.Result{}, true, nil
	}
	msg := fmt.Sprintf("waiting for work-order Secret %q", name)
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = selfhostedv1alpha1.RunnerPending, selfhostedv1alpha1.ReasonWorkOrderMissing, msg
	r.setReady(runner, metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkOrderMissing, msg)
	return ctrl.Result{RequeueAfter: workOrderPollWait}, true, nil
}

// workOrderBelongs reports whether secret is a work order the hook made for
// env: labelled with the environment and controlled by env or, after the
// hook's hand-off, by runner. The orchestrator identity can name any Secret in
// the namespace in spec.workOrderSecretRef; only these may be mounted. This
// boundary holds together with the orchestrator admission policy: without it,
// an orchestrator Role with Secret patch could relabel and re-own another
// Secret (see docs/hardening.md).
func workOrderBelongs(secret *corev1.Secret, env *selfhostedv1alpha1.ClaudeEnvironment, runner *selfhostedv1alpha1.ClaudeRunner) bool {
	if secret.Labels[selfhostedv1alpha1.LabelEnvironment] != runner.Spec.EnvironmentRef.Name {
		return false
	}
	owner := metav1.GetControllerOf(secret)
	if owner == nil || owner.APIVersion != selfhostedv1alpha1.GroupVersion.String() {
		return false
	}
	return (owner.Kind == "ClaudeEnvironment" && owner.UID == env.UID) || (owner.Kind == "ClaudeRunner" && owner.UID == runner.UID)
}

// createPod creates the runner pod. It runs only while Status.PodName is
// empty, so a pod that later disappears is never started a second time.
// Pods are immutable, so an existing pod is used as is rather than re-applied,
// but only when the runner controls it: a pod someone else made under the
// runner's name fails the runner with PodMismatch and nil is returned.
func (r *ClaudeRunnerReconciler) createPod(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment,
	runner *selfhostedv1alpha1.ClaudeRunner) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKeyFromObject(runner), pod)
	if err == nil {
		return r.adopt(runner, pod), nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	pod = builders.OnDemandRunnerPod(env, runner, builders.ConfigHash(env, nil, nil))
	if err := controllerutil.SetControllerReference(runner, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		// A pod the cache had not seen yet: use it, it was counted when created.
		existing := &corev1.Pod{}
		key := client.ObjectKeyFromObject(pod)
		err = r.Get(ctx, key, existing)
		if apierrors.IsNotFound(err) {
			err = r.Reader.Get(ctx, key, existing)
		}
		if err != nil {
			return nil, err
		}
		return r.adopt(runner, existing), nil
	}
	logf.FromContext(ctx).Info("Created runner pod", "pod", pod.Name)
	metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeCreated)
	r.Recorder.Event(runner, corev1.EventTypeNormal, "PodCreated", "created runner pod "+pod.Name)
	return pod, nil
}

// adopt returns pod when runner controls it, and otherwise fails the runner
// and returns nil. The pod is never touched.
func (r *ClaudeRunnerReconciler) adopt(runner *selfhostedv1alpha1.ClaudeRunner, pod *corev1.Pod) *corev1.Pod {
	if metav1.IsControlledBy(pod, runner) {
		return pod
	}
	r.fail(runner, selfhostedv1alpha1.ReasonPodMismatch, fmt.Sprintf("pod %s is not controlled by this ClaudeRunner", pod.Name))
	return nil
}

// deleteOwnPod deletes the pod named after the runner when the runner
// controls it, and reports whether no such pod is left. It reads uncached, so
// a pod the cache has not seen yet is not missed, and the UID precondition
// spares a pod recreated under the name since. A pod the runner does not
// control counts as gone and is never touched.
func (r *ClaudeRunnerReconciler) deleteOwnPod(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (bool, error) {
	pod := &corev1.Pod{}
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(runner), pod); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(pod, runner) {
		return true, nil
	}
	if pod.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); client.IgnoreNotFound(err) != nil {
			return false, err
		}
	}
	return false, nil
}

// existingPod returns the pod recorded in Status.PodName, or nil when an
// uncached read confirms it is gone.
func (r *ClaudeRunnerReconciler) existingPod(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Name: runner.Status.PodName, Namespace: runner.Namespace}
	err := r.Get(ctx, key, pod)
	if apierrors.IsNotFound(err) {
		err = r.Reader.Get(ctx, key, pod)
	}
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pod, nil
}

// expire deletes a terminal runner once its TTL has elapsed, or requeues for it.
func (r *ClaudeRunnerReconciler) expire(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if runner.Status.FinishedAt == nil {
		now := metav1.NewTime(r.now())
		runner.Status.FinishedAt = &now
	}
	ttl := defaultRunnerTTL
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, types.NamespacedName{Name: runner.Spec.EnvironmentRef.Name, Namespace: runner.Namespace}, env); err == nil &&
		env.Spec.OnDemand != nil && env.Spec.OnDemand.RunnerTTLSecondsAfterFinished != nil {
		ttl = time.Duration(*env.Spec.OnDemand.RunnerTTLSecondsAfterFinished) * time.Second
	}
	remaining := runner.Status.FinishedAt.Add(ttl).Sub(r.now())
	if remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	logf.FromContext(ctx).Info("Deleting expired runner")
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, runner))
}

// finalize removes the runner's own pod and waits for it to be gone before
// releasing the finalizer.
func (r *ClaudeRunnerReconciler) finalize(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer) {
		return ctrl.Result{}, nil
	}
	gone, err := r.deleteOwnPod(ctx, runner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !gone {
		return ctrl.Result{RequeueAfter: podDeletionPollWait}, nil
	}
	controllerutil.RemoveFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, runner))
}

// fail records the runner as Failed, counts it unless it already was, and
// emits a Warning event.
func (r *ClaudeRunnerReconciler) fail(runner *selfhostedv1alpha1.ClaudeRunner, reason, msg string) {
	if runner.Status.Phase != selfhostedv1alpha1.RunnerFailed {
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeFailed)
	}
	r.markFailed(runner, reason, msg)
	r.Recorder.Event(runner, corev1.EventTypeWarning, reason, msg)
}

// markFailed records the runner as Failed without counting it or emitting an event.
func (r *ClaudeRunnerReconciler) markFailed(runner *selfhostedv1alpha1.ClaudeRunner, reason, msg string) {
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = selfhostedv1alpha1.RunnerFailed, reason, msg
	if runner.Status.FinishedAt == nil {
		now := metav1.NewTime(r.now())
		runner.Status.FinishedAt = &now
	}
	r.setReady(runner, metav1.ConditionFalse, reason, msg)
}

func (r *ClaudeRunnerReconciler) setReady(runner *selfhostedv1alpha1.ClaudeRunner, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&runner.Status.Conditions, metav1.Condition{Type: selfhostedv1alpha1.ConditionRunnerReady,
		Status: status, Reason: reason, Message: msg, ObservedGeneration: runner.Generation})
}

func spawnSeconds(env *selfhostedv1alpha1.ClaudeEnvironment) int32 {
	if env.Spec.OnDemand != nil && env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds > 0 {
		return env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds
	}
	return 120
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClaudeRunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfhostedv1alpha1.ClaudeRunner{}).
		Owns(&corev1.Pod{}).
		Named("clauderunner").
		Complete(r)
}
