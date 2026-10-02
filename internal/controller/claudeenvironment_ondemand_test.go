package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	orchestratorObjName = "platform-orchestrator"
	sharedConfigMap     = "shared"
)

func markDeploymentAvailable(ctx context.Context, key types.NamespacedName) {
	dep := &appsv1.Deployment{}
	EventuallyWithOffset(1, func() error { return k8sClient.Get(ctx, key, dep) }, timeout, interval).Should(Succeed())
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, dep.Status.UpdatedReplicas = replicas, replicas, replicas, replicas
	dep.Status.ObservedGeneration = dep.Generation
	dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, dep)).To(Succeed())
}

var _ = Describe("ClaudeEnvironment on-demand mode", func() {
	ctx := context.Background()

	It("creates the orchestrator with its RBAC and reports Ready once available", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.Replicas = ptr.To[int32](2)
		env.Spec.OnDemand.MaxConcurrentRunners = 3
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		orch := types.NamespacedName{Name: orchestratorObjName, Namespace: ns}

		dep := &appsv1.Deployment{}
		Eventually(func() error { return k8sClient.Get(ctx, orch, dep) }, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		Expect(dep.Spec.Template.Spec.InitContainers[0].Image).To(Equal("example.com/claude-selfhosted-operator:test"))
		Expect(dep.Spec.Template.Spec.Containers[0].Args[:2]).To(Equal([]string{"self-hosted-runner", "orchestrator"}))
		Expect(dep.Spec.Template.Spec.ServiceAccountName).To(Equal(orchestratorObjName))
		Expect(dep.OwnerReferences[0].Name).To(Equal("platform"))
		Expect(k8sClient.Get(ctx, orch, &corev1.ServiceAccount{})).To(Succeed())
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, orch, role)).To(Succeed())
		Expect(role.Rules[1].Verbs).To(ContainElement("list"))
		Expect(k8sClient.Get(ctx, orch, &rbacv1.RoleBinding{})).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonOnDemandSecretOnOrchestrator))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))

		markDeploymentAvailable(ctx, orch)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Mode).To(Equal("onDemand"))
		Expect(got.Status.OnDemand.OrchestratorReadyReplicas).To(Equal(int32(2)))
	})

	It("degrades with HookImageUnset when the operator has no hook image", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		saved := envReconciler.HookImage
		envReconciler.HookImage = ""
		DeferCleanup(func() { envReconciler.HookImage = saved })
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonHookImageUnset))
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: orchestratorObjName, Namespace: ns}, &appsv1.Deployment{}))
		}, 2*time.Second, interval).Should(BeTrue())
	})

	It("counts runners by phase", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())
		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())

		// Phases are driven through pod status so the ClaudeRunner controller
		// derives them itself: c1 stays Pending, c2 and c3 run, c4 finishes.
		for _, name := range []string{"c1", "c2", "c3", "c4"} {
			Expect(k8sClient.Create(ctx, workOrderSecret(ns, name))).To(Succeed())
			r := runnerObj(ns, name)
			r.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))}
			Expect(k8sClient.Create(ctx, r)).To(Succeed())
			Eventually(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(r), &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		}
		setPodPhase(ctx, types.NamespacedName{Name: "c2", Namespace: ns}, corev1.PodRunning, nil)
		setPodPhase(ctx, types.NamespacedName{Name: "c3", Namespace: ns}, corev1.PodRunning, nil)
		setPodPhase(ctx, types.NamespacedName{Name: "c4", Namespace: ns}, corev1.PodSucceeded, &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.Now()})

		Eventually(func() (int32, error) {
			got := &selfhostedv1alpha1.ClaudeEnvironment{}
			if err := k8sClient.Get(ctx, key, got); err != nil || got.Status.OnDemand == nil {
				return -1, err
			}
			return got.Status.OnDemand.RunningRunners, nil
		}, timeout, interval).Should(Equal(int32(2)))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.OnDemand.PendingRunners).To(Equal(int32(1)))
	})

	It("switches fixed to onDemand and back, cleaning up each mode's objects", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		fixedKey := types.NamespacedName{Name: "platform-runner", Namespace: ns}
		orch := types.NamespacedName{Name: orchestratorObjName, Namespace: ns}
		Eventually(func() error { return k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.Fixed = nil
		env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() error { return k8sClient.Get(ctx, orch, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{})) }, timeout, interval).Should(BeTrue())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.OnDemand = nil
		env.Spec.Fixed = &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() error { return k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())
		for _, obj := range []client.Object{&appsv1.Deployment{}, &rbacv1.RoleBinding{}, &rbacv1.Role{}, &corev1.ServiceAccount{}} {
			Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, orch, obj)) }, timeout, interval).Should(BeTrue())
		}
	})

	It("checks the wrapper key even when the wrapper and host config share a ConfigMap", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: sharedConfigMap, Namespace: ns}, Data: map[string]string{"settings.json": "{}"}})).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.HostConfig = &selfhostedv1alpha1.ConfigMapRef{Name: sharedConfigMap}
		env.Spec.Runner.WrapperScript = &selfhostedv1alpha1.ConfigMapKeyRef{Name: sharedConfigMap, Key: "wrap.sh"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(condition(ctx, client.ObjectKeyFromObject(env), selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonConfigMapMissing))
	})
})
