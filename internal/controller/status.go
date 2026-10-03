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
	"cmp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// degradedSeparator joins the parts of the Degraded message.
const degradedSeparator = "; "

type degradation struct{ reason, message string }

// statusPass accumulates condition changes for one reconcile and writes them
// to the object once in finish(), so a pass produces a single status update.
type statusPass struct {
	env      *selfhostedv1alpha1.ClaudeEnvironment
	degraded []degradation
	// before holds the conditions as they were when the pass started.
	before []metav1.Condition
	// events are sent by emit once the pass's status is stored, so a pass
	// that read a stale object and lost the update race does not repeat them.
	events []queuedEvent
}

type queuedEvent struct {
	rec                        record.EventRecorder
	env                        *selfhostedv1alpha1.ClaudeEnvironment
	eventType, reason, message string
}

func newStatusPass(env *selfhostedv1alpha1.ClaudeEnvironment) *statusPass {
	before := make([]metav1.Condition, len(env.Status.Conditions))
	for i := range env.Status.Conditions {
		env.Status.Conditions[i].DeepCopyInto(&before[i])
	}
	return &statusPass{env: env, before: before}
}

func (p *statusPass) set(t string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&p.env.Status.Conditions, metav1.Condition{
		Type: t, Status: status, Reason: reason, Message: message, ObservedGeneration: p.env.Generation,
	})
}

func (p *statusPass) degrade(reason, message string) {
	p.degraded = append(p.degraded, degradation{reason, message})
}

// degradeOnce records the degradation and emits a Warning event only when
// the Degraded condition was not already True with this reason. The condition
// carries only the first reason, so every active reason is read from the
// message, where finish prefixes each part with its reason. The event is sent
// by emit after the status update succeeds.
func (p *statusPass) degradeOnce(rec record.EventRecorder, env *selfhostedv1alpha1.ClaudeEnvironment, reason, msg string) {
	p.degrade(reason, msg)
	if c := meta.FindStatusCondition(p.before, selfhostedv1alpha1.ConditionDegraded); c != nil && c.Status == metav1.ConditionTrue &&
		(strings.HasPrefix(c.Message, reason+": ") || strings.Contains(c.Message, degradedSeparator+reason+": ")) {
		return
	}
	p.event(rec, env, corev1.EventTypeWarning, reason, msg)
}

// event queues an event for emit.
func (p *statusPass) event(rec record.EventRecorder, env *selfhostedv1alpha1.ClaudeEnvironment, eventType, reason, msg string) {
	p.events = append(p.events, queuedEvent{rec, env, eventType, reason, msg})
}

// emit sends the queued events.
func (p *statusPass) emit() {
	for _, e := range p.events {
		e.rec.Event(e.env, e.eventType, e.reason, e.message)
	}
}

func (p *statusPass) isTrue(t string) bool {
	return meta.IsStatusConditionTrue(p.env.Status.Conditions, t)
}

// falseReason returns the reason of condition t, or fallback when t is unset.
func (p *statusPass) falseReason(t, fallback string) string {
	if c := meta.FindStatusCondition(p.env.Status.Conditions, t); c != nil {
		return c.Reason
	}
	return fallback
}

// finish derives Degraded, Ready and Progressing from what the pass recorded.
func (p *statusPass) finish() {
	if len(p.degraded) > 0 {
		slices.SortFunc(p.degraded, func(a, b degradation) int {
			return cmp.Or(strings.Compare(a.reason, b.reason), strings.Compare(a.message, b.message))
		})
		msgs := make([]string, 0, len(p.degraded))
		for _, d := range p.degraded {
			msgs = append(msgs, d.reason+": "+d.message)
		}
		p.set(selfhostedv1alpha1.ConditionDegraded, metav1.ConditionTrue, p.degraded[0].reason, strings.Join(msgs, degradedSeparator))
	} else {
		p.set(selfhostedv1alpha1.ConditionDegraded, metav1.ConditionFalse, selfhostedv1alpha1.ReasonAsExpected, "")
	}
	ready := p.isTrue(selfhostedv1alpha1.ConditionSecretFound) &&
		p.isTrue(selfhostedv1alpha1.ConditionFleetAvailable) && len(p.degraded) == 0
	if ready {
		p.set(selfhostedv1alpha1.ConditionReady, metav1.ConditionTrue, selfhostedv1alpha1.ReasonAsExpected, "fleet is serving")
		p.set(selfhostedv1alpha1.ConditionProgressing, metav1.ConditionFalse, selfhostedv1alpha1.ReasonAsExpected, "")
	} else {
		reason := selfhostedv1alpha1.ReasonReconciling
		if len(p.degraded) > 0 {
			reason = p.degraded[0].reason
		} else if !p.isTrue(selfhostedv1alpha1.ConditionSecretFound) {
			reason = p.falseReason(selfhostedv1alpha1.ConditionSecretFound, selfhostedv1alpha1.ReasonSecretMissing)
		} else if !p.isTrue(selfhostedv1alpha1.ConditionFleetAvailable) {
			reason = p.falseReason(selfhostedv1alpha1.ConditionFleetAvailable, selfhostedv1alpha1.ReasonWorkloadUnavailable)
		}
		p.set(selfhostedv1alpha1.ConditionReady, metav1.ConditionFalse, reason, "")
		p.set(selfhostedv1alpha1.ConditionProgressing, metav1.ConditionTrue, selfhostedv1alpha1.ReasonReconciling, "")
	}
	p.env.Status.ObservedGeneration = p.env.Generation
}
