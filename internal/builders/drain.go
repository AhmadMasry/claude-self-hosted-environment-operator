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

import selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"

// Product defaults of the runner CLI; RunnerArgs omits a flag that equals its default.
const (
	DefaultStartupTimeoutMinutes         = 15
	DefaultSessionStopGraceSeconds       = 5
	DefaultPostSessionHookTimeoutSeconds = 60
	drainFixedOverheadSeconds            = 15
	pushOutcomeOverheadSeconds           = 30
	postReleaseGraceSeconds              = 75
)

// DrainBudgetSeconds returns the terminationGracePeriodSeconds the runner
// needs to complete its documented drain path.
func DrainBudgetSeconds(s selfhostedv1alpha1.RunnerSettings) int64 {
	stop := int64(valueOr(s.SessionStopGraceSeconds, DefaultSessionStopGraceSeconds))
	hook := int64(valueOr(s.PostSessionHookTimeoutSeconds, DefaultPostSessionHookTimeoutSeconds))
	drainWait := int64(s.DrainWaitSeconds)

	budget := stop + drainWait + hook + drainFixedOverheadSeconds
	if s.PushOutcomeOnRelease {
		budget += pushOutcomeOverheadSeconds
	}
	if s.DeferShutdownMaxMinutes > 0 {
		postRelease := int64(postReleaseGraceSeconds)
		if drainWait > 60 {
			postRelease = drainWait + drainFixedOverheadSeconds
		}
		budget += int64(s.DeferShutdownMaxMinutes)*60 + postRelease
	}
	return budget
}

func valueOr(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}
