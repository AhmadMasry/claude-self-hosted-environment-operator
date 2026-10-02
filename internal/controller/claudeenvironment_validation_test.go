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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func baseEnv(name string) *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: envSecretName},
			Runner:               selfhostedv1alpha1.RunnerSpec{Image: "registry.local:5000/runner:2.1.280"},
			Fixed:                &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)},
		},
	}
}

var _ = Describe("ClaudeEnvironment CEL validation", func() {
	ctx := context.Background()

	DescribeTable("rejects invalid specs",
		func(mutate func(*selfhostedv1alpha1.ClaudeEnvironment), substr string) {
			env := baseEnv("invalid")
			mutate(env)
			err := k8sClient.Create(ctx, env)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(substr))
		},
		Entry("both modes", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		}, "exactly one of spec.fixed or spec.onDemand"),
		Entry("no mode", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
		}, "exactly one of spec.fixed or spec.onDemand"),
		Entry("onDemand with capacity 2", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
			e.Spec.Runner.Capacity = 2
		}, "onDemand requires runner.capacity == 1"),
		Entry("git proxy with capacity 2", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Capacity = 2
			e.Spec.Runner.Settings.UseAnthropicGitProxy = true
		}, "useAnthropicGitProxy requires runner.capacity == 1"),
		Entry("persistent workspace without lock", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{}
		}, "requires runner.settings.lockToAccount"),
		Entry("persistent workspace with empty lock", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{}
			e.Spec.Runner.Settings.LockToAccount = ""
		}, "requires runner.settings.lockToAccount"),
		Entry("latest tag", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner:latest"
		}, "must not use :latest"),
		Entry("registry port but no tag", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local:5000/runner"
		}, "must carry a tag or digest"),
		Entry("operator-owned flag in extraArgs", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.ExtraArgs = []string{"--capacity=4"}
		}, "operator-owned flags"),
		Entry("hook timeout too close to spawn lease", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{Orchestrator: selfhostedv1alpha1.OrchestratorSpec{
				ExpectedSpawnSeconds: 60, HookTimeoutSeconds: 60}}
		}, "hookTimeoutSeconds + 5 must be below"),
	)

	DescribeTable("accepts valid specs and applies defaults",
		func(mutate func(*selfhostedv1alpha1.ClaudeEnvironment)) {
			env := baseEnv("valid")
			mutate(env)
			customCapacity := env.Spec.Runner.Capacity != 0
			Expect(k8sClient.Create(ctx, env)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, env) })
			if !customCapacity {
				Expect(env.Spec.Runner.Capacity).To(Equal(int32(1)))
			}
			Expect(env.Spec.Runner.BaseDir).To(Equal("/workspace"))
			Expect(*env.Spec.Runner.Settings.HealthPort).To(Equal(int32(8080)))
			Expect(*env.Spec.Runner.Settings.SessionStopGraceSeconds).To(Equal(int32(5)))
			Expect(env.Spec.EnvironmentSecretRef.Key).To(Equal("environment-secret"))
		},
		Entry("tag on registry with port", func(e *selfhostedv1alpha1.ClaudeEnvironment) {}),
		Entry("digest", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}),
		Entry("fixed fleet with capacity 4 and no git proxy", func(e *selfhostedv1alpha1.ClaudeEnvironment) { e.Spec.Runner.Capacity = 4 }),
		Entry("onDemand defaults", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		}),
	)
})
