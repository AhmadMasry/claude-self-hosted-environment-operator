# On-Demand Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement on-demand mode end to end: the `ClaudeRunner` CRD, the `spawn-runner` hook binary, the orchestrator Deployment with its injected hook and least-privilege RBAC, the `ClaudeRunner` controller that runs one hardened pod per session, metrics, optional OpenTelemetry tracing, samples, and a kind e2e with a stub orchestrator.

**Architecture:** The orchestrator (the user's runner image running `claude self-hosted-runner orchestrator`) calls a static Go hook that the operator injects via an init container. The hook does one idempotent create of a work-order Secret and a `ClaudeRunner`; the `ClaudeRunner` controller turns that into a `restartPolicy: Never` pod built from the existing Restricted pod template, tracks its phase, enforces the spawn timeout, and garbage-collects after a TTL. The `ClaudeEnvironment` controller's on-demand branch owns the orchestrator Deployment, ServiceAccount, Role and RoleBinding and reports runner counts.

**Tech Stack:** Go 1.26 module, kubebuilder v4, controller-runtime v0.25, envtest, Ginkgo/Gomega, controller-runtime fake client with interceptors, OpenTelemetry Go SDK with OTLP gRPC exporter, kind.

**Spec:** `docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md` (sections 5.2, 6.1 on-demand bullets, 6.2, 6.3, 7, 8.2, 8.4). Plan 1's follow-ups assigned to this plan: `docs/superpowers/plans/2026-10-02-foundation-followups.md`.

## Global Constraints

- Module `github.com/AhmadMasry/claude-self-hosted-environment-operator`; API group `selfhosted.claudecode.dev`, version `v1alpha1`. New Go files carry the Apache header from `internal/builders/drain.go`.
- Every pod the operator creates (runner, orchestrator, and the hook init container) meets the Restricted Pod Security Standard: `runAsNonRoot: true`, `seccompProfile.type: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `readOnlyRootFilesystem: true`, no host namespaces or hostPorts, volumes only from the Restricted allowlist.
- Spawned runners run `--capacity 1`, `restartPolicy: Never`, are never restarted, and mount only the single-use work-order JWT, never the environment secret. The environment secret is mounted only on the orchestrator pod.
- The spawn-runner hook contract: idempotent on `CLAUDE_RUNNER_ORDER_ID`; exit 0 = submitted (including redelivery), exit 1 = retryable, exit 2 = non-retryable; the whole run finishes well inside the orchestrator's `--hook-timeout` (default 60 s), so the hook's context deadline is 45 s; stderr carries only the actionable reason; stdout carries one JSON line; the JWT and any email are never printed.
- Orchestrator flags rendered: `self-hosted-runner orchestrator --environment-secret-file /etc/claude/environment-secret --hooks-dir /etc/claude/hooks --health-port <n> --expected-spawn-seconds <n> --hook-timeout <n> --hook-concurrency <n> [--min-idle <n>] [--log-level <l>]`.
- Orchestrator `/healthz` always returns 200; readiness must be judged on the `"connected":true` field in the body.
- Work-order Secret is named `<orderID>-work-order` with key `jwt`; it is mounted on the runner pod at `/etc/claude/environment-secret` through the same volume the fixed mode uses, so `RunnerArgs` and the nested hooks mount stay identical.
- Names: ServiceAccount, Role, RoleBinding and Deployment for the orchestrator are all `<env>-orchestrator`; the runner Pod is named after the `ClaudeRunner`, which is named after the order ID.
- No email, JWT, or environment secret value in any log line, event, condition message, or metric label. `accountID` is stored on the `ClaudeRunner`; `CLAUDE_RUNNER_ACCOUNT_EMAIL` is never stored.
- One spec mutation per reconcile (adding a finalizer returns and requeues); status written once at the end of a pass.
- The manager learns its own image from `--hook-image`, defaulting to the `OPERATOR_IMAGE` environment variable that the kustomize `replacements` transformer fills from the manager container's image.
- Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

## Review Focus

1. A redelivered spawn request (same `CLAUDE_RUNNER_ORDER_ID`, possibly a new JWT) must create nothing new and exit 0. Tests in Task 4 (fake client) and Task 12 (e2e, the stub orchestrator calls the hook twice).
2. A `ClaudeRunner` whose work-order Secret is missing (the hook died between its two creates) must go `Failed/WorkOrderMissing` with no pod and no panic, and stay reconcilable. Test in Task 7.
3. Deleting a `ClaudeRunner` while its pod is Running must delete the pod first and only then let the object disappear, so no runner outlives its record. Test in Task 7.
4. Switching an environment from fixed to onDemand must delete the fixed Deployment or StatefulSet and set `SecretOnRunners=False`; switching back must delete the orchestrator objects. Test in Task 8.
5. onDemand with no hook image configured must set `Degraded/HookImageUnset` and apply nothing. Test in Task 8.

---

### Task 1: ClaudeRunner API and shared constants

**Files:**
- Create: `api/v1alpha1/clauderunner_types.go` (via `kubebuilder create api`, then replaced)
- Modify: `api/v1alpha1/conditions.go`
- Modify: `cmd/main.go` (scaffold registers the new controller; keep it, Task 9 completes it)
- Test: `internal/controller/clauderunner_validation_test.go`

**Interfaces:**
- Produces: types `ClaudeRunner`, `ClaudeRunnerList`, `ClaudeRunnerSpec`, `ClaudeRunnerStatus`, `LocalObjectRef{Name}`, `RunnerPhase` with constants `RunnerPending`, `RunnerRunning`, `RunnerSucceeded`, `RunnerFailed`; constants in `conditions.go` listed in Step 2; the scaffolded `ClaudeRunnerReconciler` in `internal/controller/clauderunner_controller.go` (replaced in Task 7).

- [ ] **Step 1: Scaffold the API**

```bash
kubebuilder create api --group selfhosted --version v1alpha1 --kind ClaudeRunner --resource --controller
git rm -q internal/controller/clauderunner_controller_test.go   # scaffolded placeholder; real tests come in Task 7
```

- [ ] **Step 2: Add constants**

Append to the `const` block in `api/v1alpha1/conditions.go`:

```go
	ConditionRunnerReady = "Ready"

	ReasonPodPending                   = "PodPending"
	ReasonPodRunning                   = "PodRunning"
	ReasonPodSucceeded                 = "PodSucceeded"
	ReasonPodFailed                    = "PodFailed"
	ReasonSpawnTimeout                 = "SpawnTimeout"
	ReasonWorkOrderMissing             = "WorkOrderMissing"
	ReasonEnvironmentMissing           = "EnvironmentMissing"
	ReasonOrchestratorUnavailable      = "OrchestratorUnavailable"
	ReasonOnDemandSecretOnOrchestrator = "OnDemandSecretOnOrchestrator"
	ReasonHookImageUnset               = "HookImageUnset"
	ReasonFleetAvailable               = "FleetAvailable"

	LabelOrderID          = "selfhosted.claudecode.dev/order-id"
	LabelSessionID        = "selfhosted.claudecode.dev/session-id"
	AnnotationTraceparent = "selfhosted.claudecode.dev/traceparent"
	RunnerFinalizer       = "selfhosted.claudecode.dev/runner-pod"

	WorkOrderSecretKey    = "jwt"
	WorkOrderSecretSuffix = "-work-order"

	// Environment variables the operator sets on the orchestrator container for the hook.
	EnvHookEnvironment          = "CLAUDE_OPERATOR_ENVIRONMENT"
	EnvHookNamespace            = "CLAUDE_OPERATOR_NAMESPACE"
	EnvHookMaxConcurrentRunners = "CLAUDE_OPERATOR_MAX_CONCURRENT_RUNNERS"
```

- [ ] **Step 3: Write the types**

Replace everything below the license header in `api/v1alpha1/clauderunner_types.go`:

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	AccountID string `json:"accountID,omitempty"`
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
```

Keep the scaffold's registration at the bottom of the file in whatever form kubebuilder generated (the `init()` that registers both types with `SchemeBuilder`).

- [ ] **Step 4: Write the failing validation test**

`internal/controller/clauderunner_validation_test.go`:

```go
package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

var _ = Describe("ClaudeRunner validation", func() {
	ctx := context.Background()

	It("accepts a minimal runner, defaults nothing, and rejects spec changes", func() {
		ns := newNamespace(ctx)
		r := &selfhostedv1alpha1.ClaudeRunner{
			ObjectMeta: metav1.ObjectMeta{Name: "order-abc", Namespace: ns},
			Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
				EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: "platform"},
				OrderID:            "order-abc",
				WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "order-abc-work-order"},
			},
		}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		Expect(r.Status.Phase).To(BeEmpty())

		r.Spec.SessionID = "session_changed"
		err := k8sClient.Update(ctx, r)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec is immutable"))
	})

	It("rejects an empty orderID", func() {
		ns := newNamespace(ctx)
		r := &selfhostedv1alpha1.ClaudeRunner{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
				EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: "platform"},
				WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "x"},
			},
		}
		Expect(k8sClient.Create(ctx, r)).NotTo(Succeed())
	})
})
```

- [ ] **Step 5: Generate, run, verify**

```bash
make manifests generate
make test
make lint
```

Expected: the CRD `config/crd/bases/selfhosted.claudecode.dev_clauderunners.yaml` exists with the immutability rule; all specs pass. The scaffolded `ClaudeRunnerReconciler` with its empty `Reconcile` is registered in `cmd/main.go` by the scaffold and compiles; leave it until Task 7.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(api): ClaudeRunner types, phases and on-demand constants

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: On-demand runner pod builder

**Files:**
- Create: `internal/builders/runnerpod.go`
- Test: `internal/builders/runnerpod_test.go`

**Interfaces:**
- Consumes: `RunnerPodTemplate(RunnerPodInput)`, `RunnerSelectorLabels`, `SecretMountPath`, `SecretFileName`, `ConfigHash`.
- Produces: `func WorkOrderSecretName(orderID string) string`, `func WorkOrderSecretVolume(secretName string) corev1.VolumeSource`, `func OnDemandRunnerPod(env *v1alpha1.ClaudeEnvironment, runner *v1alpha1.ClaudeRunner, hash string) *corev1.Pod`.

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

func onDemandEnv() *selfhostedv1alpha1.ClaudeEnvironment {
	env := testEnv()
	env.Spec.Fixed = nil
	env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{Orchestrator: selfhostedv1alpha1.OrchestratorSpec{
		Replicas: ptr.To[int32](1), ExpectedSpawnSeconds: 120, HookTimeoutSeconds: 60, HookConcurrency: 4,
		HealthPort: ptr.To[int32](8080)}}
	return env
}

func testRunner() *selfhostedv1alpha1.ClaudeRunner {
	return &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: "order-1", Namespace: "claude", UID: "uid-1"},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
			EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: "platform"}, OrderID: "order-1",
			SessionID: "session_abc", WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "order-1-work-order"},
		},
	}
}

func TestWorkOrderSecretName(t *testing.T) {
	if got := WorkOrderSecretName("order-1"); got != "order-1-work-order" {
		t.Fatalf("got %q", got)
	}
}

func TestOnDemandRunnerPod(t *testing.T) {
	pod := OnDemandRunnerPod(onDemandEnv(), testRunner(), "h1")
	if pod.Name != "order-1" || pod.Namespace != "claude" {
		t.Fatalf("name/namespace %s/%s", pod.Namespace, pod.Name)
	}
	if pod.APIVersion != "v1" || pod.Kind != "Pod" {
		t.Fatal("TypeMeta must be set for server-side apply")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("on-demand runners must never restart")
	}
	if pod.Labels[selfhostedv1alpha1.LabelOrderID] != "order-1" || pod.Labels[selfhostedv1alpha1.LabelSessionID] != "session_abc" ||
		pod.Labels[selfhostedv1alpha1.LabelEnvironment] != "platform" || pod.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
		t.Fatalf("labels wrong: %v", pod.Labels)
	}
	if pod.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h1" {
		t.Fatal("config hash annotation missing")
	}
	var secret *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == "environment-secret" {
			secret = &pod.Spec.Volumes[i]
		}
	}
	if secret == nil || secret.Secret == nil || secret.Secret.SecretName != "order-1-work-order" ||
		secret.Secret.Items[0].Key != selfhostedv1alpha1.WorkOrderSecretKey || secret.Secret.Items[0].Path != SecretFileName {
		t.Fatalf("work-order volume wrong: %+v", secret)
	}
	args := pod.Spec.Containers[0].Args
	if args[2] != SecretMountPath+"/"+SecretFileName {
		t.Fatalf("secret file arg wrong: %v", args[:3])
	}
	if !containsSeq(args, []string{"--capacity", "1"}) {
		t.Fatalf("capacity must be 1: %v", args)
	}
	if pod.Spec.SecurityContext == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("restricted pod security context missing")
	}
}

func TestOnDemandRunnerPodForcesCapacityOne(t *testing.T) {
	env := onDemandEnv()
	env.Spec.Runner.Capacity = 4 // CEL forbids this, but the builder must still be safe
	pod := OnDemandRunnerPod(env, testRunner(), "h")
	if !containsSeq(pod.Spec.Containers[0].Args, []string{"--capacity", "1"}) {
		t.Fatalf("capacity not forced to 1: %v", pod.Spec.Containers[0].Args)
	}
}

func TestOnDemandRunnerPodPreWarmHasNoSessionLabel(t *testing.T) {
	r := testRunner()
	r.Spec.SessionID = ""
	pod := OnDemandRunnerPod(onDemandEnv(), r, "h")
	if _, ok := pod.Labels[selfhostedv1alpha1.LabelSessionID]; ok {
		t.Fatal("pre-warming runners must not carry a session label")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/builders/... -run 'TestWorkOrderSecretName|TestOnDemandRunnerPod'
```

Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

```go
package builders

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// WorkOrderSecretName is the Secret holding one order's single-use JWT.
func WorkOrderSecretName(orderID string) string {
	return orderID + selfhostedv1alpha1.WorkOrderSecretSuffix
}

// WorkOrderSecretVolume mounts the work-order JWT where the runner expects the
// environment secret file, so RunnerArgs and the nested hook mounts are the
// same as in fixed mode.
func WorkOrderSecretVolume(secretName string) corev1.VolumeSource {
	return corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: secretName,
		Items:      []corev1.KeyToPath{{Key: selfhostedv1alpha1.WorkOrderSecretKey, Path: SecretFileName}},
	}}
}

// OnDemandRunnerPod builds the single-session pod for a ClaudeRunner. The
// caller sets the owner reference.
func OnDemandRunnerPod(env *selfhostedv1alpha1.ClaudeEnvironment, runner *selfhostedv1alpha1.ClaudeRunner, hash string) *corev1.Pod {
	// A session-bound work order registers exactly one runner; extra slots never receive work.
	single := env.DeepCopy()
	single.Spec.Runner.Capacity = 1

	tmpl := RunnerPodTemplate(RunnerPodInput{
		Env:           single,
		ConfigHash:    hash,
		SecretVolume:  WorkOrderSecretVolume(runner.Spec.WorkOrderSecretRef.Name),
		SecretFile:    SecretMountPath + "/" + SecretFileName,
		RestartPolicy: corev1.RestartPolicyNever,
	})
	tmpl.Labels[selfhostedv1alpha1.LabelOrderID] = runner.Spec.OrderID
	if runner.Spec.SessionID != "" {
		tmpl.Labels[selfhostedv1alpha1.LabelSessionID] = runner.Spec.SessionID
	}
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: runner.Name, Namespace: runner.Namespace,
			Labels: tmpl.Labels, Annotations: tmpl.Annotations,
		},
		Spec: tmpl.Spec,
	}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/builders/... -v -run 'TestWorkOrderSecretName|TestOnDemandRunnerPod' && make lint
```

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): on-demand runner pod from a work-order secret

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Orchestrator builders (ServiceAccount, Role, RoleBinding, Deployment)

**Files:**
- Create: `internal/builders/orchestrator.go`
- Test: `internal/builders/orchestrator_test.go`

**Interfaces:**
- Consumes: `EnvironmentSecretVolume`, `SecretMountPath`, `SecretFileName`, `HooksMountPath`, `HomeMountPath`, `TmpMountPath`, `DefaultHealthPort`, `valueOr`, `configMapVolume` (not needed), label constants.
- Produces: `OrchestratorName(env) string`, `OrchestratorSelectorLabels(env) map[string]string`, `OrchestratorServiceAccount(env) *corev1.ServiceAccount`, `OrchestratorRole(env) *rbacv1.Role`, `OrchestratorRoleBinding(env) *rbacv1.RoleBinding`, `OrchestratorArgs(env, secretFile string) []string`, `OrchestratorDeployment(env, hash, hookImage string) *appsv1.Deployment`, constants `HookBinaryPath = "/spawn-runner"`, `HookInstallDir = "/hooks"`, `OrchestratorContainerName = "orchestrator"`, `HookInitContainerName = "install-hook"`.

- [ ] **Step 1: Write the failing test**

```go
package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestOrchestratorRBAC(t *testing.T) {
	env := onDemandEnv()
	sa := OrchestratorServiceAccount(env)
	if sa.Name != "platform-orchestrator" || sa.Namespace != "claude" || sa.Kind != "ServiceAccount" {
		t.Fatalf("service account wrong: %+v", sa.ObjectMeta)
	}
	role := OrchestratorRole(env)
	if role.Kind != "Role" || role.Name != "platform-orchestrator" {
		t.Fatal("role identity wrong")
	}
	wantRules := []rbacv1.PolicyRule{
		{APIGroups: []string{"selfhosted.claudecode.dev"}, Resources: []string{"claudeenvironments"}, Verbs: []string{"get"}},
		{APIGroups: []string{"selfhosted.claudecode.dev"}, Resources: []string{"clauderunners"}, Verbs: []string{"create", "get"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "patch"}},
	}
	if len(role.Rules) != len(wantRules) {
		t.Fatalf("rules: got %+v", role.Rules)
	}
	for i := range wantRules {
		if !equalRule(role.Rules[i], wantRules[i]) {
			t.Fatalf("rule %d: got %+v want %+v", i, role.Rules[i], wantRules[i])
		}
	}

	env.Spec.OnDemand.MaxConcurrentRunners = 5
	role = OrchestratorRole(env)
	if !equalRule(role.Rules[1], rbacv1.PolicyRule{APIGroups: []string{"selfhosted.claudecode.dev"}, Resources: []string{"clauderunners"}, Verbs: []string{"create", "get", "list"}}) {
		t.Fatalf("list must be granted only with a cap: %+v", role.Rules[1])
	}

	rb := OrchestratorRoleBinding(env)
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "platform-orchestrator" || rb.Subjects[0].Kind != "ServiceAccount" || rb.Subjects[0].Name != "platform-orchestrator" {
		t.Fatalf("rolebinding wrong: %+v", rb)
	}
}

func equalRule(a, b rbacv1.PolicyRule) bool {
	eq := func(x, y []string) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	return eq(a.APIGroups, b.APIGroups) && eq(a.Resources, b.Resources) && eq(a.Verbs, b.Verbs)
}

func TestOrchestratorArgs(t *testing.T) {
	env := onDemandEnv()
	env.Spec.OnDemand.Orchestrator.MinIdle = 2
	env.Spec.OnDemand.Orchestrator.LogLevel = "debug"
	got := OrchestratorArgs(env, "/etc/claude/environment-secret")
	want := []string{"self-hosted-runner", "orchestrator",
		"--environment-secret-file", "/etc/claude/environment-secret",
		"--hooks-dir", "/etc/claude/hooks",
		"--health-port", "8080",
		"--expected-spawn-seconds", "120",
		"--hook-timeout", "60",
		"--hook-concurrency", "4",
		"--min-idle", "2",
		"--log-level", "debug"}
	if len(got) != len(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestOrchestratorDeployment(t *testing.T) {
	env := onDemandEnv()
	env.Spec.OnDemand.Orchestrator.Env = []corev1.EnvVar{{Name: "HTTPS_PROXY", Value: "http://proxy:3128"}}
	env.Spec.OnDemand.MaxConcurrentRunners = 7
	env.Spec.OnDemand.Orchestrator.PodTemplate.NodeSelector = map[string]string{"pool": "ops"}
	d := OrchestratorDeployment(env, "h9", "ghcr.io/x/operator:1.0")
	if d.Name != "platform-orchestrator" || d.Kind != "Deployment" || *d.Spec.Replicas != 1 {
		t.Fatalf("deployment identity wrong: %s %s %v", d.Name, d.Kind, d.Spec.Replicas)
	}
	ps := d.Spec.Template.Spec
	if d.Spec.Template.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleOrchestrator ||
		d.Spec.Template.Labels[selfhostedv1alpha1.LabelPartOf] != selfhostedv1alpha1.PartOfValue ||
		d.Spec.Selector.MatchLabels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleOrchestrator {
		t.Fatalf("labels wrong: %v", d.Spec.Template.Labels)
	}
	if d.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h9" {
		t.Fatal("hash annotation missing")
	}
	if ps.ServiceAccountName != "platform-orchestrator" || ps.AutomountServiceAccountToken == nil || !*ps.AutomountServiceAccountToken {
		t.Fatal("orchestrator needs its ServiceAccount token for the hook")
	}
	if ps.NodeSelector["pool"] != "ops" {
		t.Fatal("podTemplate overrides not applied")
	}
	if len(ps.InitContainers) != 1 || ps.InitContainers[0].Image != "ghcr.io/x/operator:1.0" || ps.InitContainers[0].Name != HookInitContainerName {
		t.Fatalf("init container wrong: %+v", ps.InitContainers)
	}
	initArgs := ps.InitContainers[0].Args
	if len(initArgs) != 2 || initArgs[0] != "--install" || initArgs[1] != HookInstallDir {
		t.Fatalf("init args wrong: %v", initArgs)
	}
	c := ps.Containers[0]
	if c.Name != OrchestratorContainerName || c.Image != "r:1" {
		t.Fatalf("orchestrator image must default to the runner image: %+v", c.Image)
	}
	if c.Args[1] != "orchestrator" {
		t.Fatalf("args wrong: %v", c.Args)
	}
	envByName := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		envByName[e.Name] = e
	}
	if envByName[selfhostedv1alpha1.EnvHookEnvironment].Value != "platform" ||
		envByName[selfhostedv1alpha1.EnvHookNamespace].ValueFrom.FieldRef.FieldPath != "metadata.namespace" ||
		envByName[selfhostedv1alpha1.EnvHookMaxConcurrentRunners].Value != "7" ||
		envByName["HTTPS_PROXY"].Value != "http://proxy:3128" {
		t.Fatalf("hook env wrong: %+v", c.Env)
	}
	if c.ReadinessProbe.Exec == nil || c.ReadinessProbe.Exec.Command[0] != "/etc/claude/hooks/spawn-runner" || c.ReadinessProbe.Exec.Command[1] != "--probe-connected" {
		t.Fatalf("readiness must use the injected binary: %+v", c.ReadinessProbe)
	}
	if c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("liveness must be the HTTP probe")
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m
	}
	if !mounts["environment-secret"].ReadOnly || mounts["environment-secret"].MountPath != "/etc/claude" ||
		!mounts["hooks"].ReadOnly || mounts["hooks"].MountPath != "/etc/claude/hooks" {
		t.Fatalf("mounts wrong: %+v", c.VolumeMounts)
	}
	for _, cc := range append(ps.InitContainers, ps.Containers...) {
		sc := cc.SecurityContext
		if sc == nil || *sc.AllowPrivilegeEscalation || !*sc.ReadOnlyRootFilesystem || sc.Capabilities.Drop[0] != "ALL" {
			t.Fatalf("container %s not restricted", cc.Name)
		}
	}
	if ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || !*ps.SecurityContext.RunAsNonRoot {
		t.Fatal("pod security context not restricted")
	}
	if *ps.TerminationGracePeriodSeconds != 30 {
		t.Fatal("orchestrator grace period should be the default 30")
	}

	env.Spec.OnDemand.Orchestrator.Image = "r:orch"
	if OrchestratorDeployment(env, "h", "op").Spec.Template.Spec.Containers[0].Image != "r:orch" {
		t.Fatal("explicit orchestrator image not honoured")
	}
	_ = ptr.To[int32]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/builders/... -run 'TestOrchestrator'
```

- [ ] **Step 3: Implement**

```go
package builders

import (
	"maps"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	HookBinaryPath            = "/spawn-runner"
	HookInstallDir            = "/hooks"
	OrchestratorContainerName = "orchestrator"
	HookInitContainerName     = "install-hook"
	hookVolumeName            = "hooks"
	orchestratorGraceSeconds  = int64(30)
)

// OrchestratorName names every orchestrator-side object for an environment.
func OrchestratorName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-orchestrator"
}

// OrchestratorSelectorLabels are the immutable selector labels of the orchestrator Deployment.
func OrchestratorSelectorLabels(env *selfhostedv1alpha1.ClaudeEnvironment) map[string]string {
	return map[string]string{
		selfhostedv1alpha1.LabelEnvironment: env.Name,
		selfhostedv1alpha1.LabelRole:        selfhostedv1alpha1.RoleOrchestrator,
	}
}

func orchestratorMeta(env *selfhostedv1alpha1.ClaudeEnvironment) metav1.ObjectMeta {
	labels := OrchestratorSelectorLabels(env)
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	return metav1.ObjectMeta{Name: OrchestratorName(env), Namespace: env.Namespace, Labels: labels}
}

// OrchestratorServiceAccount is the identity the spawn-runner hook uses.
func OrchestratorServiceAccount(env *selfhostedv1alpha1.ClaudeEnvironment) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: orchestratorMeta(env),
	}
}

// OrchestratorRole grants exactly what the hook does: read the environment,
// create ClaudeRunners (and list them only when a concurrency cap applies),
// and create the work-order Secret then hand it to the ClaudeRunner.
func OrchestratorRole(env *selfhostedv1alpha1.ClaudeEnvironment) *rbacv1.Role {
	runnerVerbs := []string{"create", "get"}
	if env.Spec.OnDemand != nil && env.Spec.OnDemand.MaxConcurrentRunners > 0 {
		runnerVerbs = append(runnerVerbs, "list")
	}
	return &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: orchestratorMeta(env),
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{selfhostedv1alpha1.GroupVersion.Group}, Resources: []string{"claudeenvironments"}, Verbs: []string{"get"}},
			{APIGroups: []string{selfhostedv1alpha1.GroupVersion.Group}, Resources: []string{"clauderunners"}, Verbs: runnerVerbs},
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "patch"}},
		},
	}
}

// OrchestratorRoleBinding binds the Role to the ServiceAccount.
func OrchestratorRoleBinding(env *selfhostedv1alpha1.ClaudeEnvironment) *rbacv1.RoleBinding {
	name := OrchestratorName(env)
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: orchestratorMeta(env),
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: env.Namespace}},
	}
}

// OrchestratorArgs renders `claude self-hosted-runner orchestrator ...`.
func OrchestratorArgs(env *selfhostedv1alpha1.ClaudeEnvironment, secretFile string) []string {
	o := env.Spec.OnDemand.Orchestrator
	a := []string{
		"self-hosted-runner", "orchestrator",
		"--environment-secret-file", secretFile,
		"--hooks-dir", HooksMountPath,
		"--health-port", strconv.Itoa(int(valueOr(o.HealthPort, DefaultHealthPort))),
		"--expected-spawn-seconds", strconv.Itoa(int(o.ExpectedSpawnSeconds)),
		"--hook-timeout", strconv.Itoa(int(o.HookTimeoutSeconds)),
		"--hook-concurrency", strconv.Itoa(int(o.HookConcurrency)),
	}
	a = appendInt(a, "--min-idle", o.MinIdle)
	return appendStr(a, "--log-level", o.LogLevel)
}

// OrchestratorDeployment runs the orchestrator from the user's image with the
// operator's spawn-runner hook injected by an init container.
func OrchestratorDeployment(env *selfhostedv1alpha1.ClaudeEnvironment, hash, hookImage string) *appsv1.Deployment {
	o := env.Spec.OnDemand.Orchestrator
	pt := o.PodTemplate
	image := o.Image
	if image == "" {
		image = env.Spec.Runner.Image
	}
	healthPort := valueOr(o.HealthPort, DefaultHealthPort)
	name := OrchestratorName(env)

	labels := map[string]string{}
	maps.Copy(labels, pt.Labels)
	maps.Copy(labels, OrchestratorSelectorLabels(env))
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	annotations := map[string]string{}
	maps.Copy(annotations, pt.Annotations)
	annotations[selfhostedv1alpha1.AnnotationConfigHash] = hash

	restricted := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	podSC := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if pt.SecurityContext != nil {
		podSC.RunAsUser, podSC.RunAsGroup = pt.SecurityContext.RunAsUser, pt.SecurityContext.RunAsGroup
		podSC.FSGroup, podSC.SupplementalGroups = pt.SecurityContext.FSGroup, pt.SecurityContext.SupplementalGroups
	}

	envVars := []corev1.EnvVar{
		{Name: selfhostedv1alpha1.EnvHookEnvironment, Value: env.Name},
		{Name: selfhostedv1alpha1.EnvHookNamespace, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: selfhostedv1alpha1.EnvHookMaxConcurrentRunners, Value: strconv.Itoa(int(env.Spec.OnDemand.MaxConcurrentRunners))},
	}
	envVars = append(envVars, o.Env...)

	volumes := []corev1.Volume{
		{Name: "environment-secret", VolumeSource: EnvironmentSecretVolume(env)},
		{Name: hookVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	for _, v := range pt.Volumes {
		volumes = append(volumes, v.ToCoreVolume())
	}
	mounts := []corev1.VolumeMount{
		{Name: "environment-secret", MountPath: SecretMountPath, ReadOnly: true},
		{Name: hookVolumeName, MountPath: HooksMountPath, ReadOnly: true},
		{Name: "home", MountPath: HomeMountPath},
		{Name: "tmp", MountPath: TmpMountPath},
	}
	mounts = append(mounts, pt.VolumeMounts...)

	healthURL := "http://127.0.0.1:" + strconv.Itoa(int(healthPort)) + "/healthz"
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: orchestratorMeta(env),
		Spec: appsv1.DeploymentSpec{
			Replicas: o.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: OrchestratorSelectorLabels(env)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					ServiceAccountName:            name,
					AutomountServiceAccountToken:  ptr.To(true),
					TerminationGracePeriodSeconds: ptr.To(orchestratorGraceSeconds),
					SecurityContext:               podSC,
					ImagePullSecrets:              pt.ImagePullSecrets,
					NodeSelector:                  pt.NodeSelector,
					Tolerations:                   pt.Tolerations,
					Affinity:                      pt.Affinity,
					TopologySpreadConstraints:     pt.TopologySpreadConstraints,
					PriorityClassName:             pt.PriorityClassName,
					RuntimeClassName:              pt.RuntimeClassName,
					SchedulerName:                 pt.SchedulerName,
					Volumes:                       volumes,
					InitContainers: []corev1.Container{{
						Name:            HookInitContainerName,
						Image:           hookImage,
						Args:            []string{"--install", HookInstallDir},
						VolumeMounts:    []corev1.VolumeMount{{Name: hookVolumeName, MountPath: HookInstallDir}},
						SecurityContext: restricted,
					}},
					Containers: []corev1.Container{{
						Name:  OrchestratorContainerName,
						Image: image,
						Args:  OrchestratorArgs(env, SecretMountPath+"/"+SecretFileName),
						Env:   envVars,
						Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: healthPort, Protocol: corev1.ProtocolTCP}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
							Command: []string{HooksMountPath + "/spawn-runner", "--probe-connected", healthURL}}},
							InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 5},
						LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
							Path: "/healthz", Port: intstr.FromString("health")}}, InitialDelaySeconds: 30, PeriodSeconds: 30},
						Resources:                pt.Resources,
						VolumeMounts:             mounts,
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						SecurityContext:          restricted,
					}},
				},
			},
		},
	}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/builders/... -v -run 'TestOrchestrator' && make lint
```

- [ ] **Step 5: Commit**

```bash
git add internal/builders
git commit -m "feat(builders): orchestrator Deployment, ServiceAccount, Role and RoleBinding

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: spawn-runner hook library

**Files:**
- Create: `internal/hook/input.go`, `internal/hook/hook.go`, `internal/hook/log.go`
- Test: `internal/hook/hook_test.go`

**Interfaces:**
- Consumes: `builders.WorkOrderSecretName`, label and annotation constants.
- Produces:
  - `type Input struct { WorkOrderFile, OrderID, SessionID, SessionUUID, ClientPlatform, PrimaryRepoURL, AccountID, Environment, Namespace string; Attempt int32; MaxConcurrentRunners int }`
  - `func ParseEnv(getenv func(string) string) (Input, error)`
  - `const ExitSubmitted = 0; ExitRetryable = 1; ExitNonRetryable = 2; Timeout = 45 * time.Second`
  - `type Result struct { ExitCode int; Outcome string; RunnerName string; Warning string; Err error }`
  - `func Run(ctx context.Context, c client.Client, in Input, jwt []byte, traceparent string) Result`
  - `func Classify(err error) int`
  - `func WriteLog(w io.Writer, in Input, res Result)` (one JSON line) and `func NewScheme() *runtime.Scheme`.

- [ ] **Step 1: Write the failing test**

`internal/hook/hook_test.go`:

```go
package hook

import (
	"context"
	"errors"
	"strings"
	"testing"
	"bytes"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	testNS      = "claude"
	testEnvName = "platform"
	testOrder   = "order-abc"
)

func envObj() *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{ObjectMeta: metav1.ObjectMeta{Name: testEnvName, Namespace: testNS, UID: "env-uid"}}
}

func input() Input {
	return Input{WorkOrderFile: "/dev/null", OrderID: testOrder, SessionID: "session_1", SessionUUID: "uuid-1",
		Attempt: 1, ClientPlatform: "web_claude_ai", PrimaryRepoURL: "https://github.com/x/y", AccountID: "user_1",
		Environment: testEnvName, Namespace: testNS}
}

func newClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(objs...).Build()
}

func TestParseEnv(t *testing.T) {
	env := map[string]string{
		"CLAUDE_RUNNER_WORK_ORDER_FILE": "/tmp/wo", "CLAUDE_RUNNER_ORDER_ID": "o1", "CLAUDE_RUNNER_SESSION_ID": "s1",
		"CLAUDE_RUNNER_SESSION_UUID": "u1", "CLAUDE_RUNNER_ATTEMPT": "2", "CLAUDE_RUNNER_CLIENT_PLATFORM": "ios",
		"CLAUDE_RUNNER_PRIMARY_REPO_URL": "https://r", "CLAUDE_RUNNER_ACCOUNT_ID": "user_9",
		"CLAUDE_RUNNER_ACCOUNT_EMAIL":   "someone@example.com",
		selfhostedv1alpha1.EnvHookEnvironment: "platform", selfhostedv1alpha1.EnvHookNamespace: "claude",
		selfhostedv1alpha1.EnvHookMaxConcurrentRunners: "3",
	}
	in, err := ParseEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if in.OrderID != "o1" || in.Attempt != 2 || in.MaxConcurrentRunners != 3 || in.Namespace != "claude" || in.AccountID != "user_9" {
		t.Fatalf("parsed wrong: %+v", in)
	}
	for _, missing := range []string{"CLAUDE_RUNNER_WORK_ORDER_FILE", "CLAUDE_RUNNER_ORDER_ID", selfhostedv1alpha1.EnvHookEnvironment, selfhostedv1alpha1.EnvHookNamespace} {
		e2 := map[string]string{}
		for k, v := range env {
			e2[k] = v
		}
		delete(e2, missing)
		if _, err := ParseEnv(func(k string) string { return e2[k] }); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s must error naming it, got %v", missing, err)
		}
	}
	env["CLAUDE_RUNNER_ATTEMPT"] = ""
	if in, err := ParseEnv(func(k string) string { return env[k] }); err != nil || in.Attempt != 0 {
		t.Fatalf("empty attempt must default to 0: %+v %v", in, err)
	}
}

func TestRunSubmitsSecretAndRunner(t *testing.T) {
	ctx := context.Background()
	c := newClient(envObj())
	res := Run(ctx, c, input(), []byte("eyJ.fake.jwt"), "00-abc-def-01")
	if res.ExitCode != ExitSubmitted || res.Err != nil || res.Outcome != "submitted" {
		t.Fatalf("unexpected result %+v", res)
	}
	runner := &selfhostedv1alpha1.ClaudeRunner{}
	if err := c.Get(ctx, types.NamespacedName{Name: testOrder, Namespace: testNS}, runner); err != nil {
		t.Fatal(err)
	}
	if runner.Spec.OrderID != testOrder || runner.Spec.SessionID != "session_1" || runner.Spec.AccountID != "user_1" ||
		runner.Spec.WorkOrderSecretRef.Name != testOrder+"-work-order" || runner.Spec.EnvironmentRef.Name != testEnvName {
		t.Fatalf("runner spec wrong: %+v", runner.Spec)
	}
	if runner.Labels[selfhostedv1alpha1.LabelEnvironment] != testEnvName || runner.Labels[selfhostedv1alpha1.LabelOrderID] != testOrder ||
		runner.Labels[selfhostedv1alpha1.LabelSessionID] != "session_1" {
		t.Fatalf("runner labels wrong: %v", runner.Labels)
	}
	if runner.Annotations[selfhostedv1alpha1.AnnotationTraceparent] != "00-abc-def-01" {
		t.Fatal("traceparent annotation missing")
	}
	if len(runner.OwnerReferences) != 1 || runner.OwnerReferences[0].Kind != "ClaudeEnvironment" || runner.OwnerReferences[0].Controller == nil || !*runner.OwnerReferences[0].Controller {
		t.Fatalf("runner must be controller-owned by the environment: %+v", runner.OwnerReferences)
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: testOrder + "-work-order", Namespace: testNS}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[selfhostedv1alpha1.WorkOrderSecretKey]) != "eyJ.fake.jwt" {
		t.Fatal("jwt not stored under the jwt key")
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "ClaudeRunner" || secret.OwnerReferences[0].Name != testOrder {
		t.Fatalf("secret must be handed to the runner: %+v", secret.OwnerReferences)
	}
	if secret.Labels[selfhostedv1alpha1.LabelOrderID] != testOrder {
		t.Fatal("secret labels missing")
	}
}

func TestRunRedeliveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newClient(envObj())
	first := Run(ctx, c, input(), []byte("jwt-1"), "")
	if first.ExitCode != ExitSubmitted {
		t.Fatalf("first: %+v", first)
	}
	second := Run(ctx, c, input(), []byte("jwt-2"), "")
	if second.ExitCode != ExitSubmitted || second.Outcome != "redelivered" {
		t.Fatalf("redelivery must exit 0: %+v", second)
	}
	runners := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := c.List(ctx, runners, client.InNamespace(testNS)); err != nil || len(runners.Items) != 1 {
		t.Fatalf("exactly one runner expected, got %d (%v)", len(runners.Items), err)
	}
	secret := &corev1.Secret{}
	_ = c.Get(ctx, types.NamespacedName{Name: testOrder + "-work-order", Namespace: testNS}, secret)
	if string(secret.Data[selfhostedv1alpha1.WorkOrderSecretKey]) != "jwt-1" {
		t.Fatal("redelivery must not overwrite the first work order")
	}
}

func TestRunEnvironmentMissingIsNonRetryable(t *testing.T) {
	res := Run(context.Background(), newClient(), input(), []byte("j"), "")
	if res.ExitCode != ExitNonRetryable || res.Err == nil {
		t.Fatalf("got %+v", res)
	}
}

func TestRunConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	active := &selfhostedv1alpha1.ClaudeRunner{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNS,
		Labels: map[string]string{selfhostedv1alpha1.LabelEnvironment: testEnvName}},
		Spec:   selfhostedv1alpha1.ClaudeRunnerSpec{EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: testEnvName}, OrderID: "other", WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "x"}},
		Status: selfhostedv1alpha1.ClaudeRunnerStatus{Phase: selfhostedv1alpha1.RunnerRunning}}
	done := active.DeepCopy()
	done.Name, done.Spec.OrderID, done.Status.Phase = "done", "done", selfhostedv1alpha1.RunnerSucceeded

	in := input()
	in.MaxConcurrentRunners = 1
	res := Run(ctx, newClient(envObj(), active, done), in, []byte("j"), "")
	if res.ExitCode != ExitRetryable || res.Outcome != "at_capacity" {
		t.Fatalf("at cap must be retryable: %+v", res)
	}
	in.MaxConcurrentRunners = 2
	if res := Run(ctx, newClient(envObj(), active, done), in, []byte("j"), ""); res.ExitCode != ExitSubmitted {
		t.Fatalf("terminal runners must not count: %+v", res)
	}
}

func TestRunClassifiesAPIErrors(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "secrets"}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"timeout", apierrors.NewTimeoutError("slow", 1), ExitRetryable},
		{"too many requests", apierrors.NewTooManyRequests("busy", 1), ExitRetryable},
		{"internal", apierrors.NewInternalError(errors.New("boom")), ExitRetryable},
		{"service unavailable", apierrors.NewServiceUnavailable("down"), ExitRetryable},
		{"transport", errors.New("dial tcp: connection refused"), ExitRetryable},
		{"forbidden", apierrors.NewForbidden(gr, "x", errors.New("no")), ExitNonRetryable},
		{"invalid", apierrors.NewBadRequest("bad"), ExitNonRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(envObj()).
				WithInterceptorFuncs(interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					return tc.err
				}}).Build()
			res := Run(ctx, c, input(), []byte("j"), "")
			if res.ExitCode != tc.want {
				t.Fatalf("got %d want %d (%+v)", res.ExitCode, tc.want, res)
			}
			if Classify(tc.err) != tc.want {
				t.Fatalf("Classify mismatch")
			}
		})
	}
}

func TestWriteLogNeverPrintsSecrets(t *testing.T) {
	var buf bytes.Buffer
	in := input()
	WriteLog(&buf, in, Result{ExitCode: 2, Outcome: "error", Err: errors.New("forbidden: token eyJhbGci.x.y for someone@example.com")})
	out := buf.String()
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(strings.TrimSpace(out), "}") {
		t.Fatalf("expected one JSON line, got %q", out)
	}
	for _, leak := range []string{"eyJhbGci", "someone@example.com"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"orderID":"order-abc"`) || !strings.Contains(out, `"exitCode":2`) {
		t.Fatalf("log missing fields: %s", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/hook/...
```

Expected: FAIL, package does not exist.

- [ ] **Step 3: Implement**

`internal/hook/input.go`:

```go
package hook

import (
	"fmt"
	"strconv"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// Input is everything the hook reads from its environment. The account email
// is deliberately not read: it is personally identifiable and never stored.
type Input struct {
	WorkOrderFile        string
	OrderID              string
	SessionID            string
	SessionUUID          string
	Attempt              int32
	ClientPlatform       string
	PrimaryRepoURL       string
	AccountID            string
	Environment          string
	Namespace            string
	MaxConcurrentRunners int
}

// ParseEnv reads the product's hook variables plus the operator's own.
func ParseEnv(getenv func(string) string) (Input, error) {
	in := Input{
		WorkOrderFile:  getenv("CLAUDE_RUNNER_WORK_ORDER_FILE"),
		OrderID:        getenv("CLAUDE_RUNNER_ORDER_ID"),
		SessionID:      getenv("CLAUDE_RUNNER_SESSION_ID"),
		SessionUUID:    getenv("CLAUDE_RUNNER_SESSION_UUID"),
		ClientPlatform: getenv("CLAUDE_RUNNER_CLIENT_PLATFORM"),
		PrimaryRepoURL: getenv("CLAUDE_RUNNER_PRIMARY_REPO_URL"),
		AccountID:      getenv("CLAUDE_RUNNER_ACCOUNT_ID"),
		Environment:    getenv(selfhostedv1alpha1.EnvHookEnvironment),
		Namespace:      getenv(selfhostedv1alpha1.EnvHookNamespace),
	}
	for name, v := range map[string]string{
		"CLAUDE_RUNNER_WORK_ORDER_FILE":       in.WorkOrderFile,
		"CLAUDE_RUNNER_ORDER_ID":              in.OrderID,
		selfhostedv1alpha1.EnvHookEnvironment: in.Environment,
		selfhostedv1alpha1.EnvHookNamespace:   in.Namespace,
	} {
		if v == "" {
			return Input{}, fmt.Errorf("required environment variable %s is not set", name)
		}
	}
	if s := getenv("CLAUDE_RUNNER_ATTEMPT"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Input{}, fmt.Errorf("CLAUDE_RUNNER_ATTEMPT is not an integer: %w", err)
		}
		in.Attempt = int32(n)
	}
	if s := getenv(selfhostedv1alpha1.EnvHookMaxConcurrentRunners); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Input{}, fmt.Errorf("%s is not an integer: %w", selfhostedv1alpha1.EnvHookMaxConcurrentRunners, err)
		}
		in.MaxConcurrentRunners = n
	}
	return in, nil
}
```

`internal/hook/hook.go`:

```go
package hook

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

// Exit codes from the product's spawn-runner hook contract.
const (
	ExitSubmitted    = 0
	ExitRetryable    = 1
	ExitNonRetryable = 2

	// Timeout bounds one hook run well inside the orchestrator's default
	// --hook-timeout of 60 seconds so the hook is never killed mid-create.
	Timeout = 45 * time.Second
)

// Result is the hook's outcome.
type Result struct {
	ExitCode   int
	Outcome    string // submitted | redelivered | at_capacity | error
	RunnerName string
	Warning    string // non-fatal follow-up problems, e.g. the owner hand-off patch failed
	Err        error
}

// NewScheme has the core and operator types the hook touches.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = selfhostedv1alpha1.AddToScheme(s)
	return s
}

// Run performs the single idempotent spawn: create the work-order Secret, then
// the ClaudeRunner named after the order ID, then hand the Secret to the
// ClaudeRunner for garbage collection.
func Run(ctx context.Context, c client.Client, in Input, jwt []byte, traceparent string) Result {
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := c.Get(ctx, types.NamespacedName{Name: in.Environment, Namespace: in.Namespace}, env); err != nil {
		return failure(fmt.Errorf("get ClaudeEnvironment %s/%s: %w", in.Namespace, in.Environment, err))
	}

	if in.MaxConcurrentRunners > 0 {
		active, err := countActiveRunners(ctx, c, in.Namespace, in.Environment)
		if err != nil {
			return failure(fmt.Errorf("list ClaudeRunners: %w", err))
		}
		if active >= in.MaxConcurrentRunners {
			return Result{ExitCode: ExitRetryable, Outcome: "at_capacity",
				Err: fmt.Errorf("%d runners active, cap is %d", active, in.MaxConcurrentRunners)}
		}
	}

	envRef := metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))
	labels := map[string]string{
		selfhostedv1alpha1.LabelEnvironment: in.Environment,
		selfhostedv1alpha1.LabelOrderID:     in.OrderID,
	}
	if in.SessionID != "" {
		labels[selfhostedv1alpha1.LabelSessionID] = in.SessionID
	}

	secretName := builders.WorkOrderSecretName(in.OrderID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: in.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{*envRef}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{selfhostedv1alpha1.WorkOrderSecretKey: jwt},
	}
	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return failure(fmt.Errorf("create work-order Secret: %w", err))
	}

	runner := &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: in.OrderID, Namespace: in.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{*envRef}},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
			EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: in.Environment},
			OrderID:            in.OrderID,
			SessionID:          in.SessionID,
			SessionUUID:        in.SessionUUID,
			Attempt:            in.Attempt,
			ClientPlatform:     in.ClientPlatform,
			PrimaryRepoURL:     in.PrimaryRepoURL,
			AccountID:          in.AccountID,
			WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: secretName},
		},
	}
	if traceparent != "" {
		runner.Annotations = map[string]string{selfhostedv1alpha1.AnnotationTraceparent: traceparent}
	}
	if err := c.Create(ctx, runner); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return Result{ExitCode: ExitSubmitted, Outcome: "redelivered", RunnerName: in.OrderID}
		}
		return failure(fmt.Errorf("create ClaudeRunner: %w", err))
	}

	// Hand the Secret to the ClaudeRunner so it is collected with it. Best
	// effort: the environment owner reference already guarantees collection.
	res := Result{ExitCode: ExitSubmitted, Outcome: "submitted", RunnerName: in.OrderID}
	patch := client.MergeFrom(secret.DeepCopy())
	secret.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(runner, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeRunner"))}
	if err := c.Patch(ctx, secret, patch); err != nil {
		res.Warning = "could not hand the work-order Secret to the ClaudeRunner: " + err.Error()
	}
	return res
}

func countActiveRunners(ctx context.Context, c client.Client, namespace, environment string) (int, error) {
	list := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := c.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: environment}); err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		if !list.Items[i].Status.Phase.IsTerminal() {
			n++
		}
	}
	return n, nil
}

func failure(err error) Result {
	return Result{ExitCode: Classify(err), Outcome: "error", Err: err}
}

// Classify maps an error onto the hook exit-code contract. Transport and
// server-side failures are retryable; everything the API rejected outright
// is not.
func Classify(err error) int {
	if err == nil {
		return ExitSubmitted
	}
	switch {
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsTooManyRequests(err),
		apierrors.IsInternalError(err), apierrors.IsServiceUnavailable(err), apierrors.IsUnexpectedServerError(err):
		return ExitRetryable
	}
	if _, isStatus := err.(apierrors.APIStatus); !isStatus {
		var statusErr *apierrors.StatusError
		if !errorsAs(err, &statusErr) {
			return ExitRetryable // transport, DNS, context deadline: not an API verdict
		}
	}
	return ExitNonRetryable
}
```

Add at the bottom of `hook.go`:

```go
// errorsAs is errors.As without importing errors into the API-error switch above.
func errorsAs(err error, target **apierrors.StatusError) bool {
	for err != nil {
		if se, ok := err.(*apierrors.StatusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
```

`internal/hook/log.go`:

```go
package hook

import (
	"encoding/json"
	"io"
	"regexp"
	"time"
)

// sensitive matches emails, JWTs and Anthropic-issued credentials so an API
// error message can never carry them into the orchestrator log.
var sensitive = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|eyJ[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+){1,2}|sk-ant-[A-Za-z0-9_-]+|ccenvkey_[A-Za-z0-9_-]+`)

type logLine struct {
	Time       string `json:"ts"`
	OrderID    string `json:"orderID"`
	SessionID  string `json:"sessionID,omitempty"`
	Attempt    int32  `json:"attempt"`
	Outcome    string `json:"outcome"`
	ExitCode   int    `json:"exitCode"`
	RunnerName string `json:"runner,omitempty"`
	Warning    string `json:"warning,omitempty"`
	Error      string `json:"error,omitempty"`
}

// WriteLog emits the hook's single structured log line.
func WriteLog(w io.Writer, in Input, res Result) {
	l := logLine{Time: time.Now().UTC().Format(time.RFC3339), OrderID: in.OrderID, SessionID: in.SessionID,
		Attempt: in.Attempt, Outcome: res.Outcome, ExitCode: res.ExitCode, RunnerName: res.RunnerName,
		Warning: Redact(res.Warning)}
	if res.Err != nil {
		l.Error = Redact(res.Err.Error())
	}
	b, _ := json.Marshal(l)
	_, _ = w.Write(append(b, '\n'))
}

// Redact replaces credential-shaped substrings with a marker.
func Redact(s string) string {
	return sensitive.ReplaceAllString(s, "[redacted]")
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/hook/... -v && make lint
```

If the linter rejects the hand-written `errorsAs`, replace it with `errors.As(err, &statusErr)` from the standard library; the behaviour is identical.

- [ ] **Step 5: Commit**

```bash
git add internal/hook
git commit -m "feat(hook): spawn-runner library with idempotent create and exit-code contract

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: spawn-runner binary: install, probe, hook modes; image build

**Files:**
- Create: `internal/hook/install.go`, `internal/hook/probe.go`, `cmd/spawn-runner/main.go`
- Modify: `Dockerfile`, `Makefile` (`build` target)
- Test: `internal/hook/install_test.go`, `internal/hook/probe_test.go`

**Interfaces:**
- Produces: `func Install(dir string) error` (copies the running executable to `<dir>/spawn-runner` with mode 0555), `func ProbeConnected(ctx context.Context, url string) (bool, error)`; binary modes `--install <dir>`, `--probe-connected <url>`, default = hook.

- [ ] **Step 1: Write the failing tests**

`internal/hook/install_test.go`:

```go
package hook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCopiesExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "spawn-runner"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o555 {
		t.Fatalf("mode %o, want 0555", st.Mode().Perm())
	}
	self, _ := os.Executable()
	want, _ := os.ReadFile(self)
	got, _ := os.ReadFile(filepath.Join(dir, "spawn-runner"))
	if len(got) == 0 || len(got) != len(want) {
		t.Fatalf("copied %d bytes, want %d", len(got), len(want))
	}
	// Re-running must replace atomically, not fail.
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
}
```

`internal/hook/probe_test.go`:

```go
package hook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeConnected(t *testing.T) {
	body := `{"status":"ok","connected":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	if ok, err := ProbeConnected(context.Background(), srv.URL); err != nil || ok {
		t.Fatalf("connected=false must probe false: %v %v", ok, err)
	}
	body = `{"status":"ok","connected":true,"queue_counts":{"pending":0}}`
	if ok, err := ProbeConnected(context.Background(), srv.URL); err != nil || !ok {
		t.Fatalf("connected=true must probe true: %v %v", ok, err)
	}
	body = `not json`
	if _, err := ProbeConnected(context.Background(), srv.URL); err == nil {
		t.Fatal("unparseable body must error")
	}
	if _, err := ProbeConnected(context.Background(), "http://127.0.0.1:1/healthz"); err == nil {
		t.Fatal("unreachable must error")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/hook/... -run 'TestInstall|TestProbe'
```

- [ ] **Step 3: Implement**

`internal/hook/install.go`:

```go
package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Install copies the running binary into dir as spawn-runner, mode 0555, so
// an init container can populate the orchestrator's hooks directory from a
// distroless image that has no shell or cp.
func Install(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".spawn-runner-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o555); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "spawn-runner"))
}
```

`internal/hook/probe.go`:

```go
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProbeConnected reads the orchestrator's /healthz body. The product returns
// 200 whenever the process is alive, so readiness must come from the
// "connected" field.
func ProbeConnected(ctx context.Context, url string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, err
	}
	var h struct {
		Connected *bool `json:"connected"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return false, fmt.Errorf("parse healthz body: %w", err)
	}
	return h.Connected != nil && *h.Connected, nil
}
```

`cmd/spawn-runner/main.go`:

```go
// Command spawn-runner is the orchestrator's spawn-runner hook. It also serves
// as its own installer (--install) and as the orchestrator readiness probe
// (--probe-connected) so the user's image needs no extra tooling.
package main

import (
	"context"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/hook"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 2 && args[0] == "--install":
		if err := hook.Install(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	case len(args) == 2 && args[0] == "--probe-connected":
		ok, err := hook.ProbeConnected(context.Background(), args[1])
		if err != nil || !ok {
			os.Exit(1)
		}
		return
	case len(args) == 1 && args[0] == "--version":
		fmt.Println(version)
		return
	}
	os.Exit(runHook())
}

func runHook() int {
	in, err := hook.ParseEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return hook.ExitNonRetryable
	}
	jwt, err := os.ReadFile(in.WorkOrderFile)
	if err != nil || len(jwt) == 0 {
		fmt.Fprintln(os.Stderr, "error: work-order file is missing or empty")
		return hook.ExitNonRetryable
	}
	ctx, cancel := context.WithTimeout(context.Background(), hook.Timeout)
	defer cancel()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no in-cluster configuration:", err)
		return hook.ExitNonRetryable
	}
	c, err := client.New(cfg, client.Options{Scheme: hook.NewScheme()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return hook.ExitRetryable
	}
	res := hook.Run(ctx, c, in, jwt, "")
	hook.WriteLog(os.Stdout, in, res)
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "error:", hook.Redact(res.Err.Error()))
	}
	return res.ExitCode
}
```

Dockerfile: change the build `RUN` to build both binaries and copy the hook into the final stage:

```dockerfile
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -ldflags "-X main.version=${VERSION}" -o manager ./cmd \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -ldflags "-X main.version=${VERSION}" -o spawn-runner ./cmd/spawn-runner
```

and after `COPY --from=builder /workspace/manager .` add `COPY --from=builder /workspace/spawn-runner /spawn-runner`.

Makefile `build` target: add a second line `go build -ldflags "-X main.version=$(VERSION)" -o bin/spawn-runner ./cmd/spawn-runner`.

- [ ] **Step 4: Verify**

```bash
go test ./internal/hook/... -v && make build && ./bin/spawn-runner --version && make docker-build IMG=example.com/op:dev && docker run --rm --entrypoint /spawn-runner example.com/op:dev --version && make lint
```

Expected: tests pass, both binaries build, the image contains `/spawn-runner`, lint clean.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(hook): spawn-runner binary with install and readiness-probe modes

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Runner phase derivation

**Files:**
- Create: `internal/controller/runnerphase.go`
- Test: `internal/controller/runnerphase_test.go`

**Interfaces:**
- Consumes: `redact`, `truncate` from `failedstart.go`; `builders.RunnerContainerName`.
- Produces: `type phaseResult struct { Phase v1alpha1.RunnerPhase; Reason, Message string; StartedAt, FinishedAt *metav1.Time }`, `func derivePhase(pod *corev1.Pod) phaseResult`, `var nowFunc = time.Now`.

- [ ] **Step 1: Write the failing test**

```go
package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

func podIn(phase corev1.PodPhase, cs corev1.ContainerStatus) *corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-time.Minute))
	cs.Name = builders.RunnerContainerName
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase, StartTime: &start, ContainerStatuses: []corev1.ContainerStatus{cs}}}
}

func TestDerivePhase(t *testing.T) {
	end := metav1.Now()
	cases := []struct {
		name    string
		pod     *corev1.Pod
		phase   selfhostedv1alpha1.RunnerPhase
		reason  string
		contain string
		started bool
		ended   bool
	}{
		{"no pod", nil, selfhostedv1alpha1.RunnerPending, selfhostedv1alpha1.ReasonPodPending, "not created", false, false},
		{"pending, image pull", podIn(corev1.PodPending, corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull failed"}}}),
			selfhostedv1alpha1.RunnerPending, "ImagePullBackOff", "pull failed", false, false},
		{"pending, unschedulable", func() *corev1.Pod {
			p := podIn(corev1.PodPending, corev1.ContainerStatus{})
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes"}}
			return p
		}(), selfhostedv1alpha1.RunnerPending, "Unschedulable", "0/3 nodes", false, false},
		{"running", podIn(corev1.PodRunning, corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}),
			selfhostedv1alpha1.RunnerRunning, selfhostedv1alpha1.ReasonPodRunning, "", true, false},
		{"succeeded", podIn(corev1.PodSucceeded, corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: end}}}),
			selfhostedv1alpha1.RunnerSucceeded, selfhostedv1alpha1.ReasonPodSucceeded, "", true, true},
		{"failed with redaction", podIn(corev1.PodFailed, corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: end,
			Message: "[runner:fatal] bad token for someone@example.com"}}}),
			selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "exit code 1", true, true},
		{"unknown", podIn(corev1.PodUnknown, corev1.ContainerStatus{}), selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed, "Unknown", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePhase(tc.pod)
			if got.Phase != tc.phase || got.Reason != tc.reason {
				t.Fatalf("got %s/%s want %s/%s", got.Phase, got.Reason, tc.phase, tc.reason)
			}
			if tc.contain != "" && !strings.Contains(got.Message, tc.contain) {
				t.Fatalf("message %q lacks %q", got.Message, tc.contain)
			}
			if strings.Contains(got.Message, "@example.com") {
				t.Fatal("message leaked an email")
			}
			if (got.StartedAt != nil) != tc.started || (got.FinishedAt != nil) != tc.ended {
				t.Fatalf("timestamps: started=%v ended=%v", got.StartedAt != nil, got.FinishedAt != nil)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/controller/... -run TestDerivePhase
```

- [ ] **Step 3: Implement**

```go
package controller

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

// nowFunc is swapped in tests to exercise time-based decisions.
var nowFunc = time.Now

type phaseResult struct {
	Phase      selfhostedv1alpha1.RunnerPhase
	Reason     string
	Message    string
	StartedAt  *metav1.Time
	FinishedAt *metav1.Time
}

// derivePhase maps a runner pod onto the ClaudeRunner lifecycle.
func derivePhase(pod *corev1.Pod) phaseResult {
	if pod == nil {
		return phaseResult{Phase: selfhostedv1alpha1.RunnerPending, Reason: selfhostedv1alpha1.ReasonPodPending, Message: "pod not created yet"}
	}
	var cs *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == builders.RunnerContainerName {
			cs = &pod.Status.ContainerStatuses[i]
		}
	}
	res := phaseResult{StartedAt: pod.Status.StartTime}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerRunning, selfhostedv1alpha1.ReasonPodRunning
	case corev1.PodSucceeded:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerSucceeded, selfhostedv1alpha1.ReasonPodSucceeded
		res.FinishedAt = terminatedAt(cs)
	case corev1.PodFailed:
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed
		res.FinishedAt = terminatedAt(cs)
		if cs != nil && cs.State.Terminated != nil {
			t := cs.State.Terminated
			res.Message = truncate(redact(fmt.Sprintf("exit code %d (%s): %s", t.ExitCode, t.Reason, fatalLine(t.Message))), failedStartMessageLimit)
		} else {
			res.Message = "pod failed: " + pod.Status.Reason
		}
	case corev1.PodUnknown:
		now := metav1.NewTime(nowFunc())
		res.Phase, res.Reason = selfhostedv1alpha1.RunnerFailed, selfhostedv1alpha1.ReasonPodFailed
		res.Message, res.FinishedAt = "pod phase Unknown: node unreachable", &now
	default: // Pending
		res.Phase, res.Reason, res.StartedAt = selfhostedv1alpha1.RunnerPending, selfhostedv1alpha1.ReasonPodPending, nil
		res.Message = "pod is pending"
		if cs != nil && cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			res.Reason, res.Message = cs.State.Waiting.Reason, truncate(redact(cs.State.Waiting.Message), failedStartMessageLimit)
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
				res.Reason, res.Message = c.Reason, truncate(redact(c.Message), failedStartMessageLimit)
			}
		}
	}
	return res
}

func terminatedAt(cs *corev1.ContainerStatus) *metav1.Time {
	if cs != nil && cs.State.Terminated != nil && !cs.State.Terminated.FinishedAt.IsZero() {
		t := cs.State.Terminated.FinishedAt
		return &t
	}
	now := metav1.NewTime(nowFunc())
	return &now
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/controller/... -run TestDerivePhase -v && make lint
```

- [ ] **Step 5: Commit**

```bash
git add internal/controller/runnerphase.go internal/controller/runnerphase_test.go
git commit -m "feat(controller): derive ClaudeRunner phase from the runner pod

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: ClaudeRunner controller

**Files:**
- Modify: `internal/controller/clauderunner_controller.go` (replace scaffold body)
- Modify: `internal/controller/suite_test.go` (register the reconciler)
- Test: `internal/controller/clauderunner_controller_test.go`

**Interfaces:**
- Consumes: `derivePhase`, `nowFunc`, `builders.OnDemandRunnerPod`, `builders.ConfigHash`, `redact`.
- Produces: `ClaudeRunnerReconciler{Client, Scheme, Recorder}` with `Reconcile` and `SetupWithManager`; package-level `runnerReconciler` and `envReconciler` pointers in `suite_test.go` for tests that need to tweak fields.

- [ ] **Step 1: Register in the suite and write the failing tests**

In `suite_test.go`, keep the existing manager setup and add, before the manager starts:

```go
	envReconciler = &ClaudeEnvironmentReconciler{
		Client: k8sManager.GetClient(), Scheme: k8sManager.GetScheme(),
		//nolint:staticcheck // the events.k8s.io replacement changes the API; migrate separately
		Recorder: k8sManager.GetEventRecorderFor("claude-selfhosted-operator-test"),
	}
	Expect(envReconciler.SetupWithManager(k8sManager)).To(Succeed())
	runnerReconciler = &ClaudeRunnerReconciler{
		Client: k8sManager.GetClient(), Scheme: k8sManager.GetScheme(),
		//nolint:staticcheck // the events.k8s.io replacement changes the API; migrate separately
		Recorder: k8sManager.GetEventRecorderFor("claude-selfhosted-operator-test"),
	}
	Expect(runnerReconciler.SetupWithManager(k8sManager)).To(Succeed())
```

replacing the inline `ClaudeEnvironmentReconciler` registration, and declare `var envReconciler *ClaudeEnvironmentReconciler; var runnerReconciler *ClaudeRunnerReconciler` with the other suite vars. (Task 8 adds `HookImage` to `envReconciler` here.)

`internal/controller/clauderunner_controller_test.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func onDemandEnvObj(ns string) *selfhostedv1alpha1.ClaudeEnvironment {
	env := fixedEnv(ns)
	env.Spec.Fixed = nil
	env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{RunnerTTLSecondsAfterFinished: ptr.To[int32](300)}
	return env
}

func workOrderSecret(ns, order string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: order + "-work-order", Namespace: ns},
		Data: map[string][]byte{selfhostedv1alpha1.WorkOrderSecretKey: []byte("eyJ.work.order")}}
}

func runnerObj(ns, order string) *selfhostedv1alpha1.ClaudeRunner {
	return &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: order, Namespace: ns, Labels: map[string]string{selfhostedv1alpha1.LabelEnvironment: "platform", selfhostedv1alpha1.LabelOrderID: order}},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: "platform"}, OrderID: order,
			SessionID: "session_" + order, WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: order + "-work-order"}},
	}
}

func runnerPhase(ctx context.Context, key types.NamespacedName) func() (selfhostedv1alpha1.RunnerPhase, error) {
	return func() (selfhostedv1alpha1.RunnerPhase, error) {
		r := &selfhostedv1alpha1.ClaudeRunner{}
		if err := k8sClient.Get(ctx, key, r); err != nil {
			return "", err
		}
		return r.Status.Phase, nil
	}
}

func setPodPhase(ctx context.Context, key types.NamespacedName, phase corev1.PodPhase, term *corev1.ContainerStateTerminated) {
	pod := &corev1.Pod{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, pod)).To(Succeed())
	now := metav1.Now()
	pod.Status.Phase = phase
	pod.Status.StartTime = &now
	state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}
	if term != nil {
		state = corev1.ContainerState{Terminated: term}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runner", State: state}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

var _ = Describe("ClaudeRunner controller", func() {
	ctx := context.Background()

	It("creates a single-session pod from the work order and follows it to Running", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-1"))).To(Succeed())
		r := runnerObj(ns, "order-1")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)

		pod := &corev1.Pod{}
		Eventually(func() error { return k8sClient.Get(ctx, key, pod) }, timeout, interval).Should(Succeed())
		Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
		Expect(pod.OwnerReferences[0].Kind).To(Equal("ClaudeRunner"))
		Expect(pod.Spec.Volumes[0].Secret.SecretName).To(Equal("order-1-work-order"))
		Expect(pod.Labels[selfhostedv1alpha1.LabelSessionID]).To(Equal("session_order-1"))
		Expect(pod.Spec.Containers[0].Args).To(ContainElements("--capacity", "1"))

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(func() []string {
			got := &selfhostedv1alpha1.ClaudeRunner{}
			_ = k8sClient.Get(ctx, key, got)
			return got.Finalizers
		}, timeout, interval).Should(ContainElement(selfhostedv1alpha1.RunnerFinalizer))

		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.PodName).To(Equal("order-1"))
		Expect(got.Status.StartedAt).NotTo(BeNil())
		Expect(got.Status.Conditions).To(ContainElement(HaveField("Type", selfhostedv1alpha1.ConditionRunnerReady)))
	})

	It("records Succeeded then garbage-collects the runner, pod and secret after the TTL", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.RunnerTTLSecondsAfterFinished = ptr.To[int32](1)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-2"))).To(Succeed())
		r := runnerObj(ns, "order-2")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())

		setPodPhase(ctx, key, corev1.PodSucceeded, &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.Now()})
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerSucceeded))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.FinishedAt).NotTo(BeNil())

		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &selfhostedv1alpha1.ClaudeRunner{})) }, 20*time.Second, interval).Should(BeTrue())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue())
	})

	It("records Failed with a redacted exit message", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-3"))).To(Succeed())
		r := runnerObj(ns, "order-3")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodFailed, &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.Now(),
			Message: "[runner:fatal] RegisterRunner auth failed for someone@example.com"})
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonPodFailed))
		Expect(got.Status.Message).To(ContainSubstring("exit code 1"))
		Expect(got.Status.Message).NotTo(ContainSubstring("example.com"))
	})

	It("marks a pod that never starts within expectedSpawnSeconds as SpawnTimeout and deletes it", func() {
		ns := newNamespace(ctx)
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 30
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-4"))).To(Succeed())
		r := runnerObj(ns, "order-4")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))

		nowFunc = func() time.Time { return time.Now().Add(2 * time.Minute) }
		DeferCleanup(func() { nowFunc = time.Now })
		Expect(k8sClient.Get(ctx, key, r)).To(Succeed())
		r.Annotations = map[string]string{"test/poke": "1"}
		Expect(k8sClient.Update(ctx, r)).To(Succeed())

		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonSpawnTimeout))
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue())
	})

	It("fails cleanly when the work-order Secret is missing", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		r := runnerObj(ns, "order-5")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonWorkOrderMissing))
		Consistently(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, 2*time.Second, interval).Should(BeTrue())
	})

	It("fails cleanly when the environment is missing", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-6"))).To(Succeed())
		r := runnerObj(ns, "order-6")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerFailed))
		got := &selfhostedv1alpha1.ClaudeRunner{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Reason).To(Equal(selfhostedv1alpha1.ReasonEnvironmentMissing))
	})

	It("deletes the pod before the runner object disappears", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-7"))).To(Succeed())
		r := runnerObj(ns, "order-7")
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		key := client.ObjectKeyFromObject(r)
		Eventually(func() error { return k8sClient.Get(ctx, key, &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		setPodPhase(ctx, key, corev1.PodRunning, nil)
		Eventually(runnerPhase(ctx, key), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerRunning))

		Expect(k8sClient.Delete(ctx, r)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &corev1.Pod{})) }, timeout, interval).Should(BeTrue(),
			fmt.Sprintf("pod %s should be deleted by the finalizer", key))
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &selfhostedv1alpha1.ClaudeRunner{})) }, timeout, interval).Should(BeTrue())
	})
})
```

- [ ] **Step 2: Run to verify they fail**

```bash
make test
```

Expected: compile error (`ClaudeRunnerReconciler` has no `Recorder`) or failing specs against the scaffolded no-op reconciler.

- [ ] **Step 3: Implement the reconciler**

Replace the scaffold body of `internal/controller/clauderunner_controller.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

const (
	defaultRunnerTTL    = 300 * time.Second
	podDeletionPollWait = 2 * time.Second
)

// ClaudeRunnerReconciler turns one ClaudeRunner into one single-session pod.
type ClaudeRunnerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners/finalizers,verbs=update
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=claudeenvironments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one ClaudeRunner: finalizer, pod, phase, spawn timeout, TTL.
func (r *ClaudeRunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	runner := &selfhostedv1alpha1.ClaudeRunner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !runner.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, runner)
	}
	if !controllerutil.ContainsFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer) {
		controllerutil.AddFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer)
		if err := r.Update(ctx, runner); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{Requeue: true}, nil //nolint:staticcheck // one mutation per pass; the update event also requeues
	}

	before := runner.Status.DeepCopy()
	res, err := r.reconcile(ctx, runner)
	runner.Status.ObservedGeneration = runner.Generation
	if !equality.Semantic.DeepEqual(before, &runner.Status) {
		if uerr := r.Status().Update(ctx, runner); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(uerr)
		}
	}
	return res, err
}

func (r *ClaudeRunnerReconciler) reconcile(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if runner.Status.Phase.IsTerminal() {
		return r.expire(ctx, runner)
	}

	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, types.NamespacedName{Name: runner.Spec.EnvironmentRef.Name, Namespace: runner.Namespace}, env); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(runner, selfhostedv1alpha1.ReasonEnvironmentMissing, fmt.Sprintf("ClaudeEnvironment %q not found", runner.Spec.EnvironmentRef.Name))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if err := r.Get(ctx, types.NamespacedName{Name: runner.Spec.WorkOrderSecretRef.Name, Namespace: runner.Namespace}, &corev1.Secret{}); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(runner, selfhostedv1alpha1.ReasonWorkOrderMissing, fmt.Sprintf("work-order Secret %q not found", runner.Spec.WorkOrderSecretRef.Name))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	pod, err := r.ensurePod(ctx, env, runner)
	if err != nil {
		return ctrl.Result{}, err
	}
	ph := derivePhase(pod)
	runner.Status.PodName = runner.Name
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = ph.Phase, ph.Reason, ph.Message
	if ph.StartedAt != nil {
		runner.Status.StartedAt = ph.StartedAt
	}

	switch ph.Phase {
	case selfhostedv1alpha1.RunnerPending:
		deadline := runner.CreationTimestamp.Add(time.Duration(spawnSeconds(env)) * time.Second)
		if now := nowFunc(); now.After(deadline) {
			msg := fmt.Sprintf("pod did not start within %d seconds (%s: %s)", spawnSeconds(env), ph.Reason, ph.Message)
			if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			r.fail(runner, selfhostedv1alpha1.ReasonSpawnTimeout, msg)
			return r.expire(ctx, runner)
		} else {
			r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
			return ctrl.Result{RequeueAfter: deadline.Sub(now) + time.Second}, nil
		}
	case selfhostedv1alpha1.RunnerRunning:
		r.setReady(runner, metav1.ConditionTrue, ph.Reason, "")
		return ctrl.Result{}, nil
	default: // terminal
		runner.Status.FinishedAt = ph.FinishedAt
		r.setReady(runner, metav1.ConditionFalse, ph.Reason, ph.Message)
		r.Recorder.Event(runner, corev1.EventTypeNormal, ph.Reason, "runner finished: "+string(ph.Phase))
		return r.expire(ctx, runner)
	}
}

// ensurePod returns the runner pod, creating it on first sight. Pods are
// immutable, so an existing pod is used as is rather than re-applied.
func (r *ClaudeRunnerReconciler) ensurePod(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, runner *selfhostedv1alpha1.ClaudeRunner) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKeyFromObject(runner), pod)
	if err == nil {
		return pod, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	pod = builders.OnDemandRunnerPod(env, runner, builders.ConfigHash(env, nil, nil))
	if err := controllerutil.SetControllerReference(runner, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	r.Recorder.Event(runner, corev1.EventTypeNormal, "PodCreated", "created runner pod "+pod.Name)
	return pod, nil
}

// expire deletes a terminal runner once its TTL has elapsed, or requeues for it.
func (r *ClaudeRunnerReconciler) expire(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if runner.Status.FinishedAt == nil {
		now := metav1.NewTime(nowFunc())
		runner.Status.FinishedAt = &now
	}
	ttl := defaultRunnerTTL
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := r.Get(ctx, types.NamespacedName{Name: runner.Spec.EnvironmentRef.Name, Namespace: runner.Namespace}, env); err == nil &&
		env.Spec.OnDemand != nil && env.Spec.OnDemand.RunnerTTLSecondsAfterFinished != nil {
		ttl = time.Duration(*env.Spec.OnDemand.RunnerTTLSecondsAfterFinished) * time.Second
	}
	remaining := runner.Status.FinishedAt.Add(ttl).Sub(nowFunc())
	if remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, runner))
}

// finalize removes the pod and waits for it to be gone before releasing the finalizer.
func (r *ClaudeRunnerReconciler) finalize(ctx context.Context, runner *selfhostedv1alpha1.ClaudeRunner) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer) {
		return ctrl.Result{}, nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: runner.Name, Namespace: runner.Namespace}}
	if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err == nil {
		return ctrl.Result{RequeueAfter: podDeletionPollWait}, nil
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(runner, selfhostedv1alpha1.RunnerFinalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, runner))
}

func (r *ClaudeRunnerReconciler) fail(runner *selfhostedv1alpha1.ClaudeRunner, reason, msg string) {
	runner.Status.Phase, runner.Status.Reason, runner.Status.Message = selfhostedv1alpha1.RunnerFailed, reason, msg
	if runner.Status.FinishedAt == nil {
		now := metav1.NewTime(nowFunc())
		runner.Status.FinishedAt = &now
	}
	r.setReady(runner, metav1.ConditionFalse, reason, msg)
	r.Recorder.Event(runner, corev1.EventTypeWarning, reason, msg)
}

func (r *ClaudeRunnerReconciler) setReady(runner *selfhostedv1alpha1.ClaudeRunner, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&runner.Status.Conditions, metav1.Condition{Type: selfhostedv1alpha1.ConditionRunnerReady,
		Status: status, Reason: reason, Message: msg, ObservedGeneration: runner.Generation})
}

func spawnSeconds(env *selfhostedv1alpha1.ClaudeEnvironment) int32 {
	if env.Spec.OnDemand != nil && env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds > 0 {
		return env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds
	}
	return 120
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClaudeRunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfhostedv1alpha1.ClaudeRunner{}).
		Owns(&corev1.Pod{}).
		Named("clauderunner").
		Complete(r)
}
```

If the linter rejects `Requeue: true`, use `RequeueAfter: time.Second` as the environment controller does. `fail` after `expire` on the spawn-timeout path: keep the `else` branch as written so both paths return; if `gocritic`/`revive` complain about the `else` after `return`, restructure into two `if`s with identical behaviour.

- [ ] **Step 4: Run and verify**

```bash
make manifests && make test && make lint
```

Expected: all specs pass including the seven new ones; `config/rbac/role.yaml` gains the clauderunners rules and pod create/delete.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(controller): ClaudeRunner controller with finalizer, spawn timeout and TTL

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: ClaudeEnvironment on-demand branch

**Files:**
- Create: `internal/builders/orchestratorhash.go` (+ test in `orchestrator_test.go`)
- Modify: `internal/controller/claudeenvironment_controller.go` (`HookImage` field, `reconcileOnDemand`, fixed-branch cleanup of orchestrator objects, `configMapRefs` fix, RBAC markers, watches, Normal event on FleetAvailable flip)
- Modify: `internal/controller/suite_test.go` (`envReconciler.HookImage = "example.com/claude-selfhosted-operator:test"`)
- Test: `internal/controller/claudeenvironment_ondemand_test.go`

**Interfaces:**
- Produces: `builders.OrchestratorConfigHash(env, secret, hookImage) string`; `ClaudeEnvironmentReconciler.HookImage string`; `reconcileOnDemand`.

- [ ] **Step 1: Add the hash builder with its test**

Append to `internal/builders/orchestrator_test.go`:

```go
func TestOrchestratorConfigHash(t *testing.T) {
	env := onDemandEnv()
	sec := &corev1.Secret{Data: map[string][]byte{"environment-secret": []byte("k1")}}
	base := OrchestratorConfigHash(env, sec, "op:1")
	if len(base) != 16 || OrchestratorConfigHash(env, sec, "op:1") != base {
		t.Fatal("hash must be 16 hex chars and deterministic")
	}
	e2 := env.DeepCopy()
	e2.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 300
	if OrchestratorConfigHash(e2, sec, "op:1") == base {
		t.Fatal("orchestrator settings must change the hash")
	}
	if OrchestratorConfigHash(env, sec, "op:2") == base {
		t.Fatal("hook image must change the hash")
	}
	s2 := sec.DeepCopy()
	s2.Data["environment-secret"] = []byte("k2")
	if OrchestratorConfigHash(env, s2, "op:1") == base {
		t.Fatal("secret rotation must change the hash")
	}
	e3 := env.DeepCopy()
	e3.Spec.OnDemand.Orchestrator.Replicas = ptr.To[int32](3)
	if OrchestratorConfigHash(e3, sec, "op:1") != base {
		t.Fatal("replicas must not change the hash")
	}
}
```

`internal/builders/orchestratorhash.go`:

```go
package builders

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// OrchestratorConfigHash digests what must roll orchestrator pods: the
// orchestrator settings except replicas, the runner image (the default
// orchestrator image), the hook image, the concurrency cap and the
// environment secret.
func OrchestratorConfigHash(env *selfhostedv1alpha1.ClaudeEnvironment, secret *corev1.Secret, hookImage string) string {
	o := env.Spec.OnDemand.Orchestrator.DeepCopy()
	o.Replicas = nil
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(o)
	writeField(h, []byte(env.Spec.Runner.Image))
	writeField(h, []byte(hookImage))
	_ = enc.Encode(env.Spec.OnDemand.MaxConcurrentRunners)
	if secret != nil {
		_, _ = h.Write([]byte{1})
		writeSortedBytes(h, secret.Data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
```

Run `go test ./internal/builders/... -run TestOrchestratorConfigHash -v` to green.

- [ ] **Step 2: Write the failing envtest cases**

`internal/controller/claudeenvironment_ondemand_test.go`:

```go
package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func markDeploymentAvailable(ctx context.Context, key types.NamespacedName) {
	dep := &appsv1.Deployment{}
	EventuallyWithOffset(1, func() error { return k8sClient.Get(ctx, key, dep) }, timeout, interval).Should(Succeed())
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, dep.Status.UpdatedReplicas = replicas, replicas, replicas, replicas
	dep.Status.ObservedGeneration = dep.Generation
	dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, dep)).To(Succeed())
}

var _ = Describe("ClaudeEnvironment on-demand mode", func() {
	ctx := context.Background()

	It("creates the orchestrator with its RBAC and reports Ready once available", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.Replicas = ptr.To[int32](2)
		env.Spec.OnDemand.MaxConcurrentRunners = 3
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		orch := types.NamespacedName{Name: "platform-orchestrator", Namespace: ns}

		dep := &appsv1.Deployment{}
		Eventually(func() error { return k8sClient.Get(ctx, orch, dep) }, timeout, interval).Should(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		Expect(dep.Spec.Template.Spec.InitContainers[0].Image).To(Equal("example.com/claude-selfhosted-operator:test"))
		Expect(dep.Spec.Template.Spec.Containers[0].Args[:2]).To(Equal([]string{"self-hosted-runner", "orchestrator"}))
		Expect(dep.Spec.Template.Spec.ServiceAccountName).To(Equal("platform-orchestrator"))
		Expect(dep.OwnerReferences[0].Name).To(Equal("platform"))
		Expect(k8sClient.Get(ctx, orch, &corev1.ServiceAccount{})).To(Succeed())
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, orch, role)).To(Succeed())
		Expect(role.Rules[1].Verbs).To(ContainElement("list"))
		Expect(k8sClient.Get(ctx, orch, &rbacv1.RoleBinding{})).To(Succeed())

		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(haveReason(metav1.ConditionFalse, selfhostedv1alpha1.ReasonOnDemandSecretOnOrchestrator))
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))

		markDeploymentAvailable(ctx, orch)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionReady), timeout, interval).Should(HaveField("Status", metav1.ConditionTrue))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Mode).To(Equal("onDemand"))
		Expect(got.Status.OnDemand.OrchestratorReadyReplicas).To(Equal(int32(2)))
	})

	It("degrades with HookImageUnset when the operator has no hook image", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		saved := envReconciler.HookImage
		envReconciler.HookImage = ""
		DeferCleanup(func() { envReconciler.HookImage = saved })
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonHookImageUnset))
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "platform-orchestrator", Namespace: ns}, &appsv1.Deployment{}))
		}, 2*time.Second, interval).Should(BeTrue())
	})

	It("counts runners by phase", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())
		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())

		// Phases are driven through pod status so the ClaudeRunner controller
		// derives them itself: c1 stays Pending, c2 and c3 run, c4 finishes.
		for _, name := range []string{"c1", "c2", "c3", "c4"} {
			Expect(k8sClient.Create(ctx, workOrderSecret(ns, name))).To(Succeed())
			r := runnerObj(ns, name)
			r.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(env, selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))}
			Expect(k8sClient.Create(ctx, r)).To(Succeed())
			Eventually(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(r), &corev1.Pod{}) }, timeout, interval).Should(Succeed())
		}
		setPodPhase(ctx, types.NamespacedName{Name: "c2", Namespace: ns}, corev1.PodRunning, nil)
		setPodPhase(ctx, types.NamespacedName{Name: "c3", Namespace: ns}, corev1.PodRunning, nil)
		setPodPhase(ctx, types.NamespacedName{Name: "c4", Namespace: ns}, corev1.PodSucceeded, &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.Now()})

		Eventually(func() (int32, error) {
			got := &selfhostedv1alpha1.ClaudeEnvironment{}
			if err := k8sClient.Get(ctx, key, got); err != nil || got.Status.OnDemand == nil {
				return -1, err
			}
			return got.Status.OnDemand.RunningRunners, nil
		}, timeout, interval).Should(Equal(int32(2)))
		got := &selfhostedv1alpha1.ClaudeEnvironment{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.OnDemand.PendingRunners).To(Equal(int32(1)))
	})

	It("switches fixed to onDemand and back, cleaning up each mode's objects", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := fixedEnv(ns)
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		key := client.ObjectKeyFromObject(env)
		fixedKey := types.NamespacedName{Name: "platform-runner", Namespace: ns}
		orch := types.NamespacedName{Name: "platform-orchestrator", Namespace: ns}
		Eventually(func() error { return k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.Fixed = nil
		env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() error { return k8sClient.Get(ctx, orch, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{})) }, timeout, interval).Should(BeTrue())
		Eventually(condition(ctx, key, selfhostedv1alpha1.ConditionSecretOnRunners), timeout, interval).Should(HaveField("Status", metav1.ConditionFalse))

		Expect(k8sClient.Get(ctx, key, env)).To(Succeed())
		env.Spec.OnDemand = nil
		env.Spec.Fixed = &selfhostedv1alpha1.FixedFleetSpec{Replicas: ptr.To[int32](1)}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() error { return k8sClient.Get(ctx, fixedKey, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())
		for _, obj := range []client.Object{&appsv1.Deployment{}, &rbacv1.RoleBinding{}, &rbacv1.Role{}, &corev1.ServiceAccount{}} {
			Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, orch, obj)) }, timeout, interval).Should(BeTrue())
		}
	})

	It("checks the wrapper key even when the wrapper and host config share a ConfigMap", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: ns}, Data: map[string]string{"settings.json": "{}"}})).To(Succeed())
		env := fixedEnv(ns)
		env.Spec.Runner.HostConfig = &selfhostedv1alpha1.ConfigMapRef{Name: "shared"}
		env.Spec.Runner.WrapperScript = &selfhostedv1alpha1.ConfigMapKeyRef{Name: "shared", Key: "wrap.sh"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(condition(ctx, client.ObjectKeyFromObject(env), selfhostedv1alpha1.ConditionDegraded), timeout, interval).Should(haveReason(metav1.ConditionTrue, selfhostedv1alpha1.ReasonConfigMapMissing))
	})
})
```

In `suite_test.go`, after constructing `envReconciler`, set `envReconciler.HookImage = "example.com/claude-selfhosted-operator:test"`.

- [ ] **Step 3: Run to verify they fail**

```bash
make test
```

Expected: compile error on `HookImage`, then failing on-demand specs.

- [ ] **Step 4: Implement**

In `claudeenvironment_controller.go`:

1. Add the field and the RBAC markers:

```go
type ClaudeEnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// HookImage is the operator's own image, used by the init container that
	// installs the spawn-runner hook into orchestrator pods.
	HookImage string
}

// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=selfhosted.claudecode.dev,resources=clauderunners,verbs=get;list;watch
```

Replace the existing `secrets;configmaps` marker with `configmaps` only plus the secrets line above (the manager must itself hold `create` and `patch` on Secrets to be allowed to grant them in the orchestrator Role; Kubernetes rejects Role creation that escalates beyond the creator's permissions).

2. Replace the `if env.Spec.OnDemand != nil { ... }` block in `reconcile` with:

```go
	if env.Spec.OnDemand != nil {
		env.Status.Mode = "onDemand"
		env.Status.Fixed = nil
		return r.reconcileOnDemand(ctx, env, pass, secret)
	}
```

3. Add `reconcileOnDemand` and helpers:

```go
func (r *ClaudeEnvironmentReconciler) reconcileOnDemand(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment, pass *statusPass, secret *corev1.Secret) (ctrl.Result, error) {
	pass.set(selfhostedv1alpha1.ConditionSecretOnRunners, metav1.ConditionFalse, selfhostedv1alpha1.ReasonOnDemandSecretOnOrchestrator,
		"the environment secret is mounted only on the orchestrator; runners receive single-use work orders")

	name := builders.FixedWorkloadName(env)
	for _, stale := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace}},
	} {
		if err := r.deleteIfOwned(ctx, env, stale); err != nil {
			return ctrl.Result{}, err
		}
	}

	if r.HookImage == "" {
		msg := "operator has no hook image configured; set --hook-image or OPERATOR_IMAGE on the manager"
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonHookImageUnset, msg)
		pass.degrade(selfhostedv1alpha1.ReasonHookImageUnset, msg)
		r.Recorder.Event(env, corev1.EventTypeWarning, selfhostedv1alpha1.ReasonHookImageUnset, msg)
		return ctrl.Result{RequeueAfter: requeueAfterUserFix}, nil
	}

	for _, obj := range []client.Object{
		builders.OrchestratorServiceAccount(env), builders.OrchestratorRole(env), builders.OrchestratorRoleBinding(env),
	} {
		if err := r.apply(ctx, env, obj); err != nil {
			return ctrl.Result{}, r.applyFailed(env, pass, obj.GetObjectKind().GroupVersionKind().Kind, err)
		}
	}
	dep := builders.OrchestratorDeployment(env, builders.OrchestratorConfigHash(env, secret, r.HookImage), r.HookImage)
	if err := r.apply(ctx, env, dep); err != nil {
		return ctrl.Result{}, r.applyFailed(env, pass, "Deployment", err)
	}

	available := false
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			available = dep.Status.ObservedGeneration == dep.Generation
		}
	}
	if available {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionTrue, selfhostedv1alpha1.ReasonWorkloadAvailable, "")
	} else {
		pass.set(selfhostedv1alpha1.ConditionFleetAvailable, metav1.ConditionFalse, selfhostedv1alpha1.ReasonOrchestratorUnavailable, "orchestrator deployment is not yet available")
	}

	counts, err := r.countRunners(ctx, env)
	if err != nil {
		return ctrl.Result{}, err
	}
	counts.OrchestratorReadyReplicas = dep.Status.ReadyReplicas
	env.Status.OnDemand = &counts
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}

func (r *ClaudeEnvironmentReconciler) countRunners(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) (selfhostedv1alpha1.OnDemandStatus, error) {
	list := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := r.List(ctx, list, client.InNamespace(env.Namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}); err != nil {
		return selfhostedv1alpha1.OnDemandStatus{}, err
	}
	var st selfhostedv1alpha1.OnDemandStatus
	for i := range list.Items {
		switch list.Items[i].Status.Phase {
		case selfhostedv1alpha1.RunnerRunning:
			st.RunningRunners++
		case "", selfhostedv1alpha1.RunnerPending:
			st.PendingRunners++
		}
	}
	return st, nil
}

// deleteOrchestratorObjects removes on-demand objects after a switch to fixed mode.
func (r *ClaudeEnvironmentReconciler) deleteOrchestratorObjects(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	name := builders.OrchestratorName(env)
	meta := metav1.ObjectMeta{Name: name, Namespace: env.Namespace}
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: meta}, &rbacv1.RoleBinding{ObjectMeta: meta}, &rbacv1.Role{ObjectMeta: meta}, &corev1.ServiceAccount{ObjectMeta: meta},
	} {
		if err := r.deleteIfOwned(ctx, env, obj); err != nil {
			return err
		}
	}
	return nil
}
```

Note `applyFailed`'s kind argument: the builders set `TypeMeta`, so `obj.GetObjectKind().GroupVersionKind().Kind` is populated before apply.

4. In `reconcileFixed`, right after the `if env.Spec.Fixed.PersistentWorkspace != nil { ... } else { ... }` block and before the `if available {` block, add:

```go
	if err := r.deleteOrchestratorObjects(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
```

Also set `env.Status.OnDemand = nil` there is already done in `reconcile`.

5. Fix the shared-ConfigMap wrapper-key check in `configMapRefs`: build the map so a non-empty required key is never overwritten by a later empty one:

```go
func configMapRefs(r selfhostedv1alpha1.RunnerSpec) map[string]string {
	refs := map[string]string{}
	add := func(name, key string) {
		if existing, ok := refs[name]; ok && existing != "" {
			return
		}
		refs[name] = key
	}
	if r.LifecycleHooks != nil {
		add(r.LifecycleHooks.Name, "")
	}
	if r.WrapperScript != nil {
		add(r.WrapperScript.Name, r.WrapperScript.Key)
	}
	if r.HostConfig != nil {
		add(r.HostConfig.Name, "")
	}
	return refs
}
```

6. Normal event when the fleet becomes available: in `Reconcile`, after `pass.finish()` and before the status write, add:

```go
	if !meta.IsStatusConditionTrue(before.Conditions, selfhostedv1alpha1.ConditionFleetAvailable) &&
		meta.IsStatusConditionTrue(env.Status.Conditions, selfhostedv1alpha1.ConditionFleetAvailable) {
		r.Recorder.Event(env, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonFleetAvailable, "runner fleet is available")
	}
```

with `"k8s.io/apimachinery/pkg/api/meta"` imported.

7. Watches in `SetupWithManager`: add `Owns(&corev1.ServiceAccount{})`, `Owns(&rbacv1.Role{})`, `Owns(&rbacv1.RoleBinding{})`, `Owns(&selfhostedv1alpha1.ClaudeRunner{})` after the existing `Owns` calls; import `rbacv1 "k8s.io/api/rbac/v1"`.

- [ ] **Step 5: Run and verify**

```bash
make manifests && make test && make lint
```

Expected: `config/rbac/role.yaml` gains serviceaccounts, roles, rolebindings, secrets create/patch; all specs pass.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(controller): on-demand mode runs the orchestrator with injected hook and RBAC

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 9: Manager wiring, hook image discovery, and on-demand metrics

**Files:**
- Modify: `cmd/main.go` (`--hook-image` flag, `HookImage` on the environment reconciler, `Recorder` on the runner reconciler)
- Modify: `config/manager/manager.yaml` (`OPERATOR_IMAGE` env on the manager container)
- Modify: `config/default/kustomization.yaml` (`replacements` block)
- Modify: `internal/metrics/metrics.go`, `internal/controller/clauderunner_controller.go` (metric calls)
- Test: `internal/metrics/metrics_test.go` (append), `cmd/options_test.go` (append)

**Interfaces:**
- Produces: flag `--hook-image` (default `os.Getenv("OPERATOR_IMAGE")`); metrics `claude_operator_environment_runners{namespace,environment,phase}` (phase `pending|running`), `claude_operator_runner_spawn_duration_seconds{namespace,environment}` histogram, `claude_operator_runners_total{namespace,environment,outcome}` counter (outcome `created|succeeded|failed|spawn_timeout`); functions `metrics.ObserveSpawn(namespace, environment string, d time.Duration)` and `metrics.CountRunner(namespace, environment, outcome string)`.
- Ruling recorded here: the spec's `claude_operator_spawn_hook_results_total` is replaced by `claude_operator_runners_total`, because retryable and non-retryable hook outcomes never produce a Kubernetes object the controller could observe; the orchestrator's own `claude_code_self_hosted_orchestrator_spawn_hooks_total` already covers hook outcomes.

- [ ] **Step 1: Write the failing tests**

Append to `internal/metrics/metrics_test.go`:

```go
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
```

Append to `cmd/options_test.go`:

```go
func TestHookImageDefault(t *testing.T) {
	t.Setenv("OPERATOR_IMAGE", "ghcr.io/x/op:1")
	if got := defaultHookImage(); got != "ghcr.io/x/op:1" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("OPERATOR_IMAGE", "")
	if got := defaultHookImage(); got != "" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/metrics/... ./cmd/...
```

- [ ] **Step 3: Implement**

`internal/metrics/metrics.go`: add

```go
const (
	labelPhase   = "phase"
	labelOutcome = "outcome"
	labelState   = "state"

	OutcomeCreated      = "created"
	OutcomeSucceeded    = "succeeded"
	OutcomeFailed       = "failed"
	OutcomeSpawnTimeout = "spawn_timeout"
)

var (
	environmentRunners = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claude_operator_environment_runners",
		Help: "On-demand runners per environment by phase (pending, running).",
	}, []string{labelNamespace, labelEnvironment, labelPhase})
	runnerSpawnDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "claude_operator_runner_spawn_duration_seconds",
		Help:    "Seconds from ClaudeRunner creation to the runner pod reaching Running.",
		Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600},
	}, []string{labelNamespace, labelEnvironment})
	runnersTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "claude_operator_runners_total",
		Help: "On-demand runner lifecycle events by outcome (created, succeeded, failed, spawn_timeout).",
	}, []string{labelNamespace, labelEnvironment, labelOutcome})
)
```

register them in `init()`, replace the `"state"` literals with `labelState`, and in `RecordEnvironment` add:

```go
	if env.Spec.OnDemand != nil && env.Status.OnDemand != nil {
		environmentRunners.WithLabelValues(ns, name, "pending").Set(float64(env.Status.OnDemand.PendingRunners))
		environmentRunners.WithLabelValues(ns, name, "running").Set(float64(env.Status.OnDemand.RunningRunners))
	} else {
		environmentRunners.DeletePartialMatch(prometheus.Labels{labelNamespace: ns, labelEnvironment: name})
	}
```

plus:

```go
// ObserveSpawn records how long a runner took from creation to Running.
func ObserveSpawn(namespace, environment string, d time.Duration) {
	runnerSpawnDuration.WithLabelValues(namespace, environment).Observe(d.Seconds())
}

// CountRunner increments the runner lifecycle counter.
func CountRunner(namespace, environment, outcome string) {
	runnersTotal.WithLabelValues(namespace, environment, outcome).Inc()
}
```

and in `ForgetEnvironment` delete the three new vecs by partial match too.

`internal/controller/clauderunner_controller.go`: in `ensurePod`, after a successful `Create`, call `metrics.CountRunner(runner.Namespace, runner.Spec.EnvironmentRef.Name, metrics.OutcomeCreated)`. In `reconcile`, capture `wasRunning := runner.Status.Phase == selfhostedv1alpha1.RunnerRunning` before deriving the phase; in the Running case, if `!wasRunning` call `metrics.ObserveSpawn(runner.Namespace, runner.Spec.EnvironmentRef.Name, nowFunc().Sub(runner.CreationTimestamp.Time))`. In the terminal default case call `CountRunner` with `OutcomeSucceeded` or `OutcomeFailed` by phase; in the spawn-timeout branch call it with `OutcomeSpawnTimeout`.

`cmd/options.go`: add

```go
func defaultHookImage() string { return os.Getenv("OPERATOR_IMAGE") }
```

`cmd/main.go`: add `flag.StringVar(&hookImage, "hook-image", defaultHookImage(), "Image that carries /spawn-runner for orchestrator pods. Defaults to $OPERATOR_IMAGE.")`, pass `HookImage: hookImage` to the `ClaudeEnvironmentReconciler`, and add `Recorder: mgr.GetEventRecorderFor("claude-selfhosted-operator")` with the same nolint line to the scaffolded `ClaudeRunnerReconciler` registration. Log `hookImage` in the startup Info line.

`config/manager/manager.yaml`: on the manager container add

```yaml
          env:
            - name: OPERATOR_IMAGE
              value: controller:latest
```

`config/default/kustomization.yaml`: append

```yaml
replacements:
  - source:
      kind: Deployment
      name: controller-manager
      fieldPath: spec.template.spec.containers.[name=manager].image
    targets:
      - select:
          kind: Deployment
          name: controller-manager
        fieldPaths:
          - spec.template.spec.containers.[name=manager].env.[name=OPERATOR_IMAGE].value
```

- [ ] **Step 4: Verify**

```bash
go test ./internal/metrics/... ./cmd/... -v && make test && make lint
make build-installer IMG=example.com/op:v9 && grep -A1 'name: OPERATOR_IMAGE' dist/install.yaml
git checkout -- dist 2>/dev/null; git status --short
```

Expected: the grep shows `value: example.com/op:v9` (the replacement follows the `images:` transformer); revert any generated `dist/` change the scaffold tracks, or leave `dist/` untracked as the scaffold's `.gitignore` dictates.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(cmd,metrics): hook image discovery and on-demand runner metrics

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 10: Optional OpenTelemetry tracing

**Files:**
- Create: `internal/telemetry/telemetry.go`
- Modify: `cmd/main.go`, `cmd/spawn-runner/main.go`, `internal/controller/claudeenvironment_controller.go`, `internal/controller/clauderunner_controller.go`, `internal/controller/suite_test.go`
- Test: `internal/telemetry/telemetry_test.go`, append one case to `internal/controller/clauderunner_controller_test.go`

**Interfaces:**
- Produces: `telemetry.Init(ctx, endpoint, service string, sampleRatio float64) (shutdown func(context.Context) error, err error)` (empty endpoint: no exporter, no-op shutdown), `telemetry.InstallExporter(exp sdktrace.SpanExporter)` for tests, `telemetry.StartSpan(ctx, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span)`, `telemetry.TraceparentFrom(ctx) string`, `telemetry.ContextWithTraceparent(ctx, tp string) context.Context`; flags `--tracing-endpoint` and `--tracing-sample-ratio` (default 0.1) on the manager; the hook reads `OTEL_EXPORTER_OTLP_ENDPOINT`.

- [ ] **Step 1: Add dependencies and write the failing tests**

```bash
go get go.opentelemetry.io/otel@latest go.opentelemetry.io/otel/sdk@latest go.opentelemetry.io/otel/trace@latest go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@latest
```

`internal/telemetry/telemetry_test.go`:

```go
package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestInitWithoutEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), "", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tp := TraceparentFrom(context.Background()); tp != "" {
		t.Fatalf("no span means no traceparent, got %q", tp)
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	InstallExporter(exp)

	ctx, hookSpan := StartSpan(context.Background(), "spawn-runner.run", attribute.String("orderID", "o1"))
	tp := TraceparentFrom(ctx)
	hookSpan.End()
	if tp == "" {
		t.Fatal("expected a traceparent from an active span")
	}

	ctx2 := ContextWithTraceparent(context.Background(), tp)
	_, ctrlSpan := StartSpan(ctx2, "clauderunner.reconcile")
	ctrlSpan.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	if spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatal("controller span must continue the hook's trace")
	}
	if spans[1].Parent.SpanID() != spans[0].SpanContext.SpanID() {
		t.Fatal("controller span must be a child of the hook span")
	}
	if ContextWithTraceparent(context.Background(), "garbage") == nil {
		t.Fatal("invalid traceparent must still return a context")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/telemetry/...
```

- [ ] **Step 3: Implement**

`internal/telemetry/telemetry.go`:

```go
// Package telemetry wires optional OpenTelemetry tracing. With no endpoint it
// installs nothing and every call is a cheap no-op.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	ServiceManager = "claude-selfhosted-operator"
	ServiceHook    = "spawn-runner"
	tracerName     = "github.com/AhmadMasry/claude-self-hosted-environment-operator"
	traceparentKey = "traceparent"
)

func init() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

// Init configures an OTLP gRPC exporter when endpoint is non-empty. The
// sampler is parent-based so a span started by the hook decides for the
// whole spawn trace; sampleRatio applies to root spans only.
func Init(ctx context.Context, endpoint, service string, sampleRatio float64) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// InstallExporter installs a synchronous, always-sampling provider for tests.
func InstallExporter(exp sdktrace.SpanExporter) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithSampler(sdktrace.AlwaysSample())))
}

// StartSpan starts a span on the global tracer.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

// TraceparentFrom renders the W3C traceparent of the active span, or "".
func TraceparentFrom(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier[traceparentKey]
}

// ContextWithTraceparent continues the trace a traceparent names. An empty or
// invalid value returns ctx unchanged.
func ContextWithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{traceparentKey: tp})
}
```

If `semconv/v1.26.0` is not in the installed SDK version, use the semconv version the `go.opentelemetry.io/otel` module ships (list `$(go env GOMODCACHE)/go.opentelemetry.io/otel@*/semconv/`).

`cmd/main.go`: add flags `--tracing-endpoint` (default `os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")`, help "OTLP gRPC endpoint for traces; empty disables tracing") and `--tracing-sample-ratio` (float64, default 0.1). After the logger is set up: `shutdown, err := telemetry.Init(ctx, tracingEndpoint, telemetry.ServiceManager, tracingSampleRatio)`; on error log and exit 1; `defer shutdown(context.Background())` with a 5-second timeout context. Use `ctrl.SetupSignalHandler()`'s context, which the scaffold already creates for `mgr.Start`.

`cmd/spawn-runner/main.go`: in `runHook`, after building `ctx`: `shutdown, err := telemetry.Init(ctx, os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), telemetry.ServiceHook, 1)`; on error print to stderr and continue without tracing; `ctx, span := telemetry.StartSpan(ctx, "spawn-runner.run", attribute.String("order_id", in.OrderID), attribute.String("session_id", in.SessionID))`; pass `telemetry.TraceparentFrom(ctx)` to `hook.Run`; `span.SetAttributes(attribute.String("outcome", res.Outcome), attribute.Int("exit_code", res.ExitCode))`; `span.End()`; `shutdown` with a 2-second timeout before returning.

`internal/controller/claudeenvironment_controller.go` `Reconcile`, first lines:

```go
	ctx, span := telemetry.StartSpan(ctx, "claudeenvironment.reconcile",
		attribute.String("k8s.namespace.name", req.Namespace), attribute.String("environment", req.Name))
	defer span.End()
```

`internal/controller/clauderunner_controller.go` `Reconcile`, after the `Get`:

```go
	ctx = telemetry.ContextWithTraceparent(ctx, runner.Annotations[selfhostedv1alpha1.AnnotationTraceparent])
	ctx, span := telemetry.StartSpan(ctx, "clauderunner.reconcile",
		attribute.String("k8s.namespace.name", req.Namespace), attribute.String("order_id", runner.Spec.OrderID))
	defer span.End()
```

`suite_test.go`: declare `var spanExporter = tracetest.NewInMemoryExporter()` and call `telemetry.InstallExporter(spanExporter)` in `BeforeSuite` before the manager starts.

Append to `clauderunner_controller_test.go`:

```go
	It("continues the trace the hook started", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, onDemandEnvObj(ns))).To(Succeed())
		Expect(k8sClient.Create(ctx, workOrderSecret(ns, "order-8"))).To(Succeed())
		r := runnerObj(ns, "order-8")
		r.Annotations = map[string]string{selfhostedv1alpha1.AnnotationTraceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		Eventually(runnerPhase(ctx, client.ObjectKeyFromObject(r)), timeout, interval).Should(Equal(selfhostedv1alpha1.RunnerPending))
		Eventually(func() bool {
			for _, s := range spanExporter.GetSpans() {
				if s.Name == "clauderunner.reconcile" && s.SpanContext.TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})
```

- [ ] **Step 4: Verify**

```bash
go mod tidy && go test ./internal/telemetry/... -v && make build && make test && make lint
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(telemetry): optional OTLP tracing from hook to ClaudeRunner reconcile

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 11: Samples and documentation for on-demand mode

**Files:**
- Create: `examples/on-demand.yaml`, `docs/on-demand.md`
- Delete: `config/samples/selfhosted_v1alpha1_clauderunner.yaml` (scaffolded; ClaudeRunners are never user-created) and its entry in `config/samples/kustomization.yaml`
- Modify: `README.md`
- Test: `api/v1alpha1/samples_test.go` (covers the new example automatically)

- [ ] **Step 1: Write the sample**

`examples/on-demand.yaml`:

```yaml
# On-demand mode: the orchestrator holds the environment secret and spawns one
# runner pod per session with a single-use work order. Requires the Secret
# from examples/fixed-fleet.yaml's comment and the namespace from examples/namespace.yaml.
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
      exitIfUnusedMinutes: 15      # standby runners from minIdle reclaim themselves
    lifecycleHooks:
      name: runner-hooks
    env:
      # Keeps the Artifact tool off in sessions and drops the *.frame.claudeusercontent.com egress requirement.
      - name: CLAUDE_CODE_DISABLE_ARTIFACT
        value: "1"
    podTemplate:
      resources:
        requests: {cpu: "1", memory: 4Gi}
        limits: {memory: 8Gi}
  onDemand:
    orchestrator:
      replicas: 2
      expectedSpawnSeconds: 120
      hookTimeoutSeconds: 60
      minIdle: 0
      podTemplate:
        resources:
          requests: {cpu: 100m, memory: 256Mi}
          limits: {memory: 512Mi}
    runnerTTLSecondsAfterFinished: 300
    maxConcurrentRunners: 50
```

Run `go test ./api/... -run TestSamplesDecodeStrictly -v`; it must pass with the new file.

- [ ] **Step 2: Write `docs/on-demand.md`**

Cover, in this order, each in a few sentences with the exact names: what the operator creates for an on-demand environment (`<env>-orchestrator` ServiceAccount, Role, RoleBinding, Deployment); the init container that installs `/spawn-runner` from the operator image and why the manager needs `--hook-image` or `OPERATOR_IMAGE`; the orchestrator readiness probe on `"connected":true`; the hook contract as implemented (order-ID idempotency, exit codes 0/1/2, 45-second deadline, the `CLAUDE_OPERATOR_*` variables, no email stored); the `ClaudeRunner` lifecycle (`Pending`, `Running`, `Succeeded`, `Failed` with reasons `PodPending`, `PodRunning`, `PodSucceeded`, `PodFailed`, `SpawnTimeout`, `WorkOrderMissing`, `EnvironmentMissing`), the finalizer, `runnerTTLSecondsAfterFinished`; `maxConcurrentRunners` and the `list` permission it adds; the manager permissions the orchestrator Role requires (`create`/`patch` on Secrets); metrics (`claude_operator_environment_runners`, `claude_operator_runner_spawn_duration_seconds`, `claude_operator_runners_total`); tracing (`--tracing-endpoint`, `--tracing-sample-ratio`, `OTEL_EXPORTER_OTLP_ENDPOINT` on the orchestrator via `onDemand.orchestrator.env`, the `traceparent` annotation); and `kubectl get crun -A` columns. Under 150 lines.

- [ ] **Step 3: Update the README**

Replace the sentence that on-demand mode is coming with a short on-demand section pointing at `examples/on-demand.yaml` and `docs/on-demand.md`, add `--hook-image` / `OPERATOR_IMAGE` to the install step (the kustomize manifests set it automatically from `IMG`), and extend the conditions table with `WorkloadApplyFailed`, `HookImageUnset`, `OrchestratorUnavailable`, and `SecretOnRunners=False/OnDemandSecretOnOrchestrator`. Keep the README under 140 lines.

- [ ] **Step 4: Verify and commit**

```bash
make test && make lint
git add -A
git commit -m "docs: on-demand mode sample and guide

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 12: Stub orchestrator and on-demand kind e2e

**Files:**
- Modify: `test/stubrunner/main.go` (orchestrator mode, `STUB_EXIT_AFTER_SECONDS`)
- Create: `test/e2e/testdata/on-demand-stub.yaml`, `test/e2e/on_demand_test.go`
- Modify: `test/e2e/e2e_test.go` (call `onDemandSpecs()` after `fixedFleetSpecs()`)

**Interfaces:**
- Consumes: the stub image is both the runner and the orchestrator image; `fixedFleetSpecs()` already creates namespace `claude-e2e`, the Secret, and the hooks/wrapper ConfigMaps in its `BeforeAll`, and its `AfterAll` deletes the namespace, so the on-demand Context must run inside the same Manager Describe and create its own namespace `claude-e2e-od` with the same Restricted labels and its own Secret.

- [ ] **Step 1: Extend the stub**

In `test/stubrunner/main.go` add orchestrator mode. Behaviour, exactly:

- Mode detection: `len(os.Args) > 2 && os.Args[1] == "self-hosted-runner" && os.Args[2] == "orchestrator"`.
- Orchestrator mode: run the existing `checkMounts` on the secret file only (no `--exec-path`), then require `--hooks-dir` and `stat(<hooks-dir>/spawn-runner)` to be executable, else `error:` and exit 1. Serve `/healthz` with body `{"status":"ok","connected":true,"queue_counts":{"pending":0,"backing_off":0,"circuit_broken":0}}` and `/metrics` with `claude_code_self_hosted_orchestrator_connected 1` on `--health-port`. After 5 seconds, run the hook twice in sequence with `exec.Command(<hooks-dir>/spawn-runner)`, environment = `os.Environ()` plus: `CLAUDE_RUNNER_WORK_ORDER_FILE=/tmp/work-order` (write the file first with content `stub-work-order-jwt`, mode 0600), `CLAUDE_RUNNER_ORDER_ID=e2e-order-1`, `CLAUDE_RUNNER_SESSION_ID=session_e2e1`, `CLAUDE_RUNNER_SESSION_UUID=11111111-1111-4111-8111-111111111111`, `CLAUDE_RUNNER_ATTEMPT=1`, `CLAUDE_RUNNER_POOL_ID=ccpool_e2e`, `CLAUDE_RUNNER_CLIENT_PLATFORM=web_claude_ai`, `CLAUDE_RUNNER_ACCOUNT_ID=user_e2e`. Pipe the hook's stdout and stderr to the stub's stdout. After each run print `stub-orchestrator: hook run <n> exit <code>`. Then keep serving until SIGTERM.
- Runner mode: unchanged, plus if `STUB_EXIT_AFTER_SECONDS` is a positive integer, exit 0 after that many seconds (print `stub-runner: session finished, exiting 0`).

- [ ] **Step 2: Write the e2e testdata and spec**

`test/e2e/testdata/on-demand-stub.yaml`:

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: e2e-od
  namespace: claude-e2e-od
spec:
  environmentSecretRef:
    name: claude-env-secret
  runner:
    image: example.com/claude-stub-runner:e2e
    capacity: 1
    env:
      - name: STUB_EXIT_AFTER_SECONDS
        value: "90"          # long enough for the spec to observe Running after Ready
    podTemplate:
      resources:
        requests: {cpu: 10m, memory: 32Mi}
  onDemand:
    orchestrator:
      replicas: 1
      expectedSpawnSeconds: 90
      hookTimeoutSeconds: 60
      podTemplate:
        resources:
          requests: {cpu: 10m, memory: 32Mi}
    runnerTTLSecondsAfterFinished: 15
```

Timing: the stub orchestrator fires the hook 5 seconds after it starts, the runner reaches Running within seconds, exits 0 after 90 seconds, and the TTL removes it 15 seconds later. The Ready spec finishes within roughly 30 seconds of the orchestrator starting, which leaves the second spec a wide window to observe Running before Succeeded.

`test/e2e/on_demand_test.go` (same package and helper style as `fixed_fleet_test.go`):

```go
package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

const odNamespace = "claude-e2e-od"

func kubectlOD(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", odNamespace}, args...)...))
}

func onDemandSpecs() {
	Context("On-demand mode", Ordered, func() {
		BeforeAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", odNamespace))
			_, err := utils.Run(exec.Command("kubectl", "label", "ns", odNamespace,
				"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/enforce-version=latest", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectlOD("create", "secret", "generic", "claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e")
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/testdata/on-demand-stub.yaml"))
			Expect(err).NotTo(HaveOccurred())
		})
		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", odNamespace, "--wait=false"))
		})

		It("brings the orchestrator up with the injected hook and reports Ready", func() {
			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "claudeenvironment", "e2e-od", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
			out, err := kubectlOD("get", "deploy", "e2e-od-orchestrator", "-o", "jsonpath={.spec.template.spec.initContainers[0].image}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal(managerImage), "hook image must be the operator image via OPERATOR_IMAGE")
			out, _ = kubectlOD("get", "events", "--field-selector", "reason=FailedCreate", "-o", "name")
			Expect(strings.TrimSpace(out)).To(BeEmpty())
		})

		It("spawns exactly one runner for the order, runs it to Succeeded, and garbage-collects it", func() {
			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "clauderunner", "e2e-order-1", "-o", "jsonpath={.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Running"))
			}, 2*time.Minute, 3*time.Second).Should(Succeed())

			out, err := kubectlOD("get", "secret", "e2e-order-1-work-order", "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("ClaudeRunner"))
			out, err = kubectlOD("get", "clauderunner", "-o", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(out)).To(HaveLen(1), "a redelivered order must not create a second runner")
			out, err = kubectlOD("get", "claudeenvironment", "e2e-od", "-o", "jsonpath={.status.onDemand.runningRunners}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("1"))

			Eventually(func(g Gomega) {
				out, err := kubectlOD("get", "clauderunner", "e2e-order-1", "-o", "jsonpath={.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Succeeded"))
			}, 2*time.Minute, 3*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				for _, res := range []string{"clauderunner/e2e-order-1", "pod/e2e-order-1", "secret/e2e-order-1-work-order"} {
					_, err := kubectlOD("get", res)
					g.Expect(err).To(HaveOccurred(), res+" should be garbage-collected after the TTL")
				}
			}, time.Minute, 3*time.Second).Should(Succeed())

			logs, err := kubectlOD("logs", "deploy/e2e-od-orchestrator", "-c", "orchestrator")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("hook run 1 exit 0"))
			Expect(logs).To(ContainSubstring("hook run 2 exit 0"))
			Expect(logs).NotTo(ContainSubstring("stub-work-order-jwt"), "the hook must never print the work order")
		})
	})
}
```

In `test/e2e/e2e_test.go`, call `onDemandSpecs()` immediately after `fixedFleetSpecs()`.

- [ ] **Step 3: Run the e2e**

```bash
make test-e2e
```

Expected: all specs pass (the scaffold's two, the fixed-fleet two, the on-demand two). If the orchestrator pod never becomes Ready, check `kubectl -n claude-e2e-od describe pod -l selfhosted.claudecode.dev/role=orchestrator` for the init container (hook image pull or copy failure) and the exec readiness probe output. If the hook exits 2 with `forbidden`, the orchestrator Role or the manager's own RBAC is short (the manager must hold every permission it grants). Fix the cause, then re-run to a clean pass. Only the Makefile-owned kind cluster; never touch other clusters.

- [ ] **Step 4: Unit tests, lint, commit**

```bash
make test && make lint
git add -A
git commit -m "test(e2e): on-demand spawn, run, and garbage collection on kind with a stub orchestrator

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

## Follow-ups deferred to Plan 3

- NetworkPolicy builder behind `spec.runner.networkPolicy`; Helm chart via `helm/v2-alpha` with the chart contract test; PodMonitor for runner and orchestrator pods; secure metrics ServiceMonitor and cert-manager toggles; nightly real-environment e2e using a real environment key and the product's CI dispatch; upgrade test; signed multi-arch release; docs set (hardening, upgrade, metrics, troubleshooting).
- From Plan 1's follow-ups still open after this plan: Go pin and `latest` references, license headers on test files, Makefile `VERSION` guard, metadata-only or label-filtered Secret and ConfigMap cache, CEL for image empty-tag and mount-path collisions, missing builder tests.
- Recreate a StatefulSet on immutable-field apply errors instead of only reporting `WorkloadApplyFailed`.
