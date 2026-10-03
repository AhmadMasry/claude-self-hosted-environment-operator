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

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	labelNamespace   = "namespace"
	labelEnvironment = "environment"
	labelPhase       = "phase"
	labelOutcome     = "outcome"
	labelState       = "state"

	OutcomeCreated      = "created"
	OutcomeSucceeded    = "succeeded"
	OutcomeFailed       = "failed"
	OutcomeSpawnTimeout = "spawn_timeout"
)

var (
	environmentReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_environment_ready",
		Help: "1 when the ClaudeEnvironment's Ready condition is True, else 0.",
	}, []string{labelNamespace, labelEnvironment})
	drainBudget = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_drain_budget_seconds",
		Help: "Computed terminationGracePeriodSeconds the runner drain path needs.",
	}, []string{labelNamespace, labelEnvironment})
	fixedReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_fixed_fleet_replicas",
		Help: "Fixed-fleet runner replicas by state (desired, ready, updated).",
	}, []string{labelNamespace, labelEnvironment, labelState})
	environmentRunners = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_environment_runners",
		Help: "On-demand runners per environment by phase (pending, running).",
	}, []string{labelNamespace, labelEnvironment, labelPhase})
	runnerSpawnDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "claude_operator_runner_spawn_duration_seconds",
		Help:    "Seconds from ClaudeRunner creation to the runner pod reaching Running.",
		Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600},
	}, []string{labelNamespace, labelEnvironment})
	runnersTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "claude_operator_runners_total",
		Help: "On-demand runner lifecycle events by outcome (created, succeeded, failed, spawn_timeout).",
	}, []string{labelNamespace, labelEnvironment, labelOutcome})
)

func init() {
	ctrlmetrics.Registry.MustRegister(environmentReady, drainBudget, fixedReplicas, environmentRunners, runnerSpawnDuration, runnersTotal)
}

// RecordEnvironment exports the environment's current status.
func RecordEnvironment(env *selfhostedv1alpha1.ClaudeEnvironment) {
	ns, name := env.Namespace, env.Name
	ready := 0.0
	if meta.IsStatusConditionTrue(env.Status.Conditions, selfhostedv1alpha1.ConditionReady) {
		ready = 1
	}
	environmentReady.WithLabelValues(ns, name).Set(ready)
	drainBudget.WithLabelValues(ns, name).Set(float64(env.Status.ComputedDrainBudgetSeconds))
	if env.Spec.Fixed != nil && env.Status.Fixed != nil {
		desired := int32(1)
		if env.Spec.Fixed.Replicas != nil {
			desired = *env.Spec.Fixed.Replicas
		}
		fixedReplicas.WithLabelValues(ns, name, "desired").Set(float64(desired))
		fixedReplicas.WithLabelValues(ns, name, "ready").Set(float64(env.Status.Fixed.ReadyReplicas))
		fixedReplicas.WithLabelValues(ns, name, "updated").Set(float64(env.Status.Fixed.UpdatedReplicas))
	} else {
		fixedReplicas.DeletePartialMatch(prometheus.Labels{labelNamespace: ns, labelEnvironment: name})
	}
	if env.Spec.OnDemand != nil && env.Status.OnDemand != nil {
		environmentRunners.WithLabelValues(ns, name, "pending").Set(float64(env.Status.OnDemand.PendingRunners))
		environmentRunners.WithLabelValues(ns, name, "running").Set(float64(env.Status.OnDemand.RunningRunners))
	} else {
		environmentRunners.DeletePartialMatch(prometheus.Labels{labelNamespace: ns, labelEnvironment: name})
	}
}

// ObserveSpawn records how long a runner took from creation to Running.
func ObserveSpawn(namespace, environment string, d time.Duration) {
	runnerSpawnDuration.WithLabelValues(namespace, environment).Observe(d.Seconds())
}

// CountRunner increments the runner lifecycle counter.
func CountRunner(namespace, environment, outcome string) {
	runnersTotal.WithLabelValues(namespace, environment, outcome).Inc()
}

// ForgetEnvironment drops every series for a deleted environment.
func ForgetEnvironment(namespace, name string) {
	labels := prometheus.Labels{labelNamespace: namespace, labelEnvironment: name}
	environmentReady.DeletePartialMatch(labels)
	drainBudget.DeletePartialMatch(labels)
	fixedReplicas.DeletePartialMatch(labels)
	environmentRunners.DeletePartialMatch(labels)
	runnerSpawnDuration.DeletePartialMatch(labels)
	runnersTotal.DeletePartialMatch(labels)
}
