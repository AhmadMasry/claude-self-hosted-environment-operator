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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/redact"
)

type phaseResult struct {
	Phase      selfhostedv1alpha1.RunnerPhase
	Reason     string
	Message    string
	StartedAt  *metav1.Time
	FinishedAt *metav1.Time
}

// derivePhase maps a runner pod onto the ClaudeRunner lifecycle.
func derivePhase(pod *corev1.Pod, now time.Time) phaseResult {
	if pod == nil {
		return phaseResult{Phase: selfhostedv1alpha1.RunnerPending, Reason: selfhostedv1alpha1.ReasonPodPending, Message: "pod not created yet"}
	}
	var cs *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == builders.RunnerContainerName {
			cs = &pod.Status.ContainerStatuses[i]
		}
	}
	res := phaseResult{StartedAt: pod.Status.StartTime}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerRunning, selfhostedv1alpha1.ReasonPodRunning
	case corev1.PodSucceeded:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerSucceeded, selfhostedv1alpha1.ReasonPodSucceeded
		res.FinishedAt = terminatedAt(cs, now)
	case corev1.PodFailed:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed
		res.FinishedAt = terminatedAt(cs, now)
		if cs != nil && cs.State.Terminated != nil {
			t := cs.State.Terminated
			res.Message = truncate(redact.String(fmt.Sprintf("exit code %d (%s): %s", t.ExitCode, t.Reason, fatalLine(t.Message))))
		} else {
			res.Message = "pod failed: " + pod.Status.Reason
		}
	case corev1.PodUnknown:
		finished := metav1.NewTime(now)
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed
		res.Message, res.FinishedAt = "pod phase Unknown: node unreachable", &finished
	default: // Pending
		res.Phase, res.Reason, res.StartedAt = selfhostedv1alpha1.RunnerPending, selfhostedv1alpha1.ReasonPodPending, nil
		res.Message = "pod is pending"
		if cs != nil && cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			res.Reason, res.Message = cs.State.Waiting.Reason, truncate(redact.String(cs.State.Waiting.Message))
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
				res.Reason, res.Message = c.Reason, truncate(redact.String(c.Message))
			}
		}
	}
	return res
}

func terminatedAt(cs *corev1.ContainerStatus, now time.Time) *metav1.Time {
	if cs != nil && cs.State.Terminated != nil && !cs.State.Terminated.FinishedAt.IsZero() {
		t := cs.State.Terminated.FinishedAt
		return &t
	}
	finished := metav1.NewTime(now)
	return &finished
}
