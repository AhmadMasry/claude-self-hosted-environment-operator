package controller

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	failedStartMinRestarts    = 3
	failedStartMaxRunDuration = 60 * time.Second
	failedStartMessageLimit   = 200
)

// detectFailedStart implements the product's definition of a failed start: the
// runner keeps exiting within a minute of starting. Restarts after long runs
// are normal drains and are ignored.
func detectFailedStart(pods []corev1.Pod) (bool, string) {
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != builders.RunnerContainerName || cs.RestartCount < failedStartMinRestarts {
				continue
			}
			t := cs.LastTerminationState.Terminated
			if t == nil || t.FinishedAt.Sub(t.StartedAt.Time) >= failedStartMaxRunDuration {
				continue
			}
			return true, fmt.Sprintf("pod %s restarted %d times with runs under %s; last run: %s",
				p.Name, cs.RestartCount, failedStartMaxRunDuration, fatalLine(t.Message))
		}
	}
	return false, ""
}

func fatalLine(msg string) string {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	for _, line := range slices.Backward(lines) {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "[runner:fatal]") || strings.HasPrefix(l, "error:") {
			return truncate(l, failedStartMessageLimit)
		}
	}
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return truncate(last, failedStartMessageLimit)
	}
	return "no termination message; run `kubectl logs --previous` on the pod"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// podStatusChanged lets pod update events through only when container statuses changed.
func podStatusChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if !ok1 || !ok2 {
				return false
			}
			return !equality.Semantic.DeepEqual(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses)
		},
	}
}
