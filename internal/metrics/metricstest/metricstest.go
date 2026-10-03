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

// Package metricstest reads operator metrics for tests. Only test files import
// it, so the manager binary does not link it.
package metricstest

import (
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// RunnersTotal reads one claude_operator_runners_total series from the
// controller-runtime registry; an absent series reads 0.
func RunnersTotal(namespace, environment, outcome string) float64 {
	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		panic(err)
	}
	for _, f := range families {
		if f.GetName() != "claude_operator_runners_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if got["namespace"] == namespace && got["environment"] == environment && got["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}
