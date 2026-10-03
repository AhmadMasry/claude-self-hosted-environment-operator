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
	"testing"
	"time"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestWorkOrderSweepAge(t *testing.T) {
	cases := []struct {
		name        string
		spawn, hook int32
		want        time.Duration
	}{
		{"both defaulted", 0, 0, 120 * time.Second},
		{"spawn deadline is longer", 30, 15, 30 * time.Second},
		{"hook timeout is longer", 20, 45, 45 * time.Second},
		{"defaulted hook timeout is longer", 20, 0, 60 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &selfhostedv1alpha1.ClaudeEnvironment{Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{OnDemand: &selfhostedv1alpha1.OnDemandSpec{
				Orchestrator: selfhostedv1alpha1.OrchestratorSpec{ExpectedSpawnSeconds: tc.spawn, HookTimeoutSeconds: tc.hook}}}}
			if got := workOrderSweepAge(env); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
