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

package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	testOrchestratorName = "platform-orchestrator"
	testAPIGroup         = "selfhosted.claudecode.dev"
	verbGet              = "get"
	verbCreate           = "create"
	logLevelDebug        = "debug"
	testSecretFile       = "/etc/claude/environment-secret"
	testHooksDir         = "/etc/claude/hooks"
	healthPortFlag       = "--health-port"
	logLevelFlag         = "--log-level"
	healthzURLPath       = "/healthz"
)

func TestOrchestratorRBAC(t *testing.T) {
	env := onDemandEnv()
	sa := OrchestratorServiceAccount(env)
	if sa.Name != testOrchestratorName || sa.Namespace != testNamespace || sa.Kind != "ServiceAccount" {
		t.Fatalf("service account wrong: %+v", sa.ObjectMeta)
	}
	role := OrchestratorRole(env)
	if role.Kind != "Role" || role.Name != testOrchestratorName {
		t.Fatal("role identity wrong")
	}
	wantRules := []rbacv1.PolicyRule{
		{APIGroups: []string{testAPIGroup}, Resources: []string{"claudeenvironments"}, Verbs: []string{verbGet}},
		{APIGroups: []string{testAPIGroup}, Resources: []string{"clauderunners"}, Verbs: []string{verbCreate, verbGet}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{verbCreate, "patch", "delete"}},
	}
	if len(role.Rules) != len(wantRules) {
		t.Fatalf("rules: got %+v", role.Rules)
	}
	for i := range wantRules {
		if !equalRule(role.Rules[i], wantRules[i]) {
			t.Fatalf("rule %d: got %+v want %+v", i, role.Rules[i], wantRules[i])
		}
	}

	env.Spec.OnDemand.MaxConcurrentRunners = 5
	role = OrchestratorRole(env)
	if !equalRule(role.Rules[1], rbacv1.PolicyRule{APIGroups: []string{testAPIGroup}, Resources: []string{"clauderunners"}, Verbs: []string{verbCreate, verbGet, "list"}}) {
		t.Fatalf("list must be granted only with a cap: %+v", role.Rules[1])
	}

	rb := OrchestratorRoleBinding(env)
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != testOrchestratorName || rb.Subjects[0].Kind != "ServiceAccount" || rb.Subjects[0].Name != testOrchestratorName {
		t.Fatalf("rolebinding wrong: %+v", rb)
	}
}

func equalRule(a, b rbacv1.PolicyRule) bool {
	eq := func(x, y []string) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	return eq(a.APIGroups, b.APIGroups) && eq(a.Resources, b.Resources) && eq(a.Verbs, b.Verbs)
}

func TestOrchestratorArgs(t *testing.T) {
	env := onDemandEnv()
	env.Spec.OnDemand.Orchestrator.MinIdle = 2
	env.Spec.OnDemand.Orchestrator.LogLevel = logLevelDebug
	got := OrchestratorArgs(env, testSecretFile)
	want := []string{"self-hosted-runner", "orchestrator",
		"--environment-secret-file", testSecretFile,
		"--hooks-dir", testHooksDir,
		healthPortFlag, "8080",
		"--expected-spawn-seconds", "120",
		"--hook-timeout", "60",
		"--hook-concurrency", "4",
		"--min-idle", "2",
		logLevelFlag, logLevelDebug}
	if len(got) != len(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestOrchestratorDeployment(t *testing.T) {
	env := onDemandEnv()
	env.Spec.OnDemand.Orchestrator.Env = []corev1.EnvVar{{Name: "HTTPS_PROXY", Value: "http://proxy:3128"}}
	env.Spec.OnDemand.MaxConcurrentRunners = 7
	env.Spec.OnDemand.Orchestrator.PodTemplate.NodeSelector = map[string]string{"pool": "ops"}
	d := OrchestratorDeployment(env, "h9", "ghcr.io/x/operator:1.0")
	if d.Name != testOrchestratorName || d.Kind != "Deployment" || *d.Spec.Replicas != 1 {
		t.Fatalf("deployment identity wrong: %s %s %v", d.Name, d.Kind, d.Spec.Replicas)
	}
	ps := d.Spec.Template.Spec
	if d.Spec.Template.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleOrchestrator ||
		d.Spec.Template.Labels[selfhostedv1alpha1.LabelPartOf] != selfhostedv1alpha1.PartOfValue ||
		d.Spec.Selector.MatchLabels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleOrchestrator {
		t.Fatalf("labels wrong: %v", d.Spec.Template.Labels)
	}
	if d.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h9" {
		t.Fatal("hash annotation missing")
	}
	if ps.ServiceAccountName != testOrchestratorName || ps.AutomountServiceAccountToken == nil || !*ps.AutomountServiceAccountToken {
		t.Fatal("orchestrator needs its ServiceAccount token for the hook")
	}
	if ps.NodeSelector["pool"] != "ops" {
		t.Fatal("podTemplate overrides not applied")
	}
	if len(ps.InitContainers) != 1 || ps.InitContainers[0].Image != "ghcr.io/x/operator:1.0" || ps.InitContainers[0].Name != HookInitContainerName {
		t.Fatalf("init container wrong: %+v", ps.InitContainers)
	}
	initCmd := ps.InitContainers[0].Command
	if len(initCmd) != 1 || initCmd[0] != "/spawn-runner" {
		t.Fatalf("init container must run the hook binary, not the image entrypoint: %v", initCmd)
	}
	initArgs := ps.InitContainers[0].Args
	if len(initArgs) != 2 || initArgs[0] != "--install" || initArgs[1] != HookInstallDir {
		t.Fatalf("init args wrong: %v", initArgs)
	}
	checkOrchestratorContainer(t, ps)
	checkOrchestratorMountsAndSecurity(t, ps)

	env.Spec.OnDemand.Orchestrator.Image = "r:orch"
	if OrchestratorDeployment(env, "h", "op").Spec.Template.Spec.Containers[0].Image != "r:orch" {
		t.Fatal("explicit orchestrator image not honoured")
	}
}

func checkOrchestratorContainer(t *testing.T, ps corev1.PodSpec) {
	t.Helper()
	c := ps.Containers[0]
	if c.Name != OrchestratorContainerName || c.Image != "r:1" {
		t.Fatalf("orchestrator image must default to the runner image: %+v", c.Image)
	}
	if c.Args[1] != "orchestrator" {
		t.Fatalf("args wrong: %v", c.Args)
	}
	envByName := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		envByName[e.Name] = e
	}
	if envByName[selfhostedv1alpha1.EnvHookEnvironment].Value != testEnvName ||
		envByName[selfhostedv1alpha1.EnvHookNamespace].ValueFrom.FieldRef.FieldPath != "metadata.namespace" ||
		envByName[selfhostedv1alpha1.EnvHookMaxConcurrentRunners].Value != "7" ||
		envByName["HTTPS_PROXY"].Value != "http://proxy:3128" {
		t.Fatalf("hook env wrong: %+v", c.Env)
	}
	if c.ReadinessProbe.Exec == nil || c.ReadinessProbe.Exec.Command[0] != "/etc/claude/hooks/spawn-runner" || c.ReadinessProbe.Exec.Command[1] != "--probe-connected" {
		t.Fatalf("readiness must use the injected binary: %+v", c.ReadinessProbe)
	}
	if c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != healthzURLPath {
		t.Fatal("liveness must be the HTTP probe")
	}
}

func checkOrchestratorMountsAndSecurity(t *testing.T, ps corev1.PodSpec) {
	t.Helper()
	c := ps.Containers[0]
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m
	}
	if !mounts["environment-secret"].ReadOnly || mounts["environment-secret"].MountPath != "/etc/claude" ||
		!mounts["hooks"].ReadOnly || mounts["hooks"].MountPath != testHooksDir {
		t.Fatalf("mounts wrong: %+v", c.VolumeMounts)
	}
	for _, cc := range append(ps.InitContainers, ps.Containers...) {
		sc := cc.SecurityContext
		if sc == nil || *sc.AllowPrivilegeEscalation || !*sc.ReadOnlyRootFilesystem || sc.Capabilities.Drop[0] != "ALL" {
			t.Fatalf("container %s not restricted", cc.Name)
		}
	}
	if ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatal("pod security context not restricted")
	}
	if *ps.TerminationGracePeriodSeconds != 30 {
		t.Fatal("orchestrator grace period should be the default 30")
	}
}

func TestOrchestratorConfigHash(t *testing.T) {
	env := onDemandEnv()
	sec := &corev1.Secret{Data: map[string][]byte{"environment-secret": []byte("k1")}}
	base := OrchestratorConfigHash(env, sec, "op:1")
	if len(base) != 16 || OrchestratorConfigHash(env, sec, "op:1") != base {
		t.Fatal("hash must be 16 hex chars and deterministic")
	}
	e2 := env.DeepCopy()
	e2.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 300
	if OrchestratorConfigHash(e2, sec, "op:1") == base {
		t.Fatal("orchestrator settings must change the hash")
	}
	if OrchestratorConfigHash(env, sec, "op:2") == base {
		t.Fatal("hook image must change the hash")
	}
	s2 := sec.DeepCopy()
	s2.Data["environment-secret"] = []byte("k2")
	if OrchestratorConfigHash(env, s2, "op:1") == base {
		t.Fatal("secret rotation must change the hash")
	}
	e3 := env.DeepCopy()
	e3.Spec.OnDemand.Orchestrator.Replicas = ptr.To[int32](3)
	if OrchestratorConfigHash(e3, sec, "op:1") != base {
		t.Fatal("replicas must not change the hash")
	}
}
