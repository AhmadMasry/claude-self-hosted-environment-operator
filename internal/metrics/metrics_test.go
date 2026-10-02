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
	"time"

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

func TestOnDemandMetrics(t *testing.T) {
	env := &selfhostedv1alpha1.ClaudeEnvironment{ObjectMeta: metav1.ObjectMeta{Name: "od", Namespace: "n"}}
	env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
	env.Status.OnDemand = &selfhostedv1alpha1.OnDemandStatus{PendingRunners: 2, RunningRunners: 5}
	RecordEnvironment(env)
	if v := testutil.ToFloat64(environmentRunners.WithLabelValues("n", "od", "pending")); v != 2 {
		t.Fatalf("pending %v", v)
	}
	if v := testutil.ToFloat64(environmentRunners.WithLabelValues("n", "od", "running")); v != 5 {
		t.Fatalf("running %v", v)
	}
	if n := testutil.CollectAndCount(fixedReplicas); n != 0 {
		t.Fatalf("fixed series must not exist for an on-demand environment, have %d", n)
	}

	CountRunner("n", "od", OutcomeCreated)
	CountRunner("n", "od", OutcomeSucceeded)
	if v := testutil.ToFloat64(runnersTotal.WithLabelValues("n", "od", OutcomeCreated)); v != 1 {
		t.Fatalf("created %v", v)
	}
	ObserveSpawn("n", "od", 12*time.Second)
	if n := testutil.CollectAndCount(runnerSpawnDuration); n != 1 {
		t.Fatalf("spawn histogram series %d", n)
	}

	ForgetEnvironment("n", "od")
	if n := testutil.CollectAndCount(environmentRunners) + testutil.CollectAndCount(runnersTotal) + testutil.CollectAndCount(runnerSpawnDuration); n != 0 {
		t.Fatalf("series must be removed after forget, have %d", n)
	}
}
