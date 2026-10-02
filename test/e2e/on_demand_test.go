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

const odNamespace = "claude-e2e-od"

func kubectlOD(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", odNamespace}, args...)...))
}

// onDemandSpecs registers the on-demand specs. The stub image stands in for both
// the orchestrator, which runs the injected hook twice for one order, and the runner.
func onDemandSpecs() {
	Context("On-demand mode", Ordered, func() {
		BeforeAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", odNamespace))
			_, err := utils.Run(exec.Command("kubectl", "label", "ns", odNamespace,
				"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/enforce-version=latest", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectlOD("create", "secret", "generic", "claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e")
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-f", testdataPath("on-demand-stub.yaml")))
			Expect(err).NotTo(HaveOccurred())
		})
		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", odNamespace, "--wait=false"))
		})

		It("brings the orchestrator up with the injected hook and reports Ready", func() {
			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "claudeenvironment", "e2e-od", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
			out, err := kubectlOD("get", "deploy", "e2e-od-orchestrator", "-o", "jsonpath={.spec.template.spec.initContainers[0].image}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal(managerImage), "hook image must be the operator image via OPERATOR_IMAGE")
			By("checking the orchestrator's API server egress policy exists (kind's default CNI does not enforce it)")
			_, err = kubectlOD("get", "networkpolicy", "e2e-od-egress-apiserver")
			Expect(err).NotTo(HaveOccurred())
			out, err = kubectlOD("get", "events", "--field-selector", "reason=FailedCreate", "-o", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(BeEmpty())
		})

		It("spawns exactly one runner for the order, runs it to Succeeded, and garbage-collects it", func() {
			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "clauderunner", "e2e-order-1", "-o", "jsonpath={.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Running"))
			}, 2*time.Minute, 3*time.Second).Should(Succeed())

			out, err := kubectlOD("get", "secret", "e2e-order-1-work-order", "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("ClaudeRunner"))
			out, err = kubectlOD("get", "clauderunner", "-o", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(out)).To(HaveLen(1), "a redelivered order must not create a second runner")
			out, err = kubectlOD("get", "claudeenvironment", "e2e-od", "-o", "jsonpath={.status.onDemand.runningRunners}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("1"))

			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "clauderunner", "e2e-order-1", "-o", "jsonpath={.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Succeeded"))
			}, 2*time.Minute, 3*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				for _, res := range []string{"clauderunner/e2e-order-1", "pod/e2e-order-1", "secret/e2e-order-1-work-order"} {
					_, err := kubectlOD("get", res)
					g.Expect(err).To(HaveOccurred(), res+" should be garbage-collected after the TTL")
					g.Expect(err.Error()).To(Or(ContainSubstring("NotFound"), ContainSubstring("not found")), res)
				}
			}, time.Minute, 3*time.Second).Should(Succeed())

			logs, err := kubectlOD("logs", "deploy/e2e-od-orchestrator", "-c", "orchestrator")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("hook run 1 exit 0"))
			Expect(logs).To(ContainSubstring("hook run 2 exit 0"))
			Expect(logs).NotTo(ContainSubstring("stub-work-order-jwt"), "the hook must never print the work order")
		})

		It("denies the orchestrator identity anything outside its own work orders", func() {
			sa := "system:serviceaccount:" + odNamespace + ":e2e-od-orchestrator"
			const secretMsg = "an orchestrator ServiceAccount may only manage its own environment's Secrets named *-work-order"
			_, err := kubectlOD("create", "secret", "generic", "evil", "--from-literal=k=v", "--as", sa)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(secretMsg))

			_, err = kubectlOD("create", "secret", "generic", "nolabel-work-order", "--from-literal=jwt=x", "--as", sa)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(secretMsg))

			manifest := `apiVersion: v1
kind: Secret
metadata:
  name: ok-work-order
  labels:
    selfhosted.claudecode.dev/environment: e2e-od
stringData:
  jwt: x
`
			apply := exec.Command("kubectl", "-n", odNamespace, "create", "--as", sa, "-f", "-")
			apply.Stdin = strings.NewReader(manifest)
			_, err = utils.Run(apply)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectlOD("delete", "secret", "ok-work-order", "--as", sa)
			Expect(err).NotTo(HaveOccurred())

			uid, err := kubectlOD("get", "claudeenvironment", "e2e-od", "-o", "jsonpath={.metadata.uid}")
			Expect(err).NotTo(HaveOccurred())
			runner := `apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeRunner
metadata:
  name: evil-order
  ownerReferences:
    - apiVersion: selfhosted.claudecode.dev/v1alpha1
      kind: ClaudeEnvironment
      name: e2e-od
      uid: ` + strings.TrimSpace(uid) + `
      controller: true
      blockOwnerDeletion: true
spec:
  environmentRef:
    name: e2e-od
  orderID: other-order
  workOrderSecretRef:
    name: other-order-work-order
`
			apply = exec.Command("kubectl", "-n", odNamespace, "create", "--as", sa, "-f", "-")
			apply.Stdin = strings.NewReader(runner)
			_, err = utils.Run(apply)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("an orchestrator may only create ClaudeRunners owned by its own ClaudeEnvironment that reference their own work order"))

			_, err = kubectlOD("create", "secret", "generic", "evil2", "--from-literal=k=v")
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectlOD("delete", "secret", "evil2")
			Expect(err).NotTo(HaveOccurred())
		})
	})
}
