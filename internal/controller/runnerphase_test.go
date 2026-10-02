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
		{"unknown", podIn(corev1.PodUnknown, corev1.ContainerStatus{}), selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "Unknown", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePhase(tc.pod)
			if got.Phase != tc.phase || got.Reason != tc.reason {
				t.Fatalf("got %s/%s want %s/%s", got.Phase, got.Reason, tc.phase, tc.reason)
			}
			if tc.contain != "" && !strings.Contains(got.Message, tc.contain) {
				t.Fatalf("message %q lacks %q", got.Message, tc.contain)
			}
			if strings.Contains(got.Message, "@example.com") {
				t.Fatal("message leaked an email")
			}
			if (got.StartedAt != nil) != tc.started || (got.FinishedAt != nil) != tc.ended {
				t.Fatalf("timestamps: started=%v ended=%v", got.StartedAt != nil, got.FinishedAt != nil)
			}
		})
	}
}
