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

package v1alpha1

const (
	ConditionReady           = "Ready"
	ConditionSecretFound     = "SecretFound"
	ConditionFleetAvailable  = "FleetAvailable"
	ConditionProgressing     = "Progressing"
	ConditionDegraded        = "Degraded"
	ConditionSecretOnRunners = "SecretOnRunners"

	ReasonSecretMissing         = "SecretMissing"
	ReasonSecretKeyMissing      = "SecretKeyMissing"
	ReasonSecretFound           = "SecretFound"
	ReasonConfigMapMissing      = "ConfigMapMissing"
	ReasonGracePeriodTooShort   = "GracePeriodTooShort"
	ReasonRunnerFailedStart     = "RunnerFailedStart"
	ReasonWorkloadAvailable     = "WorkloadAvailable"
	ReasonWorkloadUnavailable   = "WorkloadUnavailable"
	ReasonReconciling           = "Reconciling"
	ReasonAsExpected            = "AsExpected"
	ReasonFixedModeSecretOnPods = "FixedModeSecretOnPods"
	ReasonUnsupportedMode       = "UnsupportedMode"

	LabelEnvironment     = "selfhosted.claudecode.dev/environment"
	LabelRole            = "selfhosted.claudecode.dev/role"
	LabelPartOf          = "app.kubernetes.io/part-of"
	PartOfValue          = "claude-code-self-hosted-runner"
	RoleRunner           = "runner"
	RoleOrchestrator     = "orchestrator"
	AnnotationConfigHash = "selfhosted.claudecode.dev/config-hash"
	FieldOwner           = "claude-selfhosted-operator"
)
