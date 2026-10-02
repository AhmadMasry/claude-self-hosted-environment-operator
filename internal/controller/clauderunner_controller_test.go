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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"
)

func onDemandEnvObj(ns string) *selfhostedv1alpha1.ClaudeEnvironment {
	env := fixedEnv(ns)
	env.Spec.Fixed = nil
	env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{RunnerTTLSecondsAfterFinished: ptr.To[int32](300)}
	return env
}

func workOrderSecret(ns, order string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: order + "-work-order", Namespace: ns},
		Data: map[string][]byte{selfhostedv1alpha1.WorkOrderSecretKey: []byte("eyJ.work.order")}}
}

func runnerObj(ns, order string) *selfhostedv1alpha1.ClaudeRunner {
	return &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: order, Namespace: ns, Labels: map[string]string{selfhostedv1alpha1.LabelEnvironment: envName, selfhostedv1alpha1.LabelOrderID: order}},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: envName}, OrderID: order,
			SessionID: "session_" + order, WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: order + "-work-order"}},
	}
}

// ownedBy makes the created environment the runner's controller owner, as the hook does.
func ownedBy(r *selfhostedv1alpha1.ClaudeRunner, env *selfhostedv1alpha1.ClaudeEnvironment) *selfhostedv1alpha1.ClaudeRunner {
	r.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))}
	return r
}

func runnerPhase(ctx context.Context, key types.NamespacedName) func() (selfhostedv1alpha1.RunnerPhase, error) {
	return func() (selfhostedv1alpha1.RunnerPhase, error) {
		r := &selfhostedv1alpha1.ClaudeRunner{}
		if err := k8sClient.Get(ctx, key, r); err != nil {
			return "", err
		}
		return r.Status.Phase, nil
	}
}

func setPodPhase(ctx context.Context, key types.NamespacedName, phase corev1.PodPhase, term *corev1.ContainerStateTerminated) {
	pod := &corev1.Pod{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, pod)).To(Succeed())
	now := metav1.Now()
	pod.Status.Phase = phase
	pod.Status.StartTime = &now
	state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}
	if term != nil {
		state = corev1.ContainerState{Terminated: term}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runner", State: state}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

var _ = Describe("ClaudeRunner controller", func() {
	ctx := context.Background()

	It("creates a single-session pod from the work order and follows it to Running", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-1"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-1"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)

		pod := &corev1.Pod{}
		Eventually(func() error { return k8sClient.Get(ctx, key, pod) }, timeout, interval).Should(Succeed())
		Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
		Expect(pod.OwnerReferences[0].Kind).To(Equal("ClaudeRunner"))
		Expect(pod.Spec.Volumes[0].Secret.SecretName).To(Equal("order-1-work-order"))
		Expect(pod.Labels[selfhostedv1alpha1.LabelSessionID]).To(Equal("session_order-1"))
		Expect(pod.Spec.Containers[0].Args).To(ContainElements("--capacity", "1"))

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(func() []string {
			got := &selfhostedv1alpha1.ClaudeRunner{}
			_ = k8sClient.Get(ctx, key, got)
			return got.Finalizers
		}, timeout, interval).Should(ContainElement(selfhostedv1alpha1.RunnerFinalizer))

		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.PodName).To(Equal("order-1"))
		Expect(got.Status.StartedAt).NotTo(BeNil())
		Expect(got.Status.Conditions).To(ContainElement(HaveField("Type", selfhostedv1alpha1.ConditionRunnerReady)))
	})

	It("records Succeeded then garbage-collects the runner, pod and secret after the TTL", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.RunnerTTLSecondsAfterFinished = ptr.To[int32](3)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-2"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-2"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())

		setPodPhase(ctx, key, corev1.PodSucceeded, &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.Now()})
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerSucceeded))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.FinishedAt).NotTo(BeNil())

		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &selfhostedv1alpha1.ClaudeRunner{})) }, 20*time.Second, interval).Should(BeTrue())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue())
	})

	It("records Failed with a redacted exit message", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-3"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-3"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodFailed, &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.Now(),
			Message: "[runner:fatal] RegisterRunner auth failed for someone@example.com"})
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonPodFailed))
		Expect(got.Status.Message).To(ContainSubstring("exit code 1"))
		Expect(got.Status.Message).NotTo(ContainSubstring("example.com"))
	})

	It("marks a pod that never starts within expectedSpawnSeconds as SpawnTimeout and deletes it", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 30
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 15 // the CRD requires hookTimeoutSeconds >= 15 and hookTimeoutSeconds + 5 < expectedSpawnSeconds
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-4"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-4"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))

		clock.Shift(2 * time.Minute)
		DeferCleanup(func() { clock.Shift(0) })
		Expect(k8sClient.Get(ctx, key, r)).To(Succeed())
		r.Annotations = map[string]string{"test/poke": "1"}
		Expect(k8sClient.Update(ctx, r)).To(Succeed())

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonSpawnTimeout))
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue())
	})

	It("fails cleanly when the work-order Secret is still missing at the spawn deadline", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 21
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 15 // the CRD requires hookTimeoutSeconds >= 15 and hookTimeoutSeconds + 5 < expectedSpawnSeconds
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-5"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(runnerPhase(ctx, key), 40*time.Second, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonWorkOrderMissing))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 2*time.Second, interval).Should(BeTrue())
		Expect(metrics.RunnersTotalForTest(ns, envName, metrics.OutcomeFailed)).To(Equal(1.0))
	})

	It("waits for a work-order Secret that lands after the ClaudeRunner", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-9"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() string {
			got := &selfhostedv1alpha1.ClaudeRunner{}
			_ = k8sClient.Get(ctx, key, got)
			return string(got.Status.Phase) + "/" + got.Status.Reason
		}, timeout, interval).Should(Equal(string(selfhostedv1alpha1.RunnerPending) + "/" + selfhostedv1alpha1.ReasonWorkOrderMissing))
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-9"))).To(Succeed())

		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		Eventually(func() string {
			got := &selfhostedv1alpha1.ClaudeRunner{}
			_ = k8sClient.Get(ctx, key, got)
			return got.Status.PodName
		}, timeout, interval).Should(Equal("order-9"))
		Expect(runnerPhase(ctx, key)()).To(Equal(selfhostedv1alpha1.RunnerPending))
	})

	It("fails a runner whose pod disappears instead of starting a second pod", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-10"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-10"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, key, pod)).To(Succeed())
		Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonPodLost))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 3*time.Second, interval).Should(BeTrue())
	})

	It("refuses a runner whose controller owner is not the environment it names", func() {
		ns := newNamespace(ctx)
		owner := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, owner)).To(Succeed())
		named := onDemandEnvObj(ns)
		named.Name = "other"
		Expect(k8sClient.Create(ctx, named)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-11"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-11"), owner)
		r.Spec.EnvironmentRef.Name = named.Name
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonEnvironmentMismatch))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 2*time.Second, interval).Should(BeTrue())
	})

	It("refuses a runner whose controller owner has the environment's name but another UID", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-12"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-12"), env)
		r.OwnerReferences[0].UID = types.UID("00000000-0000-0000-0000-000000000000")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonEnvironmentMismatch))
		Expect(got.Status.Message).To(ContainSubstring("UID"))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 2*time.Second, interval).Should(BeTrue())
	})

	It("adopts a pod that already exists without counting or announcing a second creation", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-13"))).To(Succeed())
		// No role label, so the manager's pod cache never sees it and the
		// create runs into AlreadyExists.
		Expect(k8sClient.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "order-13", Namespace: ns},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "runner", Image: runnerImage}}}})).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-13"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)

		Eventually(func() string {
			got := &selfhostedv1alpha1.ClaudeRunner{}
			_ = k8sClient.Get(ctx, key, got)
			return got.Status.PodName
		}, timeout, interval).Should(Equal("order-13"))
		Consistently(eventCount(ctx, ns, "PodCreated"), 2*time.Second, interval).Should(Equal(int32(0)))
		Expect(metrics.RunnersTotalForTest(ns, envName, metrics.OutcomeCreated)).To(Equal(0.0))
	})

	It("fails cleanly when the environment is missing", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-6"))).To(Succeed())
		r := runnerObj(ns, "order-6")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonEnvironmentMissing))
	})

	It("deletes the pod before the runner object disappears", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-7"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-7"), env)
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))

		Expect(k8sClient.Delete(ctx, r)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue(),
			fmt.Sprintf("pod %s should be deleted by the finalizer", key))
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &selfhostedv1alpha1.ClaudeRunner{})) }, timeout, interval).Should(BeTrue())
	})

	It("continues the trace the hook started", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-8"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-8"), env)
		r.Annotations = map[string]string{selfhostedv1alpha1.AnnotationTraceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		Eventually(runnerPhase(ctx, client.ObjectKeyFromObject(r)), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(func() bool {
			for _, s := range spanExporter.GetSpans() {
				if s.Name == "clauderunner.reconcile" && s.SpanContext.TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})

	It("stops continuing the hook's trace once the runner is terminal", func() {
		const traceID = "5bf92f3577b34da6a3ce929d0e0e4737"
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		// No work-order Secret and a clock past the spawn deadline: the runner fails at once.
		clock.Shift(5 * time.Minute)
		DeferCleanup(func() { clock.Shift(0) })
		r := ownedBy(runnerObj(ns, "order-14"), env)
		r.Annotations = map[string]string{selfhostedv1alpha1.AnnotationTraceparent: "00-" + traceID + "-00f067aa0ba902b7-01"}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))

		// spans counts this runner's reconcile spans, or only those in the hook's trace.
		spans := func(inTrace bool) int {
			n := 0
			for _, s := range spanExporter.GetSpans() {
				if s.Name != "clauderunner.reconcile" || (inTrace && s.SpanContext.TraceID().String() != traceID) {
					continue
				}
				for _, a := range s.Attributes {
					if a.Key == "order_id" && a.Value.AsString() == "order-14" {
						n++
					}
				}
			}
			return n
		}
		// Let the reconciles of the transition itself settle.
		Eventually(func() bool {
			a := spans(false)
			time.Sleep(time.Second)
			return spans(false) == a
		}, timeout, interval).Should(BeTrue())
		allBefore, tracedBefore := spans(false), spans(true)

		Expect(k8sClient.Get(ctx, key, r)).To(Succeed())
		r.Annotations["test/poke"] = "1"
		Expect(k8sClient.Update(ctx, r)).To(Succeed())
		Eventually(func() int { return spans(false) }, timeout, interval).Should(BeNumerically(">", allBefore))
		Expect(spans(true)).To(Equal(tracedBefore))
	})
})
