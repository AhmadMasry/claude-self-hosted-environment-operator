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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SecretKeyRef names a Secret and key in the same namespace.
type SecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:default="environment-secret"
	// +optional
	Key string `json:"key,omitempty"`
}

// ConfigMapRef names a ConfigMap in the same namespace.
type ConfigMapRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ConfigMapKeyRef names a ConfigMap and a key within it.
type ConfigMapKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// RunnerSettings maps onto `claude self-hosted-runner` flags.
type RunnerSettings struct {
	// +optional
	ConfigureGit bool `json:"configureGit,omitempty"`
	// +kubebuilder:validation:Enum=warn;enforce;off
	// +kubebuilder:default=warn
	// +optional
	ConfineRepoSettings string `json:"confineRepoSettings,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=86400
	// +optional
	DrainWaitSeconds int32 `json:"drainWaitSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10080
	// +optional
	DeferShutdownMaxMinutes int32 `json:"deferShutdownMaxMinutes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	// +optional
	DrainGraceSeconds int32 `json:"drainGraceSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10080
	// +optional
	KillSessionAfterMinutes int32 `json:"killSessionAfterMinutes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10080
	// +optional
	ReleaseIdleSessionMinutes int32 `json:"releaseIdleSessionMinutes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10080
	// +kubebuilder:default=15
	// +optional
	StartupTimeoutMinutes *int32 `json:"startupTimeoutMinutes,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=5
	// +optional
	SessionStopGraceSeconds *int32 `json:"sessionStopGraceSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=60
	// +optional
	PostSessionHookTimeoutSeconds *int32 `json:"postSessionHookTimeoutSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10080
	// +optional
	ExitIfUnusedMinutes int32 `json:"exitIfUnusedMinutes,omitempty"`
	// +optional
	RemoveSessionState bool `json:"removeSessionState,omitempty"`
	// +optional
	PushOutcomeOnRelease bool `json:"pushOutcomeOnRelease,omitempty"`
	// +optional
	UseAnthropicGitProxy bool `json:"useAnthropicGitProxy,omitempty"`
	// +optional
	GitHostRewrites []string `json:"gitHostRewrites,omitempty"`
	// +optional
	GitSSHRewrites []string `json:"gitSSHRewrites,omitempty"`
	// +optional
	LockToAccount string `json:"lockToAccount,omitempty"`
	// +optional
	ClientLabel string `json:"clientLabel,omitempty"`
	// +kubebuilder:validation:Enum=info;debug
	// +kubebuilder:default=info
	// +optional
	LogLevel string `json:"logLevel,omitempty"`
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	// +optional
	HealthPort *int32 `json:"healthPort,omitempty"`
	// +kubebuilder:default=true
	// +optional
	TrustWorkspace *bool `json:"trustWorkspace,omitempty"`
}

// RunnerSpec describes the runner image and process.
// +kubebuilder:validation:XValidation:rule="!(self.baseDir in ['/etc/claude','/home/runner','/tmp']) && !self.baseDir.startsWith('/etc/claude/')",message="baseDir must not be /etc/claude, /home/runner or /tmp"
// +kubebuilder:validation:XValidation:rule="!has(self.podTemplate) || !has(self.podTemplate.volumeMounts) || !self.podTemplate.volumeMounts.exists(m, m.mountPath in ['/etc/claude','/home/runner','/tmp'] || m.mountPath.startsWith('/etc/claude/') || m.mountPath.startsWith('/home/runner/') || m.mountPath.startsWith('/tmp/'))",message="volumeMounts must not target /etc/claude, /home/runner or /tmp"
// +kubebuilder:validation:XValidation:rule="!has(self.env) || !self.env.exists(e, e.name in ['SELF_HOSTED_RUNNER_HOST_CONFIG_DIR','SELF_HOSTED_RUNNER_CLIENT_LABEL','SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET'])",message="env may not set operator-owned variables"
type RunnerSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.contains('@sha256:') ? size(self.substring(self.indexOf('@sha256:') + 8)) == 64 : (self.lastIndexOf(':') > self.lastIndexOf('/') && !self.endsWith(':') && !self.endsWith(':latest'))",message="runner.image must carry a tag or digest and must not use :latest"
	Image string `json:"image"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Capacity int32 `json:"capacity,omitempty"`
	// +kubebuilder:default="/workspace"
	// +optional
	BaseDir string `json:"baseDir,omitempty"`
	// +kubebuilder:default={}
	// +optional
	Settings RunnerSettings `json:"settings,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="!self.exists(a, ['--environment-secret-file','--pool-secret-file','--capacity','--base-dir','--health-port','--hooks-dir','--exec-path'].exists(f, a == f || a.startsWith(f + '=')))",message="extraArgs may not set operator-owned flags"
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	LifecycleHooks *ConfigMapRef `json:"lifecycleHooks,omitempty"`
	// +optional
	WrapperScript *ConfigMapKeyRef `json:"wrapperScript,omitempty"`
	// +optional
	HostConfig *ConfigMapRef `json:"hostConfig,omitempty"`
	// +kubebuilder:default={}
	// +optional
	PodTemplate PodTemplate `json:"podTemplate,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
	// +optional
	NetworkPolicy *NetworkPolicySpec `json:"networkPolicy,omitempty"`
}

// NetworkPolicySpec configures the optional egress policy.
type NetworkPolicySpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=18
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.matches('^[0-9]{1,3}(\\\\.[0-9]{1,3}){3}/[0-9]{1,2}$'))",message="egressCIDRs must be IPv4 CIDRs"
	// +optional
	EgressCIDRs []string `json:"egressCIDRs,omitempty"`
}

// FixedFleetSpec runs a static set of runners.
type FixedFleetSpec struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// +optional
	PersistentWorkspace *PersistentWorkspaceSpec `json:"persistentWorkspace,omitempty"`
}

// PersistentWorkspaceSpec switches the fleet to a StatefulSet with a PVC per runner.
type PersistentWorkspaceSpec struct {
	VolumeClaimTemplate corev1.PersistentVolumeClaimSpec `json:"volumeClaimTemplate"`
}

// OrchestratorSpec configures `claude self-hosted-runner orchestrator`.
// +kubebuilder:validation:XValidation:rule="!has(self.hookTimeoutSeconds) || !has(self.expectedSpawnSeconds) || self.hookTimeoutSeconds + 5 < self.expectedSpawnSeconds",message="hookTimeoutSeconds + 5 must be below expectedSpawnSeconds"
// +kubebuilder:validation:XValidation:rule="!has(self.env) || !self.env.exists(e, e.name.startsWith('CLAUDE_OPERATOR_'))",message="orchestrator.env may not set operator-owned variables"
type OrchestratorSpec struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=2
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// +optional
	Image string `json:"image,omitempty"`
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=3600
	// +kubebuilder:default=120
	// +optional
	ExpectedSpawnSeconds int32 `json:"expectedSpawnSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=15
	// +kubebuilder:default=60
	// +optional
	HookTimeoutSeconds int32 `json:"hookTimeoutSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=4
	// +optional
	HookConcurrency int32 `json:"hookConcurrency,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinIdle int32 `json:"minIdle,omitempty"`
	// +kubebuilder:validation:Enum=info;debug
	// +kubebuilder:default=info
	// +optional
	LogLevel string `json:"logLevel,omitempty"`
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	// +optional
	HealthPort *int32 `json:"healthPort,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +kubebuilder:default={}
	// +optional
	PodTemplate PodTemplate `json:"podTemplate,omitempty"`
}

// OnDemandSpec spawns one runner per session (implemented in Plan 2).
type OnDemandSpec struct {
	// +kubebuilder:default={}
	// +optional
	Orchestrator OrchestratorSpec `json:"orchestrator,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=300
	// +optional
	RunnerTTLSecondsAfterFinished *int32 `json:"runnerTTLSecondsAfterFinished,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxConcurrentRunners int32 `json:"maxConcurrentRunners,omitempty"`
}

// ClaudeEnvironmentSpec defines the desired state of ClaudeEnvironment.
// +kubebuilder:validation:XValidation:rule="has(self.fixed) != has(self.onDemand)",message="exactly one of spec.fixed or spec.onDemand must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.onDemand) || self.runner.capacity == 1",message="onDemand requires runner.capacity == 1"
// +kubebuilder:validation:XValidation:rule="!has(self.runner.settings.useAnthropicGitProxy) || !self.runner.settings.useAnthropicGitProxy || self.runner.capacity == 1",message="useAnthropicGitProxy requires runner.capacity == 1"
// +kubebuilder:validation:XValidation:rule="!has(self.fixed) || !has(self.fixed.persistentWorkspace) || (has(self.runner.settings.lockToAccount) && size(self.runner.settings.lockToAccount) > 0)",message="fixed.persistentWorkspace requires runner.settings.lockToAccount"
type ClaudeEnvironmentSpec struct {
	EnvironmentSecretRef SecretKeyRef `json:"environmentSecretRef"`
	Runner               RunnerSpec   `json:"runner"`
	// +optional
	Fixed *FixedFleetSpec `json:"fixed,omitempty"`
	// +optional
	OnDemand *OnDemandSpec `json:"onDemand,omitempty"`
}

type FixedFleetStatus struct {
	Replicas        int32 `json:"replicas"`
	ReadyReplicas   int32 `json:"readyReplicas"`
	UpdatedReplicas int32 `json:"updatedReplicas"`
}

type OnDemandStatus struct {
	OrchestratorReadyReplicas int32 `json:"orchestratorReadyReplicas"`
	PendingRunners            int32 `json:"pendingRunners"`
	RunningRunners            int32 `json:"runningRunners"`
}

// ClaudeEnvironmentStatus defines the observed state of ClaudeEnvironment.
type ClaudeEnvironmentStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Enum=fixed;onDemand
	// +optional
	Mode string `json:"mode,omitempty"`
	// +optional
	ComputedDrainBudgetSeconds int64 `json:"computedDrainBudgetSeconds,omitempty"`
	// +optional
	Fixed *FixedFleetStatus `json:"fixed,omitempty"`
	// +optional
	OnDemand *OnDemandStatus `json:"onDemand,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cenv
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.status.mode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.fixed.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClaudeEnvironment is the Schema for the claudeenvironments API.
type ClaudeEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClaudeEnvironmentSpec   `json:"spec,omitempty"`
	Status            ClaudeEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClaudeEnvironmentList contains a list of ClaudeEnvironment.
type ClaudeEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClaudeEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ClaudeEnvironment{}, &ClaudeEnvironmentList{})
		return nil
	})
}
