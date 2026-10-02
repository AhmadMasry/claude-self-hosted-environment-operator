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
		envReconciler.SetHookImage("")
		DeferCleanup(func() { envReconciler.SetHookImage(testHookImage) })
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonHookImageUnset))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.OnDemand).To(BeNil())
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: orchestratorObjName, Namespace: ns}, &appsv1.Deployment{}))
		}, 2*time.Second, interval).Should(BeTrue())
	})

	It("warns once per degradation when two hold at the same time", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		envReconciler.SetHookImage("")
		DeferCleanup(func() { envReconciler.SetHookImage(testHookImage) })
		env := onDemandEnvObj(ns)
		env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](30)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(
			HaveField("Message", And(ContainSubstring("terminationGracePeriodSeconds"), ContainSubstring("hook image"))))

		grace := eventCount(ctx, ns, selfhostedv1alpha1.ReasonGracePeriodTooShort)
		hook := eventCount(ctx, ns, selfhostedv1alpha1.ReasonHookImageUnset)
		Eventually(grace, timeout, interval).Should(Equal(int32(1)))
		Eventually(hook, timeout, interval).Should(Equal(int32(1)))
		poke(ctx, key, "1")
		poke(ctx, key, "2")
		Consistently(func() []int32 { g, _ := grace(); h, _ := hook(); return []int32{g, h} }, 3*time.Second, interval).
			Should(Equal([]int32{1, 1}))

		// Clearing one degradation and bringing it back warns exactly once more.
		envReconciler.SetHookImage(testHookImage)
		poke(ctx, key, "3")
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(
			HaveField("Message", Not(ContainSubstring("hook image"))))
		envReconciler.SetHookImage("")
		poke(ctx, key, "4")
		Eventually(hook, timeout, interval).Should(Equal(int32(2)))
		Consistently(func() []int32 { g, _ := grace(); h, _ := hook(); return []int32{g, h} }, 3*time.Second, interval).
			Should(Equal([]int32{1, 2}))
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
			r := ownedBy(runnerObj(ns, name), env)
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

	It("deletes orphaned work-order Secrets after the spawn deadline and keeps the rest", func() {
		ns := newNamespace(ctx)
		// The environment key Secret itself carries every mark a work order
		// does, so only the explicit name guard keeps it.
		envKey := envSecret(ns, "environment-secret")
		envKey.Name = "envkey" + selfhostedv1alpha1.WorkOrderSecretSuffix
		envKey.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: envName, selfhostedv1alpha1.LabelOrderID: "envkey"}
		Expect(k8sClient.Create(ctx, envKey)).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.EnvironmentSecretRef.Name = envKey.Name
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 30
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 15
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(condition(ctx, client.ObjectKeyFromObject(env), selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		envOwned := func(s *corev1.Secret) *corev1.Secret {
			s.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))}
			return s
		}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(envKey), envKey)).To(Succeed())
		Expect(k8sClient.Update(ctx, envOwned(envKey))).To(Succeed())
		workOrder := func(order, environment string) *corev1.Secret {
			s := workOrderSecret(ns, order)
			s.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: environment, selfhostedv1alpha1.LabelOrderID: order}
			return s
		}

		// The hook creates the Secret owned by the environment, then crashes
		// before creating the ClaudeRunner.
		orphan := envOwned(workOrder("orphan", envName))
		referenced := envOwned(workOrder("kept", envName))
		userSecret := envOwned(workOrderSecret(ns, "user"))
		noOrderLabel := envOwned(workOrderSecret(ns, "noorder"))
		noOrderLabel.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: envName}
		noSuffix := envOwned(workOrder("nosuffix", envName))
		noSuffix.Name = "nosuffix"
		notOwned := workOrder("unowned", envName)
		otherEnv := envOwned(workOrder("other", "other-environment"))
		for _, s := range []*corev1.Secret{orphan, referenced, userSecret, noOrderLabel, noSuffix, notOwned, otherEnv} {
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, ownedBy(runnerObj(ns, "kept"), env))).To(Succeed())

		poke(ctx, client.ObjectKeyFromObject(env), "0")
		Consistently(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.Secret{}) }, 3*time.Second, interval).Should(Succeed(), "younger than the deadline must be kept")
		clock.Shift(time.Minute)
		DeferCleanup(func() { clock.Shift(0) })
		poke(ctx, client.ObjectKeyFromObject(env), "1")
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.Secret{}))
		}, timeout, interval).Should(BeTrue())
		for why, s := range map[string]*corev1.Secret{
			"referenced by a ClaudeRunner": referenced, "a user Secret without operator labels": userSecret,
			"missing the order-id label": noOrderLabel, "without the work-order suffix": noSuffix,
			"not controlled by the environment": notOwned, "labelled for another environment": otherEnv,
			"the environment key Secret": envKey,
		} {
			Consistently(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(s), &corev1.Secret{}) }, time.Second, interval).Should(Succeed(), why+" must be kept")
		}
		Eventually(func() bool {
			events := &corev1.EventList{}
			if err := k8sClient.List(ctx, events, client.InNamespace(ns)); err != nil {
				return false
			}
			for _, e := range events.Items {
				if e.Reason == selfhostedv1alpha1.ReasonOrphanedWorkOrderDeleted && e.InvolvedObject.Name == envName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})

	It("keeps a work-order Secret whose ClaudeRunner the cache has not seen yet", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 30
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 15
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(condition(ctx, client.ObjectKeyFromObject(env), selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		workOrder := func(order string) *corev1.Secret {
			s := workOrderSecret(ns, order)
			s.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: envName, selfhostedv1alpha1.LabelOrderID: order}
			s.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))}
			return s
		}
		orphan, lagging := workOrder("orphan"), workOrder("lagging")
		Expect(k8sClient.Create(ctx, orphan)).To(Succeed())
		Expect(k8sClient.Create(ctx, lagging)).To(Succeed())
		// Without the environment label the ClaudeRunner never shows up in the
		// sweep's cached list, as if the informer had not caught up with a
		// redelivery's create; only the API server knows it exists.
		r := ownedBy(runnerObj(ns, "lagging"), env)
		r.Labels = nil
		Expect(k8sClient.Create(ctx, r)).To(Succeed())

		clock.Shift(time.Minute)
		DeferCleanup(func() { clock.Shift(0) })
		poke(ctx, client.ObjectKeyFromObject(env), "1")
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.Secret{}))
		}, timeout, interval).Should(BeTrue(), "the sweep must have run")
		Consistently(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(lagging), &corev1.Secret{}) }, 2*time.Second, interval).
			Should(Succeed(), "a Secret with a live ClaudeRunner must be kept")
		events := &corev1.EventList{}
		Expect(k8sClient.List(ctx, events, client.InNamespace(ns))).To(Succeed())
		for _, e := range events.Items {
			if e.Reason == selfhostedv1alpha1.ReasonOrphanedWorkOrderDeleted {
				Expect(e.Message).NotTo(ContainSubstring(lagging.Name))
			}
		}
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
