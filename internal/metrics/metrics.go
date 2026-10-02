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
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	labelNamespace   = "namespace"
	labelEnvironment = "environment"
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
	}, []string{labelNamespace, labelEnvironment, "state"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(environmentReady, drainBudget, fixedReplicas)
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
}

// ForgetEnvironment drops every series for a deleted environment.
func ForgetEnvironment(namespace, name string) {
	labels := prometheus.Labels{labelNamespace: namespace, labelEnvironment: name}
	environmentReady.DeletePartialMatch(labels)
	drainBudget.DeletePartialMatch(labels)
	fixedReplicas.DeletePartialMatch(labels)
}
