# Plan 3 Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every item the Plan 3 final review parked, on branch `feat/hardening-release`, before it merges.

**Architecture:** Four bounded tasks: controller security fixes (work-order provenance, pod adoption, sweep deadline, exact failure accounting, NetworkPolicy delete errors, FleetAvailable event); API, chart and release hygiene (orchestrator env cap, namespaced-mode API-server egress via a flag, semver rule, helm login, stale doc lines); tests and hygiene (headers, missing envtests, sync tests, test-only metrics helper, e2e leftover binding, on-demand upgrade e2e); scripts and CI (shellcheck, hook-copy check, real-session script hygiene, image pins, example runner arch). Each task ends green on `make lint test` and the relevant e2e.

**Tech Stack:** Go 1.26, kubebuilder v4.16, controller-runtime v0.25, envtest 1.37, Ginkgo/Gomega, kind e2e, Helm (helm/v2-alpha chart with `hack/chart-postprocess.sh`), GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-02-plan3-hardening-release-design.md` (plus the final-review triage recorded in the Plan 3 ledger). Items the review declined to judge stay out: the hook concurrency-cap race, CEL `isCIDR` (needs a 1.31 floor), the API group.

## Global Constraints

- Branch `feat/hardening-release`, draft PR #1; commit with the trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`; push after each task (no force-push); `git add` specific paths.
- No release tag. No image or chart pushed. Never trigger a workflow with `dry_run=false`. Only the Makefile-owned kind cluster (`claude-selfhosted-operator-test-e2e`); never `agent-mesh-lab`.
- Generated files (`config/crd/bases/*`, `config/rbac/role.yaml`, `**/zz_generated.*`, `PROJECT`) only via `make manifests generate`; the chart only via `make helm-chart`; restore `config/manager/kustomization.yaml` and remove `dist/install.yaml` before committing.
- Restricted Pod Security Standard stays intact; reconcilers read time via `r.now()`; no `RequeueAfter` together with an error; Kubernetes log style.
- Secrets never written to disk or logged. The API group is not discussed.
- Rulings already made: namespaced-RBAC chart mode gets a manager flag `--apiserver-endpoints` (Task 2); real-e2e diagnostics drop the runner and orchestrator log tails and keep the manager logs (Task 4); the upgrade e2e gains an on-demand environment (Task 3).
- Gates per task: `make manifests generate && git diff --exit-code config api`, `make lint`, `make test`, `go test -race ./internal/... ./cmd/...` (`KUBEBUILDER_ASSETS` from `bin/setup-envtest use 1.37 --bin-dir $PWD/bin -p path`), and where noted `make helm-lint test-chart`, `actionlint`, `shellcheck`, `make test-e2e` / `make test-e2e-upgrade`.

## Review Focus

1. A work-order Secret created by another environment's orchestrator but referenced by a runner must never be mounted — Task 1 provenance envtest.
2. A pod with the runner's name but a different controller must not be adopted — Task 1 adoption envtest.
3. `--apiserver-endpoints` set to an invalid entry must fail the manager at startup, not at reconcile — Task 2 options test.
4. A prerelease or `+build` version must never produce an invalid Docker tag — Task 2 workflow logic (`+build` refused).
5. The on-demand upgrade case must prove the orchestrator rolls to the new env var without the environment leaving Ready — Task 3 upgrade e2e.

---

### Task 1: Controller security and accounting fixes

**Files:**
- Modify: `internal/controller/clauderunner_controller.go`, `internal/controller/claudeenvironment_controller.go`, `internal/controller/status.go`, `api/v1alpha1/conditions.go`
- Test: `internal/controller/clauderunner_controller_test.go`, `internal/controller/claudeenvironment_ondemand_test.go`, `internal/controller/claudeenvironment_fixed_test.go`

**Interfaces:**
- Produces: `ReasonWorkOrderMismatch = "WorkOrderMismatch"` (ClaudeRunner terminal failure reason); `ReasonPodMismatch = "PodMismatch"`.

- [ ] **Step 1: Work-order provenance** — in the ClaudeRunner reconcile, after the work-order Secret is found (the existing `Reader` get) and before `createPod`, require: label `selfhosted.claudecode.dev/environment == runner.Spec.EnvironmentRef.Name` AND the Secret is controller-owned by either the ClaudeEnvironment (name+UID of the env the runner resolved) or this ClaudeRunner (UID). Otherwise `fail(runner, ReasonWorkOrderMismatch, "work-order Secret <name> does not belong to environment <env>")` (terminal, counted once, Warning via the existing path, no pod). Envtests: (a) Secret labelled with another environment → `WorkOrderMismatch`, no pod; (b) Secret with the right label but owned by a different ClaudeEnvironment UID → mismatch; (c) the normal hook-shaped Secret (label + env controller owner, then handed to the runner) → pod created. Read `internal/hook/hook.go` to mirror exactly what the hook sets.
- [ ] **Step 2: Pod adoption** — in `createPod`'s first-Get branch and its AlreadyExists re-read, a pod that is not `metav1.IsControlledBy(pod, runner)` is not adopted: `fail(runner, ReasonPodMismatch, "pod <name> is not controlled by this ClaudeRunner")`. Envtest: pre-create a pod with the runner's name owned by nothing → `PodMismatch`, the foreign pod is left untouched.
- [ ] **Step 3: Sweep deadline** — `sweepOrphanedWorkOrders` uses `max(expectedSpawnSeconds, hookTimeoutSeconds)` (both from the on-demand spec with their defaults). Unit-test the helper that computes it.
- [ ] **Step 4: Exact failure accounting** — on SpawnTimeout, persist `Phase=Failed` and the reason in the same intermediate status update that precedes the pod delete, so a later conflict cannot count `failed` twice; keep the existing SpawnTimeout spec green and add an assertion that `runners_total{outcome="failed"}` increments by exactly 1 across a forced conflict (use `clock.Shift` plus a concurrent status update in the test).
- [ ] **Step 5: NetworkPolicy delete errors** — `deleteIfOwned` failures in `reconcileNetworkPolicy` go through `applyFailed(..., "NetworkPolicy", ...)` like apply failures. Envtest: make the delete fail (a failing client seam like the Endpoints reader seam from the fix wave, or a finalizer on the policy that blocks deletion) → `WorkloadApplyFailed`.
- [ ] **Step 6: FleetAvailable event once** — the `FleetAvailable` Normal/Warning event must be emitted only after a successful status update (route it through `statusPass.emit()` like the Degraded Warnings). Envtest: inject a conflict on the first update and assert one event.
- [ ] **Step 7: Verify, commit, push** — gates; `make test-e2e` once (on-demand specs exercise provenance via the real hook). Commit `fix(controller): work-order provenance, pod adoption, exact failure accounting`.

---

### Task 2: API, chart and release hygiene

**Files:**
- Modify: `api/v1alpha1/claudeenvironment_types.go` (orchestrator env `MaxItems=64`), `cmd/options.go`, `cmd/options_test.go`, `cmd/main.go`, `internal/controller/claudeenvironment_controller.go` (apiserver endpoints source), `internal/builders/networkpolicy.go` (accept a pre-parsed endpoint list), `hack/chart-postprocess.sh`, `dist/chart/values.yaml` (`networkPolicy.apiServerEndpoints: []`), `test/chart/contract_test.go`, `.github/workflows/release.yml`, `docs/release.md`, `TROUBLESHOOTING.md`, `docs/hardening.md`, `README.md`

**Interfaces:**
- Produces: manager flag `--apiserver-endpoints` (comma-separated `ip:port`; when non-empty the controller uses it instead of reading `Endpoints default/kubernetes`); reconciler field `APIServerEndpoints []netip.AddrPort`; chart value `networkPolicy.apiServerEndpoints` (list of `ip:port` strings) rendered as that flag.

- [ ] **Step 1: Flag and parsing** — `registerFlags` adds `--apiserver-endpoints`; `cmd/options.go` parses with `netip.ParseAddrPort`, failing startup with a clear error on any invalid entry (test both valid and invalid in `options_test.go`). `cmd/main.go` passes the parsed list to the reconciler.
- [ ] **Step 2: Controller** — when the list is non-empty, `reconcileNetworkPolicy` builds `<env>-egress-apiserver` from it and skips the Endpoints read (so namespaced RBAC works); otherwise unchanged. Envtest: with the field set, the policy's ipBlocks and ports equal the list and no Endpoints read happens (use the reader seam to assert it is not called).
- [ ] **Step 3: Chart** — the post-process script renders `--apiserver-endpoints={{ join "," .Values.networkPolicy.apiServerEndpoints }}` only when the list is non-empty; `values.yaml` documents that `rbac.namespaced=true` requires it when any environment enables the NetworkPolicy. Contract test: rendering with `--set networkPolicy.apiServerEndpoints={10.0.0.1:6443}` yields the flag; default rendering does not. Update `TROUBLESHOOTING.md` (the namespaced-RBAC row), `docs/hardening.md` and the README values list. `make helm-chart` twice must leave the tree clean.
- [ ] **Step 4: Orchestrator env cap** — `// +kubebuilder:validation:MaxItems=64` on `OrchestratorSpec.Env`; regenerate; envtest reject with 65 entries.
- [ ] **Step 5: Release** — the semver check refuses versions containing `+` (message: "build metadata is not allowed in image tags"); add `helm registry login ghcr.io -u "$GITHUB_ACTOR" --password-stdin <<< "$GITHUB_TOKEN"` (token via `env:`) before `helm push`, gated on the non-dry path; `docs/release.md:5` says a dispatch from a branch is always a dry run and a dispatch on a tag ref publishes. Delete the stale paragraph in `TROUBLESHOOTING.md` that says no rule rejects `CLAUDE_OPERATOR_*` in orchestrator env. `actionlint` clean.
- [ ] **Step 6: Verify, commit, push** — gates plus `make helm-lint test-chart`. Commit `feat: --apiserver-endpoints for namespaced RBAC; orchestrator env cap; release hygiene`.

---

### Task 3: Tests and hygiene

**Files:**
- Modify: the three test files lacking the Apache header (find with `grep -L 'Licensed under the Apache License' $(git ls-files '*_test.go')`), `internal/controller/claudeenvironment_validation_test.go`, `internal/controller/claudeenvironment_ondemand_test.go` (HookImageUnset), `internal/builders/podtemplate_test.go` (ReservedMountPaths sync test parsing `config/crd/bases/*.yaml`), `internal/controller/clock_test.go` (new), `internal/redact/redact_test.go` (new), `internal/metrics` (move `RunnersTotalForTest` into a new package `internal/metrics/metricstest` imported only by tests; the manager binary must not link `prometheus/testutil`: prove with `go version -m bin/manager | grep -c testutil` → 0 after `make build`), `Makefile` (`cleanup-test-e2e` and the e2e suite's BeforeSuite delete `clusterrolebinding claude-selfhosted-operator-metrics-binding --ignore-not-found`), `test/e2e/upgrade_test.go` + `test/e2e/testdata/` (on-demand upgrade case)

- [ ] **Step 1: Headers and small tests** — add the headers; envtest reject case for "exactly one volume source must be set" (a `PodTemplate` volume with two sources); HookImageUnset spec: reach a populated `Status.OnDemand` first, then unset the hook image, then assert it is nil; table test for all operator-owned env names in the runner rule; `ReservedMountPaths` test that parses the CRD YAML and asserts every reserved path appears in the three CEL rules; `tick(nil)` returns a time within a second of `time.Now()`; `internal/redact` table test (three-, two-segment, bare, and a non-JWT string untouched).
- [ ] **Step 2: Metrics helper** — move the test helper to `internal/metrics/metricstest`; update test imports; `go version -m` proof.
- [ ] **Step 3: E2E leftover binding** — `cleanup-test-e2e` and BeforeSuite delete the binding; comment why.
- [ ] **Step 4: On-demand upgrade e2e** — in `upgradeSpecs()` also create an on-demand environment (reuse `test/e2e/testdata/on-demand-stub.yaml` in namespace `claude-e2e-od` with its Secret, as `onDemandSpecs` does) under the previous revision, wait Ready, and after the upgrade assert: the orchestrator Deployment rolled to a template carrying `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS`, the environment is Ready again within 3 minutes, and no `FailedCreate` events. Delete that namespace in AfterAll (wait) before `onDemandSpecs` recreates it. `make test-e2e-upgrade` green.
- [ ] **Step 5: Verify, commit, push** — gates plus `make test-e2e` and `make test-e2e-upgrade`. Commit `test: close the Plan 3 test follow-ups; on-demand upgrade case`.

---

### Task 4: Scripts and CI

**Files:**
- Modify: `Makefile` (`print-envtest-version` helper target; `CLAUDE_CODE_VERSION` empty check), `.github/workflows/test.yml` (use the helper; fix SC2016), `.github/workflows/test-e2e.yml` (fix SC2046 by quoting), `.github/workflows/real-e2e.yml` (diagnostics: manager logs only; hook-copy check step), `hack/real-session-test.sh`, `test/real-e2e/host-config.yaml` / `capture-reply.sh` (unchanged content; add `hack/check-hook-copy.sh` that diffs them, wired into `make lint` or a CI step), `test/replysink/Dockerfile` (base images by digest), `examples/runner-image/Dockerfile` (`CLAUDE_ARCH` derived from `TARGETARCH`: amd64→linux-x64, arm64→linux-arm64, overridable), `docs/testing.md`

- [ ] **Step 1: Workflows** — `actionlint` clean across all workflows (SC2046, SC2016 fixed), the race job reads the envtest version via `make -s print-envtest-version`.
- [ ] **Step 2: Hook copy check** — `hack/check-hook-copy.sh` extracts `data["capture-reply.sh"]` from the ConfigMap (python3 + PyYAML or `yq` if present; fall back to `awk` on the literal block) and diffs it against `test/real-e2e/capture-reply.sh`; run it in `test.yml` and from `make lint`.
- [ ] **Step 3: Real-session script** — Level 2 gate requires `claude auth status` to report a claude.ai login (parse its output; if it has no JSON mode, grep the documented text; record what you used); clear failure messages at every `jq -e` check and the phase/GC loops; GC loop tolerant of transient kubectl errors; temp clone and port-forward log removed on exit; do not echo the origin URL; ref fallback handles a detached HEAD (`git rev-parse --abbrev-ref HEAD` == `HEAD` → use `git rev-parse HEAD`); fail fast if `CLAUDE_CODE_VERSION` resolves empty (Makefile). Diagnostics in `real-e2e.yml`: manager logs and resource listing only. `shellcheck` clean. Dry run with fake `kubectl`/`claude` as before.
- [ ] **Step 4: Images** — replysink Dockerfile base images pinned by digest (`docker buildx imagetools inspect <ref>` to resolve); example runner `CLAUDE_ARCH` from `TARGETARCH`; `make replysink-image real-runner-image` build.
- [ ] **Step 5: Verify, commit, push** — gates; `docs/testing.md` updated for the gate and diagnostics changes. Commit `ci: shellcheck-clean workflows, hook-copy check, real-session hygiene, pinned test images`.

---

## Out of scope
- Hook concurrency-cap race (pre-existing, not in any spec).
- CEL `isCIDR` (needs a Kubernetes 1.31 floor).
- The API group.
