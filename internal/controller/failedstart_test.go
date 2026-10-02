package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

func podWithRestart(restarts int32, runFor time.Duration, msg string) corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-runFor))
	end := metav1.NewTime(time.Now())
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: builders.RunnerContainerName, RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			StartedAt: start, FinishedAt: end, ExitCode: 1, Message: msg}},
	}}}}
}

func TestDetectFailedStart(t *testing.T) {
	fatal := "2026-10-02T10:00:00Z [self-hosted-runner] starting\n[runner:fatal] --use-anthropic-git-proxy requires --capacity 1"
	cases := []struct {
		name    string
		pods    []corev1.Pod
		want    bool
		contain string
	}{
		{"healthy", []corev1.Pod{podWithRestart(0, 0, "")}, false, ""},
		{"normal drains after long runs", []corev1.Pod{podWithRestart(10, 5*time.Minute, "")}, false, ""},
		{"two short restarts is not enough", []corev1.Pod{podWithRestart(2, 5*time.Second, fatal)}, false, ""},
		{"three short restarts with fatal line", []corev1.Pod{podWithRestart(3, 5*time.Second, fatal)}, true, "[runner:fatal] --use-anthropic-git-proxy"},
		{"error prefix", []corev1.Pod{podWithRestart(4, 2*time.Second, "error: cannot create or write to base directory /workspace\nSee --help")}, true, "error: cannot create"},
		{"no message", []corev1.Pod{podWithRestart(3, 1*time.Second, "")}, true, "kubectl logs --previous"},
		{"other container ignored", func() []corev1.Pod {
			p := podWithRestart(5, time.Second, fatal)
			p.Status.ContainerStatuses[0].Name = "sidecar"
			return []corev1.Pod{p}
		}(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := detectFailedStart(tc.pods)
			if got != tc.want {
				t.Fatalf("got %v want %v (%s)", got, tc.want, msg)
			}
			if tc.contain != "" && !strings.Contains(msg, tc.contain) {
				t.Fatalf("message %q lacks %q", msg, tc.contain)
			}
			if strings.Contains(msg, "ccenvkey_") {
				t.Fatal("message leaked a secret-looking token")
			}
		})
	}
}
