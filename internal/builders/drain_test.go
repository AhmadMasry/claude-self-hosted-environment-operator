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

package builders

import (
	"testing"

	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func defaults() selfhostedv1alpha1.RunnerSettings {
	return selfhostedv1alpha1.RunnerSettings{
		SessionStopGraceSeconds:       ptr.To[int32](5),
		PostSessionHookTimeoutSeconds: ptr.To[int32](60),
	}
}

func TestDrainBudgetSeconds(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*selfhostedv1alpha1.RunnerSettings)
		want int64
	}{
		{"defaults", func(*selfhostedv1alpha1.RunnerSettings) {}, 80},
		{"drain wait 300", func(s *selfhostedv1alpha1.RunnerSettings) { s.DrainWaitSeconds = 300 }, 380},
		{"push outcome", func(s *selfhostedv1alpha1.RunnerSettings) { s.PushOutcomeOnRelease = true }, 110},
		{"defer 10 min, short drain wait", func(s *selfhostedv1alpha1.RunnerSettings) { s.DeferShutdownMaxMinutes = 10 }, 80 + 600 + 75},
		{"defer 1 min, drain wait 90", func(s *selfhostedv1alpha1.RunnerSettings) {
			s.DeferShutdownMaxMinutes = 1
			s.DrainWaitSeconds = 90
		}, (5 + 90 + 60 + 15) + 60 + (90 + 15)},
		{"nil pointers fall back to product defaults", func(s *selfhostedv1alpha1.RunnerSettings) {
			s.SessionStopGraceSeconds = nil
			s.PostSessionHookTimeoutSeconds = nil
		}, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := defaults()
			tc.mut(&s)
			if got := DrainBudgetSeconds(s); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func FuzzDrainBudgetMonotonic(f *testing.F) {
	f.Add(int32(0), int32(0), false)
	f.Fuzz(func(t *testing.T, drainWait, deferMin int32, push bool) {
		if drainWait < 0 || drainWait > 86400 || deferMin < 0 || deferMin > 10080 {
			t.Skip()
		}
		s := defaults()
		s.DrainWaitSeconds, s.DeferShutdownMaxMinutes, s.PushOutcomeOnRelease = drainWait, deferMin, push
		if DrainBudgetSeconds(s) < 80 {
			t.Fatalf("budget below product minimum")
		}
	})
}
