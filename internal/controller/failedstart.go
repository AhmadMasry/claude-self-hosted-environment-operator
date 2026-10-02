package controller

import (
	"fmt"
	"regexp"
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
	failedStartWindow         = 10 * time.Minute
	failedStartMessageLimit   = 200
	redactedMarker            = "[redacted]"
	failedStartNoMessageHint  = "no termination message; run `kubectl logs --previous` on the pod"
)

// secretPattern matches material that must never reach a condition or event:
// email addresses, JWTs, Anthropic API keys and environment keys.
var secretPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}` +
	`|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` +
	`|sk-ant-[A-Za-z0-9_-]+` +
	`|ccenvkey_[A-Za-z0-9_-]+`)

// detectFailedStart implements the product's definition of a failed start: the
// runner exited non-zero within a minute of starting, at least three times,
// most recently within the last ten minutes, and has not run stably since.
// Normal drains (exit 0) and fleets that recovered are ignored.
func detectFailedStart(pods []corev1.Pod, now time.Time) (bool, string) {
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != builders.RunnerContainerName || cs.RestartCount < failedStartMinRestarts {
				continue
			}
			t := cs.LastTerminationState.Terminated
			if t == nil || t.ExitCode == 0 ||
				t.FinishedAt.Sub(t.StartedAt.Time) >= failedStartMaxRunDuration ||
				now.Sub(t.FinishedAt.Time) > failedStartWindow {
				continue
			}
			if !stillFailing(cs.State, now) {
				continue
			}
			return true, fmt.Sprintf("pod %s restarted %d times with runs under %s; last run: %s",
				p.Name, cs.RestartCount, failedStartMaxRunDuration, fatalLine(t.Message))
		}
	}
	return false, ""
}

// stillFailing reports whether the container is waiting to restart or has
// been running for less than a minute since its last restart.
func stillFailing(state corev1.ContainerState, now time.Time) bool {
	if state.Waiting != nil {
		return true
	}
	return state.Running != nil && now.Sub(state.Running.StartedAt.Time) < failedStartMaxRunDuration
}

// fatalLine returns the last `[runner:fatal]` or `error:` line of a
// termination message, redacted and truncated. Any other log line is never
// forwarded, because the message may be a log tail holding credentials.
func fatalLine(msg string) string {
	for _, line := range slices.Backward(strings.Split(msg, "\n")) {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "[runner:fatal]") || strings.HasPrefix(l, "error:") {
			return truncate(redact(l), failedStartMessageLimit)
		}
	}
	return failedStartNoMessageHint
}

// redact replaces email addresses, JWTs and keys with [redacted].
func redact(s string) string {
	return secretPattern.ReplaceAllString(s, redactedMarker)
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
