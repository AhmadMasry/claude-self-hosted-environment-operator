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
	ctx = telemetry.ContextWithTraceparent(ctx, runner.Annotations[selfhostedv1alpha1.AnnotationTraceparent])
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
		if pod, err = r.createPod(ctx, env, runner); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		if pod, err = r.existingPod(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
		if pod == nil {
			// Never re-create: the pod carried a single-use work order.
			r.fail(runner, selfhostedv1alpha1.ReasonPodLost, fmt.Sprintf("runner pod %s disappeared before it finished", runner.Status.PodName))
			return ctrl.Result{}, nil
		}
	}
	ph := derivePhase(pod, r.now())
	runner.Status.PodName = runner.Name
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = ph.Phase, ph.Reason, ph.Message
	if ph.StartedAt != nil {
		runner.Status.StartedAt = ph.StartedAt
	}

	switch ph.Phase {
	case selfhostedv1alpha1.RunnerPending:
		deadline := runner.CreationTimestamp.Add(time.Duration(spawnSeconds(env)) * time.Second)
		now := r.now()
		if !now.After(deadline) {
			r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
			return ctrl.Result{RequeueAfter: deadline.Sub(now) + time.Second}, nil
		}
		msg := fmt.Sprintf("pod did not start within %d seconds (%s: %s)", spawnSeconds(env), ph.Reason, ph.Message)
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		r.fail(runner, selfhostedv1alpha1.ReasonSpawnTimeout, msg)
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeSpawnTimeout)
		return r.expire(ctx, runner)
	case selfhostedv1alpha1.RunnerRunning:
		if !wasRunning {
			metrics.ObserveSpawn(runner.Namespace, runner.Spec.EnvironmentRef.Name, r.now().Sub(runner.CreationTimestamp.Time))
		}
		r.setReady(runner, metav1.ConditionTrue, ph.Reason, "")
		return ctrl.Result{}, nil
	default: // terminal
		outcome := metrics.OutcomeFailed
		if ph.Phase == selfhostedv1alpha1.RunnerSucceeded {
			outcome = metrics.OutcomeSucceeded
		}
		metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, outcome)
		runner.Status.FinishedAt = ph.FinishedAt
		r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
		if ph.Phase == selfhostedv1alpha1.RunnerFailed {
			r.Recorder.Event(runner, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonRunnerFailed, "runner finished: "+ph.Reason)
		} else {
			r.Recorder.Event(runner, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonRunnerSucceeded, "runner finished: "+ph.Reason)
		}
		return r.expire(ctx, runner)
	}
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
	err = r.Reader.Get(ctx, types.NamespacedName{Name: name, Namespace: runner.Namespace}, &corev1.Secret{})
	if err == nil {
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

// createPod creates the runner pod. It runs only while Status.PodName is
// empty, so a pod that later disappears is never started a second time.
// Pods are immutable, so an existing pod is used as is rather than re-applied.
func (r *ClaudeRunnerReconciler) createPod(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment,
	runner *selfhostedv1alpha1.ClaudeRunner) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKeyFromObject(runner), pod)
	if err == nil {
		return pod, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	pod = builders.OnDemandRunnerPod(env, runner, builders.ConfigHash(env, nil, nil))
	if err := controllerutil.SetControllerReference(runner, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeCreated)
	r.Recorder.Event(runner, corev1.EventTypeNormal, "PodCreated", "created runner pod "+pod.Name)
	return pod, nil
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
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, runner))
}

// finalize removes the pod and waits for it to be gone before releasing the finalizer.
func (r *ClaudeRunnerReconciler) finalize(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer) {
		return ctrl.Result{}, nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: runner.Name, Namespace: runner.Namespace}}
	if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err == nil {
		return ctrl.Result{RequeueAfter: podDeletionPollWait}, nil
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, runner))
}

func (r *ClaudeRunnerReconciler) fail(runner *selfhostedv1alpha1.ClaudeRunner, reason, msg string) {
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = selfhostedv1alpha1.RunnerFailed, reason, msg
	if runner.Status.FinishedAt == nil {
		now := metav1.NewTime(r.now())
		runner.Status.FinishedAt = &now
	}
	r.setReady(runner, metav1.ConditionFalse, reason, msg)
	r.Recorder.Event(runner, corev1.EventTypeWarning, reason, msg)
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
