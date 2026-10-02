# Operator Foundation and Fixed-Fleet Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A working Kubernetes operator that reconciles a `ClaudeEnvironment` in fixed-fleet mode into a hardened runner Deployment or StatefulSet, with validated API, status conditions, metrics, and a kind-based e2e test.

**Architecture:** Kubebuilder v4 project. Pure builder functions turn a `ClaudeEnvironment` plus its referenced Secret and ConfigMaps into Kubernetes objects; one reconciler applies them with server-side apply and writes status once per pass. On-demand mode, the hook, and `ClaudeRunner` are Plan 2; Helm, NetworkPolicy, and release are Plan 3.

**Tech Stack:** Go 1.27, kubebuilder v4, controller-runtime, controller-gen, envtest, Ginkgo/Gomega, kind, golangci-lint, distroless images.

**Spec:** `docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`

## Global Constraints

- Module path `github.com/AhmadMasry/claude-self-hosted-environment-operator`; API group `selfhosted.claudecode.dev`, version `v1alpha1`.
- Every pod the operator creates meets the Restricted Pod Security Standard: `runAsNonRoot: true`, `seccompProfile.type: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `readOnlyRootFilesystem: true`, no host namespaces or hostPorts, volumes only from the Restricted allowlist.
- Runner container args always begin `self-hosted-runner --environment-secret-file /etc/claude/environment-secret --capacity <n> --base-dir <dir>`; `extraArgs` may not set those flags, `--health-port`, `--hooks-dir`, or `--exec-path`.
- Drain budget formula (spec section 6.1): `sessionStopGrace + drainWait + postSessionHookTimeout + 15`, `+30` if pushOutcomeOnRelease, `+ deferShutdownMaxMinutes*60 + (75 if drainWait <= 60 else drainWait+15)` if deferShutdownMaxMinutes > 0. Defaults give 80.
- Images must carry a tag or digest and never `:latest`.
- Pods carry label `app.kubernetes.io/part-of: claude-code-self-hosted-runner` and a container port named `health`.
- One spec mutation per reconcile; status written once at the end of a pass.
- No email, JWT, or environment secret value in any log line, event, condition message, or metric label.
- Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- License Apache-2.0.

## Review Focus

1. A Secret that exists but lacks the configured key: `SecretFound=False`, reason `SecretKeyMissing`, no Deployment created, no panic. Test in Task 7.
2. An image on a registry with a port and no tag (`registry.local:5000/runner`) must be rejected, while `registry.local:5000/runner:2.1.280` and a `@sha256:` digest must be accepted. Test in Task 2.
3. A user-set `terminationGracePeriodSeconds` below the computed budget: `Degraded=True`, reason `GracePeriodTooShort`, the user's value is left in place on the Deployment. Test in Task 7.
4. A referenced hooks or wrapper ConfigMap that does not exist: `Degraded=True`, reason `ConfigMapMissing`, Deployment not created, reconcile returns without error and requeues. Test in Task 7.
5. Changing the content of a referenced ConfigMap must change the pod-template hash and roll the Deployment. Test in Task 6 (hash) and Task 7 (watch).

---

### Task 1: Scaffold the project with kubebuilder

**Files:**
- Create: everything kubebuilder generates (`PROJECT`, `go.mod`, `cmd/main.go`, `api/v1alpha1/`, `internal/controller/`, `config/`, `test/`, `Makefile`, `Dockerfile`, `.golangci.yml`, `.github/workflows/`)
- Create: `LICENSE`

**Interfaces:**
- Produces: Go module `github.com/AhmadMasry/claude-self-hosted-environment-operator`; package `api/v1alpha1` with `ClaudeEnvironment` and `ClaudeEnvironmentList`; `internal/controller.ClaudeEnvironmentReconciler`; `make manifests generate build test lint`.

- [ ] **Step 1: Install kubebuilder and verify toolchain**

```bash
go install sigs.k8s.io/kubebuilder/v4@latest
kubebuilder version
go version   # expect go1.27.x
```

- [ ] **Step 2: Initialise the project**

Run from the repo root (the directory holds only `.git` and `docs/**/*.md`, which kubebuilder allows):

```bash
kubebuilder init --plugins go/v4 \
  --domain claudecode.dev \
  --repo github.com/AhmadMasry/claude-self-hosted-environment-operator \
  --project-name claude-selfhosted-operator \
  --license apache2 --owner "Ahmad Masry"
curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE
echo '.superpowers/' >> .gitignore
```

- [ ] **Step 3: Create the ClaudeEnvironment API and controller**

```bash
kubebuilder create api --group selfhosted --version v1alpha1 --kind ClaudeEnvironment --resource --controller
```

Answer `y` to both prompts if asked. The group resolves to `selfhosted.claudecode.dev`.

- [ ] **Step 4: Verify the scaffold builds and its tests pass**

```bash
make manifests generate build
make test
```

Expected: `go build` succeeds; the scaffolded Ginkgo suite downloads envtest binaries and passes.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: scaffold kubebuilder project with ClaudeEnvironment API

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: ClaudeEnvironment API types with CEL validation

**Files:**
- Modify: `api/v1alpha1/claudeenvironment_types.go` (replace scaffolded content)
- Create: `api/v1alpha1/podtemplate_types.go`
- Create: `api/v1alpha1/conditions.go`
- Delete: `internal/controller/claudeenvironment_controller_test.go` (scaffolded placeholder; it creates an empty spec that the new CEL rules reject)
- Test: `internal/controller/claudeenvironment_validation_test.go`

**Interfaces:**
- Produces: the Go types below, used verbatim by every later task. Condition type and reason constants in `conditions.go`. Label keys `LabelEnvironment`, `LabelRole`, annotation `AnnotationConfigHash`.

- [ ] **Step 1: Write the types**

`api/v1alpha1/podtemplate_types.go`:

```go
package v1alpha1

import corev1 "k8s.io/api/core/v1"

// PodTemplate is the curated subset of a PodSpec users may set. Fields that
// could break the Restricted Pod Security Standard are deliberately absent.
type PodTemplate struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// +optional
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken,omitempty"`
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`
	// +optional
	SchedulerName string `json:"schedulerName,omitempty"`
	// +optional
	SecurityContext *PodSecurityContext `json:"securityContext,omitempty"`
	// +optional
	Volumes []Volume `json:"volumes,omitempty"`
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// PodSecurityContext exposes only the identity fields; everything else is fixed by the operator.
type PodSecurityContext struct {
	// +kubebuilder:validation:Minimum=1
	// +optional
	RunAsUser *int64 `json:"runAsUser,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	RunAsGroup *int64 `json:"runAsGroup,omitempty"`
	// +optional
	FSGroup *int64 `json:"fsGroup,omitempty"`
	// +optional
	SupplementalGroups []int64 `json:"supplementalGroups,omitempty"`
}

// Volume is a pod volume limited to the sources the Restricted standard allows.
// +kubebuilder:validation:XValidation:rule="[has(self.configMap),has(self.secret),has(self.emptyDir),has(self.projected),has(self.downwardAPI),has(self.persistentVolumeClaim),has(self.ephemeral),has(self.csi)].filter(x, x).size() == 1",message="exactly one volume source must be set"
type Volume struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	ConfigMap *corev1.ConfigMapVolumeSource `json:"configMap,omitempty"`
	// +optional
	Secret *corev1.SecretVolumeSource `json:"secret,omitempty"`
	// +optional
	EmptyDir *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`
	// +optional
	Projected *corev1.ProjectedVolumeSource `json:"projected,omitempty"`
	// +optional
	DownwardAPI *corev1.DownwardAPIVolumeSource `json:"downwardAPI,omitempty"`
	// +optional
	PersistentVolumeClaim *corev1.PersistentVolumeClaimVolumeSource `json:"persistentVolumeClaim,omitempty"`
	// +optional
	Ephemeral *corev1.EphemeralVolumeSource `json:"ephemeral,omitempty"`
	// +optional
	CSI *corev1.CSIVolumeSource `json:"csi,omitempty"`
}

// ToCoreVolume converts to the core type.
func (v Volume) ToCoreVolume() corev1.Volume {
	return corev1.Volume{Name: v.Name, VolumeSource: corev1.VolumeSource{
		ConfigMap: v.ConfigMap, Secret: v.Secret, EmptyDir: v.EmptyDir, Projected: v.Projected,
		DownwardAPI: v.DownwardAPI, PersistentVolumeClaim: v.PersistentVolumeClaim,
		Ephemeral: v.Ephemeral, CSI: v.CSI,
	}}
}
```

`api/v1alpha1/conditions.go`:

```go
package v1alpha1

const (
	ConditionReady           = "Ready"
	ConditionSecretFound     = "SecretFound"
	ConditionFleetAvailable  = "FleetAvailable"
	ConditionProgressing     = "Progressing"
	ConditionDegraded        = "Degraded"
	ConditionSecretOnRunners = "SecretOnRunners"

	ReasonSecretMissing          = "SecretMissing"
	ReasonSecretKeyMissing       = "SecretKeyMissing"
	ReasonSecretFound            = "SecretFound"
	ReasonConfigMapMissing       = "ConfigMapMissing"
	ReasonGracePeriodTooShort    = "GracePeriodTooShort"
	ReasonRunnerFailedStart      = "RunnerFailedStart"
	ReasonWorkloadAvailable      = "WorkloadAvailable"
	ReasonWorkloadUnavailable    = "WorkloadUnavailable"
	ReasonReconciling            = "Reconciling"
	ReasonAsExpected             = "AsExpected"
	ReasonFixedModeSecretOnPods  = "FixedModeSecretOnPods"

	LabelEnvironment    = "selfhosted.claudecode.dev/environment"
	LabelRole           = "selfhosted.claudecode.dev/role"
	LabelPartOf         = "app.kubernetes.io/part-of"
	PartOfValue         = "claude-code-self-hosted-runner"
	RoleRunner          = "runner"
	RoleOrchestrator    = "orchestrator"
	AnnotationConfigHash = "selfhosted.claudecode.dev/config-hash"
	FieldOwner          = "claude-selfhosted-operator"
)
```

`api/v1alpha1/claudeenvironment_types.go` (replace the scaffold body, keep the license header and `init()` registration):

```go
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
type RunnerSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self.contains('@sha256:') || (self.lastIndexOf(':') > self.lastIndexOf('/') && !self.endsWith(':latest'))",message="runner.image must carry a tag or digest and must not use :latest"
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

// NetworkPolicySpec configures the optional egress policy (implemented in a later plan).
type NetworkPolicySpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="self.hookTimeoutSeconds + 5 < self.expectedSpawnSeconds",message="hookTimeoutSeconds + 5 must be below expectedSpawnSeconds"
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
	// +kubebuilder:validation:Minimum=1
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
// +kubebuilder:validation:XValidation:rule="!self.runner.settings.useAnthropicGitProxy || self.runner.capacity == 1",message="useAnthropicGitProxy requires runner.capacity == 1"
// +kubebuilder:validation:XValidation:rule="!has(self.fixed) || !has(self.fixed.persistentWorkspace) || self.runner.settings.lockToAccount != ''",message="fixed.persistentWorkspace requires runner.settings.lockToAccount"
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
	SchemeBuilder.Register(&ClaudeEnvironment{}, &ClaudeEnvironmentList{})
}
```

- [ ] **Step 2: Generate and confirm the CRD carries the CEL rules**

```bash
make manifests generate
grep -c 'x-kubernetes-validations' config/crd/bases/selfhosted.claudecode.dev_claudeenvironments.yaml
```

Expected: a count of at least 5 and no controller-gen errors.

- [ ] **Step 3: Write the failing validation tests**

Remove the scaffolded placeholder test first; it is replaced by real tests here and in Task 7:

```bash
git rm internal/controller/claudeenvironment_controller_test.go
```

`internal/controller/claudeenvironment_validation_test.go`:

```go
package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func baseEnv(name string) *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: "env-secret"},
			Runner:               selfhostedv1alpha1.RunnerSpec{Image: "registry.local:5000/runner:2.1.280"},
			Fixed:                &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)},
		},
	}
}

var _ = Describe("ClaudeEnvironment CEL validation", func() {
	ctx := context.Background()

	DescribeTable("rejects invalid specs",
		func(mutate func(*selfhostedv1alpha1.ClaudeEnvironment), substr string) {
			env := baseEnv("invalid")
			mutate(env)
			err := k8sClient.Create(ctx, env)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(substr))
		},
		Entry("both modes", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		}, "exactly one of spec.fixed or spec.onDemand"),
		Entry("no mode", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
		}, "exactly one of spec.fixed or spec.onDemand"),
		Entry("onDemand with capacity 2", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
			e.Spec.Runner.Capacity = 2
		}, "onDemand requires runner.capacity == 1"),
		Entry("git proxy with capacity 2", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Capacity = 2
			e.Spec.Runner.Settings.UseAnthropicGitProxy = true
		}, "useAnthropicGitProxy requires runner.capacity == 1"),
		Entry("persistent workspace without lock", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{}
		}, "requires runner.settings.lockToAccount"),
		Entry("latest tag", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner:latest"
		}, "must not use :latest"),
		Entry("registry port but no tag", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local:5000/runner"
		}, "must carry a tag or digest"),
		Entry("operator-owned flag in extraArgs", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.ExtraArgs = []string{"--capacity=4"}
		}, "operator-owned flags"),
		Entry("hook timeout too close to spawn lease", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{Orchestrator: selfhostedv1alpha1.OrchestratorSpec{
				ExpectedSpawnSeconds: 60, HookTimeoutSeconds: 60}}
		}, "hookTimeoutSeconds + 5 must be below"),
	)

	DescribeTable("accepts valid specs and applies defaults",
		func(mutate func(*selfhostedv1alpha1.ClaudeEnvironment)) {
			env := baseEnv("valid")
			mutate(env)
			Expect(k8sClient.Create(ctx, env)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, env) })
			Expect(env.Spec.Runner.Capacity).To(Equal(int32(1)))
			Expect(env.Spec.Runner.BaseDir).To(Equal("/workspace"))
			Expect(*env.Spec.Runner.Settings.HealthPort).To(Equal(int32(8080)))
			Expect(*env.Spec.Runner.Settings.SessionStopGraceSeconds).To(Equal(int32(5)))
			Expect(env.Spec.EnvironmentSecretRef.Key).To(Equal("environment-secret"))
		},
		Entry("tag on registry with port", func(e *selfhostedv1alpha1.ClaudeEnvironment) {}),
		Entry("digest", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}),
		Entry("onDemand defaults", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Fixed = nil
			e.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		}),
	)
})
```

- [ ] **Step 4: Run the tests to verify they fail**

```bash
make test
```

Expected: FAIL. The scaffolded `suite_test.go` installs the CRD from `config/crd/bases`, so failures should be assertion failures on the error substrings only if the types were not regenerated; if `make manifests` ran, the rejects table passes and the accepts table passes. If everything passes already, that is acceptable: proceed.

- [ ] **Step 5: Run tests to verify they pass**

```bash
make test
```

Expected: PASS, including the scaffolded reconcile test.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(api): ClaudeEnvironment types with CEL validation and defaults

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Drain budget builder

**Files:**
- Create: `internal/builders/drain.go`
- Test: `internal/builders/drain_test.go`

**Interfaces:**
- Consumes: `v1alpha1.RunnerSettings`.
- Produces: `func DrainBudgetSeconds(s v1alpha1.RunnerSettings) int64`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/builders/... -run TestDrainBudgetSeconds
```

Expected: FAIL, `undefined: DrainBudgetSeconds`.

- [ ] **Step 3: Implement**

```go
package builders

import selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"

const (
	defaultSessionStopGraceSeconds       = 5
	defaultPostSessionHookTimeoutSeconds = 60
	drainFixedOverheadSeconds            = 15
	pushOutcomeOverheadSeconds           = 30
	postReleaseGraceSeconds              = 75
)

// DrainBudgetSeconds returns the terminationGracePeriodSeconds the runner
// needs to complete its documented drain path.
func DrainBudgetSeconds(s selfhostedv1alpha1.RunnerSettings) int64 {
	stop := int64(valueOr(s.SessionStopGraceSeconds, defaultSessionStopGraceSeconds))
	hook := int64(valueOr(s.PostSessionHookTimeoutSeconds, defaultPostSessionHookTimeoutSeconds))
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
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/builders/... -run 'TestDrainBudgetSeconds|FuzzDrainBudgetMonotonic' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): drain budget computation

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Runner args builder

**Files:**
- Create: `internal/builders/args.go`
- Test: `internal/builders/args_test.go`

**Interfaces:**
- Consumes: `v1alpha1.RunnerSpec`.
- Produces: `func RunnerArgs(r v1alpha1.RunnerSpec, secretFile string) []string` and path constants `SecretMountPath = "/etc/claude"`, `SecretFileName = "environment-secret"`, `HooksMountPath = "/etc/claude/hooks"`, `WrapperMountPath = "/etc/claude/wrapper"`, `HostConfigMountPath = "/etc/claude/host-config"`.

- [ ] **Step 1: Write the failing test**

```go
package builders

import (
	"reflect"
	"testing"

	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestRunnerArgsDefaults(t *testing.T) {
	r := selfhostedv1alpha1.RunnerSpec{Image: "x:1", Capacity: 1, BaseDir: "/workspace",
		Settings: selfhostedv1alpha1.RunnerSettings{ConfineRepoSettings: "warn", LogLevel: "info",
			HealthPort: ptr.To[int32](8080), StartupTimeoutMinutes: ptr.To[int32](15),
			SessionStopGraceSeconds: ptr.To[int32](5), PostSessionHookTimeoutSeconds: ptr.To[int32](60),
			TrustWorkspace: ptr.To(true)}}
	want := []string{
		"self-hosted-runner",
		"--environment-secret-file", "/etc/claude/environment-secret",
		"--capacity", "1",
		"--base-dir", "/workspace",
		"--health-port", "8080",
		"--confine-repo-settings", "warn",
		"--log-level", "info",
	}
	if got := RunnerArgs(r, "/etc/claude/environment-secret"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestRunnerArgsAllSettings(t *testing.T) {
	r := selfhostedv1alpha1.RunnerSpec{Capacity: 2, BaseDir: "/w",
		Settings: selfhostedv1alpha1.RunnerSettings{
			ConfigureGit: true, ConfineRepoSettings: "enforce", DrainWaitSeconds: 300,
			DeferShutdownMaxMinutes: 5, DrainGraceSeconds: 120, KillSessionAfterMinutes: 240,
			ReleaseIdleSessionMinutes: 30, StartupTimeoutMinutes: ptr.To[int32](10),
			SessionStopGraceSeconds: ptr.To[int32](7), PostSessionHookTimeoutSeconds: ptr.To[int32](90),
			ExitIfUnusedMinutes: 15, RemoveSessionState: true, PushOutcomeOnRelease: true,
			UseAnthropicGitProxy: true, GitHostRewrites: []string{"github.com=ghe.internal"},
			GitSSHRewrites: []string{"ghe.internal"}, LockToAccount: "user_123", ClientLabel: "fleet-a",
			LogLevel: "debug", HealthPort: ptr.To[int32](9090), TrustWorkspace: ptr.To(false)},
		ExtraArgs:      []string{"--debug-token-dir", "/tmp/x"},
		LifecycleHooks: &selfhostedv1alpha1.ConfigMapRef{Name: "h"},
		WrapperScript:  &selfhostedv1alpha1.ConfigMapKeyRef{Name: "w", Key: "wrap.sh"},
	}
	got := RunnerArgs(r, "/etc/claude/environment-secret")
	mustContain := [][]string{
		{"--capacity", "2"}, {"--base-dir", "/w"}, {"--health-port", "9090"},
		{"--confine-repo-settings", "enforce"}, {"--log-level", "debug"}, {"--configure-git"},
		{"--drain-wait-sec", "300"}, {"--defer-shutdown-max-min", "5"}, {"--drain-grace-sec", "120"},
		{"--kill-session-after-min", "240"}, {"--release-idle-session-min", "30"},
		{"--startup-timeout-min", "10"}, {"--session-stop-grace-sec", "7"},
		{"--post-session-hook-timeout-sec", "90"}, {"--exit-if-unused-min", "15"},
		{"--remove-session-state"}, {"--push-outcome-on-release"}, {"--use-anthropic-git-proxy"},
		{"--git-host-rewrite", "github.com=ghe.internal"}, {"--git-ssh-rewrite", "ghe.internal"},
		{"--lock-to-account", "user_123"}, {"--client-label", "fleet-a"}, {"--trust-workspace", "false"},
		{"--hooks-dir", "/etc/claude/hooks"}, {"--exec-path", "/etc/claude/wrapper/wrap.sh"},
		{"--debug-token-dir", "/tmp/x"},
	}
	for _, seq := range mustContain {
		if !containsSeq(got, seq) {
			t.Errorf("missing %v in %v", seq, got)
		}
	}
	if got[len(got)-2] != "--debug-token-dir" {
		t.Errorf("extraArgs must be appended last, got %v", got)
	}
}

func containsSeq(hay, needle []string) bool {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/builders/... -run TestRunnerArgs
```

Expected: FAIL, `undefined: RunnerArgs`.

- [ ] **Step 3: Implement**

```go
package builders

import (
	"path"
	"strconv"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	SecretMountPath     = "/etc/claude"
	SecretFileName      = "environment-secret"
	HooksMountPath      = "/etc/claude/hooks"
	WrapperMountPath    = "/etc/claude/wrapper"
	HostConfigMountPath = "/etc/claude/host-config"
	DefaultHealthPort   = int32(8080)
)

// RunnerArgs renders the container args for `claude self-hosted-runner`.
// Operator-owned flags come first, settings next, extraArgs last.
func RunnerArgs(r selfhostedv1alpha1.RunnerSpec, secretFile string) []string {
	s := r.Settings
	a := []string{
		"self-hosted-runner",
		"--environment-secret-file", secretFile,
		"--capacity", strconv.Itoa(int(max(r.Capacity, 1))),
		"--base-dir", r.BaseDir,
		"--health-port", strconv.Itoa(int(valueOr(s.HealthPort, DefaultHealthPort))),
	}
	a = appendStr(a, "--confine-repo-settings", s.ConfineRepoSettings)
	a = appendStr(a, "--log-level", s.LogLevel)
	if s.ConfigureGit {
		a = append(a, "--configure-git")
	}
	a = appendInt(a, "--drain-wait-sec", s.DrainWaitSeconds)
	a = appendInt(a, "--defer-shutdown-max-min", s.DeferShutdownMaxMinutes)
	a = appendInt(a, "--drain-grace-sec", s.DrainGraceSeconds)
	a = appendInt(a, "--kill-session-after-min", s.KillSessionAfterMinutes)
	a = appendInt(a, "--release-idle-session-min", s.ReleaseIdleSessionMinutes)
	if s.StartupTimeoutMinutes != nil && *s.StartupTimeoutMinutes != 15 {
		a = appendInt(a, "--startup-timeout-min", *s.StartupTimeoutMinutes)
	}
	if s.SessionStopGraceSeconds != nil && *s.SessionStopGraceSeconds != 5 {
		a = appendInt(a, "--session-stop-grace-sec", *s.SessionStopGraceSeconds)
	}
	if s.PostSessionHookTimeoutSeconds != nil && *s.PostSessionHookTimeoutSeconds != 60 {
		a = appendInt(a, "--post-session-hook-timeout-sec", *s.PostSessionHookTimeoutSeconds)
	}
	a = appendInt(a, "--exit-if-unused-min", s.ExitIfUnusedMinutes)
	if s.RemoveSessionState {
		a = append(a, "--remove-session-state")
	}
	if s.PushOutcomeOnRelease {
		a = append(a, "--push-outcome-on-release")
	}
	if s.UseAnthropicGitProxy {
		a = append(a, "--use-anthropic-git-proxy")
	}
	for _, rw := range s.GitHostRewrites {
		a = append(a, "--git-host-rewrite", rw)
	}
	for _, rw := range s.GitSSHRewrites {
		a = append(a, "--git-ssh-rewrite", rw)
	}
	a = appendStr(a, "--lock-to-account", s.LockToAccount)
	a = appendStr(a, "--client-label", s.ClientLabel)
	if s.TrustWorkspace != nil && !*s.TrustWorkspace {
		a = append(a, "--trust-workspace", "false")
	}
	if r.LifecycleHooks != nil {
		a = append(a, "--hooks-dir", HooksMountPath)
	}
	if r.WrapperScript != nil {
		a = append(a, "--exec-path", path.Join(WrapperMountPath, r.WrapperScript.Key))
	}
	return append(a, r.ExtraArgs...)
}

func appendStr(a []string, flag, v string) []string {
	if v == "" {
		return a
	}
	return append(a, flag, v)
}

func appendInt(a []string, flag string, v int32) []string {
	if v == 0 {
		return a
	}
	return append(a, flag, strconv.Itoa(int(v)))
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/builders/... -run TestRunnerArgs -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): render runner CLI args from RunnerSpec

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Hardened runner pod template builder

**Files:**
- Create: `internal/builders/podtemplate.go`
- Test: `internal/builders/podtemplate_test.go`

**Interfaces:**
- Consumes: `RunnerArgs`, `DrainBudgetSeconds`, `v1alpha1` label constants.
- Produces:
  - `type RunnerPodInput struct { Env *v1alpha1.ClaudeEnvironment; ConfigHash string; SecretVolume corev1.VolumeSource; SecretFile string; RestartPolicy corev1.RestartPolicy }`
  - `func RunnerPodTemplate(in RunnerPodInput) corev1.PodTemplateSpec`
  - `func RunnerSelectorLabels(env *v1alpha1.ClaudeEnvironment) map[string]string`
  - `func EffectiveGracePeriod(env *v1alpha1.ClaudeEnvironment) (seconds int64, tooShort bool)`

- [ ] **Step 1: Write the failing test**

```go
package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func testEnv() *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: "claude"},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: "env-secret", Key: "environment-secret"},
			Runner: selfhostedv1alpha1.RunnerSpec{Image: "r:1", Capacity: 1, BaseDir: "/workspace",
				Settings: selfhostedv1alpha1.RunnerSettings{HealthPort: ptr.To[int32](8080),
					SessionStopGraceSeconds: ptr.To[int32](5), PostSessionHookTimeoutSeconds: ptr.To[int32](60)}},
			Fixed: &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)},
		},
	}
}

func secretInput(env *selfhostedv1alpha1.ClaudeEnvironment) RunnerPodInput {
	return RunnerPodInput{Env: env, ConfigHash: "abc",
		SecretVolume: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "env-secret",
			Items: []corev1.KeyToPath{{Key: "environment-secret", Path: SecretFileName}}}},
		SecretFile: SecretMountPath + "/" + SecretFileName, RestartPolicy: corev1.RestartPolicyAlways}
}

func TestRunnerPodTemplateRestricted(t *testing.T) {
	tmpl := RunnerPodTemplate(secretInput(testEnv()))
	ps := tmpl.Spec
	if ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatal("runAsNonRoot must be true")
	}
	if ps.SecurityContext.SeccompProfile == nil || ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("seccomp RuntimeDefault required")
	}
	if ps.HostNetwork || ps.HostPID || ps.HostIPC {
		t.Fatal("host namespaces must be off")
	}
	if len(ps.Containers) != 1 || ps.Containers[0].Name != "runner" {
		t.Fatalf("expected one container named runner, got %+v", ps.Containers)
	}
	c := ps.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container security context not restricted: %+v", sc)
	}
	if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Fatal("termination message policy must fall back to logs")
	}
	if c.Ports[0].Name != "health" || c.Ports[0].ContainerPort != 8080 {
		t.Fatalf("health port wrong: %+v", c.Ports)
	}
	if c.ReadinessProbe.HTTPGet.Path != "/healthz" || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("probes must hit /healthz")
	}
	if *ps.TerminationGracePeriodSeconds != 80 {
		t.Fatalf("grace period %d want 80", *ps.TerminationGracePeriodSeconds)
	}
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Fatal("service account token must not be mounted by default")
	}
	if tmpl.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "abc" {
		t.Fatal("config hash annotation missing")
	}
	for _, k := range []string{selfhostedv1alpha1.LabelEnvironment, selfhostedv1alpha1.LabelRole, selfhostedv1alpha1.LabelPartOf} {
		if tmpl.Labels[k] == "" {
			t.Fatalf("label %s missing", k)
		}
	}
	wantMounts := map[string]string{"environment-secret": "/etc/claude", "workspace": "/workspace", "home": "/home/runner", "tmp": "/tmp"}
	for _, m := range c.VolumeMounts {
		if p, ok := wantMounts[m.Name]; ok && m.MountPath == p {
			delete(wantMounts, m.Name)
		}
	}
	if len(wantMounts) != 0 {
		t.Fatalf("missing mounts %v", wantMounts)
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "environment-secret" && !m.ReadOnly {
			t.Fatal("secret mount must be read-only")
		}
	}
}

func TestRunnerPodTemplateOptionalMounts(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: "hooks"}
	env.Spec.Runner.WrapperScript = &selfhostedv1alpha1.ConfigMapKeyRef{Name: "wrap", Key: "w.sh"}
	env.Spec.Runner.HostConfig = &selfhostedv1alpha1.ConfigMapRef{Name: "cfg"}
	tmpl := RunnerPodTemplate(secretInput(env))
	c := tmpl.Spec.Containers[0]
	byName := map[string]corev1.Volume{}
	for _, v := range tmpl.Spec.Volumes {
		byName[v.Name] = v
	}
	if *byName["hooks"].ConfigMap.DefaultMode != 0o555 || *byName["wrapper"].ConfigMap.DefaultMode != 0o555 || *byName["host-config"].ConfigMap.DefaultMode != 0o444 {
		t.Fatal("hooks/wrapper must be 0555 and host-config 0444")
	}
	var sawHostCfg bool
	for _, e := range c.Env {
		if e.Name == "SELF_HOSTED_RUNNER_HOST_CONFIG_DIR" && e.Value == HostConfigMountPath {
			sawHostCfg = true
		}
	}
	if !sawHostCfg {
		t.Fatal("SELF_HOSTED_RUNNER_HOST_CONFIG_DIR not set")
	}
	for _, m := range c.VolumeMounts {
		if (m.Name == "hooks" || m.Name == "wrapper" || m.Name == "host-config") && !m.ReadOnly {
			t.Fatalf("%s must be read-only", m.Name)
		}
	}
}

func TestRunnerPodTemplateUserOverrides(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.PodTemplate = selfhostedv1alpha1.PodTemplate{
		Labels: map[string]string{"team": "a", selfhostedv1alpha1.LabelRole: "hacked"},
		ServiceAccountName: "runner-sa", AutomountServiceAccountToken: ptr.To(true),
		SecurityContext: &selfhostedv1alpha1.PodSecurityContext{RunAsUser: ptr.To[int64](10001), FSGroup: ptr.To[int64](10001)},
		Volumes: []selfhostedv1alpha1.Volume{{Name: "cache", EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		VolumeMounts: []corev1.VolumeMount{{Name: "cache", MountPath: "/cache"}},
	}
	env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](600)
	tmpl := RunnerPodTemplate(secretInput(env))
	if tmpl.Labels["team"] != "a" || tmpl.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
		t.Fatal("user labels merge but operator labels win")
	}
	if tmpl.Spec.ServiceAccountName != "runner-sa" || !*tmpl.Spec.AutomountServiceAccountToken {
		t.Fatal("service account overrides not applied")
	}
	if *tmpl.Spec.SecurityContext.RunAsUser != 10001 || *tmpl.Spec.SecurityContext.FSGroup != 10001 {
		t.Fatal("identity overrides not applied")
	}
	if *tmpl.Spec.TerminationGracePeriodSeconds != 600 {
		t.Fatal("user grace period must be honoured")
	}
	found := false
	for _, m := range tmpl.Spec.Containers[0].VolumeMounts {
		if m.Name == "cache" && m.MountPath == "/cache" {
			found = true
		}
	}
	if !found {
		t.Fatal("user volume mount missing")
	}
}

func TestEffectiveGracePeriod(t *testing.T) {
	env := testEnv()
	if s, short := EffectiveGracePeriod(env); s != 80 || short {
		t.Fatalf("computed: got %d %v", s, short)
	}
	env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](30)
	if s, short := EffectiveGracePeriod(env); s != 30 || !short {
		t.Fatalf("too short: got %d %v", s, short)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/builders/... -run 'TestRunnerPodTemplate|TestEffectiveGracePeriod'
```

Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

```go
package builders

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	RunnerContainerName = "runner"
	HomeMountPath       = "/home/runner"
	TmpMountPath        = "/tmp"
	WorkspaceVolume     = "workspace"
)

// RunnerPodInput is everything RunnerPodTemplate needs beyond the environment.
type RunnerPodInput struct {
	Env           *selfhostedv1alpha1.ClaudeEnvironment
	ConfigHash    string
	SecretVolume  corev1.VolumeSource // environment secret or per-order work-order secret
	SecretFile    string              // path passed to --environment-secret-file
	RestartPolicy corev1.RestartPolicy
	// WorkspaceFromPVC, when true, omits the workspace emptyDir because a
	// StatefulSet volumeClaimTemplate named WorkspaceVolume provides it.
	WorkspaceFromPVC bool
}

// RunnerSelectorLabels are the immutable labels used as the workload selector.
func RunnerSelectorLabels(env *selfhostedv1alpha1.ClaudeEnvironment) map[string]string {
	return map[string]string{
		selfhostedv1alpha1.LabelEnvironment: env.Name,
		selfhostedv1alpha1.LabelRole:        selfhostedv1alpha1.RoleRunner,
	}
}

// EffectiveGracePeriod returns the grace period to put on the pod and whether
// a user-supplied value is below the computed drain budget.
func EffectiveGracePeriod(env *selfhostedv1alpha1.ClaudeEnvironment) (int64, bool) {
	budget := DrainBudgetSeconds(env.Spec.Runner.Settings)
	if u := env.Spec.Runner.TerminationGracePeriodSeconds; u != nil {
		return *u, *u < budget
	}
	return budget, false
}

// RunnerPodTemplate builds the hardened pod template for a runner.
func RunnerPodTemplate(in RunnerPodInput) corev1.PodTemplateSpec {
	env := in.Env
	r := env.Spec.Runner
	pt := r.PodTemplate
	healthPort := valueOr(r.Settings.HealthPort, DefaultHealthPort)
	grace, _ := EffectiveGracePeriod(env)

	labels := map[string]string{}
	maps.Copy(labels, pt.Labels)
	maps.Copy(labels, RunnerSelectorLabels(env))
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue

	annotations := map[string]string{}
	maps.Copy(annotations, pt.Annotations)
	annotations[selfhostedv1alpha1.AnnotationConfigHash] = in.ConfigHash

	volumes := []corev1.Volume{
		{Name: "environment-secret", VolumeSource: in.SecretVolume},
		{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if !in.WorkspaceFromPVC {
		volumes = append(volumes, corev1.Volume{Name: WorkspaceVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	mounts := []corev1.VolumeMount{
		{Name: "environment-secret", MountPath: SecretMountPath, ReadOnly: true},
		{Name: WorkspaceVolume, MountPath: r.BaseDir},
		{Name: "home", MountPath: HomeMountPath},
		{Name: "tmp", MountPath: TmpMountPath},
	}
	envVars := append([]corev1.EnvVar{}, r.Env...)
	if r.Settings.ClientLabel == "" {
		envVars = append(envVars, corev1.EnvVar{Name: "SELF_HOSTED_RUNNER_CLIENT_LABEL",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}})
	}
	if r.LifecycleHooks != nil {
		volumes = append(volumes, configMapVolume("hooks", r.LifecycleHooks.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: "hooks", MountPath: HooksMountPath, ReadOnly: true})
	}
	if r.WrapperScript != nil {
		volumes = append(volumes, configMapVolume("wrapper", r.WrapperScript.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: "wrapper", MountPath: WrapperMountPath, ReadOnly: true})
	}
	if r.HostConfig != nil {
		volumes = append(volumes, configMapVolume("host-config", r.HostConfig.Name, 0o444))
		mounts = append(mounts, corev1.VolumeMount{Name: "host-config", MountPath: HostConfigMountPath, ReadOnly: true})
		envVars = append(envVars, corev1.EnvVar{Name: "SELF_HOSTED_RUNNER_HOST_CONFIG_DIR", Value: HostConfigMountPath})
	}
	for _, v := range pt.Volumes {
		volumes = append(volumes, v.ToCoreVolume())
	}
	mounts = append(mounts, pt.VolumeMounts...)

	podSC := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if pt.SecurityContext != nil {
		podSC.RunAsUser = pt.SecurityContext.RunAsUser
		podSC.RunAsGroup = pt.SecurityContext.RunAsGroup
		podSC.FSGroup = pt.SecurityContext.FSGroup
		podSC.SupplementalGroups = pt.SecurityContext.SupplementalGroups
	}

	container := corev1.Container{
		Name:  RunnerContainerName,
		Image: r.Image,
		Args:  RunnerArgs(r, in.SecretFile),
		Env:   envVars,
		Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: healthPort, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: "/healthz", Port: intstr.FromString("health")}}, InitialDelaySeconds: 5, PeriodSeconds: 10},
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: "/healthz", Port: intstr.FromString("health")}}, InitialDelaySeconds: 30, PeriodSeconds: 30},
		Resources:                pt.Resources,
		VolumeMounts:             mounts,
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}

	automount := false
	if pt.AutomountServiceAccountToken != nil {
		automount = *pt.AutomountServiceAccountToken
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
		Spec: corev1.PodSpec{
			Containers:                    []corev1.Container{container},
			Volumes:                       volumes,
			RestartPolicy:                 in.RestartPolicy,
			TerminationGracePeriodSeconds: ptr.To(grace),
			ServiceAccountName:            pt.ServiceAccountName,
			AutomountServiceAccountToken:  ptr.To(automount),
			ImagePullSecrets:              pt.ImagePullSecrets,
			NodeSelector:                  pt.NodeSelector,
			Tolerations:                   pt.Tolerations,
			Affinity:                      pt.Affinity,
			TopologySpreadConstraints:     pt.TopologySpreadConstraints,
			PriorityClassName:             pt.PriorityClassName,
			RuntimeClassName:              pt.RuntimeClassName,
			SchedulerName:                 pt.SchedulerName,
			SecurityContext:               podSC,
		},
	}
}

func configMapVolume(name, cmName string, mode int32) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: cmName}, DefaultMode: ptr.To(mode)}}}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/builders/... -v
```

Expected: PASS for all builder tests.

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): Restricted-compliant runner pod template

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Config hash and fixed-fleet workload builders

**Files:**
- Create: `internal/builders/hash.go`
- Create: `internal/builders/fixed.go`
- Test: `internal/builders/hash_test.go`, `internal/builders/fixed_test.go`

**Interfaces:**
- Consumes: `RunnerPodTemplate`, `RunnerSelectorLabels`.
- Produces:
  - `func ConfigHash(env *v1alpha1.ClaudeEnvironment, secret *corev1.Secret, configMaps []*corev1.ConfigMap) string`
  - `func FixedWorkloadName(env *v1alpha1.ClaudeEnvironment) string` returning `<env>-runner`
  - `func FixedDeployment(env *v1alpha1.ClaudeEnvironment, hash string) *appsv1.Deployment`
  - `func FixedStatefulSet(env *v1alpha1.ClaudeEnvironment, hash string) *appsv1.StatefulSet`
  - `func EnvironmentSecretVolume(env *v1alpha1.ClaudeEnvironment) corev1.VolumeSource`

- [ ] **Step 1: Write the failing tests**

`internal/builders/hash_test.go`:

```go
package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigHashChangesWithInputs(t *testing.T) {
	env := testEnv()
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "env-secret"}, Data: map[string][]byte{"environment-secret": []byte("k1")}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "hooks"}, Data: map[string]string{"checkout": "#!/bin/sh"}}

	base := ConfigHash(env, sec, []*corev1.ConfigMap{cm})
	if len(base) != 16 {
		t.Fatalf("hash should be 16 hex chars, got %q", base)
	}
	if ConfigHash(env, sec, []*corev1.ConfigMap{cm}) != base {
		t.Fatal("hash must be deterministic")
	}
	sec2 := sec.DeepCopy()
	sec2.Data["environment-secret"] = []byte("k2")
	if ConfigHash(env, sec2, []*corev1.ConfigMap{cm}) == base {
		t.Fatal("secret change must change hash")
	}
	cm2 := cm.DeepCopy()
	cm2.Data["checkout"] = "#!/bin/bash"
	if ConfigHash(env, sec, []*corev1.ConfigMap{cm2}) == base {
		t.Fatal("configmap change must change hash")
	}
	env2 := env.DeepCopy()
	env2.Spec.Runner.Settings.DrainWaitSeconds = 10
	if ConfigHash(env2, sec, []*corev1.ConfigMap{cm}) == base {
		t.Fatal("runner spec change must change hash")
	}
	env3 := env.DeepCopy()
	env3.Spec.Fixed.Replicas = new(int32)
	if ConfigHash(env3, sec, []*corev1.ConfigMap{cm}) != base {
		t.Fatal("replica change must not change hash")
	}
	if ConfigHash(env, nil, nil) == "" {
		t.Fatal("nil inputs must still hash")
	}
}
```

`internal/builders/fixed_test.go`:

```go
package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestFixedDeployment(t *testing.T) {
	env := testEnv()
	env.Spec.Fixed.Replicas = ptr.To[int32](3)
	d := FixedDeployment(env, "h1")
	if d.Name != "platform-runner" || d.Namespace != "claude" {
		t.Fatalf("name/namespace wrong: %s/%s", d.Namespace, d.Name)
	}
	if d.APIVersion != "apps/v1" || d.Kind != "Deployment" {
		t.Fatal("TypeMeta must be set for server-side apply")
	}
	if *d.Spec.Replicas != 3 {
		t.Fatal("replicas not propagated")
	}
	if d.Spec.Selector.MatchLabels[selfhostedv1alpha1.LabelEnvironment] != "platform" {
		t.Fatal("selector must use environment label")
	}
	if d.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h1" {
		t.Fatal("hash annotation missing")
	}
	if d.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Fatal("fixed runners restart")
	}
	v := d.Spec.Template.Spec.Volumes[0]
	if v.Secret == nil || v.Secret.SecretName != "env-secret" || v.Secret.Items[0].Key != "environment-secret" || v.Secret.Items[0].Path != "environment-secret" {
		t.Fatalf("secret volume wrong: %+v", v)
	}
	if d.Spec.Template.Spec.Containers[0].Args[2] != "/etc/claude/environment-secret" {
		t.Fatal("secret file path wrong")
	}
}

func TestFixedStatefulSet(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.Settings.LockToAccount = "user_1"
	env.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{
		VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}}}}
	s := FixedStatefulSet(env, "h2")
	if s.Kind != "StatefulSet" || s.Name != "platform-runner" {
		t.Fatal("statefulset identity wrong")
	}
	if s.Spec.ServiceName != "platform-runner" {
		t.Fatal("serviceName must be set")
	}
	if len(s.Spec.VolumeClaimTemplates) != 1 || s.Spec.VolumeClaimTemplates[0].Name != WorkspaceVolume {
		t.Fatal("volumeClaimTemplate must be named workspace")
	}
	for _, v := range s.Spec.Template.Spec.Volumes {
		if v.Name == WorkspaceVolume {
			t.Fatal("workspace must come from the PVC, not an emptyDir")
		}
	}
	if s.Spec.PodManagementPolicy != "Parallel" {
		t.Fatal("runners are independent; use Parallel")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/builders/... -run 'TestConfigHash|TestFixed'
```

Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

`internal/builders/hash.go`:

```go
package builders

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// ConfigHash digests everything that must roll runner pods when it changes:
// the runner spec, the environment secret's data, and referenced ConfigMaps.
// Replica counts and other workload-level fields are excluded on purpose.
func ConfigHash(env *selfhostedv1alpha1.ClaudeEnvironment, secret *corev1.Secret, configMaps []*corev1.ConfigMap) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(env.Spec.Runner)
	if secret != nil {
		writeSortedBytes(h, secret.Data)
	}
	sort.Slice(configMaps, func(i, j int) bool { return configMaps[i].Name < configMaps[j].Name })
	for _, cm := range configMaps {
		if cm == nil {
			continue
		}
		_, _ = h.Write([]byte(cm.Name))
		writeSortedStrings(h, cm.Data)
		writeSortedBytes(h, cm.BinaryData)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func writeSortedBytes(h interface{ Write([]byte) (int, error) }, m map[string][]byte) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write(m[k])
	}
}

func writeSortedStrings(h interface{ Write([]byte) (int, error) }, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte(m[k]))
	}
}
```

`internal/builders/fixed.go`:

```go
package builders

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// FixedWorkloadName is the name of the Deployment or StatefulSet for a fixed fleet.
func FixedWorkloadName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-runner"
}

// EnvironmentSecretVolume mounts the environment key at /etc/claude/environment-secret.
func EnvironmentSecretVolume(env *selfhostedv1alpha1.ClaudeEnvironment) corev1.VolumeSource {
	return corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: env.Spec.EnvironmentSecretRef.Name,
		Items:      []corev1.KeyToPath{{Key: env.Spec.EnvironmentSecretRef.Key, Path: SecretFileName}},
	}}
}

func fixedInput(env *selfhostedv1alpha1.ClaudeEnvironment, hash string, pvc bool) RunnerPodInput {
	return RunnerPodInput{
		Env: env, ConfigHash: hash,
		SecretVolume:     EnvironmentSecretVolume(env),
		SecretFile:       SecretMountPath + "/" + SecretFileName,
		RestartPolicy:    corev1.RestartPolicyAlways,
		WorkspaceFromPVC: pvc,
	}
}

func workloadMeta(env *selfhostedv1alpha1.ClaudeEnvironment) metav1.ObjectMeta {
	labels := RunnerSelectorLabels(env)
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	return metav1.ObjectMeta{Name: FixedWorkloadName(env), Namespace: env.Namespace, Labels: labels}
}

// FixedDeployment builds the Deployment for a fixed fleet without persistent workspace.
func FixedDeployment(env *selfhostedv1alpha1.ClaudeEnvironment, hash string) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: workloadMeta(env),
		Spec: appsv1.DeploymentSpec{
			Replicas: env.Spec.Fixed.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: RunnerSelectorLabels(env)},
			Template: RunnerPodTemplate(fixedInput(env, hash, false)),
		},
	}
}

// FixedStatefulSet builds the StatefulSet for a fixed fleet with a persistent workspace.
func FixedStatefulSet(env *selfhostedv1alpha1.ClaudeEnvironment, hash string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: workloadMeta(env),
		Spec: appsv1.StatefulSetSpec{
			Replicas:            env.Spec.Fixed.Replicas,
			ServiceName:         FixedWorkloadName(env),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: RunnerSelectorLabels(env)},
			Template:            RunnerPodTemplate(fixedInput(env, hash, true)),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolume},
				Spec:       env.Spec.Fixed.PersistentWorkspace.VolumeClaimTemplate,
			}},
		},
	}
}
```

- [ ] **Step 4: Run to verify they pass**

```bash
go test ./internal/builders/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): config hash and fixed-fleet Deployment/StatefulSet

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: ClaudeEnvironment reconciler for fixed mode

**Files:**
- Modify: `api/v1alpha1/conditions.go` (add `ReasonUnsupportedMode`)
- Modify: `internal/controller/claudeenvironment_controller.go` (replace scaffold body)
- Create: `internal/controller/status.go`
- Modify: `internal/controller/suite_test.go` (start a manager with the reconciler)
- Test: `internal/controller/claudeenvironment_fixed_test.go`

**Interfaces:**
- Consumes: all `builders` functions from Tasks 3 to 6.
- Produces: `ClaudeEnvironmentReconciler{Client, Scheme, Recorder}` with `Reconcile` and `SetupWithManager`; `statusPass` helper; index names `indexSecretName = "spec.environmentSecretRef.name"` and `indexConfigMapNames = "spec.runner.configMapNames"`. Task 8 adds a hook point `r.checkRunnerPods` inside `reconcileFixed`; Task 9 adds metric calls in `Reconcile`.

- [ ] **Step 1: Add the missing reason constant**

Append to the `const` block in `api/v1alpha1/conditions.go`:

```go
	ReasonUnsupportedMode = "UnsupportedMode"
```

- [ ] **Step 2: Write the status helper**

`internal/controller/status.go`:

```go
package controller

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

type degradation struct{ reason, message string }

// statusPass accumulates condition changes for one reconcile and writes them
// to the object once in finish(), so a pass produces a single status update.
type statusPass struct {
	env      *selfhostedv1alpha1.ClaudeEnvironment
	degraded []degradation
}

func newStatusPass(env *selfhostedv1alpha1.ClaudeEnvironment) *statusPass {
	return &statusPass{env: env}
}

func (p *statusPass) set(t string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&p.env.Status.Conditions, metav1.Condition{
		Type: t, Status: status, Reason: reason, Message: message, ObservedGeneration: p.env.Generation,
	})
}

func (p *statusPass) degrade(reason, message string) {
	p.degraded = append(p.degraded, degradation{reason, message})
}

func (p *statusPass) isTrue(t string) bool {
	return meta.IsStatusConditionTrue(p.env.Status.Conditions, t)
}

// finish derives Degraded, Ready and Progressing from what the pass recorded.
func (p *statusPass) finish() {
	if len(p.degraded) > 0 {
		msgs := make([]string, 0, len(p.degraded))
		for _, d := range p.degraded {
			msgs = append(msgs, d.message)
		}
		p.set(selfhostedv1alpha1.ConditionDegraded, metav1.ConditionTrue, p.degraded[0].reason, strings.Join(msgs, "; "))
	} else {
		p.set(selfhostedv1alpha1.ConditionDegraded, metav1.ConditionFalse, selfhostedv1alpha1.ReasonAsExpected, "")
	}
	ready := p.isTrue(selfhostedv1alpha1.ConditionSecretFound) &&
		p.isTrue(selfhostedv1alpha1.ConditionFleetAvailable) && len(p.degraded) == 0
	if ready {
		p.set(selfhostedv1alpha1.ConditionReady, metav1.ConditionTrue, selfhostedv1alpha1.ReasonAsExpected, "fleet is serving")
		p.set(selfhostedv1alpha1.ConditionProgressing, metav1.ConditionFalse, selfhostedv1alpha1.ReasonAsExpected, "")
	} else {
		reason := selfhostedv1alpha1.ReasonReconciling
		if len(p.degraded) > 0 {
			reason = p.degraded[0].reason
		} else if !p.isTrue(selfhostedv1alpha1.ConditionSecretFound) {
			reason = selfhostedv1alpha1.ReasonSecretMissing
		} else if !p.isTrue(selfhostedv1alpha1.ConditionFleetAvailable) {
			reason = selfhostedv1alpha1.ReasonWorkloadUnavailable
		}
		p.set(selfhostedv1alpha1.ConditionReady, metav1.ConditionFalse, reason, "")
		p.set(selfhostedv1alpha1.ConditionProgressing, metav1.ConditionTrue, selfhostedv1alpha1.ReasonReconciling, "")
	}
	p.env.Status.ObservedGeneration = p.env.Generation
}
```

- [ ] **Step 3: Write the failing reconciler tests**

Replace the body of `BeforeSuite` in `internal/controller/suite_test.go` after `k8sClient` is created (keep everything the scaffold already does for envtest start, scheme, and client) with a running manager:

```go
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect((&ClaudeEnvironmentReconciler{
		Client:   k8sManager.GetClient(),
		Scheme:   k8sManager.GetScheme(),
		Recorder: k8sManager.GetEventRecorderFor("claude-selfhosted-operator-test"),
	}).SetupWithManager(k8sManager)).To(Succeed())
	go func() {
		defer GinkgoRecover()
		Expect(k8sManager.Start(ctx)).To(Succeed())
	}()
```

Add imports `ctrl "sigs.k8s.io/controller-runtime"` and `metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"`. The scaffold's `k8sClient` is a direct client; keep using it in tests so reads bypass the manager cache.

`internal/controller/claudeenvironment_fixed_test.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	timeout  = 10 * time.Second
	interval = 200 * time.Millisecond
)

var nsCounter int

func newNamespace(ctx context.Context) string {
	nsCounter++
	name := fmt.Sprintf("fixed-%d-%d", GinkgoParallelProcess(), nsCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func envSecret(ns, key string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "env-secret", Namespace: ns},
		Data: map[string][]byte{key: []byte("ccenvkey_test")}}
}

func fixedEnv(ns string) *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: ns},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{
			EnvironmentSecretRef: selfhostedv1alpha1.SecretKeyRef{Name: "env-secret"},
			Runner:               selfhostedv1alpha1.RunnerSpec{Image: "registry.local/runner:2.1.280"},
			Fixed:                &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](2)},
		},
	}
}

func condition(ctx context.Context, key types.NamespacedName, t string) func() (*metav1.Condition, error) {
	return func() (*metav1.Condition, error) {
		env := &selfhostedv1alpha1.ClaudeEnvironment{}
		if err := k8sClient.Get(ctx, key, env); err != nil {
			return nil, err
		}
		c := meta.FindStatusCondition(env.Status.Conditions, t)
		if c == nil {
			return nil, fmt.Errorf("condition %s not set yet", t)
		}
		return c, nil
	}
}

func haveReason(status metav1.ConditionStatus, reason string) OmegaMatcher {
	return And(HaveField("Status", status), HaveField("Reason", reason))
}

var _ = Describe("ClaudeEnvironment fixed mode", func() {
	ctx := context.Background()

	It("creates a hardened Deployment and reports Ready once the Deployment is available", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)

		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, dep)
		}, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		Expect(*dep.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(80)))
		Expect(dep.Spec.Template.Spec.Containers[0].Args[:3]).To(Equal([]string{"self-hosted-runner", "--environment-secret-file", "/etc/claude/environment-secret"}))
		Expect(dep.OwnerReferences).To(HaveLen(1))
		Expect(dep.OwnerReferences[0].Name).To(Equal("platform"))
		Expect(dep.Spec.Template.Annotations).To(HaveKey(selfhostedv1alpha1.AnnotationConfigHash))

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonSecretFound))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonFixedModeSecretOnPods))

		// Simulate the deployment controller.
		dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, dep.Status.UpdatedReplicas = 2, 2, 2, 2
		dep.Status.ObservedGeneration = dep.Generation
		dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Mode).To(Equal("fixed"))
		Expect(got.Status.ComputedDrainBudgetSeconds).To(Equal(int64(80)))
		Expect(got.Status.Fixed.ReadyReplicas).To(Equal(int32(2)))
		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
	})

	It("reports SecretMissing and creates no Deployment when the Secret is absent", func() {
		ns := newNamespace(ctx)
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())

		// Creating the Secret later recovers without any change to the environment.
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
	})

	It("reports SecretKeyMissing when the Secret lacks the configured key", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "wrong-key"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretFound), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretKeyMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())
	})

	It("reports ConfigMapMissing and creates no Deployment when a referenced ConfigMap is absent", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: "hooks"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonConfigMapMissing))
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, &appsv1.Deployment{})
		}, 2*time.Second, interval).ShouldNot(Succeed())
	})

	It("rolls the pod template when a referenced ConfigMap or the Secret changes", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "hooks", Namespace: ns}, Data: map[string]string{"checkout": "v1"}}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.LifecycleHooks = &selfhostedv1alpha1.ConfigMapRef{Name: "hooks"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())

		hash := func() (string, error) {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, dep); err != nil {
				return "", err
			}
			return dep.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash], nil
		}
		var h1 string
		Eventually(func() error { var err error; h1, err = hash(); return err }, timeout, interval).Should(Succeed())

		cm.Data["checkout"] = "v2"
		Expect(k8sClient.Update(ctx, cm)).To(Succeed())
		Eventually(hash, timeout, interval).ShouldNot(Equal(h1))

		var h2 string
		Eventually(func() error { var err error; h2, err = hash(); return err }, timeout, interval).Should(Succeed())
		sec := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "env-secret", Namespace: ns}, sec)).To(Succeed())
		sec.Data["environment-secret"] = []byte("ccenvkey_rotated")
		Expect(k8sClient.Update(ctx, sec)).To(Succeed())
		Eventually(hash, timeout, interval).ShouldNot(Equal(h2))
	})

	It("keeps a user grace period that is too short and flags it as Degraded", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.TerminationGracePeriodSeconds = ptr.To[int64](30)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonGracePeriodTooShort))
		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, dep)
		}, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(30)))
	})

	It("creates a StatefulSet for a persistent workspace and removes a stale Deployment", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, &appsv1.Deployment{})
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		env.Spec.Runner.Settings.LockToAccount = "user_123"
		env.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{
			VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: mustQuantity("10Gi")}}}}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())

		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, sts)
		}, timeout, interval).Should(Succeed())
		Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal(builders.WorkspaceVolume))
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "platform-runner", Namespace: ns}, &appsv1.Deployment{})
		}, timeout, interval).ShouldNot(Succeed())
	})

	It("marks onDemand as unsupported in this version", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Fixed = nil
		env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonUnsupportedMode))
	})
})
```

Add this helper to the same file:

```go
func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }
```

with import `"k8s.io/apimachinery/pkg/api/resource"`.

- [ ] **Step 4: Run to verify the tests fail**

```bash
make test
```

Expected: compile failure on `Recorder` field and missing `SetupWithManager` changes, or test failures because the scaffolded reconciler does nothing.

- [ ] **Step 5: Implement the reconciler**

`internal/controller/claudeenvironment_controller.go` (replace the scaffold body, keep the license header):

```go
package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	requeueAfterUserFix = 30 * time.Second
	resyncPeriod        = 10 * time.Minute

	indexSecretName     = "spec.environmentSecretRef.name"
	indexConfigMapNames = "spec.runner.configMapNames"
)

// ClaudeEnvironmentReconciler reconciles a ClaudeEnvironment object.
type ClaudeEnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one ClaudeEnvironment towards its desired state.
func (r *ClaudeEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("environment", req.Name, "namespace", req.Namespace)
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !env.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	before := env.Status.DeepCopy()
	pass := newStatusPass(env)
	res, err := r.reconcile(ctx, env, pass)
	pass.finish()

	if !equality.Semantic.DeepEqual(before, &env.Status) {
		if uerr := r.Status().Update(ctx, env); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, uerr
		}
	}
	if err != nil {
		log.Error(err, "reconcile failed")
	}
	return res, err
}

func (r *ClaudeEnvironmentReconciler) reconcile(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) (ctrl.Result, error) {
	secret, ok, err := r.resolveSecret(ctx, env, pass)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, err
	}
	configMaps, ok, err := r.resolveConfigMaps(ctx, env, pass)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, err
	}

	env.Status.ComputedDrainBudgetSeconds = builders.DrainBudgetSeconds(env.Spec.Runner.Settings)
	if _, tooShort := builders.EffectiveGracePeriod(env); tooShort {
		msg := fmt.Sprintf("terminationGracePeriodSeconds %d is below the computed drain budget of %d seconds",
			*env.Spec.Runner.TerminationGracePeriodSeconds, env.Status.ComputedDrainBudgetSeconds)
		pass.degrade(selfhostedv1alpha1.ReasonGracePeriodTooShort, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonGracePeriodTooShort, msg)
	}

	if env.Spec.OnDemand != nil {
		env.Status.Mode = "onDemand"
		env.Status.Fixed = nil
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonUnsupportedMode, "")
		pass.degrade(selfhostedv1alpha1.ReasonUnsupportedMode, "onDemand mode is not implemented in this operator version")
		return ctrl.Result{}, nil
	}
	env.Status.Mode = "fixed"
	env.Status.OnDemand = nil
	return r.reconcileFixed(ctx, env, pass, secret, configMaps)
}

func (r *ClaudeEnvironmentReconciler) resolveSecret(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) (*corev1.Secret, bool, error) {
	ref := env.Spec.EnvironmentSecretRef
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: env.Namespace}, secret)
	switch {
	case apierrors.IsNotFound(err):
		msg := fmt.Sprintf("Secret %q not found", ref.Name)
		pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretMissing, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonSecretMissing, msg)
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	if len(secret.Data[ref.Key]) == 0 {
		msg := fmt.Sprintf("Secret %q has no key %q", ref.Name, ref.Key)
		pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionFalse, selfhostedv1alpha1.ReasonSecretKeyMissing, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonSecretKeyMissing, msg)
		return nil, false, nil
	}
	pass.set(selfhostedv1alpha1.ConditionSecretFound, metav1.ConditionTrue, selfhostedv1alpha1.ReasonSecretFound, "")
	return secret, true, nil
}

// configMapRefs lists the ConfigMaps a runner spec references, with the key a
// wrapper script must contain ("" when any content is fine).
func configMapRefs(r selfhostedv1alpha1.RunnerSpec) map[string]string {
	refs := map[string]string{}
	if r.LifecycleHooks != nil {
		refs[r.LifecycleHooks.Name] = ""
	}
	if r.WrapperScript != nil {
		refs[r.WrapperScript.Name] = r.WrapperScript.Key
	}
	if r.HostConfig != nil {
		refs[r.HostConfig.Name] = ""
	}
	return refs
}

func (r *ClaudeEnvironmentReconciler) resolveConfigMaps(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) ([]*corev1.ConfigMap, bool, error) {
	var out []*corev1.ConfigMap
	ok := true
	for name, requiredKey := range configMapRefs(env.Spec.Runner) {
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: env.Namespace}, cm)
		switch {
		case apierrors.IsNotFound(err):
			msg := fmt.Sprintf("ConfigMap %q not found", name)
			pass.degrade(selfhostedv1alpha1.ReasonConfigMapMissing, msg)
			r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonConfigMapMissing, msg)
			ok = false
			continue
		case err != nil:
			return nil, false, err
		}
		if requiredKey != "" {
			if _, has := cm.Data[requiredKey]; !has {
				if _, hasBin := cm.BinaryData[requiredKey]; !hasBin {
					pass.degrade(selfhostedv1alpha1.ReasonConfigMapMissing, fmt.Sprintf("ConfigMap %q has no key %q", name, requiredKey))
					ok = false
					continue
				}
			}
		}
		out = append(out, cm)
	}
	return out, ok, nil
}

func (r *ClaudeEnvironmentReconciler) reconcileFixed(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, secret *corev1.Secret, configMaps []*corev1.ConfigMap) (ctrl.Result, error) {
	hash := builders.ConfigHash(env, secret, configMaps)
	pass.set(selfhostedv1alpha1.ConditionSecretOnRunners, metav1.ConditionTrue, selfhostedv1alpha1.ReasonFixedModeSecretOnPods,
		"fixed mode mounts the environment secret on every runner pod; prefer onDemand for production")

	desired := int32(1)
	if env.Spec.Fixed.Replicas != nil {
		desired = *env.Spec.Fixed.Replicas
	}

	var available bool
	if env.Spec.Fixed.PersistentWorkspace != nil {
		sts := builders.FixedStatefulSet(env, hash)
		if err := r.apply(ctx, env, sts); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteIfOwned(ctx, env, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: sts.Name, Namespace: sts.Namespace}}); err != nil {
			return ctrl.Result{}, err
		}
		available = sts.Status.ObservedGeneration == sts.Generation && sts.Status.ReadyReplicas >= desired
		env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: sts.Status.Replicas, ReadyReplicas: sts.Status.ReadyReplicas, UpdatedReplicas: sts.Status.UpdatedReplicas}
	} else {
		dep := builders.FixedDeployment(env, hash)
		if err := r.apply(ctx, env, dep); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteIfOwned(ctx, env, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: dep.Name, Namespace: dep.Namespace}}); err != nil {
			return ctrl.Result{}, err
		}
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				available = dep.Status.ObservedGeneration == dep.Generation
			}
		}
		env.Status.Fixed = &selfhostedv1alpha1.FixedFleetStatus{Replicas: dep.Status.Replicas, ReadyReplicas: dep.Status.ReadyReplicas, UpdatedReplicas: dep.Status.UpdatedReplicas}
	}

	if available {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionTrue, selfhostedv1alpha1.ReasonWorkloadAvailable, "")
	} else {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonWorkloadUnavailable, "runner workload is not yet available")
	}

	if err := r.checkRunnerPods(ctx, env, pass); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}

// checkRunnerPods is the hook point Task 8 fills in with failed-start detection.
func (r *ClaudeEnvironmentReconciler) checkRunnerPods(context.Context, *selfhostedv1alpha1.ClaudeEnvironment, *statusPass) error {
	return nil
}

// apply server-side-applies obj with the environment as controller owner. The
// object is updated in place with the server's response, including status.
func (r *ClaudeEnvironmentReconciler) apply(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, obj client.Object) error {
	if err := controllerutil.SetControllerReference(env, obj, r.Scheme); err != nil {
		return err
	}
	return r.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner(selfhostedv1alpha1.FieldOwner))
}

// deleteIfOwned removes a stale workload left behind by a mode switch.
func (r *ClaudeEnvironmentReconciler) deleteIfOwned(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, obj client.Object) error {
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(obj, env) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClaudeEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &selfhostedv1alpha1.ClaudeEnvironment{}, indexSecretName, func(o client.Object) []string {
		return []string{o.(*selfhostedv1alpha1.ClaudeEnvironment).Spec.EnvironmentSecretRef.Name}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &selfhostedv1alpha1.ClaudeEnvironment{}, indexConfigMapNames, func(o client.Object) []string {
		refs := configMapRefs(o.(*selfhostedv1alpha1.ClaudeEnvironment).Spec.Runner)
		names := make([]string, 0, len(refs))
		for n := range refs {
			names = append(names, n)
		}
		return names
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfhostedv1alpha1.ClaudeEnvironment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.environmentsReferencing(indexSecretName))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.environmentsReferencing(indexConfigMapNames))).
		Named("claudeenvironment").
		Complete(r)
}

func (r *ClaudeEnvironmentReconciler) environmentsReferencing(index string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := &selfhostedv1alpha1.ClaudeEnvironmentList{}
		if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace()), client.MatchingFields{index: obj.GetName()}); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	}
}
```

- [ ] **Step 6: Regenerate RBAC and run the tests**

```bash
make manifests
make test
```

Expected: PASS. `config/rbac/role.yaml` now lists deployments, statefulsets, secrets, configmaps, pods, events.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(controller): reconcile fixed-fleet ClaudeEnvironments with status conditions

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: Failed-start detection for fixed fleets

**Files:**
- Create: `internal/controller/failedstart.go`
- Modify: `internal/controller/claudeenvironment_controller.go` (replace `checkRunnerPods` body; add Pod watch)
- Test: `internal/controller/failedstart_test.go`, append a case to `internal/controller/claudeenvironment_fixed_test.go`

**Interfaces:**
- Produces: `func detectFailedStart(pods []corev1.Pod) (failed bool, message string)`.

- [ ] **Step 1: Write the failing unit test**

`internal/controller/failedstart_test.go`:

```go
package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func podWithRestart(name string, restarts int32, runFor time.Duration, msg string) corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-runFor))
	end := metav1.NewTime(time.Now())
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "runner", RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			StartedAt: start, FinishedAt: end, ExitCode: 1, Message: msg}},
	}}}}
}

func TestDetectFailedStart(t *testing.T) {
	fatal := "2026-10-02T10:00:00Z [self-hosted-runner] starting\n[runner:fatal] --use-anthropic-git-proxy requires --capacity 1"
	cases := []struct {
		name    string
		pods    []corev1.Pod
		want    bool
		contain string
	}{
		{"healthy", []corev1.Pod{podWithRestart("a", 0, 0, "")}, false, ""},
		{"normal drains after long runs", []corev1.Pod{podWithRestart("a", 10, 5*time.Minute, "")}, false, ""},
		{"two short restarts is not enough", []corev1.Pod{podWithRestart("a", 2, 5*time.Second, fatal)}, false, ""},
		{"three short restarts with fatal line", []corev1.Pod{podWithRestart("a", 3, 5*time.Second, fatal)}, true, "[runner:fatal] --use-anthropic-git-proxy"},
		{"error prefix", []corev1.Pod{podWithRestart("a", 4, 2*time.Second, "error: cannot create or write to base directory /workspace\nSee --help")}, true, "error: cannot create"},
		{"no message", []corev1.Pod{podWithRestart("a", 3, 1*time.Second, "")}, true, "kubectl logs --previous"},
		{"other container ignored", func() []corev1.Pod {
			p := podWithRestart("a", 5, time.Second, fatal)
			p.Status.ContainerStatuses[0].Name = "sidecar"
			return []corev1.Pod{p}
		}(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := detectFailedStart(tc.pods)
			if got != tc.want {
				t.Fatalf("got %v want %v (%s)", got, tc.want, msg)
			}
			if tc.contain != "" && !strings.Contains(msg, tc.contain) {
				t.Fatalf("message %q lacks %q", msg, tc.contain)
			}
			if strings.Contains(msg, "ccenvkey_") {
				t.Fatal("message leaked a secret-looking token")
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/controller/... -run TestDetectFailedStart
```

Expected: FAIL, `undefined: detectFailedStart`.

- [ ] **Step 3: Implement detection and wire it in**

`internal/controller/failedstart.go`:

```go
package controller

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	failedStartMinRestarts    = 3
	failedStartMaxRunDuration = 60 * time.Second
	failedStartMessageLimit   = 200
)

// detectFailedStart implements the product's definition of a failed start: the
// runner keeps exiting within a minute of starting. Restarts after long runs
// are normal drains and are ignored.
func detectFailedStart(pods []corev1.Pod) (bool, string) {
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != builders.RunnerContainerName || cs.RestartCount < failedStartMinRestarts {
				continue
			}
			t := cs.LastTerminationState.Terminated
			if t == nil || t.FinishedAt.Sub(t.StartedAt.Time) >= failedStartMaxRunDuration {
				continue
			}
			return true, fmt.Sprintf("pod %s restarted %d times with runs under %s; last run: %s",
				p.Name, cs.RestartCount, failedStartMaxRunDuration, fatalLine(t.Message))
		}
	}
	return false, ""
}

func fatalLine(msg string) string {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.Contains(l, "[runner:fatal]") || strings.HasPrefix(l, "error:") {
			return truncate(l, failedStartMessageLimit)
		}
	}
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return truncate(last, failedStartMessageLimit)
	}
	return "no termination message; run `kubectl logs --previous` on the pod"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

Replace `checkRunnerPods` in `claudeenvironment_controller.go`:

```go
// checkRunnerPods flags a fleet whose runners exit right after starting.
func (r *ClaudeEnvironmentReconciler) checkRunnerPods(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass) error {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(env.Namespace), client.MatchingLabels(builders.RunnerSelectorLabels(env))); err != nil {
		return err
	}
	if failed, msg := detectFailedStart(pods.Items); failed {
		pass.degrade(selfhostedv1alpha1.ReasonRunnerFailedStart, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonRunnerFailedStart, msg)
	}
	return nil
}
```

Add a Pod watch in `SetupWithManager`, before `.Named(...)`:

```go
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			name, ok := o.GetLabels()[selfhostedv1alpha1.LabelEnvironment]
			if !ok || o.GetLabels()[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: o.GetNamespace()}}}
		}), builder.WithPredicates(podStatusChanged())).
```

Add the predicate to `failedstart.go`; create and delete events pass through, updates only when container statuses changed:

```go
// podStatusChanged lets pod update events through only when container statuses changed.
func podStatusChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if !ok1 || !ok2 {
				return false
			}
			return !equality.Semantic.DeepEqual(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses)
		},
	}
}
```

Imports needed in the controller file: `sigs.k8s.io/controller-runtime/pkg/builder`, `sigs.k8s.io/controller-runtime/pkg/predicate`; in `failedstart.go`: `k8s.io/apimachinery/pkg/api/equality`, `sigs.k8s.io/controller-runtime/pkg/event`, `sigs.k8s.io/controller-runtime/pkg/predicate`.

- [ ] **Step 4: Add the envtest case**

Append to the `Describe` in `claudeenvironment_fixed_test.go`:

```go
	It("marks the environment Degraded when runner pods keep failing at start", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())

		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "platform-runner-abc", Namespace: ns,
			Labels: builders.RunnerSelectorLabels(env)},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "runner", Image: "registry.local/runner:2.1.280"}}}}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		now := metav1.Now()
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runner", RestartCount: 3,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1, StartedAt: metav1.NewTime(now.Add(-5 * time.Second)), FinishedAt: now,
				Message: "[runner:fatal] --use-anthropic-git-proxy requires --capacity 1"}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(
			And(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonRunnerFailedStart),
				HaveField("Message", ContainSubstring("[runner:fatal]"))))
	})
```

- [ ] **Step 5: Run all tests**

```bash
make test
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(controller): detect runner failed starts and surface them as Degraded

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 9: Operator metrics

**Files:**
- Create: `internal/metrics/metrics.go`
- Modify: `internal/controller/claudeenvironment_controller.go` (record and forget)
- Test: `internal/metrics/metrics_test.go`

**Interfaces:**
- Produces: `metrics.RecordEnvironment(env *v1alpha1.ClaudeEnvironment)`, `metrics.ForgetEnvironment(namespace, name string)`. Series: `claude_operator_environment_ready{namespace,environment}`, `claude_operator_drain_budget_seconds{namespace,environment}`, `claude_operator_fixed_fleet_replicas{namespace,environment,state}` with state `desired|ready|updated`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/metrics/...
```

Expected: FAIL, package does not exist.

- [ ] **Step 3: Implement**

```go
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

var (
	environmentReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_environment_ready",
		Help: "1 when the ClaudeEnvironment's Ready condition is True, else 0.",
	}, []string{"namespace", "environment"})
	drainBudget = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_drain_budget_seconds",
		Help: "Computed terminationGracePeriodSeconds the runner drain path needs.",
	}, []string{"namespace", "environment"})
	fixedReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_fixed_fleet_replicas",
		Help: "Fixed-fleet runner replicas by state (desired, ready, updated).",
	}, []string{"namespace", "environment", "state"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(environmentReady, drainBudget, fixedReplicas)
}

// RecordEnvironment exports the environment's current status.
func RecordEnvironment(env *selfhostedv1alpha1.ClaudeEnvironment) {
	ns, name := env.Namespace, env.Name
	ready := 0.0
	if meta.IsStatusConditionTrue(env.Status.Conditions, selfhostedv1alpha1.ConditionReady) {
		ready = 1
	}
	environmentReady.WithLabelValues(ns, name).Set(ready)
	drainBudget.WithLabelValues(ns, name).Set(float64(env.Status.ComputedDrainBudgetSeconds))
	if env.Spec.Fixed != nil && env.Status.Fixed != nil {
		desired := int32(1)
		if env.Spec.Fixed.Replicas != nil {
			desired = *env.Spec.Fixed.Replicas
		}
		fixedReplicas.WithLabelValues(ns, name, "desired").Set(float64(desired))
		fixedReplicas.WithLabelValues(ns, name, "ready").Set(float64(env.Status.Fixed.ReadyReplicas))
		fixedReplicas.WithLabelValues(ns, name, "updated").Set(float64(env.Status.Fixed.UpdatedReplicas))
	} else {
		fixedReplicas.DeletePartialMatch(prometheus.Labels{"namespace": ns, "environment": name})
	}
}

// ForgetEnvironment drops every series for a deleted environment.
func ForgetEnvironment(namespace, name string) {
	labels := prometheus.Labels{"namespace": namespace, "environment": name}
	environmentReady.DeletePartialMatch(labels)
	drainBudget.DeletePartialMatch(labels)
	fixedReplicas.DeletePartialMatch(labels)
}
```

In `Reconcile`, change the not-found branch and the success tail:

```go
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			metrics.ForgetEnvironment(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
```

and after the status update block, before `return res, err`:

```go
	metrics.RecordEnvironment(env)
```

with import `"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/metrics"`.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/metrics/... -v && make test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(metrics): environment readiness, drain budget and fleet replica gauges

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 10: Manager flags: log format, log level, watch namespaces, version

**Files:**
- Create: `cmd/options.go`
- Modify: `cmd/main.go`
- Modify: `Makefile` (ldflags version)
- Test: `cmd/options_test.go`

**Interfaces:**
- Produces: `parseWatchNamespaces(s string) map[string]cache.Config`, `parseLogLevel(s string) (zapcore.Level, error)`; flags `--log-format json|text` (default json), `--log-level debug|info|error` (default info), `--watch-namespaces ns1,ns2` (default all); package var `version` set by `-ldflags "-X main.version=..."`.

- [ ] **Step 1: Write the failing test**

`cmd/options_test.go`:

```go
package main

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestParseWatchNamespaces(t *testing.T) {
	if got := parseWatchNamespaces(""); got != nil {
		t.Fatalf("empty must mean all namespaces (nil), got %v", got)
	}
	got := parseWatchNamespaces(" a, b ,,c")
	if len(got) != 3 {
		t.Fatalf("want 3 namespaces, got %v", got)
	}
	for _, n := range []string{"a", "b", "c"} {
		if _, ok := got[n]; !ok {
			t.Fatalf("missing %s", n)
		}
	}
}

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]zapcore.Level{"debug": zapcore.DebugLevel, "info": zapcore.InfoLevel, "error": zapcore.ErrorLevel} {
		got, err := parseLogLevel(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %v %v", in, got, err)
		}
	}
	if _, err := parseLogLevel("loud"); err == nil {
		t.Fatal("invalid level must error")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./cmd/...
```

Expected: FAIL, undefined functions.

- [ ] **Step 3: Implement options and wire main**

`cmd/options.go`:

```go
package main

import (
	"fmt"
	"strings"

	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// version is set at build time with -ldflags "-X main.version=<semver>".
var version = "dev"

func parseWatchNamespaces(s string) map[string]cache.Config {
	var out map[string]cache.Config
	for _, part := range strings.Split(s, ",") {
		ns := strings.TrimSpace(part)
		if ns == "" {
			continue
		}
		if out == nil {
			out = map[string]cache.Config{}
		}
		out[ns] = cache.Config{}
	}
	return out
}

func parseLogLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	}
	return 0, fmt.Errorf("unknown log level %q (debug, info, error)", s)
}
```

In `cmd/main.go`, inside `main()` where the scaffold declares flags, add:

```go
	var logFormat, logLevel, watchNamespaces string
	flag.StringVar(&logFormat, "log-format", "json", "Log format: json or text.")
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug, info or error.")
	flag.StringVar(&watchNamespaces, "watch-namespaces", "", "Comma-separated namespaces to watch. Empty watches all namespaces.")
```

Replace the scaffold's logger setup (`opts := zap.Options{...}; opts.BindFlags(...); flag.Parse(); ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))`) with:

```go
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	lvl, err := parseLogLevel(logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts), zap.UseDevMode(logFormat == "text"), zap.Level(lvl)))
	setupLog.Info("starting claude-selfhosted-operator", "version", version, "logFormat", logFormat)
```

In the `ctrl.Options{...}` literal passed to `ctrl.NewManager`, add:

```go
		Cache: cache.Options{DefaultNamespaces: parseWatchNamespaces(watchNamespaces)},
```

and change the reconciler registration to pass the recorder:

```go
	if err := (&controller.ClaudeEnvironmentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("claude-selfhosted-operator"),
	}).SetupWithManager(mgr); err != nil {
```

Add imports `"fmt"`, `"sigs.k8s.io/controller-runtime/pkg/cache"` as needed. In the `Makefile`, add `VERSION ?= $(shell git describe --tags --always --dirty)` near the top and change the `build` target's `go build` to `go build -ldflags "-X main.version=$(VERSION)" -o bin/manager cmd/main.go`. In the `Dockerfile`, add `ARG VERSION=dev` and `-ldflags "-X main.version=${VERSION}"` to its `go build` line.

- [ ] **Step 4: Verify**

```bash
go test ./cmd/... -v && make build && ./bin/manager --help 2>&1 | grep -E 'log-format|log-level|watch-namespaces'
```

Expected: tests PASS, three flags listed.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(cmd): log format/level flags, namespace scoping and build version

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 11: Samples, runner image Dockerfile, README quickstart

**Files:**
- Modify: `config/samples/selfhosted_v1alpha1_claudeenvironment.yaml`
- Create: `examples/fixed-fleet.yaml`, `examples/hooks-configmap.yaml`, `examples/runner-image/Dockerfile`, `examples/runner-image/README.md`
- Modify: `README.md`
- Test: `api/v1alpha1/samples_test.go`

**Interfaces:**
- Produces: sample manifests that decode strictly into `ClaudeEnvironment`; the runner image recipe Plan 2 and Plan 3 reuse.

- [ ] **Step 1: Write the failing sample test**

`api/v1alpha1/samples_test.go`:

```go
package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSamplesDecodeStrictly(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob("../../config/samples/*claudeenvironment*.yaml")
	files = append(files, more...)
	if len(files) == 0 {
		t.Fatal("no sample files found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(data), "\n---") {
			if !strings.Contains(doc, "kind: ClaudeEnvironment") {
				continue
			}
			var env ClaudeEnvironment
			if err := yaml.UnmarshalStrict([]byte(doc), &env); err != nil {
				t.Errorf("%s: %v", f, err)
			}
			if env.Spec.Runner.Image == "" || strings.HasSuffix(env.Spec.Runner.Image, ":latest") {
				t.Errorf("%s: sample image must be pinned", f)
			}
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./api/... -run TestSamplesDecodeStrictly
```

Expected: FAIL, no sample files or the scaffolded sample lacks `runner.image`.

- [ ] **Step 3: Write the samples and Dockerfile**

`config/samples/selfhosted_v1alpha1_claudeenvironment.yaml`:

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: platform
spec:
  environmentSecretRef:
    name: claude-env-secret
  runner:
    image: registry.example.com/claude-runner:2.1.280
    capacity: 1
    settings:
      configureGit: true
      confineRepoSettings: enforce
      drainWaitSeconds: 300
      removeSessionState: true
    podTemplate:
      resources:
        requests: {cpu: "1", memory: 4Gi}
        limits: {memory: 8Gi}
  fixed:
    replicas: 3
```

`examples/fixed-fleet.yaml` (namespace plus Secret placeholder plus the same environment):

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: claude-runners
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
---
# Create the Secret from the key copied in claude.ai admin settings, never from a committed file:
#   (umask 077 && cat > ./environment-secret)   # paste, Enter, Ctrl-D
#   kubectl -n claude-runners create secret generic claude-env-secret --from-file=environment-secret=./environment-secret
#   rm ./environment-secret
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: platform
  namespace: claude-runners
spec:
  environmentSecretRef:
    name: claude-env-secret
  runner:
    image: registry.example.com/claude-runner:2.1.280
    capacity: 1
    settings:
      configureGit: true
      confineRepoSettings: enforce
      drainWaitSeconds: 300
      killSessionAfterMinutes: 240
      releaseIdleSessionMinutes: 30
      removeSessionState: true
    lifecycleHooks:
      name: runner-hooks
    env:
      - name: CLAUDE_CODE_DISABLE_ARTIFACT
        value: "1"
    podTemplate:
      resources:
        requests: {cpu: "1", memory: 4Gi}
        limits: {memory: 8Gi}
  fixed:
    replicas: 3
```

`examples/hooks-configmap.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: runner-hooks
  namespace: claude-runners
data:
  post-session: |
    #!/bin/sh
    # Runs after every session. Push a snapshot branch so uncommitted work survives.
    set -eu
    cd "${CLAUDE_RUNNER_CHECKOUT_PATH:-.}" 2>/dev/null || exit 0
    git -c commit.gpgsign=false add -A >/dev/null 2>&1 || exit 0
    git -c commit.gpgsign=false commit -qm "wip: snapshot from session ${CLAUDE_RUNNER_SESSION_ID:-unknown}" >/dev/null 2>&1 || exit 0
    git push -q origin "HEAD:refs/heads/claude/snapshot-${CLAUDE_RUNNER_SESSION_UUID:-unknown}" >/dev/null 2>&1 || true
```

`examples/runner-image/Dockerfile`:

```dockerfile
# Runner and orchestrator image. One image serves both roles: the operator
# runs `claude self-hosted-runner` or `claude self-hosted-runner orchestrator`.
FROM debian:bookworm-slim
ARG CLAUDE_CODE_VERSION
ARG CLAUDE_ARCH=linux-x64
ARG RUNNER_UID=10001
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION:?set with --build-arg CLAUDE_CODE_VERSION}/${CLAUDE_ARCH}/claude" \
      -o /usr/local/bin/claude \
 && chmod 0755 /usr/local/bin/claude
# Verify the binary against the release's signed manifest before promoting the
# image: https://code.claude.com/docs/en/setup#binary-integrity-and-code-signing
RUN groupadd -g "${RUNNER_UID}" runner \
 && useradd -m -u "${RUNNER_UID}" -g "${RUNNER_UID}" -s /usr/sbin/nologin runner \
 && mkdir -p /workspace /etc/claude/hooks /home/runner/.claude \
 && chown -R runner:runner /workspace /home/runner
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'
USER ${RUNNER_UID}:${RUNNER_UID}
ENV HOME=/home/runner
ENTRYPOINT ["claude"]
```

`examples/runner-image/README.md`:

```markdown
# Runner image

Anthropic publishes no runner image; build this one and push it to your registry.

    docker build \
      --build-arg CLAUDE_CODE_VERSION="$(curl -fsSL https://downloads.claude.ai/claude-code-releases/stable)" \
      -t registry.example.com/claude-runner:2.1.280 examples/runner-image

Pin `CLAUDE_CODE_VERSION` to a specific release for reproducible builds. Use
`--build-arg CLAUDE_ARCH=linux-arm64` on ARM nodes. Layer your toolchains on
top; keep the non-root user and the pre-created `/workspace`, `/home/runner`
and `/etc/claude/hooks` directories, because the operator runs the container
with a read-only root filesystem and mounts emptyDirs over `/workspace`,
`/home/runner` and `/tmp`.
```

Replace `README.md` with a quickstart: what the operator does, the five steps (enable self-hosted environments and create one in claude.ai, create the Secret as in `examples/fixed-fleet.yaml`, build the image, `make install && make deploy IMG=...` or the Helm chart in a later release, `kubectl apply -f examples/hooks-configmap.yaml -f examples/fixed-fleet.yaml`), how to read status (`kubectl get cenv -A`, conditions and reasons), and links to the spec and the Claude docs. Keep it under 120 lines.

- [ ] **Step 4: Verify**

```bash
go test ./api/... -run TestSamplesDecodeStrictly -v
docker build --build-arg CLAUDE_CODE_VERSION="$(curl -fsSL https://downloads.claude.ai/claude-code-releases/stable)" -t claude-runner:local examples/runner-image
docker run --rm claude-runner:local self-hosted-runner --help | head -3
```

Expected: test PASS; image builds; the help text mentions `--environment-secret-file`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: samples, runner image Dockerfile and README quickstart

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 12: Stub runner image and kind e2e for fixed mode

**Files:**
- Create: `test/stubrunner/main.go`, `test/stubrunner/Dockerfile`
- Create: `test/e2e/testdata/fixed-fleet-stub.yaml`
- Create: `test/e2e/fixed_fleet_test.go`
- Modify: `Makefile` (`stub-image` target), `test/e2e/e2e_suite_test.go` (build and load the stub), `config/manager/manager.yaml` (PSS labels on the operator namespace if the scaffold lacks them)

**Interfaces:**
- Consumes: scaffolded `test/utils` helpers (`utils.Run`, `utils.LoadImageToKindClusterWithName`, `utils.GetProjectDir`).
- Produces: `make stub-image STUB_IMG=...`; e2e proving a fixed fleet becomes Ready under an enforced Restricted namespace.

- [ ] **Step 1: Write the stub runner**

`test/stubrunner/main.go`:

```go
// Command stubrunner stands in for the real runner in e2e tests. It accepts the
// real runner's args, serves /healthz and /metrics on --health-port, and exits
// 0 on SIGTERM. It never contacts Anthropic.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func healthPort(args []string) string {
	for i, a := range args {
		if a == "--health-port" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "8080"
}

func main() {
	port := healthPort(os.Args[1:])
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","runner_id":"ccrunner_stub","active_sessions":0,"last_poll_at":null,"last_poll_age_ms":null}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "claude_code_self_hosted_runner_capacity 1")
		fmt.Fprintln(w, "claude_code_self_hosted_runner_active_sessions 0")
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}()
	fmt.Println("[self-hosted-runner] stub listening on", port, "args:", os.Args[1:])
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
```

`test/stubrunner/Dockerfile`:

```dockerfile
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/stubrunner ./test/stubrunner
RUN CGO_ENABLED=0 go build -trimpath -o /out/stubrunner ./test/stubrunner

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/stubrunner /stubrunner
USER 65532:65532
ENTRYPOINT ["/stubrunner"]
```

Makefile addition:

```make
STUB_IMG ?= example.com/claude-stub-runner:e2e

.PHONY: stub-image
stub-image: ## Build the e2e stub runner image.
	$(CONTAINER_TOOL) build -t $(STUB_IMG) -f test/stubrunner/Dockerfile .
```

- [ ] **Step 2: Write the e2e test data and test**

`test/e2e/testdata/fixed-fleet-stub.yaml`:

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: e2e
  namespace: claude-e2e
spec:
  environmentSecretRef:
    name: claude-env-secret
  runner:
    image: example.com/claude-stub-runner:e2e
    capacity: 1
    podTemplate:
      resources:
        requests: {cpu: 10m, memory: 32Mi}
  fixed:
    replicas: 2
```

In `test/e2e/e2e_suite_test.go`, inside `BeforeSuite` after the scaffold builds and loads the manager image, add:

```go
	By("building the stub runner image")
	cmd = exec.Command("make", "stub-image", fmt.Sprintf("STUB_IMG=%s", stubImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the stub runner image")

	By("loading the stub runner image on Kind")
	err = utils.LoadImageToKindClusterWithName(stubImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the stub runner image into Kind")
```

with a package-level `const stubImage = "example.com/claude-stub-runner:e2e"`.

`test/e2e/fixed_fleet_test.go`:

```go
package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

const e2eNamespace = "claude-e2e"

var _ = Describe("Fixed fleet", Ordered, func() {
	BeforeAll(func() {
		By("creating a namespace that enforces the Restricted Pod Security Standard")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", e2eNamespace))
		_, err := utils.Run(exec.Command("kubectl", "label", "ns", e2eNamespace,
			"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/enforce-version=latest", "--overwrite"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "create", "secret", "generic", "claude-env-secret",
			"--from-literal=environment-secret=ccenvkey_e2e"))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", e2eNamespace, "--wait=false"))
	})

	It("becomes Ready with Restricted-compliant runner pods", func() {
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/testdata/fixed-fleet-stub.yaml"))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "claudeenvironment", "e2e",
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("True"))
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		out, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", "e2e-runner",
			"-o", "jsonpath={.spec.template.spec.terminationGracePeriodSeconds}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("80"))

		out, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "pods",
			"-l", "selfhosted.claudecode.dev/environment=e2e", "-o", "jsonpath={.items[*].status.phase}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Fields(out)).To(Equal([]string{"Running", "Running"}))

		By("checking no pod was rejected by the Pod Security admission controller")
		out, _ = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "events",
			"--field-selector", "reason=FailedCreate", "-o", "name"))
		Expect(strings.TrimSpace(out)).To(BeEmpty(), fmt.Sprintf("FailedCreate events: %s", out))
	})

	It("rolls the fleet when the Secret is rotated", func() {
		before, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", "e2e-runner",
			"-o", `jsonpath={.spec.template.metadata.annotations.selfhosted\.claudecode\.dev/config-hash}`))
		Expect(err).NotTo(HaveOccurred())
		patch := `{"stringData":{"environment-secret":"ccenvkey_rotated"}}`
		_, err = utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "patch", "secret", "claude-env-secret", "-p", patch))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			after, err := utils.Run(exec.Command("kubectl", "-n", e2eNamespace, "get", "deploy", "e2e-runner",
				"-o", `jsonpath={.spec.template.metadata.annotations.selfhosted\.claudecode\.dev/config-hash}`))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(after).NotTo(Equal(before))
		}, time.Minute, 2*time.Second).Should(Succeed())
	})
})
```

If `config/manager/manager.yaml`'s Namespace lacks `pod-security.kubernetes.io/enforce: restricted`, add it under `metadata.labels` so the operator's own namespace is enforced too.

- [ ] **Step 3: Run the e2e suite locally**

```bash
kind create cluster --name kind || true
make test-e2e
```

Expected: the scaffolded controller checks and the two fixed-fleet specs pass. If the stub pods never reach Running, inspect `kubectl -n claude-e2e describe rs` for Pod Security violations and fix the builder, not the namespace label.

- [ ] **Step 4: Confirm CI wiring**

Open `.github/workflows/test-e2e.yml` (scaffolded). It must run `make test-e2e` on a kind cluster; no change is needed beyond confirming `docker` is available for `make stub-image`. Run `make lint` and fix anything it reports.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test(e2e): fixed fleet on kind with a stub runner under Restricted PSS

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

## Follow-ups deferred to later plans

- Plan 2: `ClaudeRunner` API and controller, `cmd/spawn-runner`, orchestrator Deployment with injected hook and per-environment RBAC, orchestrator exec readiness probe on `connected`, OTel tracing with `traceparent` propagation, `claude_operator_environment_runners` and spawn metrics, and lifting the `UnsupportedMode` degradation.
- Plan 3: NetworkPolicy builder behind `spec.runner.networkPolicy`, Helm chart via `helm/v2-alpha` with the chart contract test, secure metrics ServiceMonitor and cert-manager toggles, PodMonitor for runner pods, nightly real-environment e2e and upgrade test, signed multi-arch release, docs set (hardening, upgrade, metrics, troubleshooting).
- Known cost: watching all Secrets and ConfigMaps cluster-wide loads the manager cache in large clusters. A label selector on the cache for those types is a Plan 3 candidate.
