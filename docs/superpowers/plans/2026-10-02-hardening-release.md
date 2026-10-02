# Hardening, Chart, Real-Environment Test, and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the operator releasable: clear every recorded follow-up (minus the approved won't-do list), add the pre-1.0 hardening items, ship the Helm chart and monitoring manifests, add the two-level real-environment test and the upgrade test, build the release pipeline verified by a dry run, and complete the docs set.

**Architecture:** Debt first (behavioural fixes, API tightening, test hygiene, race-safe clocks), then structural hardening (filtered caches, orphan sweep, derived hook deadline, ValidatingAdmissionPolicy, NetworkPolicy), then packaging (chart generated from the kustomize installer with a contract test), then test tiers (upgrade e2e in CI, manual real-environment workflow with a key-only level and a token-gated session level), then release tooling and docs. One branch, one final review, no release tag.

**Tech Stack:** Go 1.26, kubebuilder v4 (`helm/v2-alpha` plugin), controller-runtime v0.25, envtest, Ginkgo, kind, Helm 3, GitHub Actions, docker buildx, cosign (keyless), syft, Kubernetes 1.30+ for ValidatingAdmissionPolicy.

**Spec:** `docs/superpowers/specs/2026-10-02-plan3-hardening-release-design.md` (addendum to `docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`). Follow-up inventories: `docs/superpowers/plans/2026-10-02-foundation-followups.md`, `docs/superpowers/plans/2026-10-02-on-demand-followups.md`.

## Global Constraints

- Module `github.com/AhmadMasry/claude-self-hosted-environment-operator`; API group `selfhosted.claudecode.dev/v1alpha1`; GitHub repo `AhmadMasry/claude-self-hosted-environment-operator` with `origin` already configured; the feature branch is pushed so CI runs on it.
- No release tag is created. The release workflow is verified only through its `dry_run` input. The runner image is never pushed to any registry.
- Every pod the operator creates stays Restricted-compliant; no email, JWT, or environment secret value in any log, event, condition, metric label, or span attribute.
- Won't-do list (approved): `enforce-version: latest` on sample namespaces; a redelivered order keeps the first JWT. Everything else in both follow-up files is done here.
- `make test`, `make lint` (0 issues), the new `-race` run, `make test-e2e` must be green at every commit; `make manifests generate` must leave no diff.
- Commits end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Only the Makefile-owned kind cluster (`claude-selfhosted-operator-test-e2e`) may be used locally; the unrelated `agent-mesh-lab` cluster is never touched.
- CI secrets available on the repo: `CLAUDE_ENVIRONMENT_KEY`, `CLAUDE_ENVIRONMENT_ID`. Optional: `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`, `CLAUDE_CODE_OAUTH_SCOPES`.

## Review Focus

1. A cluster older than Kubernetes 1.30 has no `ValidatingAdmissionPolicy` API: the chart must install cleanly with `admissionPolicy.enabled=false`, and the docs must say so. Test in Task 9 (chart contract test renders no admission objects when disabled).
2. `spec.runner.networkPolicy.enabled: true` with an empty `egressCIDRs` must produce a policy that still allows DNS and denies everything else, not an invalid object. Test in Task 8.
3. The orphan sweep must never delete a work-order Secret that is younger than `expectedSpawnSeconds` or that a ClaudeRunner still references. Test in Task 6.
4. An orchestrator started from an older environment object without the new `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS` variable must still get a sane hook deadline (default 60 s minus margin). Test in Task 6.
5. Upgrading the operator over an existing environment must leave that environment valid and Ready even though the CRD gained new validation rules. Test in Task 10.

---

### Task 1: Repository docs, CI baseline, feature branch on GitHub

**Files:**
- Modify: `README.md` (top section), `AGENTS.md` (trim), `.github/workflows/test.yml` (race job)
- Create: `SECURITY.md`, `CONTRIBUTING.md`, `.github/CODEOWNERS`, `.github/dependabot.yml`

**Interfaces:**
- Produces: a `feat/hardening-release` branch on `origin` with CI running on it; the `race` job every later task must keep green.

- [ ] **Step 1: Push the branch so CI runs on it**

```bash
git push -u origin feat/hardening-release
gh pr create --draft --title "Plan 3: hardening, chart, real-environment test, release" --body "Tracking PR for CI on the Plan 3 branch. Merged locally by fast-forward when complete." 2>&1 | tail -1
```

- [ ] **Step 2: Add the race job to `test.yml`**

Append a second job after `test`:

```yaml
  race:
    permissions:
      contents: read
    name: Race detector
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
        with:
          persist-credentials: false
      - name: Setup Go
        uses: actions/setup-go@4b73464bb391d4059bd26b0524d20df3927bd417 # v6.3.0
        with:
          go-version-file: go.mod
      - name: Unit and envtest suites under -race
        run: |
          make envtest
          KUBEBUILDER_ASSETS="$(bin/setup-envtest use $(grep ENVTEST_K8S_VERSION Makefile | head -1 | sed 's/.*= *//') --bin-dir bin -p path)" \
            go test -race ./internal/... ./cmd/... 2>&1 | tail -20
```

Read the Makefile's `test` target to copy exactly how it resolves `KUBEBUILDER_ASSETS` (the `setup-envtest` invocation and `ENVTEST_K8S_VERSION` variable) and use that same expression; the snippet above shows the intent, the Makefile is the source of truth. This job is expected to FAIL on this branch until Task 2 removes the test-global races; that is deliberate and recorded in the commit message.

- [ ] **Step 3: Write the repository documents**

`SECURITY.md`:

```markdown
# Security policy

Report vulnerabilities privately through GitHub's "Report a vulnerability"
button on the Security tab of this repository. Do not open a public issue.

You will get an acknowledgement within 3 working days and a fix or
mitigation plan within 14 days for confirmed issues. Credit is given in
the release notes unless you prefer otherwise.

In scope: the operator manager, the spawn-runner hook, the generated
manifests and Helm chart, and the sample Dockerfiles. Out of scope: the
Claude Code binary and Anthropic's control plane (report those to
Anthropic), and misconfiguration of a user's cluster.
```

`CONTRIBUTING.md`:

```markdown
# Contributing

## Toolchain

Go (version in `go.mod`), Docker, kind, kubectl, Helm 3, kubebuilder v4.
`make` installs controller-gen, kustomize, envtest and golangci-lint into
`bin/` on first use.

## Workflow

1. Branch from `master`.
2. `make manifests generate` after editing `api/` markers or RBAC markers;
   commit the generated files.
3. `make test` (unit + envtest), `make lint`, and `go test -race ./internal/... ./cmd/...`
   must pass.
4. `make test-e2e` runs the kind suite (creates and deletes its own cluster).
5. Open a pull request; CI runs lint, tests, race, chart lint and the e2e.

## Test tiers

- Unit: pure functions in `internal/builders`, `internal/hook`, `internal/metrics`, `internal/telemetry`.
- envtest: both controllers against a real API server, `internal/controller`.
- e2e: kind cluster with stub runner and orchestrator images, `test/e2e`.
- Real environment: manual workflow, see `docs/testing.md`.

## Commit messages

Conventional prefixes (`feat`, `fix`, `docs`, `test`, `chore`) with a short
subject. Generated files are committed together with the change that
regenerates them.
```

`.github/CODEOWNERS`:

```
* @AhmadMasry
```

`.github/dependabot.yml`:

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule: {interval: weekly}
    groups:
      kubernetes: {patterns: ["k8s.io/*", "sigs.k8s.io/*"]}
      opentelemetry: {patterns: ["go.opentelemetry.io/*"]}
  - package-ecosystem: github-actions
    directory: /
    schedule: {interval: weekly}
  - package-ecosystem: docker
    directory: /
    schedule: {interval: weekly}
  - package-ecosystem: docker
    directory: /examples/runner-image
    schedule: {interval: weekly}
```

README top section (replace everything above the first `##` heading):

```markdown
# Claude Code self-hosted environment operator

[![Lint](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml)
[![E2E](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml)

A Kubernetes operator that runs [Claude Code self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments)
on your cluster. You create one `ClaudeEnvironment` per environment from
claude.ai admin settings, point it at a runner image you build and a Secret
holding the environment key, and the operator runs the fleet: a fixed set
of runners, or an orchestrator that spawns one hardened pod per session.
Every pod meets the Restricted Pod Security Standard by default.

Status: pre-release. The API is `v1alpha1` and the API group is provisional
until the first tagged release.

Install: kustomize installer (`dist/install.yaml` on each release) or the
Helm chart (`oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator`).
Docs: [quickstart](#quickstart) below, [on-demand mode](docs/on-demand.md),
[hardening](docs/hardening.md), [metrics](docs/metrics.md),
[upgrade](docs/upgrade.md), [testing](docs/testing.md),
[troubleshooting](TROUBLESHOOTING.md).
```

Keep the existing quickstart below it under a `## Quickstart` heading (rename the current first heading if needed). The docs linked above are created in Tasks 11 and 13; a link to a not-yet-existing file is acceptable on this branch and resolves by the end of the plan.

`AGENTS.md`: replace the whole file with:

```markdown
# AGENTS.md

Guidance for AI agents and humans working in this repository.

- Generated files are never edited by hand: `config/crd/bases/*.yaml`,
  `config/rbac/role.yaml`, `**/zz_generated.*.go`, `PROJECT`, `dist/`.
  Run `make manifests generate` after editing `api/` or RBAC markers and
  commit the result.
- Keep the kubebuilder layout: `api/v1alpha1`, `internal/controller`,
  `internal/builders` (pure functions that turn CRs into objects),
  `internal/hook` (the spawn-runner hook), `cmd/` (manager) and
  `cmd/spawn-runner`.
- Do not remove `// +kubebuilder:scaffold:*` markers.
- Tests: unit (`go test ./internal/...`), envtest (`make test`), kind e2e
  (`make test-e2e`, isolated cluster), race (`go test -race`). Add a test
  with every behaviour change; see CONTRIBUTING.md.
- Logging follows the Kubernetes style guide: capitalised, no trailing
  period, key-value pairs. Never log secrets, JWTs, or emails.
- Every pod the operator builds must satisfy the Restricted Pod Security
  Standard; the builders set the fields explicitly.
```

- [ ] **Step 4: Verify and commit**

```bash
make lint && git add -A && git commit -m "chore: repository documents, dependabot, race job (expected red until the clock refactor)

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
gh run list --branch feat/hardening-release --limit 5
```

Expected: Lint and Tests green; the new race job red with a `DATA RACE` report mentioning `nowFunc` or `HookImage`; E2E green.

---

### Task 2: Injectable clocks and race-safe test overrides

**Files:**
- Modify: `internal/controller/runnerphase.go`, `internal/controller/clauderunner_controller.go`, `internal/controller/claudeenvironment_controller.go`, `internal/controller/suite_test.go`, `internal/controller/clauderunner_controller_test.go`, `internal/controller/claudeenvironment_ondemand_test.go`, `internal/controller/runnerphase_test.go`, `internal/controller/failedstart.go` (if it reads `nowFunc`), `cmd/main.go` (unchanged unless it sets the fields)
- Test: existing specs, plus the `-race` run

**Interfaces:**
- Produces: `type Clock func() time.Time`; `ClaudeRunnerReconciler.Clock Clock` and `ClaudeEnvironmentReconciler.Clock Clock` (nil means `time.Now`) with method `now() time.Time`; `derivePhase(pod *corev1.Pod, now time.Time) phaseResult`; `(*ClaudeEnvironmentReconciler).SetHookImage(string)` and `hookImage() string` guarded by a `sync.RWMutex`; test helper `type testClock struct` with `Now()` and `Shift(d time.Duration)`.

- [ ] **Step 1: Confirm the race**

```bash
make envtest
KUBEBUILDER_ASSETS="$(bin/setup-envtest use $(grep ENVTEST_K8S_VERSION Makefile | head -1 | sed 's/.*= *//') --bin-dir bin -p path)" go test -race ./internal/controller/... 2>&1 | grep -E 'DATA RACE|FAIL|ok' | head
```

Expected: `DATA RACE` reported.

- [ ] **Step 2: Replace the globals**

`internal/controller/runnerphase.go`: delete `var nowFunc = time.Now`; change `derivePhase(pod *corev1.Pod)` to `derivePhase(pod *corev1.Pod, now time.Time)` and `terminatedAt(cs)` to `terminatedAt(cs *corev1.ContainerStatus, now time.Time)`, using `now` where `nowFunc()` was called. Update `runnerphase_test.go` to pass `time.Now()`.

Add to `internal/controller/clock.go`:

```go
package controller

import "time"

// Clock returns the current time. A nil Clock on a reconciler means time.Now.
type Clock func() time.Time

func tick(c Clock) time.Time {
	if c != nil {
		return c()
	}
	return time.Now()
}
```

`clauderunner_controller.go`: add `Clock Clock` to the struct; add `func (r *ClaudeRunnerReconciler) now() time.Time { return tick(r.Clock) }`; replace every `nowFunc()` with `r.now()` and every `derivePhase(pod)` with `derivePhase(pod, r.now())`.

`claudeenvironment_controller.go`: add `Clock Clock` and `mu sync.RWMutex` to the struct; add

```go
func (r *ClaudeEnvironmentReconciler) now() time.Time { return tick(r.Clock) }

// SetHookImage replaces the hook image at runtime; tests use it.
func (r *ClaudeEnvironmentReconciler) SetHookImage(image string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.HookImage = image
}

func (r *ClaudeEnvironmentReconciler) hookImage() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.HookImage
}
```

and replace every read of `r.HookImage` inside reconcile paths with `r.hookImage()`. `checkRunnerPods` passes `r.now()` to `detectFailedStart`.

- [ ] **Step 3: Test clock**

Add to `suite_test.go`:

```go
type testClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *testClock) Shift(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = d
}

var clock = &testClock{}
```

and set `Clock: clock.Now` on both reconcilers when constructing them. In the spawn-timeout spec replace `nowFunc = ...` with `clock.Shift(2 * time.Minute)` and `DeferCleanup(func() { clock.Shift(0) })`. In the `HookImageUnset` spec replace the direct field write with `envReconciler.SetHookImage("")` and `DeferCleanup(func() { envReconciler.SetHookImage(saved) })` where `saved` was read through a new exported `HookImageValue()`? No: read it once in the suite into a constant `testHookImage` and restore to that.

- [ ] **Step 4: Verify race-free and commit**

```bash
make test && make lint
KUBEBUILDER_ASSETS="$(bin/setup-envtest use $(grep ENVTEST_K8S_VERSION Makefile | head -1 | sed 's/.*= *//') --bin-dir bin -p path)" go test -race ./internal/... ./cmd/... 2>&1 | grep -E 'DATA RACE|FAIL|^ok' 
git add -A && git commit -m "fix(controller): injectable clocks and race-safe hook image override

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

Expected: no `DATA RACE`, all packages `ok`, the GitHub race job turns green.

---

### Task 3: Debt sweep, controller behaviour

**Files:**
- Modify: `internal/controller/claudeenvironment_controller.go`, `internal/controller/status.go`, `internal/controller/clauderunner_controller.go`, `internal/hook/hook.go`, `internal/hook/log.go`, `internal/hook/install.go`, `internal/builders/orchestrator.go` (label helper), `api/v1alpha1/conditions.go`
- Test: `internal/controller/claudeenvironment_fixed_test.go`, `claudeenvironment_ondemand_test.go`, `clauderunner_controller_test.go`, `internal/hook/hook_test.go`, `internal/hook/install_test.go`, `internal/builders/orchestrator_test.go`

**Interfaces:**
- Produces: `statusPass.degradeOnce(recorder, env, reason, msg)` (emits a Warning only when the Degraded condition was not already True with that reason); `(*ClaudeEnvironmentReconciler).apply` emits a Normal `Created` event when the object did not exist; `ReasonCreated = "Created"`; `labelValue(id string) string` in `api/v1alpha1` (returns the id if it is a valid label value under 64 chars, else `sha256-<first 16 hex>`); `runnerPhaseChanged()` predicate.

Each item: write the test first, see it fail, change the code, see it pass. Items are independent; commit once at the end.

- [ ] **Item A: no `RequeueAfter` with an error.** In `reconcile`, lines returning `ctrl.Result{RequeueAfter: requeueAfterUserFix}, err`: return `ctrl.Result{}, err` when `err != nil`, and `ctrl.Result{RequeueAfter: requeueAfterUserFix}, nil` when `!ok`. Test: none needed beyond existing specs; `make lint` keeps `staticcheck` quiet.

- [ ] **Item B: stale counts.** On the `applyFailed` paths set `env.Status.Fixed = nil` (fixed) or `env.Status.OnDemand = nil` (on-demand) before returning; on the `HookImageUnset` path set `env.Status.OnDemand = nil`. Test: extend the existing "reports a failed workload apply" spec to assert `Status.Fixed == nil` after the failure, and the `HookImageUnset` spec to assert `Status.OnDemand == nil`.

- [ ] **Item C: Warning events only on transition.** Add to `status.go`:

```go
// degradeOnce records the degradation and emits a Warning event only when
// the Degraded condition was not already True with this reason.
func (p *statusPass) degradeOnce(rec record.EventRecorder, env *selfhostedv1alpha1.ClaudeEnvironment, reason, msg string) {
	p.degrade(reason, msg)
	if c := meta.FindStatusCondition(p.before, selfhostedv1alpha1.ConditionDegraded); c != nil && c.Status == metav1.ConditionTrue && c.Reason == reason {
		return
	}
	rec.Event(env, corev1.EventTypeWarning, reason, msg)
}
```

`statusPass` gains `before []metav1.Condition` (a deep copy of the conditions taken in `newStatusPass`). Replace the `pass.degrade(...); r.Recorder.Event(...)` pairs for `GracePeriodTooShort`, `RunnerFailedStart`, `HookImageUnset`, `ConfigMapMissing` with `pass.degradeOnce(r.Recorder, env, reason, msg)`. Test: in the `GracePeriodTooShort` spec, after Degraded is True, poke the environment (annotation update) twice and assert with `kubectl`-style listing (`k8sClient.List(ctx, &corev1.EventList{}, client.InNamespace(ns))`) that exactly one event with reason `GracePeriodTooShort` exists.

- [ ] **Item D: Normal `Created` events.** In `apply`, before the patch do `existing := obj.DeepCopyObject().(client.Object); notFound := apierrors.IsNotFound(r.Get(ctx, client.ObjectKeyFromObject(obj), existing))`; after a successful patch, if `notFound` emit `r.Recorder.Event(env, corev1.EventTypeNormal, selfhostedv1alpha1.ReasonCreated, fmt.Sprintf("created %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName()))`. Add `ReasonCreated = "Created"`. Test: the "creates a hardened Deployment" spec asserts an event with reason `Created` and message containing `Deployment platform-runner` exists.

- [ ] **Item E: UID owner check.** In `ownerMismatch`, also require `owner.UID == env.UID`; message "controller owner UID does not match ClaudeEnvironment". Test: a runner owned by a reference with the right name but a random UID must go `Failed/EnvironmentMismatch`.

- [ ] **Item F: `SpawnTimeout` survives a status conflict.** In the timeout branch, set `runner.Status.Reason = ReasonSpawnTimeout` and `runner.Status.Message` before deleting the pod; in the `PodName`-set branch, when `existingPod` returns nil and `runner.Status.Reason == ReasonSpawnTimeout`, call `fail` with `ReasonSpawnTimeout` (not `PodLost`). Test: envtest cannot force a conflict; add a unit test on a new pure helper `lostReason(current string) string` returning `SpawnTimeout` when the stored reason is `SpawnTimeout`, else `PodLost`, and use it in the controller.

- [ ] **Item G: `runners_total{failed}` on every `fail`.** Move `metrics.CountRunner(ns, env, metrics.OutcomeFailed)` into `fail()` (guarded by `runner.Status.Phase != RunnerFailed` before the change so it counts once) and remove the duplicate call from the terminal branch for the failed case. Test: metrics package test unchanged; envtest `WorkOrderMissing` spec asserts via `testutil.ToFloat64` on the counter (import the metrics package's exported accessor `metrics.RunnersTotalForTest(ns, env, outcome) float64`, add it).

- [ ] **Item H: `createPod` on `AlreadyExists`.** On `AlreadyExists`, re-`Get` the pod (cached, then `Reader`) and return it without counting or emitting. Test: pre-create a pod named like the runner before creating the runner; assert exactly one `PodCreated` event and `runners_total{created}` unchanged.

- [ ] **Item I: trace continuation stops at terminal.** In the runner `Reconcile`, call `telemetry.ContextWithTraceparent` only when `!runner.Status.Phase.IsTerminal()`. Test: in the tracing spec, after the runner is Failed (use the missing-secret path with a short deadline), poke it and assert no new `clauderunner.reconcile` span carries the hook's trace ID after the terminal transition (count spans with that trace ID before and after; equal).

- [ ] **Item J: spawn duration from pod start.** `metrics.ObserveSpawn` receives `pod.Status.StartTime.Sub(runner.CreationTimestamp.Time)` when `StartTime` is set, else `r.now().Sub(...)`. Test: builder-level unit test of a new pure helper `spawnDuration(runner, pod, now) time.Duration`.

- [ ] **Item K: controller log lines.** Using `logf.FromContext(ctx).WithValues("orderID", runner.Spec.OrderID, "sessionID", runner.Spec.SessionID)`, log `Created runner pod` (name), `Runner phase changed` (from, to), `Runner spawn timed out`, `Deleting expired runner`. Test: none (logging); `make lint` passes; no secret values (session ID is an opaque ID).

- [ ] **Item L: phase-change predicate on `Owns(ClaudeRunner)`.** Add to `failedstart.go` (next to `podStatusChanged`):

```go
// runnerPhaseChanged lets ClaudeRunner update events through only when the phase changed.
func runnerPhaseChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok1 := e.ObjectOld.(*selfhostedv1alpha1.ClaudeRunner)
		n, ok2 := e.ObjectNew.(*selfhostedv1alpha1.ClaudeRunner)
		return ok1 && ok2 && o.Status.Phase != n.Status.Phase
	}}
}
```

and use `Owns(&selfhostedv1alpha1.ClaudeRunner{}, builder.WithPredicates(runnerPhaseChanged()))`. Test: the "counts runners by phase" spec still passes (phase changes must still propagate).

- [ ] **Item M: immutability test trigger.** In the `WorkloadApplyFailed` spec, change `serviceName` instead of the claim template storage (set `env.Spec.Runner.Settings.LockToAccount` unchanged; the builder derives `serviceName` from the name, so instead change the test to patch the StatefulSet's `serviceName` through the environment is impossible). Replace the trigger with: after the StatefulSet exists, update the environment's `fixed.persistentWorkspace.volumeClaimTemplate.accessModes` from `ReadWriteOnce` to `ReadWriteMany`; both are immutable template fields today and `accessModes` stays immutable under KEP-4650 (which only allows resize). Assert the same conditions.

- [ ] **Item N: label values for long IDs.** Add to `api/v1alpha1/labels.go`:

```go
package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"

	"k8s.io/apimachinery/pkg/util/validation"
)

// LabelValue returns id when it is a valid label value, otherwise a stable
// sha256-prefixed digest so long or unusual IDs can still be selected on.
func LabelValue(id string) string {
	if len(validation.IsValidLabelValue(id)) == 0 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256-" + hex.EncodeToString(sum[:8])
}
```

Use it for `LabelOrderID` and `LabelSessionID` values in the hook and in `builders.OnDemandRunnerPod`, and in the environment controller's orphan sweep (Task 6) when matching. Test: unit test with a 70-character ID and an ID containing `/`.

- [ ] **Item O: hook redaction and labels.** In `log.go` add `|eyJ[A-Za-z0-9_-]{20,}` to the pattern so a bare JWT header segment is redacted; in `hook.go` build separate label maps for the Secret and the ClaudeRunner (`maps.Clone`). Tests: a bare `eyJ...` string is redacted; the Secret's labels map is not the same object as the runner's (assert by mutating one after `Run` through the fake client's stored objects is not possible, so assert both objects carry the expected labels independently).

- [ ] **Item P: `Install` temp-file cleanup.** On any failure after `CreateTemp`, `os.Remove(tmp.Name())`. Test: make `dir` read-only after creating the temp file is awkward; instead test with a `dir` that does not exist (CreateTemp fails before any temp file) and a success path that asserts no `.spawn-runner-*` files remain.

- [ ] **Commit**

```bash
make manifests && make test && make lint && git add -A && git commit -m "fix: controller and hook follow-ups from the Plan 1 and 2 reviews

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 4: Debt sweep, API validation and builders

**Files:**
- Modify: `api/v1alpha1/claudeenvironment_types.go`, `api/v1alpha1/podtemplate_types.go`, `internal/builders/args.go`, `internal/builders/drain.go`, `internal/builders/podtemplate.go`, `cmd/main.go`
- Test: `internal/controller/claudeenvironment_validation_test.go`, `internal/builders/podtemplate_test.go`, `internal/builders/args_test.go`

**Interfaces:**
- Produces: shared constants `builders.DefaultStartupTimeoutMinutes = 15`, `DefaultSessionStopGraceSeconds = 5`, `DefaultPostSessionHookTimeoutSeconds = 60` (used by both `args.go` and `drain.go`); volume-name constants `volEnvironmentSecret`, `volHome`, `volTmp`, `volHooks`, `volWrapper`, `volHostConfig`; the operator-reserved mount paths exported as `ReservedMountPaths = []string{"/etc/claude", "/home/runner", "/tmp"}`.

- [ ] **Step 1: Validation tests first**

Append entries to the reject table in `claudeenvironment_validation_test.go`:

```go
		Entry("empty tag", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner:"
		}, "must carry a tag or digest"),
		Entry("empty digest", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Image = "registry.local/runner@sha256:"
		}, "must carry a tag or digest"),
		Entry("baseDir on a reserved path", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.BaseDir = "/tmp"
		}, "baseDir must not be /etc/claude, /home/runner or /tmp"),
		Entry("user mount on a reserved path", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.PodTemplate.Volumes = []selfhostedv1alpha1.Volume{{Name: "x", EmptyDir: &corev1.EmptyDirVolumeSource{}}}
			e.Spec.Runner.PodTemplate.VolumeMounts = []corev1.VolumeMount{{Name: "x", MountPath: "/etc/claude/hooks"}}
		}, "volumeMounts must not target /etc/claude, /home/runner or /tmp"),
		Entry("user env var with an operator-owned name", func(e *selfhostedv1alpha1.ClaudeEnvironment) {
			e.Spec.Runner.Env = []corev1.EnvVar{{Name: "SELF_HOSTED_RUNNER_HOST_CONFIG_DIR", Value: "/x"}}
		}, "env may not set operator-owned variables"),
```

and a schema-level case proving forbidden pod fields are unknown:

```go
	It("rejects forbidden pod fields at the schema level", func() {
		for _, field := range []string{"hostNetwork", "hostPID", "privileged"} {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(selfhostedv1alpha1.GroupVersion.WithKind("ClaudeEnvironment"))
			obj.SetName("forbidden-" + strings.ToLower(field))
			obj.SetNamespace("default")
			Expect(unstructured.SetNestedField(obj.Object, "r:1", "spec", "runner", "image")).To(Succeed())
			Expect(unstructured.SetNestedField(obj.Object, map[string]any{"name": "s"}, "spec", "environmentSecretRef")).To(Succeed())
			Expect(unstructured.SetNestedField(obj.Object, map[string]any{}, "spec", "fixed")).To(Succeed())
			Expect(unstructured.SetNestedField(obj.Object, true, "spec", "runner", "podTemplate", field)).To(Succeed())
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred(), field)
			Expect(err.Error()).To(ContainSubstring("unknown field"), field)
		}
	})
```

Note: the suite's client must be created with strict field validation for the "unknown field" error; set `client.Options{Scheme: scheme.Scheme}` plus `k8sClient = client.WithFieldValidation(k8sClient, metav1.FieldValidationStrict)` in the suite (controller-runtime v0.25 supports it); if not available, use `k8sClient.Create(ctx, obj, client.FieldValidation(metav1.FieldValidationStrict))` per call.

- [ ] **Step 2: CEL and marker changes**

In `claudeenvironment_types.go`:

- Image rule becomes: `self.contains('@sha256:') ? size(self.substring(self.indexOf('@sha256:') + 8)) == 64 : (self.lastIndexOf(':') > self.lastIndexOf('/') && !self.endsWith(':') && !self.endsWith(':latest'))`, same message.
- On `RunnerSpec` add `// +kubebuilder:validation:XValidation:rule="!(self.baseDir in ['/etc/claude','/home/runner','/tmp']) && !self.baseDir.startsWith('/etc/claude/')",message="baseDir must not be /etc/claude, /home/runner or /tmp"` and `// +kubebuilder:validation:XValidation:rule="!self.podTemplate.volumeMounts.exists(m, m.mountPath in ['/etc/claude','/home/runner','/tmp'] || m.mountPath.startsWith('/etc/claude/') || m.mountPath.startsWith('/home/runner/') || m.mountPath.startsWith('/tmp/'))",message="volumeMounts must not target /etc/claude, /home/runner or /tmp"` and `// +kubebuilder:validation:XValidation:rule="!self.env.exists(e, e.name in ['SELF_HOSTED_RUNNER_HOST_CONFIG_DIR','SELF_HOSTED_RUNNER_CLIENT_LABEL','SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET'])",message="env may not set operator-owned variables"`. `PodTemplate.VolumeMounts` and `RunnerSpec.Env` need `MaxItems` (64) for CEL cost; add them.
- `OrchestratorSpec.HookTimeoutSeconds` minimum becomes 15 (used by Task 6; do it here with the other markers).

`make manifests` then Step 1's tests go green.

- [ ] **Step 3: Builder constants and tests**

`drain.go` keeps the three product defaults as exported constants named above; `args.go` compares against them instead of literals. `podtemplate.go` uses the volume-name constants for every volume and mount. Add to `podtemplate_test.go`:

```go
func TestRunnerPodTemplateWorkspaceFromPVC(t *testing.T) {
	in := secretInput(testEnv())
	in.WorkspaceFromPVC = true
	tmpl := RunnerPodTemplate(in)
	for _, v := range tmpl.Spec.Volumes {
		if v.Name == WorkspaceVolume {
			t.Fatal("workspace emptyDir must be omitted when a PVC provides it")
		}
	}
	var mounted bool
	for _, m := range tmpl.Spec.Containers[0].VolumeMounts {
		if m.Name == WorkspaceVolume && m.MountPath == "/workspace" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("workspace mount must remain")
	}
}

func TestRunnerPodTemplateClientLabelEnv(t *testing.T) {
	tmpl := RunnerPodTemplate(secretInput(testEnv()))
	var found bool
	for _, e := range tmpl.Spec.Containers[0].Env {
		if e.Name == "SELF_HOSTED_RUNNER_CLIENT_LABEL" && e.ValueFrom != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.name" {
			found = true
		}
	}
	if !found {
		t.Fatal("client label must default to the pod name via the downward API")
	}
	env := testEnv()
	env.Spec.Runner.Settings.ClientLabel = "fleet"
	for _, e := range RunnerPodTemplate(secretInput(env)).Spec.Containers[0].Env {
		if e.Name == "SELF_HOSTED_RUNNER_CLIENT_LABEL" {
			t.Fatal("an explicit client label must not add the downward-API env var")
		}
	}
}

func TestRunnerPodTemplateProbePort(t *testing.T) {
	c := RunnerPodTemplate(secretInput(testEnv())).Spec.Containers[0]
	if c.ReadinessProbe.HTTPGet.Port.StrVal != "health" || c.LivenessProbe.HTTPGet.Port.StrVal != "health" {
		t.Fatalf("probes must reference the named health port: %v %v", c.ReadinessProbe.HTTPGet.Port, c.LivenessProbe.HTTPGet.Port)
	}
}
```

`cmd/main.go`: stop calling `opts.BindFlags(flag.CommandLine)`; construct `zap.Options{}` only (so `--zap-*` flags disappear). Update `cmd/options_test.go` with a test that the flag set contains `log-level` and not `zap-log-level` (parse a fresh `flag.FlagSet` the same way `main` does by moving flag registration into `func registerFlags(fs *flag.FlagSet) *options` in `cmd/options.go`; `main` calls it with `flag.CommandLine`).

- [ ] **Step 4: Verify and commit**

```bash
make manifests generate && make test && make lint && git add -A && git commit -m "fix(api,builders): tighten image, mount, env validation; share defaults; builder tests

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 5: Debt sweep, tests, tooling, pins, docs notes

**Files:**
- Modify: `Dockerfile`, `.devcontainer/devcontainer.json`, `.devcontainer/post-install.sh`, `.custom-gcl.yml`, `Makefile`, `cmd/options_test.go`, `internal/builders/fixed_test.go`, `internal/builders/hash_test.go`, `internal/controller/clauderunner_controller_test.go`, `internal/controller/clauderunner_validation_test.go`, `internal/telemetry/telemetry_test.go`, `internal/hook/probe_test.go`, `test/stubrunner/main.go`, `test/e2e/fixed_fleet_test.go`, `test/e2e/on_demand_test.go`, `config/default/kustomization.yaml`, `README.md`, `docs/on-demand.md`

- [ ] **Pins.** `Dockerfile` `ARG BASE_IMAGE=golang:1.26` stays; `.devcontainer/devcontainer.json` uses the same `1.26` tag; `.devcontainer/post-install.sh` pins kind, kubectl and kubebuilder to explicit versions (read the current latest from each project's releases page and pin); `.custom-gcl.yml` pins the logtools plugin to its current tagged version.

- [ ] **Makefile.** `docker-build`: `$(if $(VERSION),--build-arg VERSION=$(VERSION))`.

- [ ] **License headers.** Add the Apache header (copy from `internal/builders/drain.go`) to `cmd/options_test.go`, `internal/builders/fixed_test.go`, `internal/builders/hash_test.go`, `internal/controller/clauderunner_controller_test.go`.

- [ ] **Telemetry tests.** `TestInitWithoutEndpointIsNoop`: after `Init`, `_, span := StartSpan(ctx, "x")`; assert `!span.SpanContext().IsValid()` or `!span.IsRecording()`. `TestTraceparentRoundTrip`: replace the nil check with `Expect` that `trace.SpanContextFromContext(ContextWithTraceparent(ctx, "garbage")).IsValid()` is false.

- [ ] **Runner tests.** "lands after" case: before creating the Secret, `Eventually` the runner reaches Pending with `Status.Reason == WorkOrderMissing`, then create the Secret. Empty-orderID case: use `WorkOrderSecretRef{Name: "-work-order"}` so the only rejection is `MinLength` and assert `apierrors.IsInvalid(err)` plus the field path `spec.orderID` in the message.

- [ ] **Hook tests.** `probe_test.go`: guard `body` with a `sync.Mutex` read in the handler and written in the test.

- [ ] **Stub.** Split `main` into `runRunner(args)` and `runOrchestrator(args)` with handler constructors `runnerHealthz()` and `orchestratorHealthz()`; behaviour unchanged (the e2e proves it).

- [ ] **E2E.** In both e2e files: `kubectl get events` results check `err`; the GC check requires the error text to contain `NotFound` or `not found`; testdata paths built from `utils.GetProjectDir()`; the `Running Running` assertion wrapped in `Eventually`; one `stubImage` constant used by the suite and both testdata files (testdata keeps the literal, a test asserts the literal equals the constant by reading the file).

- [ ] **Kustomization.** Align the commented cert-manager `replacements` entries two spaces in under the real `replacements:` and keep the two scaffold markers at column 0 directly above the block they inject into; add a comment line explaining the markers.

- [ ] **Docs notes.** README hardening paragraph: `lockToAccount` accepts an email and that value is stored in the ClaudeEnvironment object and passed as a pod argument, so treat the object as containing personal data; the operator watches Secrets and ConfigMaps cluster-wide unless `--watch-namespaces` is set. `docs/on-demand.md`: the same `--watch-namespaces` note.

- [ ] **Commit**

```bash
make test && make lint && make test-e2e 2>&1 | tail -3 && git add -A && git commit -m "chore: pins, headers, test hardening and docs notes from the follow-up lists

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

Expected: e2e 6 of 6.

---

### Task 6: Filtered caches, orphan sweep, derived hook deadline

**Files:**
- Modify: `internal/controller/claudeenvironment_controller.go` (`CacheByObject`, `reconcileOnDemand`), `internal/builders/orchestrator.go` (env var), `internal/hook/input.go`, `internal/hook/hook.go`, `cmd/spawn-runner/main.go`, `api/v1alpha1/conditions.go`
- Test: `internal/controller/claudeenvironment_ondemand_test.go`, `internal/builders/orchestrator_test.go`, `internal/hook/hook_test.go`, `internal/controller/cache_test.go`

**Interfaces:**
- Produces: `EnvHookTimeoutSeconds = "CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS"`; `hook.Input.HookTimeoutSeconds int` (default 60); `func (in Input) Deadline() time.Duration` (timeout minus 10 s, floor 5 s); `sweepOrphanedWorkOrders(ctx, env) error`.

- [ ] **Step 1: Tests**

`internal/controller/cache_test.go`:

```go
func TestCacheByObjectFiltersOperatorObjects(t *testing.T) {
	byObject, err := CacheByObject()
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		cfg, ok := byObject[obj]
		if !ok || cfg.Label == nil {
			t.Fatalf("%T must have a label selector", obj)
		}
	}
	for _, obj := range []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}} {
		if _, ok := byObject[obj]; ok {
			t.Fatalf("%T must stay unfiltered (user-named)", obj)
		}
	}
	sel := byObject[&rbacv1.Role{}].Label
	if !sel.Matches(labels.Set{"app.kubernetes.io/part-of": "claude-code-self-hosted-runner"}) {
		t.Fatal("operator-created RBAC objects must match")
	}
	if sel.Matches(labels.Set{"app": "other"}) {
		t.Fatal("unrelated objects must not match")
	}
}
```

Map keys in `cache.ByObject` are compared by type pointer identity in controller-runtime; use the same literal objects (`&corev1.Pod{}` etc.) in the test lookup by iterating the map and matching on `fmt.Sprintf("%T", k)` instead of indexing, if direct indexing does not find them.

Hook tests:

```go
func TestDeadline(t *testing.T) {
	cases := map[int]time.Duration{0: 50 * time.Second, 60: 50 * time.Second, 15: 5 * time.Second, 12: 5 * time.Second, 120: 110 * time.Second}
	for timeout, want := range cases {
		in := Input{HookTimeoutSeconds: timeout}
		if got := in.Deadline(); got != want {
			t.Fatalf("timeout %d: got %v want %v", timeout, got, want)
		}
	}
}
```

and extend `TestParseEnv` with `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS=45` giving `HookTimeoutSeconds == 45`, absent giving 60.

Builder test: `TestOrchestratorDeployment` asserts env `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS` equals `strconv.Itoa(int(o.HookTimeoutSeconds))`.

Orphan sweep envtest (in `claudeenvironment_ondemand_test.go`):

```go
	It("deletes orphaned work-order Secrets after the spawn deadline and keeps the rest", func() {
		ns := newNamespace(ctx)
		Expect(k8sClient.Create(ctx, envSecret(ns, "environment-secret"))).To(Succeed())
		env := onDemandEnvObj(ns)
		env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds = 10
		env.Spec.OnDemand.Orchestrator.HookTimeoutSeconds = 4
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
		Eventually(condition(ctx, client.ObjectKeyFromObject(env), selfhostedv1alpha1.ConditionFleetAvailable), timeout, interval).ShouldNot(BeNil())

		orphan := workOrderSecret(ns, "orphan")
		orphan.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: envName, selfhostedv1alpha1.LabelOrderID: "orphan"}
		Expect(k8sClient.Create(ctx, orphan)).To(Succeed())
		referenced := workOrderSecret(ns, "kept")
		referenced.Labels = map[string]string{selfhostedv1alpha1.LabelEnvironment: envName, selfhostedv1alpha1.LabelOrderID: "kept"}
		Expect(k8sClient.Create(ctx, referenced)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		Expect(k8sClient.Create(ctx, ownedBy(runnerObj(ns, "kept"), env))).To(Succeed())

		Consistently(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.Secret{}) }, 3*time.Second, interval).Should(Succeed(), "younger than the deadline must be kept")
		clock.Shift(time.Minute)
		DeferCleanup(func() { clock.Shift(0) })
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), env)).To(Succeed())
		env.Annotations = map[string]string{"test/poke": "1"}
		Expect(k8sClient.Update(ctx, env)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(orphan), &corev1.Secret{})) }, timeout, interval).Should(BeTrue())
		Consistently(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(referenced), &corev1.Secret{}) }, 2*time.Second, interval).Should(Succeed(), "referenced must be kept")
	})
```

- [ ] **Step 2: Implement**

`CacheByObject`:

```go
func CacheByObject() (map[client.Object]cache.ByObject, error) {
	role, err := labels.NewRequirement(selfhostedv1alpha1.LabelRole, selection.In,
		[]string{selfhostedv1alpha1.RoleRunner, selfhostedv1alpha1.RoleOrchestrator})
	if err != nil {
		return nil, err
	}
	partOf, err := labels.NewRequirement(selfhostedv1alpha1.LabelPartOf, selection.Equals, []string{selfhostedv1alpha1.PartOfValue})
	if err != nil {
		return nil, err
	}
	operatorOwned := cache.ByObject{Label: labels.NewSelector().Add(*partOf)}
	return map[client.Object]cache.ByObject{
		&corev1.Pod{}:            {Label: labels.NewSelector().Add(*role)},
		&corev1.ServiceAccount{}: operatorOwned,
		&rbacv1.Role{}:           operatorOwned,
		&rbacv1.RoleBinding{}:    operatorOwned,
	}, nil
}
```

Orphan sweep, called at the end of `reconcileOnDemand`:

```go
// sweepOrphanedWorkOrders deletes work-order Secrets that no ClaudeRunner
// references once the spawn deadline has passed, so a hook crash between its
// two creates cannot leave a live JWT behind.
func (r *ClaudeEnvironmentReconciler) sweepOrphanedWorkOrders(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(env.Namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}); err != nil {
		return err
	}
	runners := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := r.List(ctx, runners, client.InNamespace(env.Namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: env.Name}); err != nil {
		return err
	}
	referenced := map[string]bool{}
	for i := range runners.Items {
		referenced[runners.Items[i].Spec.WorkOrderSecretRef.Name] = true
	}
	deadline := time.Duration(env.Spec.OnDemand.Orchestrator.ExpectedSpawnSeconds) * time.Second
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if !strings.HasSuffix(s.Name, selfhostedv1alpha1.WorkOrderSecretSuffix) || referenced[s.Name] {
			continue
		}
		if r.now().Sub(s.CreationTimestamp.Time) < deadline {
			continue
		}
		if err := r.Delete(ctx, s); client.IgnoreNotFound(err) != nil {
			return err
		}
		r.Recorder.Event(env, corev1.EventTypeNormal, "OrphanedWorkOrderDeleted", "deleted unreferenced work-order Secret "+s.Name)
	}
	return nil
}
```

The sweep needs `delete` on Secrets, already granted. Secrets are listed from the cache (cluster-wide informer already exists).

`builders/orchestrator.go`: add `{Name: selfhostedv1alpha1.EnvHookTimeoutSeconds, Value: strconv.Itoa(int(o.HookTimeoutSeconds))}` to the orchestrator env. `conditions.go`: add the constant.

`hook/input.go`: parse `EnvHookTimeoutSeconds` into `HookTimeoutSeconds` (default 60 when unset or 0). Add:

```go
// Deadline is the context budget for one run: the orchestrator's hook
// timeout minus a margin for process start and exit, never below 5 seconds.
func (in Input) Deadline() time.Duration {
	t := in.HookTimeoutSeconds
	if t <= 0 {
		t = 60
	}
	d := time.Duration(t)*time.Second - 10*time.Second
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
```

`cmd/spawn-runner/main.go`: use `in.Deadline()` instead of `hook.Timeout`; delete the `Timeout` constant.

- [ ] **Step 3: Verify and commit**

```bash
make manifests && make test && make lint && git add -A && git commit -m "feat: filtered caches for operator RBAC objects, orphaned work-order sweep, derived hook deadline

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 7: ValidatingAdmissionPolicy for the orchestrator identity

**Files:**
- Create: `config/admission/kustomization.yaml`, `config/admission/orchestrator-policy.yaml`
- Modify: `config/default/kustomization.yaml` (add `../admission`), `test/e2e/on_demand_test.go` (denial spec), `docs/on-demand.md` (short note; the full doc update is Task 13)

**Interfaces:**
- Produces: policies `claude-selfhosted-operator-orchestrator-secrets` and `claude-selfhosted-operator-orchestrator-runners` with bindings of the same names; the `namePrefix` in `config/default` applies, so the installer names are `claude-selfhosted-operator-orchestrator-secrets` etc. only if the manifests omit the prefix; name them `orchestrator-secrets` and `orchestrator-runners` in the files and let the prefix produce the final names.

- [ ] **Step 1: Write the manifests**

`config/admission/orchestrator-policy.yaml`:

```yaml
# Confines every <env>-orchestrator ServiceAccount to its own work orders.
# Requires Kubernetes 1.30 or later (ValidatingAdmissionPolicy GA).
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: orchestrator-secrets
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE", "PATCH", "DELETE"]
        resources: ["secrets"]
  matchConditions:
    - name: orchestrator-service-account
      expression: "request.userInfo.username.startsWith('system:serviceaccount:') && request.userInfo.username.endsWith('-orchestrator')"
  validations:
    - expression: "(request.operation == 'DELETE' ? oldObject : object).metadata.name.endsWith('-work-order')"
      reason: Forbidden
      message: "an orchestrator ServiceAccount may only manage Secrets named *-work-order"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: orchestrator-secrets
spec:
  policyName: orchestrator-secrets
  validationActions: [Deny]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: orchestrator-runners
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["selfhosted.claudecode.dev"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE"]
        resources: ["clauderunners"]
  matchConditions:
    - name: orchestrator-service-account
      expression: "request.userInfo.username.startsWith('system:serviceaccount:') && request.userInfo.username.endsWith('-orchestrator')"
  validations:
    - expression: >-
        has(object.metadata.ownerReferences) &&
        object.metadata.ownerReferences.exists(o,
          o.kind == 'ClaudeEnvironment' && has(o.controller) && o.controller &&
          request.userInfo.username == 'system:serviceaccount:' + object.metadata.namespace + ':' + o.name + '-orchestrator')
      reason: Forbidden
      message: "an orchestrator may only create ClaudeRunners owned by its own ClaudeEnvironment"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: orchestrator-runners
spec:
  policyName: orchestrator-runners
  validationActions: [Deny]
```

`config/admission/kustomization.yaml`:

```yaml
resources:
  - orchestrator-policy.yaml
```

Add `- ../admission` to the `resources` list in `config/default/kustomization.yaml` with a comment: `# Requires Kubernetes 1.30+; remove this line on older clusters.`

Note: the `namePrefix` in `config/default` prefixes the policy and binding names but not the `policyName` reference inside the binding. Kustomize's name reference transformer handles `ValidatingAdmissionPolicyBinding.spec.policyName` only in recent kustomize versions; verify with `make build-installer` that the binding's `policyName` equals the prefixed policy name. If it does not, add to `config/default/kustomization.yaml` a `configurations:` entry with a `nameReference` for `kind: ValidatingAdmissionPolicy` → `fieldSpecs: [{kind: ValidatingAdmissionPolicyBinding, path: spec/policyName}]`.

- [ ] **Step 2: E2E denial spec**

Append to the on-demand Context in `test/e2e/on_demand_test.go`, after the GC spec:

```go
		It("denies the orchestrator identity anything outside its own work orders", func() {
			sa := "system:serviceaccount:" + odNamespace + ":e2e-od-orchestrator"
			_, err := kubectlOD("create", "secret", "generic", "evil", "--from-literal=k=v", "--as", sa)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("orchestrator ServiceAccount may only manage Secrets named *-work-order"))
			_, err = kubectlOD("create", "secret", "generic", "ok-work-order", "--from-literal=jwt=x", "--as", sa)
			Expect(err).NotTo(HaveOccurred())
			_, _ = kubectlOD("delete", "secret", "ok-work-order", "--as", sa)
		})
```

- [ ] **Step 3: Verify and commit**

```bash
make build-installer IMG=example.com/op:dev && grep -c 'ValidatingAdmissionPolicy' dist/install.yaml && git checkout -- config/manager/kustomization.yaml 2>/dev/null; rm -rf dist
make test-e2e 2>&1 | tail -3
git add -A && git commit -m "feat(admission): confine orchestrator identities to their own work orders

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

Expected: 4 occurrences in the installer (2 policies, 2 bindings); e2e 7 of 7.

---

### Task 8: NetworkPolicy per environment

**Files:**
- Create: `internal/builders/networkpolicy.go`, `internal/builders/networkpolicy_test.go`
- Modify: `internal/controller/claudeenvironment_controller.go` (apply or delete after the mode branch; RBAC marker; `Owns(&networkingv1.NetworkPolicy{})`), `test/e2e/testdata/fixed-fleet-stub.yaml` (enable with a CIDR), `test/e2e/fixed_fleet_test.go` (object-shape assertion)
- Test: `internal/builders/networkpolicy_test.go`, `internal/controller/claudeenvironment_fixed_test.go`

**Interfaces:**
- Produces: `builders.NetworkPolicyName(env) string` (`<env>-egress`), `builders.EnvironmentNetworkPolicy(env) *networkingv1.NetworkPolicy`.

- [ ] **Step 1: Builder test**

```go
package builders

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestEnvironmentNetworkPolicy(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, EgressCIDRs: []string{"169.254.0.0/16", "10.0.0.0/8"}}
	np := EnvironmentNetworkPolicy(env)
	if np.Name != "platform-egress" || np.Namespace != "claude" || np.Kind != "NetworkPolicy" {
		t.Fatalf("identity wrong: %+v", np.ObjectMeta)
	}
	if np.Spec.PodSelector.MatchLabels[selfhostedv1alpha1.LabelEnvironment] != "platform" || len(np.Spec.PodSelector.MatchLabels) != 1 {
		t.Fatalf("must select every pod of the environment (runner and orchestrator): %v", np.Spec.PodSelector)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatal("egress-only policy expected")
	}
	if len(np.Spec.Egress) != 2 {
		t.Fatalf("expected a DNS rule and an HTTPS rule, got %d", len(np.Spec.Egress))
	}
	dns := np.Spec.Egress[0]
	if len(dns.Ports) != 2 || *dns.Ports[0].Port != intstr.FromInt32(53) || *dns.Ports[0].Protocol != "UDP" || *dns.Ports[1].Protocol != "TCP" {
		t.Fatalf("DNS rule wrong: %+v", dns.Ports)
	}
	if dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" || dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Fatalf("DNS peer wrong: %+v", dns.To[0])
	}
	https := np.Spec.Egress[1]
	if len(https.To) != 2 || *https.Ports[0].Port != intstr.FromInt32(443) {
		t.Fatalf("HTTPS rule wrong: %+v", https)
	}
	if ex := https.To[0].IPBlock.Except; len(ex) != 1 || ex[0] != "169.254.169.254/32" {
		t.Fatalf("a CIDR containing the metadata endpoint must exclude it: %+v", https.To[0].IPBlock)
	}
	if ex := https.To[1].IPBlock.Except; len(ex) != 0 {
		t.Fatalf("a CIDR not containing the metadata endpoint must have no except (the API server rejects it): %+v", https.To[1].IPBlock)
	}
}

func TestEnvironmentNetworkPolicyNoCIDRsIsDNSOnly(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true}
	np := EnvironmentNetworkPolicy(env)
	if len(np.Spec.Egress) != 1 || np.Spec.Egress[0].Ports[0].Port.IntVal != 53 {
		t.Fatalf("with no CIDRs only DNS may be allowed, got %+v", np.Spec.Egress)
	}
}
```

Import `"k8s.io/apimachinery/pkg/util/intstr"` in the test.

- [ ] **Step 2: Builder**

```go
package builders

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const metadataEndpointCIDR = "169.254.169.254/32"

// containsMetadataEndpoint reports whether cidr covers the cloud metadata
// address. The API server rejects an except block outside its cidr, so the
// exclusion is added only where it is valid. CIDR syntax is validated by CEL.
func containsMetadataEndpoint(cidr string) bool {
	_, block, err := net.ParseCIDR(cidr)
	return err == nil && block.Contains(net.ParseIP("169.254.169.254"))
}

// NetworkPolicyName names the per-environment egress policy.
func NetworkPolicyName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-egress"
}

// EnvironmentNetworkPolicy is a default-deny egress policy for every pod of
// the environment: DNS to kube-dns, TCP 443 to the user's CIDRs with the
// cloud metadata endpoint always excluded. The user supplies the CIDRs for
// api.anthropic.com and the git host; hostnames cannot be expressed here.
func EnvironmentNetworkPolicy(env *selfhostedv1alpha1.ClaudeEnvironment) *networkingv1.NetworkPolicy {
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	dns := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &udp, Port: ptr.To(intstr.FromInt32(53))},
			{Protocol: &tcp, Port: ptr.To(intstr.FromInt32(53))},
		},
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
	}
	egress := []networkingv1.NetworkPolicyEgressRule{dns}
	if np := env.Spec.Runner.NetworkPolicy; np != nil && len(np.EgressCIDRs) > 0 {
		https := networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr.To(intstr.FromInt32(443))}}}
		for _, cidr := range np.EgressCIDRs {
			block := &networkingv1.IPBlock{CIDR: cidr}
			if containsMetadataEndpoint(cidr) {
				block.Except = []string{metadataEndpointCIDR}
			}
			https.To = append(https.To, networkingv1.NetworkPolicyPeer{IPBlock: block})
		}
		egress = append(egress, https)
	}
	labels := map[string]string{selfhostedv1alpha1.LabelEnvironment: env.Name, selfhostedv1alpha1.LabelPartOf: selfhostedv1alpha1.PartOfValue}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: NetworkPolicyName(env), Namespace: env.Namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{selfhostedv1alpha1.LabelEnvironment: env.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      egress,
		},
	}
}
```

Import `corev1 "k8s.io/api/core/v1"` and `"net"`. Add a CEL rule on `NetworkPolicySpec.EgressCIDRs` items: `// +kubebuilder:validation:XValidation:rule="self.all(c, c.matches('^[0-9]{1,3}(\\.[0-9]{1,3}){3}/[0-9]{1,2}$'))",message="egressCIDRs must be IPv4 CIDRs"` (IPv6 is out of scope for v1alpha1; the metadata endpoint exclusion is IPv4 only).

- [ ] **Step 3: Controller and e2e**

In `reconcile`, after the mode branch returns successfully (wrap: compute `res, err := branch(...)`, then if `err == nil` call `r.reconcileNetworkPolicy(ctx, env)`):

```go
func (r *ClaudeEnvironmentReconciler) reconcileNetworkPolicy(ctx context.Context, env *selfhostedv1alpha1.ClaudeEnvironment) error {
	np := env.Spec.Runner.NetworkPolicy
	obj := builders.EnvironmentNetworkPolicy(env)
	if np == nil || !np.Enabled {
		return r.deleteIfOwned(ctx, env, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: obj.Name, Namespace: obj.Namespace}})
	}
	return r.apply(ctx, env, obj)
}
```

Add the marker `// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete` and `Owns(&networkingv1.NetworkPolicy{})` (add the type to `CacheByObject` with the part-of selector). Envtest: enabling the policy creates `<env>-egress` with the right selector; disabling deletes it. E2E: `fixed-fleet-stub.yaml` enables it with `egressCIDRs: ["10.0.0.0/8"]` and the Ready spec asserts the NetworkPolicy exists (kind's default CNI does not enforce it, so the stub keeps working).

- [ ] **Step 4: Verify and commit**

```bash
make manifests && make test && make lint && make test-e2e 2>&1 | tail -3
git add -A && git commit -m "feat: per-environment default-deny egress NetworkPolicy

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 9: Helm chart, PodMonitor, chart contract test

**Files:**
- Create: `dist/chart/**` (generated), `hack/chart-postprocess.sh`, `test/chart/contract_test.go`, `config/prometheus/podmonitor.yaml`
- Modify: `PROJECT` (plugin entry, generated), `Makefile` (`helm-chart`, `test-chart`, `helm-lint`), `config/prometheus/kustomization.yaml`, `.github/workflows/test.yml` (chart lint and contract test), `.gitignore` (ignore `dist/install.yaml`, track `dist/chart`), `README.md` (Helm install)

**Interfaces:**
- Produces: `make helm-chart` (regenerates `dist/chart` from the installer and post-processes toggles), `make helm-lint`, `make test-chart`; chart values `admissionPolicy.enabled`, `prometheus.enabled`, `certManager.enable`, `rbac.namespaced`, `metrics.secure`, `controllerManager.container.image`, `controllerManager.container.resources`, `watchNamespaces`, `tracing.endpoint`, `tracing.sampleRatio`.

- [ ] **Step 1: Generate the chart**

```bash
make manifests build-installer IMG=ghcr.io/ahmadmasry/claude-self-hosted-environment-operator:0.0.0
kubebuilder edit --plugins=helm/v2-alpha
git checkout -- config/manager/kustomization.yaml
ls dist/chart dist/chart/templates
```

Expected: `Chart.yaml`, `values.yaml`, `templates/{crd,rbac,manager,metrics,extras,...}`. `dist/` is not ignored today and `dist/install.yaml` is untracked; add `dist/install.yaml` to `.gitignore` and track `dist/chart`.

- [ ] **Step 2: Post-process toggles and values**

`hack/chart-postprocess.sh` (run by `make helm-chart` after regeneration; idempotent):

```bash
#!/usr/bin/env bash
# Adds Helm conditionals the helm/v2-alpha plugin does not know about:
# the admission policy (needs Kubernetes 1.30) and the runner PodMonitor.
set -euo pipefail
chart=dist/chart
for f in "$chart"/templates/extras/*orchestrator-*.yaml "$chart"/templates/admission/*.yaml; do
  [ -f "$f" ] || continue
  grep -q 'admissionPolicy.enabled' "$f" && continue
  { echo '{{- if .Values.admissionPolicy.enabled }}'; cat "$f"; echo '{{- end }}'; } > "$f.tmp" && mv "$f.tmp" "$f"
done
for f in "$chart"/templates/prometheus/*podmonitor*.yaml "$chart"/templates/extras/*podmonitor*.yaml; do
  [ -f "$f" ] || continue
  grep -q 'prometheus.enabled' "$f" && continue
  { echo '{{- if .Values.prometheus.enabled }}'; cat "$f"; echo '{{- end }}'; } > "$f.tmp" && mv "$f.tmp" "$f"
done
grep -q '^admissionPolicy:' "$chart/values.yaml" || cat >> "$chart/values.yaml" <<'EOV'

# ValidatingAdmissionPolicy confining each orchestrator ServiceAccount to its
# own work orders. Requires Kubernetes 1.30 or later; disable on older clusters.
admissionPolicy:
  enabled: true

# Manager flags surfaced as values.
watchNamespaces: ""   # comma-separated; empty watches all namespaces
tracing:
  endpoint: ""        # OTLP gRPC endpoint; empty disables tracing
  sampleRatio: 0.1
EOV
```

Where the plugin places the admission objects depends on the plugin version (look at `dist/chart/templates` after generation and adjust the two globs so they match; the script must find the files, verify with `grep -l admissionPolicy.enabled -r dist/chart/templates`). The manager Deployment template needs `--watch-namespaces={{ .Values.watchNamespaces }}`, `--tracing-endpoint={{ .Values.tracing.endpoint }}` and `--tracing-sample-ratio={{ .Values.tracing.sampleRatio }}` added to the container args in `templates/manager/manager.yaml` (a preserved file after the first generation, so edit it once by hand and keep it).

`config/prometheus/podmonitor.yaml` (from the product's documented example, labels adjusted):

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: runner-metrics
  namespace: system
  labels:
    app.kubernetes.io/name: claude-selfhosted-operator
spec:
  namespaceSelector:
    any: true
  selector:
    matchLabels:
      app.kubernetes.io/part-of: claude-code-self-hosted-runner
  podMetricsEndpoints:
    - port: health
      path: /metrics
      interval: 30s
```

Add it to `config/prometheus/kustomization.yaml`. The `../prometheus` resource stays commented in `config/default` (kubebuilder convention: users enable it), so the chart's `prometheus.enabled` toggle produces both monitors; verify in the contract test.

Makefile targets:

```make
.PHONY: helm-chart
helm-chart: manifests build-installer ## Regenerate dist/chart from the kustomize installer.
	$(KUBEBUILDER) edit --plugins=helm/v2-alpha --force
	hack/chart-postprocess.sh
	git checkout -- config/manager/kustomization.yaml

.PHONY: helm-lint
helm-lint: ## Lint the chart.
	helm lint dist/chart
	helm lint dist/chart --set admissionPolicy.enabled=false --set prometheus.enabled=true

.PHONY: test-chart
test-chart: ## Run the chart contract test (needs helm on PATH).
	go test -tags chart ./test/chart/... -v
```

`KUBEBUILDER ?= kubebuilder` variable near the other tool variables. Note `--force` regenerates templates but preserves `values.yaml`, `NOTES.txt`, `_helpers.tpl`; the hand-edited `manager.yaml` is NOT preserved by `--force` per the plugin docs, so after `make helm-chart` the three args must be re-applied: put them in the post-process script as a `sed` insertion on `templates/manager/manager.yaml` guarded by a `grep -q tracing-endpoint`.

- [ ] **Step 3: Contract test**

`test/chart/contract_test.go` (`//go:build chart`):

```go
//go:build chart

package chart

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func render(t *testing.T, sets ...string) []unstructured.Unstructured {
	t.Helper()
	args := []string{"template", "rel", filepath.Join(repoRoot(t), "dist", "chart"), "--namespace", "ns"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var objs []unstructured.Unstructured
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var u unstructured.Unstructured
		if err := dec.Decode(&u); err != nil {
			break
		}
		if len(u.Object) > 0 {
			objs = append(objs, u)
		}
	}
	return objs
}

func find(objs []unstructured.Unstructured, kind, nameSuffix string) *unstructured.Unstructured {
	for i := range objs {
		if objs[i].GetKind() == kind && strings.HasSuffix(objs[i].GetName(), nameSuffix) {
			return &objs[i]
		}
	}
	return nil
}

func TestChartRBACMatchesControllerGen(t *testing.T) {
	objs := render(t)
	rendered := find(objs, "ClusterRole", "manager-role")
	if rendered == nil {
		t.Fatal("manager ClusterRole not rendered")
	}
	var got rbacv1.ClusterRole
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, &got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var want rbacv1.ClusterRole
	if err := sigsyaml.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(got.Rules, want.Rules) {
		t.Fatalf("chart ClusterRole rules drifted from controller-gen output:\nchart=%v\nrole.yaml=%v", got.Rules, want.Rules)
	}
}

func TestChartManagerIsRestricted(t *testing.T) {
	objs := render(t)
	d := find(objs, "Deployment", "controller-manager")
	if d == nil {
		t.Fatal("manager Deployment not rendered")
	}
	var dep appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(d.Object, &dep); err != nil {
		t.Fatal(err)
	}
	ps := dep.Spec.Template.Spec
	if ps.SecurityContext == nil || ps.SecurityContext.RunAsNonRoot == nil || !*ps.SecurityContext.RunAsNonRoot || ps.SecurityContext.SeccompProfile == nil {
		t.Fatalf("pod security context not restricted: %+v", ps.SecurityContext)
	}
	for _, c := range ps.Containers {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
			t.Fatalf("container %s not restricted: %+v", c.Name, sc)
		}
		if c.Name == "manager" {
			joined := strings.Join(c.Args, " ")
			for _, flag := range []string{"--hook-image", "--watch-namespaces", "--tracing-endpoint"} {
				if !strings.Contains(joined, flag) && !envHas(c, "OPERATOR_IMAGE") {
					t.Fatalf("manager args/env must carry %s or OPERATOR_IMAGE: %v", flag, c.Args)
				}
			}
		}
	}
}

func envHas(c corev1Container, name string) bool { return false }
```

Replace the last helper with a real check: iterate `c.Env` for `OPERATOR_IMAGE` (import `corev1`), and assert separately that `--watch-namespaces` and `--tracing-endpoint` appear in the args. Then:

```go
func TestChartCRDsMatchGenerated(t *testing.T) {
	objs := render(t)
	dir := filepath.Join(repoRoot(t), "config", "crd", "bases")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var want apiextensionsv1.CustomResourceDefinition
		if err := sigsyaml.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		rendered := find(objs, "CustomResourceDefinition", want.Name)
		if rendered == nil {
			t.Fatalf("CRD %s missing from chart", want.Name)
		}
		var got apiextensionsv1.CustomResourceDefinition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, &got); err != nil {
			t.Fatal(err)
		}
		if !equality.Semantic.DeepEqual(got.Spec, want.Spec) {
			t.Fatalf("CRD %s spec drifted between chart and config/crd/bases", want.Name)
		}
	}
}

func TestChartAdmissionPolicyToggle(t *testing.T) {
	if find(render(t), "ValidatingAdmissionPolicy", "orchestrator-secrets") == nil {
		t.Fatal("admission policy must render by default")
	}
	if find(render(t, "admissionPolicy.enabled=false"), "ValidatingAdmissionPolicy", "orchestrator-secrets") != nil {
		t.Fatal("admission policy must not render when disabled")
	}
	if find(render(t, "prometheus.enabled=true"), "PodMonitor", "runner-metrics") == nil {
		t.Fatal("PodMonitor must render when prometheus is enabled")
	}
}
```

`go get k8s.io/apiextensions-apiserver` if not already a dependency.

- [ ] **Step 4: CI and README**

`test.yml` test job: add steps `uses: azure/setup-helm@<pinned sha>` and `run: make helm-lint test-chart`. README install section: the `helm install` command from the spec plus the namespace label command.

- [ ] **Step 5: Verify and commit**

```bash
make helm-chart && make helm-lint && make test-chart && make test && make lint
git add -A && git commit -m "feat(chart): Helm chart from the kustomize installer with toggles and a contract test

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 10: Upgrade e2e

**Files:**
- Create: `test/e2e/upgrade_test.go`, `hack/build-previous.sh`
- Modify: `test/e2e/e2e_suite_test.go` (read `UPGRADE_FROM_INSTALLER`, `UPGRADE_FROM_IMAGE`; load the previous image into kind), `test/e2e/e2e_test.go` (call `upgradeSpecs()` first), `Makefile` (`test-e2e-upgrade`), `.github/workflows/test-e2e.yml` (upgrade job)

**Interfaces:**
- Consumes: `fixedFleetSpecs()` namespace `claude-e2e` and its stub manifests; `utils.Run`, `utils.LoadImageToKindClusterWithName` from `test/utils`.
- Produces: env vars `UPGRADE_FROM_INSTALLER` (path to a `dist/install.yaml` built from the merge-base revision) and `UPGRADE_FROM_IMAGE` (image tag loaded into kind for that revision). When both are unset, the upgrade specs are skipped with a message.

- [ ] **Step 1: Build the previous revision**

`hack/build-previous.sh`:

```bash
#!/usr/bin/env bash
# Builds the operator image and installer of a previous git revision into
# an out-of-tree worktree, for the upgrade e2e. Usage:
#   hack/build-previous.sh <rev> <image> <out-dir>
set -euo pipefail
rev=$1; image=$2; out=$3
wt=$(mktemp -d)
trap 'git worktree remove --force "$wt"' EXIT
git worktree add --detach "$wt" "$rev"
mkdir -p "$out"
make -C "$wt" docker-build build-installer IMG="$image"
cp "$wt/dist/install.yaml" "$out/install.yaml"
```

- [ ] **Step 2: Upgrade spec**

`test/e2e/upgrade_test.go`:

```go
/*
Copyright 2026.
(Apache 2.0 header as in the other e2e files)
*/

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/test/utils"
)

const upgradeNamespace = "claude-e2e-upgrade"

// upgradeSpecs installs the previous revision's installer, creates an
// environment with it, then applies the current installer and asserts the
// environment stays valid and becomes Ready under the new manager. Runs
// before the other specs because it installs the current manager itself.
func upgradeSpecs() {
	fromInstaller := os.Getenv("UPGRADE_FROM_INSTALLER")
	fromImage := os.Getenv("UPGRADE_FROM_IMAGE")

	Context("Upgrade from the previous revision", func() {
		BeforeAll(func() {
			if fromInstaller == "" || fromImage == "" {
				Skip("UPGRADE_FROM_INSTALLER and UPGRADE_FROM_IMAGE not set")
			}
			By("loading the previous image into kind")
			Expect(utils.LoadImageToKindClusterWithName(fromImage)).To(Succeed())
			By("installing the previous revision")
			cmd := exec.Command("kubectl", "apply", "--server-side", "-f", fromInstaller)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			cmd = exec.Command("kubectl", "rollout", "status", "-n", namespace, "deployment/claude-selfhosted-operator-controller-manager", "--timeout=120s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			By("creating a namespace and environment under the previous revision")
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", upgradeNamespace))
			_, err = utils.Run(exec.Command("kubectl", "label", "ns", upgradeNamespace, "pod-security.kubernetes.io/enforce=restricted", "--overwrite"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "create", "secret", "generic", "-n", upgradeNamespace,
				"claude-env-secret", "--from-literal=environment-secret=ccenvkey_e2e"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-n", upgradeNamespace, "-f", "test/e2e/testdata/fixed-fleet-configmaps.yaml"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "apply", "-n", upgradeNamespace, "-f", "test/e2e/testdata/fixed-fleet-stub.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "claudeenvironment", "e2e", "-n", upgradeNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			if fromInstaller == "" || fromImage == "" {
				return
			}
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", upgradeNamespace, "--ignore-not-found", "--wait=true", "--timeout=120s"))
		})

		It("applies the current installer over the previous one and keeps the environment Ready", func() {
			By("applying the current installer")
			cmd := exec.Command("make", "deploy", "IMG="+managerImage)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			By("waiting for the manager to roll to the new image")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "deployment", "-n", namespace, "claude-selfhosted-operator-controller-manager", "-o", "jsonpath={.spec.template.spec.containers[?(@.name=='manager')].image}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal(managerImage))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
			_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "-n", namespace, "deployment/claude-selfhosted-operator-controller-manager", "--timeout=120s"))
			Expect(err).NotTo(HaveOccurred())
			By("asserting the CRDs carry the current schema and CEL rules")
			out, err := utils.Run(exec.Command("kubectl", "get", "crd", "claudeenvironments.selfhosted.claudecode.dev", "-o", "jsonpath={.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.runner.x-kubernetes-validations}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("baseDir"), "the runner CEL rules from this revision must be present after upgrade")
			By("asserting the runner StatefulSet converges under the new manager")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "statefulset", "-n", upgradeNamespace, "-l", "selfhosted.claudecode.dev/environment=e2e", "-o", "jsonpath={.items[0].status.currentRevision} {.items[0].status.updateRevision} {.items[0].status.readyReplicas} {.items[0].spec.replicas}"))
				g.Expect(err).NotTo(HaveOccurred())
				f := strings.Fields(out)
				g.Expect(f).To(HaveLen(4))
				g.Expect(f[0]).To(Equal(f[1]), "rollout not finished")
				g.Expect(f[2]).To(Equal(f[3]), "not all replicas ready")
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
			By("asserting the pre-existing environment stays Ready and valid")
			Consistently(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "claudeenvironment", "e2e", "-n", upgradeNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 60*time.Second, 5*time.Second).Should(Succeed())
			events, err := utils.Run(exec.Command("kubectl", "get", "events", "-n", upgradeNamespace, "--field-selector=reason=FailedCreate", "-o", "name"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(events)).To(BeEmpty())
			By("asserting the new manager still rejects an invalid environment")
			_, err = utils.Run(exec.Command("kubectl", "apply", "-n", upgradeNamespace, "-f", "test/e2e/testdata/invalid-env.yaml"))
			Expect(err).To(HaveOccurred())
		})
	})
}
```

`test/e2e/testdata/invalid-env.yaml`: a ClaudeEnvironment with `image: "nginx"` (no tag) so the Task 4 CEL rule rejects it. In `e2e_test.go` the `Manager` Describe calls `upgradeSpecs()` before `fixedFleetSpecs()`. The suite's `BeforeSuite` already runs `make deploy` for the current image; the upgrade spec's `make deploy` is therefore a second apply over the previous revision when the upgrade env vars are set. To make the ordering real, the suite must NOT deploy the current revision when `UPGRADE_FROM_INSTALLER` is set (guard the existing `make deploy` call with `if os.Getenv("UPGRADE_FROM_INSTALLER") == ""`); the upgrade spec deploys it, and the following fixed-fleet and on-demand specs then run against the upgraded manager.

- [ ] **Step 3: Makefile and CI**

```make
UPGRADE_FROM ?= $(shell git merge-base origin/master HEAD 2>/dev/null || git rev-parse HEAD~1)
.PHONY: test-e2e-upgrade
test-e2e-upgrade: setup-test-e2e manifests generate fmt vet ## Run e2e including the upgrade specs from UPGRADE_FROM (default: merge-base with master).
	hack/build-previous.sh $(UPGRADE_FROM) example.com/claude-selfhosted-operator:previous $(CURDIR)/bin/previous
	UPGRADE_FROM_INSTALLER=$(CURDIR)/bin/previous/install.yaml UPGRADE_FROM_IMAGE=example.com/claude-selfhosted-operator:previous \
		KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e
```

`test-e2e.yml`: add a job `upgrade` identical to the existing one but with `fetch-depth: 0` on checkout and `run: make test-e2e-upgrade`. On `master` pushes `UPGRADE_FROM` falls back to `HEAD~1`, which is the previous merged state.

- [ ] **Step 4: Verify and commit**

```bash
make test-e2e-upgrade 2>&1 | tail -5
git add -A && git commit -m "test(e2e): upgrade from the previous revision keeps environments valid and Ready

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

---

### Task 11: Real-environment test (reply sink, CI runner image, script, workflow, docs)

**Files:**
- Create: `test/replysink/main.go`, `test/replysink/main_test.go`, `test/replysink/Dockerfile`, `test/real-e2e/replysink.yaml`, `test/real-e2e/runner.Dockerfile`, `test/real-e2e/host-config.yaml`, `test/real-e2e/capture-reply.sh`, `test/real-e2e/environment.yaml`, `test/real-e2e/orchestrator-secret.yaml.tmpl`, `hack/real-session-test.sh`, `.github/workflows/real-e2e.yml`, `docs/testing.md`
- Modify: `Makefile` (`replysink-image`, `real-runner-image`, `real-session-test`), `.gitignore` (nothing secret is written to disk; the script uses env vars only)

**Interfaces:**
- Consumes: operator installer (`make deploy IMG=`), `examples/runner-image/Dockerfile` as the base of the real runner image; product CLI: `claude auth login` honouring `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES`; `claude -p "<prompt>" --environment <id> --ref <ref> --output-format json`.
- Produces: `make real-session-test` (needs `CLAUDE_ENVIRONMENT_KEY`, `CLAUDE_ENVIRONMENT_ID`, optional `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`, `CLAUDE_CODE_OAUTH_SCOPES`, `TEST_REPO`, `TEST_REF`); env var contract `E2E_REPLY_URL` consumed by the Stop hook.

- [ ] **Step 1: Reply sink**

`test/replysink/main.go`:

```go
// replysink is a tiny HTTP service for the real-environment test. Runner
// Stop hooks POST their last assistant message to /<session_id>; the test
// GETs the same path. It is in-memory and single-replica by design.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type sink struct {
	mu      sync.Mutex
	replies map[string][]byte
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(r.URL.Path, "/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "path must be /<session_id>", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.replies[id] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		s.mu.Lock()
		body, ok := s.replies[id]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: &sink{replies: map[string][]byte{}}, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("replysink listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
```

`main_test.go`: `httptest.NewRecorder` round trip: GET unknown → 404; POST `/s1` body `{"reply":"hi"}` → 204; GET `/s1` → 200 with the same body; POST `/` → 400. `test/replysink/Dockerfile`: multi-stage `golang:1.27` build of `./test/replysink` with `CGO_ENABLED=0`, final `gcr.io/distroless/static:nonroot`, `USER 65532:65532`. `test/real-e2e/replysink.yaml`: Deployment (1 replica, Restricted security context, port 8080 named `http`, resources 50m/64Mi) and Service `replysink` port 8080, both in the test namespace with `app.kubernetes.io/name: replysink`.

- [ ] **Step 2: Real runner image and host config**

The operator mounts an emptyDir over `/home/runner` and (Task 4) forbids user env vars with operator-owned names, so the Stop hook reaches the runner through the existing `runner.hostConfig` feature: a ConfigMap mounted read-only (mode 0444) at `/etc/claude/host-config` with `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR` set by the operator. The hook is invoked through `bash` because the mount is not executable.

`test/real-e2e/host-config.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: real-host-config
data:
  settings.json: |
    {
      "hooks": {
        "Stop": [
          { "hooks": [ { "type": "command", "command": "bash /etc/claude/host-config/capture-reply.sh" } ] }
        ]
      }
    }
  capture-reply.sh: |
    #!/usr/bin/env bash
    # Stop hook: posts the last assistant message of the transcript to the
    # reply sink keyed by session id. Fails open: never blocks the session.
    set -uo pipefail
    input=$(cat)
    session_id=$(printf '%s' "$input" | jq -r '.session_id')
    transcript=$(printf '%s' "$input" | jq -r '.transcript_path')
    [ -n "$session_id" ] && [ -f "$transcript" ] || exit 0
    reply=$(jq -c 'select(.type=="assistant") | .message.content[]? | select(.type=="text") | .text' "$transcript" | tail -n 1)
    printf '{"session_id":%s,"reply":%s}' "$(printf '%s' "$session_id" | jq -R .)" "${reply:-\"\"}" \
      | curl -sS -m 10 -X POST -H 'Content-Type: application/json' --data-binary @- "${E2E_REPLY_URL:?}/$session_id" || true
    exit 0
```

Keep a copy of the script at `test/real-e2e/capture-reply.sh` for `shellcheck`, and add a unit-style check in the verify step that the ConfigMap's copy equals the file (`diff <(yq '.data["capture-reply.sh"]' test/real-e2e/host-config.yaml) test/real-e2e/capture-reply.sh`, or a `python3 -c` with PyYAML if `yq` is absent).

`test/real-e2e/runner.Dockerfile` (the base is the project's example image: Debian bookworm, runner UID 10001, `git` and `curl` present, `jq` absent):

```dockerfile
# Real-environment test runner: the example runner image plus jq for the
# Stop hook. Built in CI and never published: it bundles Anthropic's binary.
ARG BASE=example.com/claude-runner:real-e2e
FROM ${BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends jq && rm -rf /var/lib/apt/lists/*
USER 10001:10001
```

`test/real-e2e/environment.yaml` (field names as in `api/v1alpha1/claudeenvironment_types.go`: the mode is implied by `onDemand`, settings live under `runner.settings`, resources under `runner.podTemplate`):

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: real
spec:
  environmentSecretRef:
    name: real-environment-key
    key: environment-secret
  runner:
    image: ${RUNNER_IMAGE}
    settings:
      releaseIdleSessionMinutes: 1
      killSessionAfterMinutes: 5
    hostConfig:
      name: real-host-config
    env:
      - name: E2E_REPLY_URL
        value: http://replysink.${NAMESPACE}.svc:8080
    podTemplate:
      resources:
        requests: { cpu: 500m, memory: 1Gi }
        limits: { cpu: "2", memory: 4Gi }
  onDemand:
    orchestrator:
      expectedSpawnSeconds: 180
    runnerTTLSecondsAfterFinished: 120
```

`test/real-e2e/orchestrator-secret.yaml.tmpl`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: real-environment-key
stringData:
  environment-secret: ${CLAUDE_ENVIRONMENT_KEY}
```

It is rendered with `envsubst` straight into `kubectl apply -f -`; nothing secret is written to disk.

- [ ] **Step 3: The script**

`hack/real-session-test.sh`:

```bash
#!/usr/bin/env bash
# Real-environment test. Level 1 (always): the orchestrator registers with
# Anthropic using CLAUDE_ENVIRONMENT_KEY and reports connected. Level 2
# (when CLAUDE_CODE_OAUTH_REFRESH_TOKEN is set, or an interactive
# `claude auth login` already exists): start a session against the
# environment and check the reply, the ClaudeRunner lifecycle and GC.
# Needs: kubectl against the Makefile-owned kind cluster, docker, claude.
set -euo pipefail
: "${CLAUDE_ENVIRONMENT_KEY:?}" "${CLAUDE_ENVIRONMENT_ID:?}"
NAMESPACE=${NAMESPACE:-claude-real-e2e}
RUNNER_IMAGE=${RUNNER_IMAGE:-example.com/claude-runner:real-e2e-test}
TEST_REPO=${TEST_REPO:-}
TEST_REF=${TEST_REF:-main}
export NAMESPACE RUNNER_IMAGE

echo "== namespace and sink"
kubectl create ns "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl label ns "$NAMESPACE" pod-security.kubernetes.io/enforce=restricted --overwrite
kubectl apply -n "$NAMESPACE" -f test/real-e2e/replysink.yaml
kubectl rollout status -n "$NAMESPACE" deployment/replysink --timeout=120s
kubectl apply -n "$NAMESPACE" -f test/real-e2e/host-config.yaml
envsubst < test/real-e2e/orchestrator-secret.yaml.tmpl | kubectl apply -n "$NAMESPACE" -f -
envsubst '$RUNNER_IMAGE $NAMESPACE' < test/real-e2e/environment.yaml | kubectl apply -n "$NAMESPACE" -f -

echo "== level 1: orchestrator connected"
kubectl wait -n "$NAMESPACE" claudeenvironment/real --for=condition=Ready=True --timeout=300s
kubectl get -n "$NAMESPACE" claudeenvironment/real -o jsonpath='{.status.conditions}' | jq .

if [ -z "${CLAUDE_CODE_OAUTH_REFRESH_TOKEN:-}" ] && ! claude auth status >/dev/null 2>&1; then
  echo "== level 2 skipped: no CLAUDE_CODE_OAUTH_REFRESH_TOKEN and no interactive login"
  exit 0
fi
[ -n "$TEST_REPO" ] || { echo "TEST_REPO is required for level 2" >&2; exit 2; }
if [ -n "${CLAUDE_CODE_OAUTH_REFRESH_TOKEN:-}" ]; then
  echo "== level 2: logging in with the refresh token"
  claude auth login
fi

echo "== level 2: starting a session"
pf_log=$(mktemp)
kubectl port-forward -n "$NAMESPACE" svc/replysink 18080:8080 >"$pf_log" 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
sleep 2
marker="operator-real-e2e-$(date +%s)"
result=$(claude -p "Reply with exactly the text: $marker" --environment "$CLAUDE_ENVIRONMENT_ID" --repo "$TEST_REPO" --ref "$TEST_REF" --output-format json)
session_id=$(printf '%s' "$result" | jq -r '.session_id')
echo "session $session_id"
[ -n "$session_id" ] && [ "$session_id" != null ]

echo "== level 2: runner lifecycle"
for _ in $(seq 1 60); do
  phase=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ -n "$phase" ] && break
  sleep 5
done
echo "runner phase: ${phase:-none}"
[ -n "${phase:-}" ]

echo "== level 2: reply captured"
for _ in $(seq 1 60); do
  if reply=$(curl -sf "http://127.0.0.1:18080/$session_id"); then break; fi
  sleep 5
done
echo "$reply" | jq .
printf '%s' "$reply" | jq -e --arg m "$marker" '.reply | contains($m)' >/dev/null

echo "== level 2: runner reaches Succeeded and is garbage collected"
kubectl wait -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s
for _ in $(seq 1 40); do
  n=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o name | wc -l | tr -d ' ')
  [ "$n" = 0 ] && break
  sleep 5
done
[ "$n" = 0 ]
echo "== real-environment test passed"
```

Verify the exact `claude -p` flags for routing a session to an environment, the repository and the ref against the product reference page (`code.claude.com/docs/en/self-hosted-environments` and the CLI reference) before finalising; the spec addendum records `--environment <ccpool_id> --ref <ref>`, and the repository flag name must be taken from the reference page. Also confirm the `claude auth login` non-interactive behaviour with `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` on the docs page referenced in the spec addendum section 5.1; if the CLI requires a different subcommand, use that one and update the spec addendum.

Makefile:

```make
REPLYSINK_IMG ?= example.com/replysink:real-e2e
REAL_RUNNER_IMG ?= example.com/claude-runner:real-e2e-test
CLAUDE_CODE_VERSION ?= $(shell curl -fsSL https://downloads.claude.ai/claude-code-releases/stable)
.PHONY: replysink-image real-runner-image real-session-test
replysink-image: ## Build the reply sink used by the real-environment test.
	$(CONTAINER_TOOL) build -t $(REPLYSINK_IMG) -f test/replysink/Dockerfile .
real-runner-image: ## Build the real runner image (never pushed).
	$(CONTAINER_TOOL) build -t example.com/claude-runner:real-e2e --build-arg CLAUDE_CODE_VERSION=$(CLAUDE_CODE_VERSION) examples/runner-image
	$(CONTAINER_TOOL) build -t $(REAL_RUNNER_IMG) --build-arg BASE=example.com/claude-runner:real-e2e -f test/real-e2e/runner.Dockerfile .
real-session-test: setup-test-e2e docker-build replysink-image real-runner-image ## Deploy to the kind cluster and run hack/real-session-test.sh.
	$(KIND) load docker-image $(IMG) $(REPLYSINK_IMG) $(REAL_RUNNER_IMG) --name $(KIND_CLUSTER)
	$(MAKE) deploy IMG=$(IMG)
	RUNNER_IMAGE=$(REAL_RUNNER_IMG) hack/real-session-test.sh
```

`test/real-e2e/replysink.yaml` uses image `example.com/replysink:real-e2e` with `imagePullPolicy: IfNotPresent` (loaded into kind).

- [ ] **Step 4: Workflow**

`.github/workflows/real-e2e.yml`:

```yaml
name: real-e2e
on:
  workflow_dispatch:
    inputs:
      test_repo:
        description: Repository the session checks out (owner/name); needed for level 2
        required: false
        default: ""
      ref:
        description: Git ref for the session
        required: false
        default: main
      keep_cluster:
        description: Keep the kind cluster after the run (self-hosted runners only)
        required: false
        default: "false"
jobs:
  real:
    runs-on: ubuntu-latest
    timeout-minutes: 45
    permissions:
      contents: read
    env:
      KIND_CLUSTER: claude-selfhosted-operator-real-e2e
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
      - uses: actions/setup-go@4b73464bb391d4059bd26b0524d20df3927bd417 # v6.3.0
        with:
          go-version-file: go.mod
      - name: Guard secrets
        run: |
          [ -n "${{ secrets.CLAUDE_ENVIRONMENT_KEY }}" ] || { echo "CLAUDE_ENVIRONMENT_KEY missing"; exit 1; }
          [ -n "${{ secrets.CLAUDE_ENVIRONMENT_ID }}" ] || { echo "CLAUDE_ENVIRONMENT_ID missing"; exit 1; }
      - name: Install the latest version of kind
        run: |
          curl -Lo ./kind https://kind.sigs.k8s.io/dl/latest/kind-linux-$(go env GOARCH)
          chmod +x ./kind
          sudo mv ./kind /usr/local/bin/kind
      - name: Install claude CLI
        run: curl -fsSL https://claude.ai/install.sh | bash && echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - name: Run
        env:
          CLAUDE_ENVIRONMENT_KEY: ${{ secrets.CLAUDE_ENVIRONMENT_KEY }}
          CLAUDE_ENVIRONMENT_ID: ${{ secrets.CLAUDE_ENVIRONMENT_ID }}
          CLAUDE_CODE_OAUTH_REFRESH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_REFRESH_TOKEN }}
          CLAUDE_CODE_OAUTH_SCOPES: ${{ secrets.CLAUDE_CODE_OAUTH_SCOPES }}
          TEST_REPO: ${{ inputs.test_repo }}
          TEST_REF: ${{ inputs.ref }}
        run: make real-session-test IMG=example.com/claude-selfhosted-operator:real-e2e
      - name: Diagnostics
        if: failure()
        run: |
          kubectl get claudeenvironments,clauderunners,pods -A -o wide || true
          kubectl logs -n claude-selfhosted-operator-system deployment/claude-selfhosted-operator-controller-manager --tail=300 || true
          kubectl logs -n claude-real-e2e -l selfhosted.claudecode.dev/environment=real --all-containers --tail=200 || true
      - name: Teardown
        if: always() && inputs.keep_cluster != 'true'
        run: make cleanup-test-e2e
```

The action SHAs are the ones already used in `test.yml`. Verify the CLI install command against the product's setup page before committing. The runner image build pulls Anthropic's binary via `examples/runner-image/Dockerfile`; nothing is pushed.

- [ ] **Step 5: `docs/testing.md`**

Sections: test levels (unit, envtest, kind e2e, upgrade e2e, real-environment test levels 1 and 2); how to run each; the real-environment prerequisites (a dedicated environment in a Team or Enterprise org used only for this test; the org's GitHub connection must cover `test_repo`; the two secrets; the optional refresh token: mint with `claude setup-token`-style flow documented on the product page, store `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` as Actions secrets, renew every 30 days); the human recipe `make real-session-test` with an interactive `claude auth login` instead of the token; what each level proves; cost note (one short session per run); the `keep_cluster` input; and the rule that the real runner image is never published. Write the minting steps from the product's documentation page on ephemeral CI credentials (fetch it during the task; quote the command names exactly).

- [ ] **Step 6: Verify and commit**

```bash
go test ./test/replysink/... && make replysink-image real-runner-image && shellcheck hack/real-session-test.sh hack/build-previous.sh hack/chart-postprocess.sh test/real-e2e/capture-reply.sh && make lint
git add -A && git commit -m "test: real-environment test with reply sink, two levels and a human recipe

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

The workflow itself is exercised by the user from the Actions tab after merge (Level 1 with the two secrets already stored; Level 2 once the token secrets exist). Record in the PR description that the run is pending.

---

### Task 12: Release workflow (dry run only)

**Files:**
- Create: `.github/workflows/release.yml`, `docs/release.md`
- Modify: `Makefile` (`helm-package`), `dist/chart/Chart.yaml` (version placeholder handling in the workflow, not by hand)

**Interfaces:**
- Produces: workflow `release` on `push: tags: ['v*']` and `workflow_dispatch` with `dry_run` (default `true`) and `version` inputs. Dry run builds everything and uploads artifacts but pushes no image, no chart, and creates no GitHub Release.

- [ ] **Step 1: Workflow**

```yaml
name: release
on:
  push:
    tags: ["v*"]
  workflow_dispatch:
    inputs:
      version:
        description: Version to build (e.g. 0.1.0); used when not running from a tag
        required: true
      dry_run:
        description: Build and verify only; push nothing
        required: false
        default: "true"
permissions:
  contents: read
jobs:
  release:
    runs-on: ubuntu-latest
    timeout-minutes: 40
    permissions:
      contents: write      # GitHub Release
      packages: write      # GHCR image and chart
      id-token: write      # keyless cosign
    env:
      IMAGE: ghcr.io/ahmadmasry/claude-self-hosted-environment-operator
      CHART_REPO: oci://ghcr.io/ahmadmasry/charts
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
      - uses: actions/setup-go@4b73464bb391d4059bd26b0524d20df3927bd417 # v6.3.0
        with:
          go-version-file: go.mod
      - id: ver
        run: |
          if [[ "$GITHUB_REF" == refs/tags/v* ]]; then v="${GITHUB_REF#refs/tags/v}"; dry=false; else v="${{ inputs.version }}"; dry="${{ inputs.dry_run }}"; fi
          echo "version=$v" >> "$GITHUB_OUTPUT"; echo "major_minor=${v%.*}" >> "$GITHUB_OUTPUT"; echo "dry=$dry" >> "$GITHUB_OUTPUT"
      - run: make test
      - uses: docker/setup-qemu-action@<sha>   # v3
      - uses: docker/setup-buildx-action@<sha> # v3
      - uses: sigstore/cosign-installer@<sha>  # v3
      - uses: anchore/sbom-action/download-syft@<sha> # v0
      - uses: azure/setup-helm@<sha>           # v4
      - name: Login to GHCR
        if: steps.ver.outputs.dry == 'false'
        uses: docker/login-action@<sha>        # v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Build image (multi-arch)
        id: build
        uses: docker/build-push-action@<sha>   # v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: ${{ steps.ver.outputs.dry == 'false' }}
          tags: ${{ env.IMAGE }}:v${{ steps.ver.outputs.version }},${{ env.IMAGE }}:v${{ steps.ver.outputs.major_minor }}
          build-args: VERSION=${{ steps.ver.outputs.version }}
          provenance: false
      - name: Sign image
        if: steps.ver.outputs.dry == 'false'
        run: cosign sign --yes ${{ env.IMAGE }}@${{ steps.build.outputs.digest }}
      - name: SBOM
        run: syft packages dir:. -o spdx-json > sbom.spdx.json
      - name: Attest SBOM
        if: steps.ver.outputs.dry == 'false'
        run: cosign attest --yes --type spdxjson --predicate sbom.spdx.json ${{ env.IMAGE }}@${{ steps.build.outputs.digest }}
      - name: Installer and chart
        run: |
          make build-installer IMG=${{ env.IMAGE }}:v${{ steps.ver.outputs.version }}
          make helm-chart IMG=${{ env.IMAGE }}:v${{ steps.ver.outputs.version }}
          helm package dist/chart --version ${{ steps.ver.outputs.version }} --app-version ${{ steps.ver.outputs.version }} -d out
          make helm-lint test-chart
      - name: Push chart
        if: steps.ver.outputs.dry == 'false'
        run: helm push out/claude-selfhosted-operator-${{ steps.ver.outputs.version }}.tgz ${{ env.CHART_REPO }}
      - name: Upload dry-run artifacts
        if: steps.ver.outputs.dry == 'true'
        uses: actions/upload-artifact@<sha>    # v4
        with:
          name: release-${{ steps.ver.outputs.version }}
          path: |
            dist/install.yaml
            out/*.tgz
            sbom.spdx.json
      - name: GitHub Release
        if: steps.ver.outputs.dry == 'false'
        uses: softprops/action-gh-release@<sha> # v2
        with:
          files: |
            dist/install.yaml
            out/*.tgz
            sbom.spdx.json
          generate_release_notes: true
```

Image tags are `vX.Y.Z` and `vX.Y`; no `latest` tag is pushed. In the Makefile set `VERSION ?= $(shell git describe --tags --always --dirty)` so local builds and the release carry the same version string (Task 5 made the build arg conditional on `VERSION`). Resolve every `<sha>` to the current release commit of that action (`gh api repos/<owner>/<repo>/git/ref/tags/<tag>`) and write the version tag in the trailing comment, matching the pin style of `test.yml`. `make helm-chart` must accept `IMG` so the chart's default image tag is the release tag (the installer already does; the chart plugin derives from it).

- [ ] **Step 2: `docs/release.md`**

Checklist for the maintainer: confirm the API group decision is settled (link the base spec section) before the first tag; run `real-e2e` (both levels) and the dry-run release from the Actions tab; tag `vX.Y.Z` on `master`; verify the image signature with `cosign verify --certificate-identity-regexp 'https://github.com/AhmadMasry/claude-self-hosted-environment-operator/.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com ghcr.io/ahmadmasry/claude-self-hosted-environment-operator:vX.Y.Z` and `cosign verify-attestation --type spdxjson` with the same identity flags; verify the chart pulls with `helm pull oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator --version X.Y.Z`; GHCR packages must be set public once after the first push (Settings → Packages); and that no `latest` image tag exists: consumers pin `vX.Y.Z` or `vX.Y`.

- [ ] **Step 3: Verify and commit**

```bash
actionlint .github/workflows/*.yml   # go install github.com/rhysd/actionlint/cmd/actionlint@latest if missing
git add -A && git commit -m "ci: release workflow with dry run, multi-arch image, cosign, SBOM and OCI chart

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

After the branch is pushed, trigger `release` from the Actions tab with `version=0.0.0-dryrun` and `dry_run=true` on the PR branch (workflow_dispatch allows a branch choice) and confirm the artifacts upload. Report the run URL in the PR.

---

### Task 13: Documentation set and final consistency

**Files:**
- Create: `docs/hardening.md`, `docs/upgrade.md`, `docs/metrics.md`, `TROUBLESHOOTING.md`
- Modify: `README.md` (install options: installer, Helm; links to every doc; security section pointer), `docs/on-demand.md` (admission policy, orphan sweep, hook timeout env), the README configuration section (networkPolicy, new CEL rules, reserved paths and env names, `--watch-namespaces`, tracing values), `CHANGELOG.md` (Unreleased section listing Plans 1–3 by area), `docs/superpowers/plans/2026-10-02-foundation-followups.md` and `2026-10-02-on-demand-followups.md` (mark each item Done with the task number, or Won't-do with the ruling)

- [ ] **Step 1: Write the docs**

`docs/hardening.md`: Anthropic's hardening checklist from the self-hosted production page mapped item by item to the operator field or default that satisfies it (fetch the page during the task; one table row per checklist item); threat model summary (orchestrator identity, runner pods, operator); Restricted PSS with the namespace label command; the ValidatingAdmissionPolicy (what it blocks, the 1.30 floor, how to disable in the chart and in kustomize); NetworkPolicy (enable, the CIDR inputs for `api.anthropic.com` and the git host, the metadata-endpoint exclusion, the fact that hostnames cannot be expressed and a DNS-aware CNI policy is the alternative); Secret hygiene (environment key Secret, work-order Secrets, the orphan sweep); RBAC scope (`--watch-namespaces`, cluster-wide Secret and ConfigMap caches explained with the label selectors); image supply chain (signed operator image, SBOM, cosign verify command; the runner image is yours to build).

`docs/upgrade.md`: supported path (N-1 to N, tested by the upgrade e2e); procedure for installer users (`kubectl apply --server-side -f dist/install.yaml` of the new version, CRDs first by the installer order); for Helm users (`helm upgrade`, note that Helm does not upgrade CRDs in `crds/` but this chart renders them in `templates/crd`, so they are upgraded); what changes with a CRD schema tightening (existing objects stay valid; new CEL rules are enforced only on write); rollback (reapply the previous installer; status fields unknown to the old version are dropped).

`docs/metrics.md`: every operator metric with type, labels and meaning (the six from the summary plus controller-runtime's defaults); the ServiceMonitor and PodMonitor; alert rules merged with the product's documented examples (fetch them during the task) plus operator-specific ones (environment not Ready for 10m; `runners_total{outcome="failed"}` rate; spawn duration p95 above `expectedSpawnSeconds`); the Grafana panel suggestions as PromQL lines.

`TROUBLESHOOTING.md`: symptoms → cause → fix table: `FleetAvailable=False/WorkloadApplyFailed`; orchestrator never Ready (key invalid, egress blocked, probe); runner `FailedStart` with the redacted log tail; `SpawnTimeout`; `PodLost`; `EnvironmentMismatch`; admission denial messages; `OrphanedWorkOrderDeleted` events; PSS violations (`pods "x" is forbidden: violates PodSecurity`); `--watch-namespaces` mistakes; how to raise log level and enable tracing.

- [ ] **Step 2: Consistency pass**

```bash
grep -rn 'nightly' docs README.md .github | grep -v superpowers   # must be empty
grep -rn 'example.com/claude-selfhosted-operator' docs README.md  # only in dev/test instructions
grep -rln 'ValidatingAdmissionPolicy' docs README.md config dist/chart | sort
go run ./hack/... 2>/dev/null; make manifests generate && git diff --exit-code config api
make test lint helm-lint test-chart && make test-e2e 2>&1 | tail -3
```

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "docs: hardening, upgrade, metrics, troubleshooting and follow-up closure

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>" && git push
```

Mark the PR ready for review with a body that lists: every follow-up item closed with its task, the won't-do rulings, the pending user actions (run `real-e2e` Level 1 now; mint the token for Level 2; set GHCR packages public after the first real release), and the reminder that no tag is created by this plan.

---

## Deferred and out of scope

- **Release tag.** No `vX.Y.Z` tag is created. The user brings the API-group decision first; `docs/release.md` makes that the first checklist item.
- **Runner image publication.** Never; the real-e2e and stub images stay local to the CI job or the kind cluster.
- **Scheduled real-environment runs.** Manual only. A `schedule:` trigger can be added to `real-e2e.yml` later if the user wants a cadence.
- **Won't-do rulings (approved):** `enforce-version: latest` on sample namespaces; redelivery keeps the first JWT.
- **Hostname-based egress.** NetworkPolicy takes CIDRs only; DNS-aware policies are CNI specific and documented as an alternative in `docs/hardening.md`.
- **Multi-version API / conversion webhook.** Not needed until a `v1beta1` exists.
- **OLM bundle.** Not requested; the Helm chart and installer cover distribution.
