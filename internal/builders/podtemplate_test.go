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
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func testEnv() *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: "claude"},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: "env-secret", Key: testSecretKey},
			Runner: selfhostedv1alpha1.RunnerSpec{Image: "r:1", Capacity: 1, BaseDir: testBaseDir,
				Settings: selfhostedv1alpha1.RunnerSettings{HealthPort: ptr.To[int32](8080),
					SessionStopGraceSeconds: ptr.To[int32](5), PostSessionHookTimeoutSeconds: ptr.To[int32](60)}},
			Fixed: &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)},
		},
	}
}

func secretInput(env *selfhostedv1alpha1.ClaudeEnvironment) RunnerPodInput {
	return RunnerPodInput{Env: env, ConfigHash: "abc",
		SecretVolume: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "env-secret",
			Items: []corev1.KeyToPath{{Key: testSecretKey, Path: SecretFileName}}}},
		SecretFile: SecretMountPath + "/" + SecretFileName, RestartPolicy: corev1.RestartPolicyAlways}
}

const (
	testSecretKey      = "environment-secret"
	testBaseDir        = "/workspace"
	testCacheName      = "cache"
	testHealthPortName = "health"
)

func TestRunnerPodTemplateRestricted(t *testing.T) {
	tmpl := RunnerPodTemplate(secretInput(testEnv()))
	ps := tmpl.Spec
	if ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatal("runAsNonRoot must be true")
	}
	if ps.SecurityContext.SeccompProfile == nil || ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("seccomp RuntimeDefault required")
	}
	if ps.HostNetwork || ps.HostPID || ps.HostIPC {
		t.Fatal("host namespaces must be off")
	}
	if len(ps.Containers) != 1 || ps.Containers[0].Name != "runner" {
		t.Fatalf("expected one container named runner, got %+v", ps.Containers)
	}
	c := ps.Containers[0]
	checkRunnerContainer(t, c, ps)
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Fatal("service account token must not be mounted by default")
	}
	if tmpl.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "abc" {
		t.Fatal("config hash annotation missing")
	}
	for _, k := range []string{selfhostedv1alpha1.LabelEnvironment, selfhostedv1alpha1.LabelRole, selfhostedv1alpha1.LabelPartOf} {
		if tmpl.Labels[k] == "" {
			t.Fatalf("label %s missing", k)
		}
	}
	wantMounts := map[string]string{testSecretKey: "/etc/claude", "workspace": testBaseDir, "home": "/home/runner", "tmp": "/tmp"}
	for _, m := range c.VolumeMounts {
		if p, ok := wantMounts[m.Name]; ok && m.MountPath == p {
			delete(wantMounts, m.Name)
		}
	}
	if len(wantMounts) != 0 {
		t.Fatalf("missing mounts %v", wantMounts)
	}
	for _, m := range c.VolumeMounts {
		if m.Name == testSecretKey && !m.ReadOnly {
			t.Fatal("secret mount must be read-only")
		}
	}
}

func checkRunnerContainer(t *testing.T, c corev1.Container, ps corev1.PodSpec) {
	t.Helper()
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container security context not restricted: %+v", sc)
	}
	if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Fatal("termination message policy must fall back to logs")
	}
	if c.Ports[0].Name != testHealthPortName || c.Ports[0].ContainerPort != 8080 {
		t.Fatalf("health port wrong: %+v", c.Ports)
	}
	if c.ReadinessProbe.HTTPGet.Path != "/healthz" || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("probes must hit /healthz")
	}
	if *ps.TerminationGracePeriodSeconds != 80 {
		t.Fatalf("grace period %d want 80", *ps.TerminationGracePeriodSeconds)
	}
}

func TestRunnerPodTemplateOptionalMounts(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: "hooks"}
	env.Spec.Runner.WrapperScript = &selfhostedv1alpha1.ConfigMapKeyRef{Name: "wrap", Key: "w.sh"}
	env.Spec.Runner.HostConfig = &selfhostedv1alpha1.ConfigMapRef{Name: "cfg"}
	tmpl := RunnerPodTemplate(secretInput(env))
	c := tmpl.Spec.Containers[0]
	byName := map[string]corev1.Volume{}
	for _, v := range tmpl.Spec.Volumes {
		byName[v.Name] = v
	}
	if *byName["hooks"].ConfigMap.DefaultMode != 0o555 || *byName["wrapper"].ConfigMap.DefaultMode != 0o555 || *byName["host-config"].ConfigMap.DefaultMode != 0o444 {
		t.Fatal("hooks/wrapper must be 0555 and host-config 0444")
	}
	var sawHostCfg bool
	for _, e := range c.Env {
		if e.Name == "SELF_HOSTED_RUNNER_HOST_CONFIG_DIR" && e.Value == HostConfigMountPath {
			sawHostCfg = true
		}
	}
	if !sawHostCfg {
		t.Fatal("SELF_HOSTED_RUNNER_HOST_CONFIG_DIR not set")
	}
	for _, m := range c.VolumeMounts {
		if (m.Name == "hooks" || m.Name == "wrapper" || m.Name == "host-config") && !m.ReadOnly {
			t.Fatalf("%s must be read-only", m.Name)
		}
	}
}

func TestRunnerPodTemplateUserOverrides(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.PodTemplate = selfhostedv1alpha1.PodTemplate{
		Labels:             map[string]string{"team": "a", selfhostedv1alpha1.LabelRole: "hacked"},
		ServiceAccountName: "runner-sa", AutomountServiceAccountToken: new(true),
		SecurityContext: &selfhostedv1alpha1.PodSecurityContext{RunAsUser: ptr.To[int64](10001), FSGroup: ptr.To[int64](10001)},
		Volumes:         []selfhostedv1alpha1.Volume{{Name: testCacheName, EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: testCacheName, MountPath: "/cache"}},
	}
	env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](600)
	tmpl := RunnerPodTemplate(secretInput(env))
	if tmpl.Labels["team"] != "a" || tmpl.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
		t.Fatal("user labels merge but operator labels win")
	}
	if tmpl.Spec.ServiceAccountName != "runner-sa" || !*tmpl.Spec.AutomountServiceAccountToken {
		t.Fatal("service account overrides not applied")
	}
	if *tmpl.Spec.SecurityContext.RunAsUser != 10001 || *tmpl.Spec.SecurityContext.FSGroup != 10001 {
		t.Fatal("identity overrides not applied")
	}
	if *tmpl.Spec.TerminationGracePeriodSeconds != 600 {
		t.Fatal("user grace period must be honoured")
	}
	found := false
	for _, m := range tmpl.Spec.Containers[0].VolumeMounts {
		if m.Name == testCacheName && m.MountPath == "/cache" {
			found = true
		}
	}
	if !found {
		t.Fatal("user volume mount missing")
	}
}

func TestEffectiveGracePeriod(t *testing.T) {
	env := testEnv()
	if s, short := EffectiveGracePeriod(env); s != 80 || short {
		t.Fatalf("computed: got %d %v", s, short)
	}
	env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](30)
	if s, short := EffectiveGracePeriod(env); s != 30 || !short {
		t.Fatalf("too short: got %d %v", s, short)
	}
}

func TestRunnerPodTemplateWorkspaceFromPVC(t *testing.T) {
	in := secretInput(testEnv())
	in.WorkspaceFromPVC = true
	tmpl := RunnerPodTemplate(in)
	for _, v := range tmpl.Spec.Volumes {
		if v.Name == WorkspaceVolume {
			t.Fatal("workspace emptyDir must be omitted when a PVC provides it")
		}
	}
	var mounted bool
	for _, m := range tmpl.Spec.Containers[0].VolumeMounts {
		if m.Name == WorkspaceVolume && m.MountPath == testBaseDir {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("workspace mount must remain")
	}
}

func TestRunnerPodTemplateClientLabelEnv(t *testing.T) {
	tmpl := RunnerPodTemplate(secretInput(testEnv()))
	var found bool
	for _, e := range tmpl.Spec.Containers[0].Env {
		if e.Name == "SELF_HOSTED_RUNNER_CLIENT_LABEL" && e.ValueFrom != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.name" {
			found = true
		}
	}
	if !found {
		t.Fatal("client label must default to the pod name via the downward API")
	}
	env := testEnv()
	env.Spec.Runner.Settings.ClientLabel = "fleet"
	for _, e := range RunnerPodTemplate(secretInput(env)).Spec.Containers[0].Env {
		if e.Name == "SELF_HOSTED_RUNNER_CLIENT_LABEL" {
			t.Fatal("an explicit client label must not add the downward-API env var")
		}
	}
}

func TestRunnerPodTemplateProbePort(t *testing.T) {
	c := RunnerPodTemplate(secretInput(testEnv())).Spec.Containers[0]
	if c.ReadinessProbe.HTTPGet.Port.StrVal != testHealthPortName || c.LivenessProbe.HTTPGet.Port.StrVal != testHealthPortName {
		t.Fatalf("probes must reference the named health port: %v %v", c.ReadinessProbe.HTTPGet.Port, c.LivenessProbe.HTTPGet.Port)
	}
}

// collectRules gathers every x-kubernetes-validations rule below a schema.
func collectRules(s apiextensionsv1.JSONSchemaProps) []string {
	var rules []string
	for _, v := range s.XValidations {
		rules = append(rules, v.Rule)
	}
	for _, p := range s.Properties {
		rules = append(rules, collectRules(p)...)
	}
	if s.Items != nil && s.Items.Schema != nil {
		rules = append(rules, collectRules(*s.Items.Schema)...)
	}
	return rules
}

// The CEL rules in the CRD hard-code the reserved paths; this keeps them in
// step with ReservedMountPaths.
func TestReservedMountPathsMatchCRDRules(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "selfhosted.claudecode.dev_claudeenvironments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	crd := apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	runner := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["runner"]
	var baseDirRule, mountRule string
	for _, rule := range collectRules(runner) {
		switch {
		case strings.Contains(rule, "baseDir"):
			baseDirRule = rule
		case strings.Contains(rule, "volumeMounts"):
			mountRule = rule
		}
	}
	if baseDirRule == "" || mountRule == "" {
		t.Fatalf("runner rules not found: baseDir=%q volumeMounts=%q", baseDirRule, mountRule)
	}
	for _, p := range ReservedMountPaths {
		for name, rule := range map[string]string{"baseDir": baseDirRule, "volumeMounts": mountRule} {
			if !strings.Contains(rule, "'"+p+"'") {
				t.Errorf("reserved path %s missing from the %s rule: %s", p, name, rule)
			}
		}
	}
}
