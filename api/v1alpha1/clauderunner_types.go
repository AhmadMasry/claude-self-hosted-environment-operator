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
	EnvironmentRef LocalObjectRef `json:"environmentRef"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	OrderID string `json:"orderID"`
	// +optional
	SessionID string `json:"sessionID,omitempty"`
	// +optional
	SessionUUID string `json:"sessionUUID,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// +optional
	ClientPlatform string `json:"clientPlatform,omitempty"`
	// +optional
	PrimaryRepoURL string `json:"primaryRepoURL,omitempty"`
	// AccountID is the tagged account ID of the session creator. Never an email.
	// +optional
	AccountID          string         `json:"accountID,omitempty"`
	WorkOrderSecretRef LocalObjectRef `json:"workOrderSecretRef"`
}

// ClaudeRunnerStatus is derived from the runner pod.
type ClaudeRunnerStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase RunnerPhase `json:"phase,omitempty"`
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
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
