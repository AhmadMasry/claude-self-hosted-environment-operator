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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics/metricstest"
)

const (
	accessListName = "platform-access"
	accessListKey  = "accounts"
	alice          = "user_01EXAMPLE0000000000000001"
	bob            = "user_01EXAMPLE0000000000000002"
)

func accessListSpec(mode selfhostedv1alpha1.AuthorizationMode) *selfhostedv1alpha1.AuthorizationSpec {
	return &selfhostedv1alpha1.AuthorizationSpec{Mode: mode, AllowedAccountsFrom: selfhostedv1alpha1.AccountListSource{
		ConfigMapKeyRef: selfhostedv1alpha1.ConfigMapKeyRef{Name: accessListName, Key: accessListKey}}}
}

func accessList(ns string, lines ...string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: accessListName, Namespace: ns},
		Data: map[string]string{accessListKey: strings.Join(lines, "\n")}}
}

// restrictedEnv is an on-demand environment with an access list; an empty mode
// is left for the API server to default.
func restrictedEnv(ns string, mode selfhostedv1alpha1.AuthorizationMode) *selfhostedv1alpha1.ClaudeEnvironment {
	env := onDemandEnvObj(ns)
	env.Spec.Authorization = accessListSpec(mode)
	return env
}

// startRunner creates the work order and the ClaudeRunner the hook would for account.
func startRunner(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, order, account string) types.NamespacedName {
	ExpectWithOffset(1, k8sClient.Create(ctx, hookWorkOrder(env, order))).To(Succeed())
	r := ownedBy(runnerObj(env.Namespace, order), env)
	r.Spec.AccountID = account
	ExpectWithOffset(1, k8sClient.Create(ctx, r)).To(Succeed())
	return client.ObjectKeyFromObject(r)
}

// runUntilRunning starts a runner for account and reports its pod Running.
func runUntilRunning(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, order, account string) types.NamespacedName {
	key := startRunner(ctx, env, order, account)
	EventuallyWithOffset(1, func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
	setPodPhase(ctx, key, corev1.PodRunning, nil)
	EventuallyWithOffset(1, runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))
	return key
}

func setAccessList(ctx context.Context, ns string, lines ...string) {
	ExpectWithOffset(1, k8sClient.Update(ctx, accessList(ns, lines...))).To(Succeed())
}

func updateEnv(ctx context.Context, key types.NamespacedName, mutate func(*selfhostedv1alpha1.ClaudeEnvironment)) {
	ExpectWithOffset(1, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		env := &selfhostedv1alpha1.ClaudeEnvironment{}
		if err := k8sClient.Get(ctx, key, env); err != nil {
			return err
		}
		mutate(env)
		return k8sClient.Update(ctx, env)
	})).To(Succeed())
}

// expectStopped waits for the runner to fail as Unauthorized with its pod gone
// and returns the message it carries.
func expectStopped(ctx context.Context, key types.NamespacedName) string {
	EventuallyWithOffset(1, runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
	got := &selfhostedv1alpha1.ClaudeRunner{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, got)).To(Succeed())
	ExpectWithOffset(1, got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonUnauthorized))
	EventuallyWithOffset(1, func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue())
	return got.Status.Message
}

var _ = Describe("Access list", func() {
	ctx := context.Background()

	It("refuses an account that is not on the list, and a session without one, before any pod exists", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice))).To(Succeed())
		env := restrictedEnv(ns, "")
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(env.Spec.Authorization.Mode).To(Equal(selfhostedv1alpha1.AuthorizationEnforce), "an unset mode must default to Enforce")

		Expect(expectStopped(ctx, startRunner(ctx, env, "order-denied", bob))).To(ContainSubstring("account " + bob + " is not on"))
		Expect(expectStopped(ctx, startRunner(ctx, env, "order-anon", ""))).To(ContainSubstring("no user account ID"))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeUnauthorized)).To(Equal(2.0))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeFailed)).To(Equal(2.0))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeCreated)).To(Equal(0.0), "no pod may be created for a refused account")
	})

	It("stops a running session once its account is taken off the list, and leaves the others alone", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice+"  # lead", bob))).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		aliceRunner := runUntilRunning(ctx, env, "order-alice", alice)
		bobRunner := runUntilRunning(ctx, env, "order-bob", bob)

		setAccessList(ctx, ns, bob)
		Expect(expectStopped(ctx, aliceRunner)).To(ContainSubstring("account " + alice + " is not on"))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeUnauthorized)).To(Equal(1.0))
		Consistently(runnerPhase(ctx, bobRunner), "2s", interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))
		Expect(k8sClient.Get(ctx, bobRunner, &corev1.Pod{})).To(Succeed())
	})

	It("stops a runner that names the environment without carrying its label", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice))).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, hookWorkOrder(env, "order-bare"))).To(Succeed())
		r := ownedBy(runnerObj(ns, "order-bare"), env)
		r.Labels, r.Spec.AccountID = nil, alice
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))

		setAccessList(ctx, ns, bob)
		Expect(expectStopped(ctx, key)).To(ContainSubstring("account " + alice + " is not on"))
	})

	It("stops nobody while the list cannot be read, and applies the edit once it can", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice))).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := runUntilRunning(ctx, env, "order-unread", alice)

		runnerReader.fail.Store(true)
		DeferCleanup(func() { runnerReader.fail.Store(false) })
		setAccessList(ctx, ns, bob)
		Consistently(runnerPhase(ctx, key), "2s", interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))
		Expect(k8sClient.Get(ctx, key, &corev1.Pod{})).To(Succeed())

		runnerReader.fail.Store(false)
		Expect(expectStopped(ctx, key)).To(ContainSubstring("account " + alice + " is not on"))
	})

	It("records a session that finished as Succeeded even when its account has since left the list", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice))).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := runUntilRunning(ctx, env, "order-done", alice)

		// The read failure keeps the controller from acting on the edit until
		// the pod has finished; the pass that follows sees both at once.
		runnerReader.fail.Store(true)
		DeferCleanup(func() { runnerReader.fail.Store(false) })
		setAccessList(ctx, ns, bob)
		setPodPhase(ctx, key, corev1.PodSucceeded, &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.Now()})
		runnerReader.fail.Store(false)

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerSucceeded))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeUnauthorized)).To(Equal(0.0))
	})

	It("stops every running session when the list is deleted", func() {
		ns := newNamespace(ctx)
		list := accessList(ns, alice)
		Expect(k8sClient.Create(ctx, list)).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := runUntilRunning(ctx, env, "order-gone", alice)

		Expect(k8sClient.Delete(ctx, list)).To(Succeed())
		Expect(expectStopped(ctx, key)).To(ContainSubstring("access list is unavailable"))
	})

	It("lets an unlisted account run in Audit mode, reports it, and stops it once the mode is Enforce", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, accessList(ns, alice))).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationAudit)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := runUntilRunning(ctx, env, "order-audit", bob)
		Eventually(eventMessages(ctx, ns, selfhostedv1alpha1.ReasonUnauthorizedAudit), timeout, interval).
			Should(ContainElement(ContainSubstring("account " + bob + " is not on")))
		Expect(metricstest.RunnersTotal(ns, envName, metrics.OutcomeUnauthorized)).To(Equal(0.0))

		updateEnv(ctx, client.ObjectKeyFromObject(env), func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Authorization.Mode = selfhostedv1alpha1.AuthorizationEnforce
		})
		Expect(expectStopped(ctx, key)).To(ContainSubstring("account " + bob + " is not on"))
	})

	It("reports the access list on the environment and grants the orchestrator that one ConfigMap", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		list := accessList(ns, alice, bob)
		Expect(k8sClient.Create(ctx, list)).To(Succeed())
		env := restrictedEnv(ns, selfhostedv1alpha1.AuthorizationEnforce)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		orch := types.NamespacedName{Name: orchestratorObjName, Namespace: ns}
		grant := rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}, ResourceNames: []string{accessListName}}
		roleRules := func() ([]rbacv1.PolicyRule, error) {
			role := &rbacv1.Role{}
			return role.Rules, k8sClient.Get(ctx, orch, role)
		}

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionAccessRestricted), timeout, interval).Should(And(
			haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonAccessListEnforced), HaveField("Message", ContainSubstring("2 accounts allowed"))))
		Eventually(roleRules, timeout, interval).Should(ContainElement(grant))

		By("rejecting standby runners added to a restricted environment later")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			got := &selfhostedv1alpha1.ClaudeEnvironment{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return err
			}
			got.Spec.OnDemand.Orchestrator.MinIdle = 1
			return k8sClient.Update(ctx, got)
		})).To(MatchError(ContainSubstring("authorization cannot be combined with onDemand.orchestrator.minIdle")))

		By("ignoring lines that are not account IDs and reporting their numbers, never their text")
		setAccessList(ctx, ns, alice, "bob@example.com", "user_")
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(And(
			haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonAccessListInvalid),
			HaveField("Message", And(ContainSubstring("lines 2, 3 are not account IDs"), Not(ContainSubstring("bob@example.com"))))))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionAccessRestricted), timeout, interval).Should(
			HaveField("Message", ContainSubstring("1 account allowed")))
		Expect(eventMessages(ctx, ns, selfhostedv1alpha1.ReasonAccessListInvalid)()).NotTo(ContainElement(ContainSubstring("bob@example.com")))

		By("saying that everyone is refused once the list is gone")
		Expect(k8sClient.Delete(ctx, list)).To(Succeed())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(And(
			haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonAccessListMissing), HaveField("Message", ContainSubstring("every session is refused"))))

		By("reporting Audit mode as not restricting")
		updateEnv(ctx, key, func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Authorization.Mode = selfhostedv1alpha1.AuthorizationAudit
		})
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionAccessRestricted), timeout, interval).Should(
			haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonAccessListAudited))

		By("dropping the condition and the grant with spec.authorization")
		updateEnv(ctx, key, func(e *selfhostedv1alpha1.ClaudeEnvironment) { e.Spec.Authorization = nil })
		Eventually(func() (*metav1.Condition, error) {
			got := &selfhostedv1alpha1.ClaudeEnvironment{}
			err := k8sClient.Get(ctx, key, got)
			return meta.FindStatusCondition(got.Status.Conditions, selfhostedv1alpha1.ConditionAccessRestricted), err
		}, timeout, interval).Should(BeNil())
		Eventually(roleRules, timeout, interval).ShouldNot(ContainElement(grant))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))
	})
})
