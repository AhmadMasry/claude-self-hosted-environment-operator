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
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestRecordAndForget(t *testing.T) {
	env := &selfhostedv1alpha1.ClaudeEnvironment{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "n"}}
	env.Spec.Fixed = &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](3)}
	env.Status.ComputedDrainBudgetSeconds = 80
	env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: 3, ReadyReplicas: 2, UpdatedReplicas: 3}
	meta.SetStatusCondition(&env.Status.Conditions, metav1.Condition{Type: selfhostedv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "x"})

	RecordEnvironment(env)
	if v := testutil.ToFloat64(environmentReady.WithLabelValues("n", "e")); v != 1 {
		t.Fatalf("ready gauge %v", v)
	}
	if v := testutil.ToFloat64(drainBudget.WithLabelValues("n", "e")); v != 80 {
		t.Fatalf("drain budget %v", v)
	}
	if v := testutil.ToFloat64(fixedReplicas.WithLabelValues("n", "e", "ready")); v != 2 {
		t.Fatalf("ready replicas %v", v)
	}
	if v := testutil.ToFloat64(fixedReplicas.WithLabelValues("n", "e", "desired")); v != 3 {
		t.Fatalf("desired replicas %v", v)
	}

	ForgetEnvironment("n", "e")
	if n := testutil.CollectAndCount(environmentReady); n != 0 {
		t.Fatalf("expected ready series removed, have %d", n)
	}
	if n := testutil.CollectAndCount(fixedReplicas); n != 0 {
		t.Fatalf("expected replica series removed, have %d", n)
	}
}
