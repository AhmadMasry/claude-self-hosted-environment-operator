//go:build e2e
// +build e2e

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

package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

const aclNamespace = "claude-e2e-acl"

// openRunner is the ClaudeRunner the hook creates in the "open" environment,
// whose access list names the stub orchestrator's account.
const openRunner = "open-e2e-order-1"

func kubectlACL(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", aclNamespace}, args...)...))
}

// accessListSpecs registers the access-list specs. The stub orchestrator runs
// the injected hook for account user_e2e, so the hook reads each list with the
// orchestrator's real ServiceAccount and Role.
func accessListSpecs() {
	Context("Access list", Ordered, func() {
		BeforeAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", aclNamespace))
			_, err := utils.Run(exec.Command("kubectl", "label", "ns", aclNamespace,
				"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/enforce-version=latest", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectlACL("create", "secret", "generic", "claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e")
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdataPath("access-list-stub.yaml")))
			Expect(err).NotTo(HaveOccurred())
		})
		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", aclNamespace, "--wait=false"))
		})

		It("starts a runner for an account on the list", func() {
			Eventually(func(g Gomega) {
				out, err := kubectlACL("get", "clauderunner", openRunner, "-o", "jsonpath={.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Running"))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())
			out, err := kubectlACL("get", "claudeenvironment", "open", "-o", `jsonpath={.status.conditions[?(@.type=="AccessRestricted")].reason}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("AccessListEnforced"))
			logs, err := kubectlACL("logs", "deploy/open-orchestrator", "-c", "orchestrator")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring(`"accountID":"user_e2e"`))
			Expect(logs).To(ContainSubstring(`"authorization":"allowed"`))
		})

		It("refuses an account that is not on the list and creates nothing for it", func() {
			Eventually(func(g Gomega) {
				logs, err := kubectlACL("logs", "deploy/closed-orchestrator", "-c", "orchestrator")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("hook run 2 exit 2"))
				// "denied" proves the hook read the list: a Role without the grant
				// would end as outcome "error".
				g.Expect(logs).To(ContainSubstring(`"outcome":"denied"`))
				g.Expect(logs).To(ContainSubstring("account user_e2e is not on this self-hosted environment's access list"))
				g.Expect(logs).NotTo(ContainSubstring("hook run 1 exit 0"))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())
			out, err := kubectlACL("get", "clauderunner", "-l", "claude.cloudeng.ps/environment=closed", "-o", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(BeEmpty(), "a refused session must not leave a ClaudeRunner")
		})

		It("lets each orchestrator read its own list and no other ConfigMap", func() {
			sa := "system:serviceaccount:" + aclNamespace + ":closed-orchestrator"
			for resource, want := range map[string]string{
				"configmap/closed-access": "yes",
				"configmap/open-access":   "no",
			} {
				out, _ := kubectlACL("auth", "can-i", "get", resource, "--as", sa)
				Expect(strings.TrimSpace(out)).To(Equal(want), resource)
			}
			for _, verb := range []string{"list", "update", "patch", "delete"} {
				out, _ := kubectlACL("auth", "can-i", verb, "configmap/closed-access", "--as", sa)
				Expect(strings.TrimSpace(out)).To(Equal("no"), verb)
			}
		})

		It("stops the running session once its account is taken off the list", func() {
			_, err := kubectlACL("patch", "configmap", "open-access", "--type=merge", "-p", `{"data":{"accounts":"user_other\n"}}`)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := kubectlACL("get", "clauderunner", openRunner, "-o", "jsonpath={.status.phase}/{.status.reason}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Failed/Unauthorized"))
				_, err = kubectlACL("get", "pod", openRunner)
				g.Expect(err).To(HaveOccurred(), "the runner pod must be deleted")
				g.Expect(err.Error()).To(Or(ContainSubstring("NotFound"), ContainSubstring("not found")))
			}, time.Minute, 2*time.Second).Should(Succeed())

			By("collecting the stopped runner after its TTL, like any finished runner")
			Eventually(func(g Gomega) {
				_, err := kubectlACL("get", "clauderunner", openRunner)
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(Or(ContainSubstring("NotFound"), ContainSubstring("not found")))
			}, time.Minute, 3*time.Second).Should(Succeed())
		})
	})
}
