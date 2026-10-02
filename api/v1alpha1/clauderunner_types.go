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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// LocalObjectRef names an object in the same namespace.
type LocalObjectRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// RunnerPhase is the lifecycle phase of a spawned runner.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type RunnerPhase string

const (
	RunnerPending   RunnerPhase = "Pending"
	RunnerRunning   RunnerPhase = "Running"
	RunnerSucceeded RunnerPhase = "Succeeded"
	RunnerFailed    RunnerPhase = "Failed"
)

// IsTerminal reports whether the phase is final.
func (p RunnerPhase) IsTerminal() bool {
	return p == RunnerSucceeded || p == RunnerFailed
}

// ClaudeRunnerSpec is written once by the spawn-runner hook and never changes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.workOrderSecretRef.name == self.orderID + '-work-order'",message="workOrderSecretRef.name must be <orderID>-work-order"
type ClaudeRunnerSpec struct {
	// EnvironmentRef names the on-demand ClaudeEnvironment that controls this runner.
	EnvironmentRef LocalObjectRef `json:"environmentRef"`
	// OrderID is the work order ID; the ClaudeRunner and its pod are named after it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	OrderID string `json:"orderID"`
	// SessionID is the session ID from CLAUDE_RUNNER_SESSION_ID.
	// +optional
	SessionID string `json:"sessionID,omitempty"`
	// SessionUUID is the session UUID from CLAUDE_RUNNER_SESSION_UUID.
	// +optional
	SessionUUID string `json:"sessionUUID,omitempty"`
	// Attempt is the delivery attempt from CLAUDE_RUNNER_ATTEMPT (0 when unset).
	// +kubebuilder:validation:Minimum=0
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// ClientPlatform is the client platform from CLAUDE_RUNNER_CLIENT_PLATFORM.
	// +optional
	ClientPlatform string `json:"clientPlatform,omitempty"`
	// PrimaryRepoURL is the primary repository URL from CLAUDE_RUNNER_PRIMARY_REPO_URL.
	// +optional
	PrimaryRepoURL string `json:"primaryRepoURL,omitempty"`
	// AccountID is the tagged account ID of the session creator. Never an email.
	// +optional
	AccountID string `json:"accountID,omitempty"`
	// WorkOrderSecretRef names the Secret that holds the single-use work-order JWT.
	WorkOrderSecretRef LocalObjectRef `json:"workOrderSecretRef"`
}

// ClaudeRunnerStatus is derived from the runner pod.
type ClaudeRunnerStatus struct {
	// ObservedGeneration is the generation the status was last computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is the runner's lifecycle phase: Pending, Running, Succeeded or Failed.
	// +optional
	Phase RunnerPhase `json:"phase,omitempty"`
	// PodName is the runner pod, set once the pod has been created.
	// +optional
	PodName string `json:"podName,omitempty"`
	// StartedAt is when the runner pod started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// FinishedAt is when the runner reached a terminal phase; the TTL counts from it.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Reason is a CamelCase reason for the current phase.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message is a redacted, human-readable detail for the current phase.
	// +optional
	Message string `json:"message,omitempty"`
	// Conditions holds the Ready condition.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=crun
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Session",type=string,JSONPath=`.spec.sessionID`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClaudeRunner is one spawned on-demand runner. It is created by the
// spawn-runner hook, never by users.
type ClaudeRunner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClaudeRunnerSpec   `json:"spec,omitempty"`
	Status            ClaudeRunnerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClaudeRunnerList contains a list of ClaudeRunner.
type ClaudeRunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClaudeRunner `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ClaudeRunner{}, &ClaudeRunnerList{})
		return nil
	})
}
