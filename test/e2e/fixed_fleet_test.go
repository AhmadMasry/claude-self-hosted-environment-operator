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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

const (
	e2eNamespace    = "claude-e2e"
	deployName      = "e2e-runner"
	configHashQuery = `jsonpath={.spec.template.metadata.annotations.selfhosted\.claudecode\.dev/config-hash}`
)

// fixedFleetSpecs registers the fixed-mode specs. They are called from inside the
// Manager Describe so they run after the operator is deployed and Running.
func fixedFleetSpecs() {
	Context("Fixed fleet", Ordered, func() {
		BeforeAll(func() {
			By("creating a namespace that enforces the Restricted Pod Security Standard")
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", e2eNamespace))
			_, err := utils.Run(exec.Command("kubectl", "label", "ns", e2eNamespace,
				"pod-security.kubernetes.io/enforce=restricted",
				"pod-security.kubernetes.io/enforce-version=latest", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "create", "secret", "generic",
				"claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e"))
			Expect(err).NotTo(HaveOccurred())
			By("creating the hooks and wrapper ConfigMaps the environment mounts")
			_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdataPath("fixed-fleet-configmaps.yaml")))
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", e2eNamespace, "--wait=false"))
		})

		It("becomes Ready with Restricted-compliant runner pods", func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdataPath("fixed-fleet-stub.yaml")))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "claudeenvironment", "e2e",
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			out, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", deployName,
				"-o", "jsonpath={.spec.template.spec.terminationGracePeriodSeconds}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("80"))

			By("checking the runner args point at the nested hooks and wrapper mounts the stub verified")
			out, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", deployName,
				"-o", "jsonpath={.spec.template.spec.containers[0].args}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(And(ContainSubstring(`"--hooks-dir","/etc/claude/hooks"`),
				ContainSubstring(`"--exec-path","/etc/claude/wrapper/wrap.sh"`)))

			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "pods",
					"-l", "selfhosted.claudecode.dev/environment=e2e", "-o", "jsonpath={.items[*].status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.Fields(out)).To(Equal([]string{"Running", "Running"}))
			}, time.Minute, 2*time.Second).Should(Succeed())

			By("checking the egress NetworkPolicy exists (kind's default CNI does not enforce it)")
			out, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "networkpolicy", "e2e-egress",
				"-o", "jsonpath={.spec.policyTypes} {.spec.egress[1].to[0].ipBlock.cidr}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("Egress"))
			Expect(out).To(ContainSubstring("10.0.0.0/8"))

			By("checking no pod was rejected by the Pod Security admission controller")
			out, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "events",
				"--field-selector", "reason=FailedCreate", "-o", "name"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(BeEmpty(), fmt.Sprintf("FailedCreate events: %s", out))
		})

		It("rolls the fleet when the Secret is rotated", func() {
			before, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", deployName,
				"-o", configHashQuery))
			Expect(err).NotTo(HaveOccurred())
			patch := `{"stringData":{"environment-secret":"ccenvkey_rotated"}}`
			_, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "patch", "secret", "claude-env-secret",
				"-p", patch))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				after, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", deployName,
					"-o", configHashQuery))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(after).NotTo(Equal(before))
			}, time.Minute, 2*time.Second).Should(Succeed())
		})
	})
}
