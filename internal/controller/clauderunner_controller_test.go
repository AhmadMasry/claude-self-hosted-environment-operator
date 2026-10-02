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
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-1"))).To(Succeed())
		r := runnerObj(ns, "order-1")
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
		r := runnerObj(ns, "order-2")
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
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-3"))).To(Succeed())
		r := runnerObj(ns, "order-3")
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
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 5 // the CRD requires hookTimeoutSeconds + 5 < expectedSpawnSeconds
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-4"))).To(Succeed())
		r := runnerObj(ns, "order-4")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))

		nowFunc = func() time.Time { return time.Now().Add(2 * time.Minute) }
		DeferCleanup(func() { nowFunc = time.Now })
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
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 10
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 4 // the CRD requires hookTimeoutSeconds + 5 < expectedSpawnSeconds
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		r := runnerObj(ns, "order-5")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(runnerPhase(ctx, key), 15*time.Second, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonWorkOrderMissing))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 2*time.Second, interval).Should(BeTrue())
	})

	It("waits for a work-order Secret that lands after the ClaudeRunner", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		r := runnerObj(ns, "order-9")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		time.Sleep(2 * time.Second)
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
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-10"))).To(Succeed())
		r := runnerObj(ns, "order-10")
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
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-7"))).To(Succeed())
		r := runnerObj(ns, "order-7")
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
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-8"))).To(Succeed())
		r := runnerObj(ns, "order-8")
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
})
