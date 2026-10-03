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
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	timeout  = 10 * time.Second
	interval = 200 * time.Millisecond

	envSecretName  = "env-secret"
	runnerName     = "platform-runner"
	hooksName      = "hooks"
	envName        = "platform"
	testEgressCIDR = "10.0.0.0/8"
	runnerImage    = "registry.local/runner:2.1.280"
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
		ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: ns},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: envSecretName},
			Runner:               selfhostedv1alpha1.RunnerSpec{Image: runnerImage},
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

// eventCount sums the occurrences of events with reason in ns; the recorder
// aggregates repeats of the same event into one object with a count.
func eventCount(ctx context.Context, ns, reason string) func() (int32, error) {
	return func() (int32, error) {
		list := &corev1.EventList{}
		if err := k8sClient.List(ctx, list, client.InNamespace(ns)); err != nil {
			return 0, err
		}
		var n int32
		for _, e := range list.Items {
			if e.Reason == reason {
				n += max(e.Count, 1)
			}
		}
		return n, nil
	}
}

// eventMessages lists the messages of events with reason in ns.
func eventMessages(ctx context.Context, ns, reason string) func() ([]string, error) {
	return func() ([]string, error) {
		list := &corev1.EventList{}
		if err := k8sClient.List(ctx, list, client.InNamespace(ns)); err != nil {
			return nil, err
		}
		var out []string
		for _, e := range list.Items {
			if e.Reason == reason {
				out = append(out, e.Message)
			}
		}
		return out, nil
	}
}

// poke updates an annotation so the environment is reconciled again.
func poke(ctx context.Context, key types.NamespacedName, value string) {
	ExpectWithOffset(1, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		env := &selfhostedv1alpha1.ClaudeEnvironment{}
		if err := k8sClient.Get(ctx, key, env); err != nil {
			return err
		}
		env.Annotations = map[string]string{"test/poke": value}
		return k8sClient.Update(ctx, env)
	})).To(Succeed())
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
		Expect(dep.OwnerReferences[0].Name).To(Equal(envName))
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
		Eventually(eventMessages(ctx, ns, selfhostedv1alpha1.ReasonCreated), timeout, interval).Should(
			ContainElement(ContainSubstring("Deployment platform-runner")))
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
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretKeyMissing))
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

		// A Warning is emitted on the transition to Degraded only, not on every pass.
		Eventually(eventCount(ctx, ns, selfhostedv1alpha1.ReasonGracePeriodTooShort), timeout, interval).Should(Equal(int32(1)))
		poke(ctx, key, "1")
		poke(ctx, key, "2")
		Consistently(eventCount(ctx, ns, selfhostedv1alpha1.ReasonGracePeriodTooShort), 3*time.Second, interval).Should(Equal(int32(1)))
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

	It("reports WorkloadApplyFailed and not Ready when the StatefulSet cannot be applied", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.Settings.LockToAccount = "user_123"
		env.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{
			VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: mustQuantity("10Gi")}}}}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)

		sts := &appsv1.StatefulSet{}
		stsKey := types.NamespacedName{Name: runnerName, Namespace: ns}
		Eventually(func() error { return k8sClient.Get(ctx, stsKey, sts) }, timeout, interval).Should(Succeed())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())

		// Simulate the StatefulSet controller so the fleet is Ready before the change.
		sts.Status.Replicas, sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas = 2, 2, 2
		sts.Status.ObservedGeneration = sts.Generation
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
		Expect(k8sClient.Get(ctx, stsKey, sts)).To(Succeed())
		generation := sts.Generation

		// volumeClaimTemplates accessModes are immutable (KEP-4650 relaxes only
		// the storage request), so this apply is rejected.
		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.Fixed.PersistentWorkspace.VolumeClaimTemplate.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).Should(
			And(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkloadApplyFailed),
				HaveField("Message", ContainSubstring("StatefulSet"))))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(
			haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkloadApplyFailed))
		got := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, stsKey, got)).To(Succeed())
		Expect(got.Generation).To(Equal(generation))
		Expect(got.Spec.VolumeClaimTemplates[0].Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}))
		Eventually(func() (*selfhostedv1alpha1.FixedFleetStatus, error) {
			e := &selfhostedv1alpha1.ClaudeEnvironment{}
			err := k8sClient.Get(ctx, key, e)
			return e.Status.Fixed, err
		}, timeout, interval).Should(BeNil())
	})

	It("keeps a stable Degraded message and ResourceVersion with several missing ConfigMaps", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: hooksName}
		env.Spec.Runner.HostConfig = &selfhostedv1alpha1.ConfigMapRef{Name: "cfg"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonConfigMapMissing))

		snapshot := func() (string, error) {
			got := &selfhostedv1alpha1.ClaudeEnvironment{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return "", err
			}
			c := meta.FindStatusCondition(got.Status.Conditions, selfhostedv1alpha1.ConditionDegraded)
			if c == nil {
				return "", fmt.Errorf("Degraded not set")
			}
			return c.Message + "|" + got.ResourceVersion, nil
		}
		var first string
		Eventually(func() error { var err error; first, err = snapshot(); return err }, timeout, interval).Should(Succeed())
		Consistently(snapshot, 3*time.Second, interval).Should(Equal(first))
	})

	It("creates a default-deny egress NetworkPolicy when enabled and deletes it when disabled", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, EgressCIDRs: []string{testEgressCIDR}}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		npKey := types.NamespacedName{Name: builders.NetworkPolicyName(env), Namespace: ns}

		np := &networkingv1.NetworkPolicy{}
		Eventually(func() error { return k8sClient.Get(ctx, npKey, np) }, timeout, interval).Should(Succeed())
		Expect(np.Name).To(Equal(envName + "-egress"))
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{selfhostedv1alpha1.LabelEnvironment: envName}))
		Expect(np.Spec.PolicyTypes).To(Equal([]networkingv1.PolicyType{networkingv1.PolicyTypeEgress}))
		Expect(np.Spec.Egress).To(HaveLen(2))
		Expect(np.Spec.Egress[0].To[0].PodSelector.MatchLabels).To(HaveKeyWithValue("k8s-app", "kube-dns"))
		Expect(np.OwnerReferences).To(HaveLen(1))
		Expect(np.OwnerReferences[0].Name).To(Equal(envName))
		By("creating no API server policy in fixed mode, which has no orchestrator")
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: builders.APIServerNetworkPolicyName(env), Namespace: ns}, &networkingv1.NetworkPolicy{}))
		}, time.Second, interval).Should(BeTrue())

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.Runner.NetworkPolicy.Enabled = false
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, npKey, &networkingv1.NetworkPolicy{}))
		}, timeout, interval).Should(BeTrue())
	})

	It("lets the on-demand orchestrator reach the API server and removes both policies when disabled", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, EgressCIDRs: []string{testEgressCIDR}}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		egressKey := types.NamespacedName{Name: builders.NetworkPolicyName(env), Namespace: ns}
		apiKey := types.NamespacedName{Name: builders.APIServerNetworkPolicyName(env), Namespace: ns}

		Eventually(func() error { return k8sClient.Get(ctx, egressKey, &networkingv1.NetworkPolicy{}) }, timeout, interval).Should(Succeed())
		np := &networkingv1.NetworkPolicy{}
		Eventually(func() error { return k8sClient.Get(ctx, apiKey, np) }, timeout, interval).Should(Succeed())
		Expect(np.Name).To(Equal(env.Name + "-egress-apiserver"))
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(builders.OrchestratorSelectorLabels(env)))
		Expect(np.Labels).To(And(HaveKeyWithValue(selfhostedv1alpha1.LabelEnvironment, env.Name), HaveKeyWithValue(selfhostedv1alpha1.LabelPartOf, selfhostedv1alpha1.PartOfValue)))

		eps := &corev1.Endpoints{} //nolint:staticcheck // mirrors the controller read
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "kubernetes"}, eps)).To(Succeed())
		var cidrs []string
		for _, sub := range eps.Subsets {
			for _, a := range sub.Addresses {
				cidrs = append(cidrs, a.IP+"/32")
			}
		}
		Expect(cidrs).NotTo(BeEmpty())
		Expect(np.Spec.Egress).To(HaveLen(1))
		for _, peer := range np.Spec.Egress[0].To {
			Expect(peer.IPBlock.CIDR).To(HaveSuffix("/32"))
			Expect(cidrs).To(ContainElement(peer.IPBlock.CIDR))
		}
		Expect(np.Spec.Egress[0].To).To(HaveLen(len(cidrs)))

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.Runner.NetworkPolicy.Enabled = false
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		for _, k := range []types.NamespacedName{egressKey, apiKey} {
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, k, &networkingv1.NetworkPolicy{}))
			}, timeout, interval).Should(BeTrue(), k.Name)
		}
	})

	It("reports an API server endpoints failure as WorkloadApplyFailed and never applies the default-deny policy", func() {
		envReader.fail.Store(true)
		DeferCleanup(func() { envReader.fail.Store(false) })
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, EgressCIDRs: []string{testEgressCIDR}}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)

		Eventually(func(g Gomega) {
			c, err := condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable)()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(c.Reason).To(Equal(selfhostedv1alpha1.ReasonWorkloadApplyFailed))
			g.Expect(c.Message).To(ContainSubstring("injected endpoints failure"))
		}, timeout, interval).Should(Succeed())
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: builders.NetworkPolicyName(env), Namespace: ns}, &networkingv1.NetworkPolicy{}))
		}, 2*time.Second, interval).Should(BeTrue())
	})

	It("marks the environment Degraded when runner pods keep failing at start", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())

		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "platform-runner-abc", Namespace: ns,
			Labels: builders.RunnerSelectorLabels(env)},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: builders.RunnerContainerName, Image: runnerImage}}}}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		now := metav1.Now()
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: builders.RunnerContainerName, RestartCount: 3,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-5 * time.Second)), FinishedAt: now,
				Message: "[runner:fatal] --use-anthropic-git-proxy requires --capacity 1"}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(
			And(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonRunnerFailedStart),
				HaveField("Message", ContainSubstring("[runner:fatal]"))))
	})
})

func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }
