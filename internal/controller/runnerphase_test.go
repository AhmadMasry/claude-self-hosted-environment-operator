package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

func podIn(phase corev1.PodPhase, cs corev1.ContainerStatus) *corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-time.Minute))
	cs.Name = builders.RunnerContainerName
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase, StartTime: &start, ContainerStatuses: []corev1.ContainerStatus{cs}}}
}

func TestDerivePhase(t *testing.T) {
	end := metav1.Now()
	cases := []struct {
		name    string
		pod     *corev1.Pod
		phase   selfhostedv1alpha1.RunnerPhase
		reason  string
		contain string
		started bool
		ended   bool
	}{
		{"no pod", nil, selfhostedv1alpha1.RunnerPending, selfhostedv1alpha1.ReasonPodPending, "not created", false, false},
		{"pending, image pull", podIn(corev1.PodPending, corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull failed"}}}),
			selfhostedv1alpha1.RunnerPending, "ImagePullBackOff", "pull failed", false, false},
		{"pending, unschedulable", func() *corev1.Pod {
			p := podIn(corev1.PodPending, corev1.ContainerStatus{})
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes"}}
			return p
		}(), selfhostedv1alpha1.RunnerPending, "Unschedulable", "0/3 nodes", false, false},
		{"running", podIn(corev1.PodRunning, corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}),
			selfhostedv1alpha1.RunnerRunning, selfhostedv1alpha1.ReasonPodRunning, "", true, false},
		{"succeeded", podIn(corev1.PodSucceeded, corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: end}}}),
			selfhostedv1alpha1.RunnerSucceeded, selfhostedv1alpha1.ReasonPodSucceeded, "", true, true},
		{"failed with redaction", podIn(corev1.PodFailed, corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: end,
			Message: "[runner:fatal] bad token for someone@example.com"}}}),
			selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "exit code 1", true, true},
		{"failed with a bare JWT", podIn(corev1.PodFailed, corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "StartError", FinishedAt: end,
			Message: "[runner:fatal] token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9 rejected"}}}),
			selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "[redacted]", true, true},
		{"unknown", podIn(corev1.PodUnknown, corev1.ContainerStatus{}), selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "Unknown", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePhase(tc.pod, time.Now())
			if got.Phase != tc.phase || got.Reason != tc.reason {
				t.Fatalf("got %s/%s want %s/%s", got.Phase, got.Reason, tc.phase, tc.reason)
			}
			if tc.contain != "" && !strings.Contains(got.Message, tc.contain) {
				t.Fatalf("message %q lacks %q", got.Message, tc.contain)
			}
			if strings.Contains(got.Message, "@example.com") || strings.Contains(got.Message, "eyJ") {
				t.Fatal("message leaked a credential")
			}
			if (got.StartedAt != nil) != tc.started || (got.FinishedAt != nil) != tc.ended {
				t.Fatalf("timestamps: started=%v ended=%v", got.StartedAt != nil, got.FinishedAt != nil)
			}
		})
	}
}

func TestLostReason(t *testing.T) {
	if got := lostReason(selfhostedv1alpha1.ReasonSpawnTimeout); got != selfhostedv1alpha1.ReasonSpawnTimeout {
		t.Fatalf("a timed-out runner whose pod is gone must stay SpawnTimeout, got %s", got)
	}
	for _, current := range []string{"", selfhostedv1alpha1.ReasonPodPending, selfhostedv1alpha1.ReasonPodRunning} {
		if got := lostReason(current); got != selfhostedv1alpha1.ReasonPodLost {
			t.Fatalf("lostReason(%q) = %s, want PodLost", current, got)
		}
	}
}

func TestSpawnDuration(t *testing.T) {
	created := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	runner := &selfhostedv1alpha1.ClaudeRunner{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)}}
	now := created.Add(time.Minute)
	started := metav1.NewTime(created.Add(7 * time.Second))
	if got := spawnDuration(runner, &corev1.Pod{Status: corev1.PodStatus{StartTime: &started}}, now); got != 7*time.Second {
		t.Fatalf("with a pod start time: got %s, want 7s", got)
	}
	if got := spawnDuration(runner, &corev1.Pod{}, now); got != time.Minute {
		t.Fatalf("without a pod start time: got %s, want 1m", got)
	}
}
