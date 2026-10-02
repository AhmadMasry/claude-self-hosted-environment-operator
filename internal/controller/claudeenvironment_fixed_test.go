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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	timeout  = 10 * time.Second
	interval = 200 * time.Millisecond

	envSecretName = "env-secret"
	runnerName    = "platform-runner"
	hooksName     = "hooks"
)

var nsCounter int

func newNamespace(ctx context.Context) string {
	nsCounter++
	name := fmt.Sprintf("fixed-%d-%d", GinkgoParallelProcess(), nsCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func envSecret(ns, key string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: envSecretName, Namespace: ns},
		Data: map[string][]byte{key: []byte("ccenvkey_test")}}
}

func fixedEnv(ns string) *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: ns},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: envSecretName},
			Runner:               selfhostedv1alpha1.RunnerSpec{Image: "registry.local/runner:2.1.280"},
			Fixed:                &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](2)},
		},
	}
}

func condition(ctx context.Context, key types.NamespacedName, t string) func() (*metav1.Condition, error) {
	return func() (*metav1.Condition, error) {
		env := &selfhostedv1alpha1.ClaudeEnvironment{}
		if err := k8sClient.Get(ctx, key, env); err != nil {
			return nil, err
		}
		c := meta.FindStatusCondition(env.Status.Conditions, t)
		if c == nil {
			return nil, fmt.Errorf("condition %s not set yet", t)
		}
		return c, nil
	}
}

func haveReason(status metav1.ConditionStatus, reason string) OmegaMatcher {
	return And(HaveField("Status", status), HaveField("Reason", reason))
}

var _ = Describe("ClaudeEnvironment fixed mode", func() {
	ctx := context.Background()

	It("creates a hardened Deployment and reports Ready once the Deployment is available", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)

		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, dep)
		}, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		Expect(*dep.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(80)))
		Expect(dep.Spec.Template.Spec.Containers[0].Args[:3]).To(Equal([]string{"self-hosted-runner", "--environment-secret-file", "/etc/claude/environment-secret"}))
		Expect(dep.OwnerReferences).To(HaveLen(1))
		Expect(dep.OwnerReferences[0].Name).To(Equal("platform"))
		Expect(dep.Spec.Template.Annotations).To(HaveKey(selfhostedv1alpha1.AnnotationConfigHash))

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonSecretFound))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonFixedModeSecretOnPods))

		// Simulate the deployment controller.
		dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, dep.Status.UpdatedReplicas = 2, 2, 2, 2
		dep.Status.ObservedGeneration = dep.Generation
		dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Mode).To(Equal("fixed"))
		Expect(got.Status.ComputedDrainBudgetSeconds).To(Equal(int64(80)))
		Expect(got.Status.Fixed.ReadyReplicas).To(Equal(int32(2)))
		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
	})

	It("reports SecretMissing and creates no Deployment when the Secret is absent", func() {
		ns := newNamespace(ctx)
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())

		// Creating the Secret later recovers without any change to the environment.
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
	})

	It("reports SecretKeyMissing when the Secret lacks the configured key", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "wrong-key"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretKeyMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())
	})

	It("reports ConfigMapMissing and creates no Deployment when a referenced ConfigMap is absent", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: hooksName}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonConfigMapMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())
	})

	It("rolls the pod template when a referenced ConfigMap or the Secret changes", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: hooksName, Namespace: ns}, Data: map[string]string{"checkout": "v1"}}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: hooksName}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())

		hash := func() (string, error) {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, dep); err != nil {
				return "", err
			}
			return dep.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash], nil
		}
		var h1 string
		Eventually(func() error { var err error; h1, err = hash(); return err }, timeout, interval).Should(Succeed())

		cm.Data["checkout"] = "v2"
		Expect(k8sClient.Update(ctx, cm)).To(Succeed())
		Eventually(hash, timeout, interval).ShouldNot(Equal(h1))

		var h2 string
		Eventually(func() error { var err error; h2, err = hash(); return err }, timeout, interval).Should(Succeed())
		sec := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: envSecretName, Namespace: ns}, sec)).To(Succeed())
		sec.Data["environment-secret"] = []byte("ccenvkey_rotated")
		Expect(k8sClient.Update(ctx, sec)).To(Succeed())
		Eventually(hash, timeout, interval).ShouldNot(Equal(h2))
	})

	It("keeps a user grace period that is too short and flags it as Degraded", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](30)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonGracePeriodTooShort))
		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, dep)
		}, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(30)))
	})

	It("creates a StatefulSet for a persistent workspace and removes a stale Deployment", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, &appsv1.Deployment{})
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		env.Spec.Runner.Settings.LockToAccount = "user_123"
		env.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{
			VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: mustQuantity("10Gi")}}}}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())

		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, sts)
		}, timeout, interval).Should(Succeed())
		Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal(builders.WorkspaceVolume))
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerName, Namespace: ns}, &appsv1.Deployment{})
		}, timeout, interval).ShouldNot(Succeed())
	})

	It("marks onDemand as unsupported in this version", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Fixed = nil
		env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonUnsupportedMode))
	})
})

func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }
