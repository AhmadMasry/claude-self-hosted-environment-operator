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
	"path"
	"strconv"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	SecretMountPath     = "/etc/claude"
	SecretFileName      = "environment-secret"
	HooksMountPath      = "/etc/claude/hooks"
	WrapperMountPath    = "/etc/claude/wrapper"
	HostConfigMountPath = "/etc/claude/host-config"
	DefaultHealthPort   = int32(8080)
)

// RunnerArgs renders the container args for `claude self-hosted-runner`.
// Operator-owned flags come first, settings next, extraArgs last.
func RunnerArgs(r selfhostedv1alpha1.RunnerSpec, secretFile string) []string {
	s := r.Settings
	a := []string{
		"self-hosted-runner",
		"--environment-secret-file", secretFile,
		"--capacity", strconv.Itoa(int(max(r.Capacity, 1))),
		"--base-dir", r.BaseDir,
		"--health-port", strconv.Itoa(int(valueOr(s.HealthPort, DefaultHealthPort))),
	}
	a = appendStr(a, "--confine-repo-settings", s.ConfineRepoSettings)
	a = appendStr(a, "--log-level", s.LogLevel)
	if s.ConfigureGit {
		a = append(a, "--configure-git")
	}
	a = appendInt(a, "--drain-wait-sec", s.DrainWaitSeconds)
	a = appendInt(a, "--defer-shutdown-max-min", s.DeferShutdownMaxMinutes)
	a = appendInt(a, "--drain-grace-sec", s.DrainGraceSeconds)
	a = appendInt(a, "--kill-session-after-min", s.KillSessionAfterMinutes)
	a = appendInt(a, "--release-idle-session-min", s.ReleaseIdleSessionMinutes)
	if s.StartupTimeoutMinutes != nil && *s.StartupTimeoutMinutes != 15 {
		a = appendInt(a, "--startup-timeout-min", *s.StartupTimeoutMinutes)
	}
	if s.SessionStopGraceSeconds != nil && *s.SessionStopGraceSeconds != 5 {
		a = appendInt(a, "--session-stop-grace-sec", *s.SessionStopGraceSeconds)
	}
	if s.PostSessionHookTimeoutSeconds != nil && *s.PostSessionHookTimeoutSeconds != 60 {
		a = appendInt(a, "--post-session-hook-timeout-sec", *s.PostSessionHookTimeoutSeconds)
	}
	a = appendInt(a, "--exit-if-unused-min", s.ExitIfUnusedMinutes)
	if s.RemoveSessionState {
		a = append(a, "--remove-session-state")
	}
	if s.PushOutcomeOnRelease {
		a = append(a, "--push-outcome-on-release")
	}
	if s.UseAnthropicGitProxy {
		a = append(a, "--use-anthropic-git-proxy")
	}
	for _, rw := range s.GitHostRewrites {
		a = append(a, "--git-host-rewrite", rw)
	}
	for _, rw := range s.GitSSHRewrites {
		a = append(a, "--git-ssh-rewrite", rw)
	}
	a = appendStr(a, "--lock-to-account", s.LockToAccount)
	a = appendStr(a, "--client-label", s.ClientLabel)
	if s.TrustWorkspace != nil && !*s.TrustWorkspace {
		a = append(a, "--trust-workspace", "false")
	}
	if r.LifecycleHooks != nil {
		a = append(a, "--hooks-dir", HooksMountPath)
	}
	if r.WrapperScript != nil {
		a = append(a, "--exec-path", path.Join(WrapperMountPath, r.WrapperScript.Key))
	}
	return append(a, r.ExtraArgs...)
}

func appendStr(a []string, flag, v string) []string {
	if v == "" {
		return a
	}
	return append(a, flag, v)
}

func appendInt(a []string, flag string, v int32) []string {
	if v == 0 {
		return a
	}
	return append(a, flag, strconv.Itoa(int(v)))
}
