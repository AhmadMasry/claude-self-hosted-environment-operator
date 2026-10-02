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
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

// upgradeNamespace matches the namespace hard-coded in the fixed-fleet testdata.
const upgradeNamespace = e2eNamespace

// upgradeSpecs installs the previous revision's installer, creates an
// environment with it, then applies the current installer and asserts the
// environment stays valid and becomes Ready under the new manager. Runs
// before the other specs because it installs the current manager itself.
func upgradeSpecs() {
	fromInstaller := os.Getenv("UPGRADE_FROM_INSTALLER")
	fromImage := os.Getenv("UPGRADE_FROM_IMAGE")

	Context("Upgrade from the previous revision", func() {
		BeforeAll(func() {
			if fromInstaller == "" || fromImage == "" {
				Skip("UPGRADE_FROM_INSTALLER and UPGRADE_FROM_IMAGE not set")
			}
			By("loading the previous image into kind")
			Expect(utils.LoadImageToKindClusterWithName(fromImage)).To(Succeed())
			By("installing the previous revision")
			cmd := exec.Command("kubectl", "apply", "--server-side", "-f", fromInstaller)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			cmd = exec.Command("kubectl", "rollout", "status", "-n", namespace, "deployment/claude-selfhosted-operator-controller-manager", "--timeout=120s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			By("creating a namespace and environment under the previous revision")
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", upgradeNamespace))
			_, err = utils.Run(exec.Command("kubectl", "label", "ns", upgradeNamespace, "pod-security.kubernetes.io/enforce=restricted", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			By("confirming the previous CRD accepts a reserved-path volumeMount")
			_, err = utils.Run(exec.Command("kubectl", "apply", "--dry-run=server", "-n", upgradeNamespace, "-f", testdataPath("invalid-env.yaml")))
			Expect(err).NotTo(HaveOccurred(), "the previous revision must not carry the reserved-path rule")
			_, err = utils.Run(exec.Command("kubectl", "create", "secret", "generic", "-n", upgradeNamespace,
				"claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-n", upgradeNamespace, "-f", testdataPath("fixed-fleet-configmaps.yaml")))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-n", upgradeNamespace, "-f", testdataPath("fixed-fleet-stub.yaml")))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "claudeenvironment", "e2e", "-n", upgradeNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			if fromInstaller == "" || fromImage == "" {
				return
			}
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", upgradeNamespace, "--ignore-not-found", "--wait=true", "--timeout=120s"))
		})

		It("applies the current installer over the previous one and keeps the environment Ready", func() {
			By("applying the current installer")
			cmd := exec.Command("make", "deploy", "IMG="+managerImage)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			By("waiting for the manager to roll to the new image")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "deployment", "-n", namespace, "claude-selfhosted-operator-controller-manager", "-o", "jsonpath={.spec.template.spec.containers[?(@.name=='manager')].image}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal(managerImage))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
			_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "-n", namespace, "deployment/claude-selfhosted-operator-controller-manager", "--timeout=120s"))
			Expect(err).NotTo(HaveOccurred())
			By("asserting the CRDs carry the current schema and CEL rules")
			out, err := utils.Run(exec.Command("kubectl", "get", "crd", "claudeenvironments.selfhosted.claudecode.dev", "-o", "jsonpath={.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.runner.x-kubernetes-validations}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("baseDir"), "the runner CEL rules from this revision must be present after upgrade")
			By("asserting the runner Deployment converges under the new manager")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "deployment", deployName, "-n", upgradeNamespace,
					"-o", "jsonpath={.status.observedGeneration} {.metadata.generation} {.status.updatedReplicas} {.status.readyReplicas} {.spec.replicas}"))
				g.Expect(err).NotTo(HaveOccurred())
				f := strings.Fields(out)
				g.Expect(f).To(HaveLen(5))
				g.Expect(f[0]).To(Equal(f[1]), "rollout not observed")
				g.Expect(f[2]).To(Equal(f[4]), "not all replicas updated")
				g.Expect(f[3]).To(Equal(f[4]), "not all replicas ready")
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
			By("asserting the pre-existing environment stays Ready and valid")
			Consistently(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "claudeenvironment", "e2e", "-n", upgradeNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 60*time.Second, 5*time.Second).Should(Succeed())
			events, err := utils.Run(exec.Command("kubectl", "get", "events", "-n", upgradeNamespace, "--field-selector=reason=FailedCreate", "-o", "name"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(events)).To(BeEmpty())
			By("asserting the new manager still rejects an invalid environment")
			_, err = utils.Run(exec.Command("kubectl", "apply", "--dry-run=server", "-n", upgradeNamespace, "-f", testdataPath("invalid-env.yaml")))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("volumeMounts must not target /etc/claude, /home/runner or /tmp"))
		})
	})
}
