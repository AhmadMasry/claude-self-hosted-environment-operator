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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/redact"
)

var testNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// podWithRestart returns a pod whose runner last ran for runFor, exited 1 just
// now and is waiting in CrashLoopBackOff.
func podWithRestart(restarts int32, runFor time.Duration, msg string) corev1.Pod {
	start := metav1.NewTime(testNow.Add(-runFor))
	end := metav1.NewTime(testNow)
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: builders.RunnerContainerName, RestartCount: restarts,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			StartedAt: start, FinishedAt: end, ExitCode: 1, Message: msg}},
	}}}}
}

func TestDetectFailedStart(t *testing.T) {
	fatal := "2026-10-02T10:00:00Z [self-hosted-runner] starting\n[runner:fatal] --use-anthropic-git-proxy requires --capacity 1"
	long := "[runner:fatal] " + strings.Repeat("a", 184) + strings.Repeat("é", 20)
	recovered := podWithRestart(3, 5*time.Second, fatal)
	recovered.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{
		StartedAt: metav1.NewTime(testNow.Add(-20 * time.Minute))}}
	recovered.Status.ContainerStatuses[0].LastTerminationState.Terminated.StartedAt = metav1.NewTime(testNow.Add(-20*time.Minute - 5*time.Second))
	recovered.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = metav1.NewTime(testNow.Add(-20 * time.Minute))
	drained := podWithRestart(3, 5*time.Second, "")
	drained.Status.ContainerStatuses[0].LastTerminationState.Terminated.ExitCode = 0
	justRestarted := podWithRestart(3, 5*time.Second, fatal)
	justRestarted.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{
		StartedAt: metav1.NewTime(testNow.Add(-10 * time.Second))}}
	cases := []struct {
		name    string
		pods    []corev1.Pod
		want    bool
		contain string
	}{
		{"healthy", []corev1.Pod{podWithRestart(0, 0, "")}, false, ""},
		{"normal drains after long runs", []corev1.Pod{podWithRestart(10, 5*time.Minute, "")}, false, ""},
		{"two short restarts is not enough", []corev1.Pod{podWithRestart(2, 5*time.Second, fatal)}, false, ""},
		{"recovered container with an old termination", []corev1.Pod{recovered}, false, ""},
		{"short run that exited 0", []corev1.Pod{drained}, false, ""},
		{"CrashLoopBackOff with exit 1 and a recent termination", []corev1.Pod{podWithRestart(3, 5*time.Second, fatal)}, true, "[runner:fatal] --use-anthropic-git-proxy"},
		{"running again for under a minute", []corev1.Pod{justRestarted}, true, "[runner:fatal]"},
		{"error prefix", []corev1.Pod{podWithRestart(4, 2*time.Second, "error: cannot create or write to base directory /workspace\nSee --help")}, true, "error: cannot create"},
		{"long multibyte fatal line", []corev1.Pod{podWithRestart(3, time.Second, long)}, true, "[runner:fatal] aaa"},
		{"no message", []corev1.Pod{podWithRestart(3, 1*time.Second, "")}, true, "kubectl logs --previous"},
		{"arbitrary last log line is not forwarded", []corev1.Pod{podWithRestart(3, time.Second, "starting\nsigned in as dev@example.com")}, true, "kubectl logs --previous"},
		{"email in a fatal line is redacted", []corev1.Pod{podWithRestart(3, time.Second, "[runner:fatal] account dev.ops+ci@example.co.uk is not allowed")}, true, redact.Marker},
		{"JWT in a fatal line is redacted", []corev1.Pod{podWithRestart(3, time.Second, "error: token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl rejected")}, true, redact.Marker},
		{"bare JWT in a fatal line is redacted", []corev1.Pod{podWithRestart(3, time.Second, "[runner:fatal] token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9 rejected")}, true, redact.Marker},
		{"two-segment JWT in a fatal line is redacted", []corev1.Pod{podWithRestart(3, time.Second, "error: token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0 rejected")}, true, redact.Marker},
		{"keys in a fatal line are redacted", []corev1.Pod{podWithRestart(3, time.Second, "[runner:fatal] bad key sk-ant-api03-AbC_d-1 or ccenvkey_xyz-1")}, true, redact.Marker},
		{"other container ignored", func() []corev1.Pod {
			p := podWithRestart(5, time.Second, fatal)
			p.Status.ContainerStatuses[0].Name = "sidecar"
			return []corev1.Pod{p}
		}(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := detectFailedStart(tc.pods, testNow)
			if got != tc.want {
				t.Fatalf("got %v want %v (%s)", got, tc.want, msg)
			}
			if tc.contain != "" && !strings.Contains(msg, tc.contain) {
				t.Fatalf("message %q lacks %q", msg, tc.contain)
			}
			if !utf8.ValidString(msg) {
				t.Fatalf("message is not valid UTF-8: %q", msg)
			}
			for _, leak := range []string{"ccenvkey_", "sk-ant-", "eyJ", "@example."} {
				if strings.Contains(msg, leak) {
					t.Fatalf("message leaked %q: %q", leak, msg)
				}
			}
		})
	}
}
