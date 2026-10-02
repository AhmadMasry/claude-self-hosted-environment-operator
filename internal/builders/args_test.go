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
	"reflect"
	"testing"

	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const debugTokenDirFlag = "--debug-token-dir"

func TestRunnerArgsDefaults(t *testing.T) {
	trustTrue := true
	r := selfhostedv1alpha1.RunnerSpec{Image: "x:1", Capacity: 1, BaseDir: "/workspace",
		Settings: selfhostedv1alpha1.RunnerSettings{ConfineRepoSettings: "warn", LogLevel: "info",
			HealthPort: ptr.To[int32](8080), StartupTimeoutMinutes: ptr.To[int32](15),
			SessionStopGraceSeconds: ptr.To[int32](5), PostSessionHookTimeoutSeconds: ptr.To[int32](60),
			TrustWorkspace: &trustTrue}}
	want := []string{
		"self-hosted-runner",
		"--environment-secret-file", "/etc/claude/environment-secret",
		"--capacity", "1",
		"--base-dir", "/workspace",
		"--health-port", "8080",
		"--confine-repo-settings", "warn",
		"--log-level", "info",
	}
	if got := RunnerArgs(r, "/etc/claude/environment-secret"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestRunnerArgsAllSettings(t *testing.T) {
	trustFalse := false
	r := selfhostedv1alpha1.RunnerSpec{Capacity: 2, BaseDir: "/w",
		Settings: selfhostedv1alpha1.RunnerSettings{
			ConfigureGit: true, ConfineRepoSettings: "enforce", DrainWaitSeconds: 300,
			DeferShutdownMaxMinutes: 5, DrainGraceSeconds: 120, KillSessionAfterMinutes: 240,
			ReleaseIdleSessionMinutes: 30, StartupTimeoutMinutes: ptr.To[int32](10),
			SessionStopGraceSeconds: ptr.To[int32](7), PostSessionHookTimeoutSeconds: ptr.To[int32](90),
			ExitIfUnusedMinutes: 15, RemoveSessionState: true, PushOutcomeOnRelease: true,
			UseAnthropicGitProxy: true, GitHostRewrites: []string{"github.com=ghe.internal"},
			GitSSHRewrites: []string{"ghe.internal"}, LockToAccount: "user_123", ClientLabel: "fleet-a",
			LogLevel: "debug", HealthPort: ptr.To[int32](9090), TrustWorkspace: &trustFalse},
		ExtraArgs:      []string{debugTokenDirFlag, "/tmp/x"},
		LifecycleHooks: &selfhostedv1alpha1.ConfigMapRef{Name: "h"},
		WrapperScript:  &selfhostedv1alpha1.ConfigMapKeyRef{Name: "w", Key: "wrap.sh"},
	}
	got := RunnerArgs(r, "/etc/claude/environment-secret")
	mustContain := [][]string{
		{"--capacity", "2"}, {"--base-dir", "/w"}, {"--health-port", "9090"},
		{"--confine-repo-settings", "enforce"}, {"--log-level", "debug"}, {"--configure-git"},
		{"--drain-wait-sec", "300"}, {"--defer-shutdown-max-min", "5"}, {"--drain-grace-sec", "120"},
		{"--kill-session-after-min", "240"}, {"--release-idle-session-min", "30"},
		{"--startup-timeout-min", "10"}, {"--session-stop-grace-sec", "7"},
		{"--post-session-hook-timeout-sec", "90"}, {"--exit-if-unused-min", "15"},
		{"--remove-session-state"}, {"--push-outcome-on-release"}, {"--use-anthropic-git-proxy"},
		{"--git-host-rewrite", "github.com=ghe.internal"}, {"--git-ssh-rewrite", "ghe.internal"},
		{"--lock-to-account", "user_123"}, {"--client-label", "fleet-a"}, {"--trust-workspace", "false"},
		{"--hooks-dir", "/etc/claude/hooks"}, {"--exec-path", "/etc/claude/wrapper/wrap.sh"},
		{debugTokenDirFlag, "/tmp/x"},
	}
	for _, seq := range mustContain {
		if !containsSeq(got, seq) {
			t.Errorf("missing %v in %v", seq, got)
		}
	}
	if got[len(got)-2] != debugTokenDirFlag {
		t.Errorf("extraArgs must be appended last, got %v", got)
	}
}

func containsSeq(hay, needle []string) bool {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
