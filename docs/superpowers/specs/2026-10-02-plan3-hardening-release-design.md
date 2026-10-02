# Plan 3: Hardening, Packaging, and Release — Design Addendum

Date: 2026-10-02
Status: approved in conversation; written for review
Extends: `docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`
(sections 7, 8.2, 9, 10, 11, 12 remain authoritative; this addendum records
the decisions the base spec left open and the follow-ups carried from
Plans 1 and 2).

## 1. Purpose and decisions

Plan 3 makes the operator releasable: it clears the technical debt recorded
in `docs/superpowers/plans/2026-10-02-foundation-followups.md` and
`docs/superpowers/plans/2026-10-02-on-demand-followups.md`, adds the
production items the Plan 2 review required before 1.0, ships the Helm
chart and monitoring manifests, adds the manual real-environment test and
the upgrade test, builds the release pipeline, and completes the docs set.

Decisions made with the user on 2026-10-02:

- GitHub repository `AhmadMasry/claude-self-hosted-environment-operator`
  (public). `master` is pushed; the scaffolded Lint, Tests and E2E
  workflows run there. Secrets `CLAUDE_ENVIRONMENT_KEY` and
  `CLAUDE_ENVIRONMENT_ID` exist; `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` is added
  when the real-environment workflow lands.
- Operator image is public on GHCR
  (`ghcr.io/ahmadmasry/claude-self-hosted-environment-operator`), multi-arch,
  signed. The runner image bundles Anthropic's proprietary `claude` binary
  and is built in CI only, never pushed anywhere.
- The real-environment test is manual (`workflow_dispatch`) and serves as
  the pre-release gate; a schedule is present but commented out. It uses a
  dedicated test environment in the user's company Team organization.
- No release tag is created in Plan 3. The API group
  `selfhosted.claudecode.dev` stays provisional until the user decides;
  release tooling is verified with a dry run only. The first tag will be
  `v0.1.0`.
- Won't-do list (approved): `enforce-version: latest` on sample namespaces;
  a redelivered order keeps the first JWT. Everything else in both
  follow-up files is done in this plan.
- Plan structure A: one plan, debt first, then hardening, chart, tests,
  release, docs.

## 2. Repository, CI baseline, debt sweep

### 2.1 Repository

- `origin` is the GitHub repo; `master` is the default branch. README gains a
  top section: one-paragraph description, status badges (Lint, Tests, E2E),
  install options (kustomize installer, Helm), links to the docs set.
- `.gitignore` and the Apache-2.0 `LICENSE` already exist. Add
  `SECURITY.md` (reporting path: GitHub private vulnerability reporting),
  `CONTRIBUTING.md` (toolchain, `make` targets, test tiers, PR checklist),
  `CODEOWNERS` (`* @AhmadMasry`).
- `AGENTS.md` is trimmed to the parts that are true for this repo: generated
  files never edited by hand, `make manifests generate` after marker edits,
  the logging style, test tiers. The "never create files manually" rule is
  removed.

### 2.2 CI baseline

- Keep the scaffolded `lint.yml`, `test.yml`, `test-e2e.yml`. Add to
  `test.yml` a `-race` run of `./internal/... ./cmd/...` (envtest included).
- Add `helm lint` and the chart contract test (section 4) to the test job
  once the chart exists.
- Add the upgrade e2e (section 5.2) to `test-e2e.yml`.
- Dependabot (or Renovate) config for Go modules, Actions and Docker base
  images, weekly.

### 2.3 Debt sweep (every follow-up except the won't-do list)

Grouped by file; each is a small, independent change with its own test
where a test is meaningful.

Plan 1 follow-ups:

- Go floor stays 1.26 in `go.mod`; Dockerfiles and the devcontainer pin the
  same toolchain tag; `.custom-gcl.yml` and the devcontainer post-install
  pin versions instead of `latest`.
- Makefile: `--build-arg VERSION=$(VERSION)` only when `VERSION` is
  non-empty.
- License headers on `cmd/options_test.go`, `internal/builders/fixed_test.go`,
  `internal/builders/hash_test.go`, `internal/controller/clauderunner_controller_test.go`.
- `reconcile` returns `ctrl.Result{}` together with a non-nil error (never
  `RequeueAfter` plus error).
- Apply-failed path clears `status.fixed` counts rather than leaving stale
  values; the equivalent for `status.onDemand` on the hook-image-unset path.
- Log flags: `--zap-log-level` and `--zap-devel` are removed from the bound
  flag set (only `--log-level` and `--log-format` remain), with a
  deprecation note in the release notes.
- Image CEL: reject an empty tag (`!self.endsWith(':')`) and an empty
  digest; mount-collision CEL on `podTemplate.volumeMounts` and
  `runner.baseDir` against `/etc/claude`, `/home/runner`, `/tmp`.
- Builder tests: `WorkspaceFromPVC`, the client-label fieldRef env var,
  the named probe port, a user mount collision (now rejected by CEL, tested
  at the API level), the Volume exactly-one rule and the absence of
  forbidden podTemplate fields (schema-level test that `hostPath`,
  `hostNetwork`, `privileged` are unknown fields).
- Shared default constants between `args.go` and `drain.go`.
- Volume-name constants in `podtemplate.go`; duplicate user env vars named
  like operator-set ones are rejected by CEL.
- `ccenvkey_` assertion replaced by the real redaction test already added;
  remove the vacuous one.
- E2E: assert errors from `kubectl get events`; use `utils.GetProjectDir`
  for testdata paths; wrap the phase assertion in `Eventually`; one shared
  constant for the stub image.
- Docs: `lockToAccount` may be an email that lands in the pod args and the
  ClaudeEnvironment object.
- A Normal `Created` event when a Deployment, StatefulSet or orchestrator
  Deployment is first created (detected by an empty `ResourceVersion`
  before apply through a pre-apply cached Get).
- Warning events (`GracePeriodTooShort`, `RunnerFailedStart`,
  `HookImageUnset`) emitted only when the corresponding condition transitions
  to True, not on every pass.

Plan 2 follow-ups:

- `ownerMismatch` also compares the owner reference UID with the live
  environment's UID.
- The StatefulSet immutability test provokes the apply failure by changing
  `serviceName`.
- `runners_total{outcome="failed"}` counted on every `fail()` path.
- `createPod` on `AlreadyExists`: no second `created` count or `PodCreated`
  event; the pod is re-read before deriving the phase.
- Spawn-timeout path: the phase recorded is `SpawnTimeout` even when the
  status write conflicts (store the pending reason before the delete and
  re-apply it).
- Trace continuation stops once the runner is terminal.
- Spawn duration measured from the pod's `status.startTime` when present.
- Structured log lines in the ClaudeRunner controller: pod created, phase
  transitions, timeout, TTL deletion, each with `orderID` and `sessionID`.
- A predicate on the environment controller's ClaudeRunner and Pod watches
  so only phase changes enqueue an environment reconcile.
- Weak assertions tightened: e2e GC check requires `NotFound`; the
  `FailedCreate` query asserts its error; telemetry tests assert a
  non-recording span and an invalid extracted span context; the
  "work-order lands later" test first observes the waiting state; the
  empty-orderID test uses a valid secret name.
- `Install` removes its temp file on failure; the probe test synchronises
  its handler body; the stub's handlers are split by mode.
- Kustomization: the commented cert-manager replacements are aligned with
  the real entry, and the scaffold markers are kept where kubebuilder
  injects.
- Order and session IDs longer than 63 characters: the label values use
  the ID when it fits and a sha256-prefixed hash otherwise; names are
  unaffected (the hook already fails loudly beyond 253).
- Redaction also matches a bare `eyJ` segment without dots.
- Separate label maps for the Secret and the ClaudeRunner in the hook.

Test hygiene:

- `nowFunc` and `HookImage` are no longer package globals mutated by tests.
  Each reconciler gets a `Clock` field (`func() time.Time`, defaulting to
  `time.Now`), and the environment reconciler's `HookImage` is read through
  a getter that tests override per spec with a mutex-protected test hook.
  The suite runs under `-race` in CI.

## 3. Production hardening additions

### 3.1 Filtered caches

`CacheByObject` adds label selectors for `corev1.ServiceAccount`,
`rbacv1.Role`, `rbacv1.RoleBinding` on
`app.kubernetes.io/part-of=claude-code-self-hosted-runner`, which every
operator-created object carries. Secrets and ConfigMaps stay unfiltered
(user-named); `docs/hardening.md` states the cost and recommends
`--watch-namespaces` for large clusters.

### 3.2 ValidatingAdmissionPolicy for the orchestrator identity

Kubernetes 1.30 or later (GA, `admissionregistration.k8s.io/v1`). Shipped
in `config/admission/` and the chart, toggle `admissionPolicy.enabled`
(default true), documented as optional for older clusters.

- Policy `claude-selfhosted-operator-orchestrator`: for requests whose
  `request.userInfo.username` matches
  `system:serviceaccount:<ns>:<name>-orchestrator` and whose target is a
  Secret: CREATE, UPDATE, PATCH, DELETE allowed only when
  `object.metadata.name.endsWith('-work-order')` (`oldObject` for DELETE).
  For ClaudeRunners created by such a user: the controller owner reference
  must name a ClaudeEnvironment whose name equals the ServiceAccount's
  `<name>` prefix.
- Binding: `validationActions: [Deny]`, all namespaces (the username match
  makes it a no-op elsewhere).
- An e2e spec creates a Secret named `evil` with the orchestrator
  ServiceAccount's token (`kubectl --as`) and expects a denial.

### 3.3 NetworkPolicy

As base spec section 7, behind `spec.runner.networkPolicy.enabled`
(default false): one NetworkPolicy per environment selecting
`selfhosted.claudecode.dev/environment=<env>` pods (runner and
orchestrator), `policyTypes: [Egress]`, allow UDP and TCP 53 to pods in
`kube-system` labelled `k8s-app=kube-dns`, allow TCP 443 to each entry of
`spec.runner.networkPolicy.egressCIDRs`, and an `except` of
`169.254.169.254/32` on every CIDR block. Builder plus unit tests; e2e
asserts the object shape only (kind's default CNI does not enforce
policies).

### 3.4 Orphaned work-order sweep

The environment controller, on each on-demand pass, lists Secrets labelled
`selfhosted.claudecode.dev/environment=<env>` (the hook sets it), and
deletes any `*-work-order` Secret older than `expectedSpawnSeconds` whose
`selfhosted.claudecode.dev/order-id` label has no ClaudeRunner. Envtest
covers it. Requires `delete` on Secrets, which the manager already holds.

### 3.5 Hook deadline

The orchestrator container gets `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS` from
`spec.onDemand.orchestrator.hookTimeoutSeconds`; the hook uses
`timeout - 10s` with a floor of 5s as its context deadline. The CRD minimum
for `hookTimeoutSeconds` becomes 15.

## 4. Chart and monitoring

- `kubebuilder edit --plugins=helm/v2-alpha` generates `dist/chart`
  (committed). Regeneration is `make manifests build-installer` then
  `kubebuilder edit --plugins=helm/v2-alpha --force`; `values.yaml`,
  `NOTES.txt`, `_helpers.tpl` are preserved and hand-maintained.
- `values.yaml` exposes: image, `rbac.namespaced`, `metrics.secure`,
  `certManager.enabled`, `prometheus.enabled` (ServiceMonitor for the
  manager, PodMonitor for runner and orchestrator pods),
  `admissionPolicy.enabled`, manager resources (512Mi default limit),
  `watchNamespaces`, `tracing.endpoint`, `tracing.sampleRatio`.
- `config/prometheus/` gains the PodMonitor from the product's documented
  example (label `app.kubernetes.io/part-of`, port `health`, path
  `/metrics`).
- Chart contract test (`test/chart/contract_test.go`, needs `helm` on
  PATH): renders the chart with default values and asserts the ClusterRole
  rules equal `config/rbac/role.yaml`, the manager pod carries every
  Restricted field, the CRDs equal `config/crd/bases`, and the admission
  policy is present when enabled and absent when disabled.
- README install: `helm install claude-selfhosted-operator oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator --namespace claude-selfhosted-operator-system --create-namespace`, then the Restricted label command.

## 5. Real-environment workflow and upgrade test

### 5.1 `real-e2e.yml` (manual)

- Trigger: `workflow_dispatch` with inputs `test_repo` (default this repo)
  and `ref` (default `master`); a commented `schedule`.
- Guard step: exits with a clear message unless `CLAUDE_ENVIRONMENT_KEY`,
  `CLAUDE_ENVIRONMENT_ID` and `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` are set.
- Steps: kind cluster (`make setup-test-e2e`); build and load the operator
  image; build and load the CI runner image from
  `test/real-e2e/runner.Dockerfile` (`FROM` the example image built with
  the stable `CLAUDE_CODE_VERSION`, plus `jq`, plus the product's
  remote-variant Stop hook at `/home/runner/.claude/hooks/e2e-stop-hook-capture.sh`
  and the matching `settings.json`); deploy the operator; apply
  `test/real-e2e/replysink.yaml` (a tiny Go HTTP receiver in
  `test/replysink`, distroless, exposing `POST /<session_id>` and
  `GET /<session_id>`); create the namespace, the Secret from
  `CLAUDE_ENVIRONMENT_KEY`, and `test/real-e2e/environment.yaml` (on-demand,
  1 orchestrator replica, `runner.env` `E2E_REPLY_URL=http://replysink.<ns>.svc:8080`,
  `expectedSpawnSeconds` 180, TTL 120); wait for Ready.
- Dispatch (product recipe): install the Claude Code CLI on the job VM,
  `claude auth login` non-interactively with
  `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES`, then from
  the checkout `claude -p "<sentinel prompt>" --environment "$CLAUDE_ENVIRONMENT_ID" --ref <ref> --output-format json`,
  poll the sink via `kubectl port-forward` or `kubectl exec` for the
  sentinel, send one follow-up with `--cloud <session_id>`, poll again.
- Operator assertions: a ClaudeRunner appears within `expectedSpawnSeconds`,
  reaches Running then Succeeded (the runner exits after the session is
  released; the test sends the session an "end" instruction and relies on
  `releaseIdleSessionMinutes: 1` plus `killSessionAfterMinutes: 5`), then
  is garbage-collected.
- Teardown always runs: delete the namespace, `make cleanup-test-e2e`.
- Prerequisites on the user's side, documented in `docs/testing.md`: a
  dedicated test environment in the Team org; the org's GitHub connection
  covers `test_repo`; a dedicated automation account whose refresh token is
  stored as `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and renewed every 30 days
  (the product caps the refresh grant at 30 days; exact minting steps in
  the doc).

### 5.2 Upgrade test

A Ginkgo spec in `test/e2e` gated by `UPGRADE_FROM` (a chart reference or
a git ref). CI sets it to the latest tag when one exists, otherwise to the
merge base of the pull request, and builds that revision's installer and
image in a temporary worktree. The spec installs the old version, creates a
fixed-fleet environment with the stub image at two replicas, waits for
Ready, installs the candidate chart over it, and asserts: CRDs updated
(the candidate's `x-kubernetes-validations` present), the environment
returns to Ready, the runner Deployment rolled (new pod-template hash),
and no `FailedCreate` events.

## 6. Release tooling and docs

### 6.1 `release.yml`

- Triggers: push of tags `v*`, and `workflow_dispatch` with `dry_run`
  (default true) which builds and signs nothing and pushes nothing.
- Steps: checkout; Go setup; `make test`; buildx for `linux/amd64,linux/arm64`;
  push to GHCR with the tag and `latest`-free tags only (`vX.Y.Z`, `vX.Y`);
  keyless cosign signing via GitHub OIDC; SBOM (syft) attached as an
  attestation; `make build-installer`; `helm package` and `helm push` to
  `oci://ghcr.io/ahmadmasry/charts`; GitHub Release with `dist/install.yaml`,
  the chart `.tgz`, and generated notes.
- Versioning: tag equals chart `version` and `appVersion`; the Makefile
  `VERSION` derives from `git describe`.

### 6.2 Docs set

`docs/hardening.md` (Anthropic's hardening checklist mapped item by item to
operator fields and defaults), `docs/upgrade.md` (chart upgrade, CRD
updates, rollback), `docs/metrics.md` (every operator series, the PodMonitor,
alert rules merged with the product's examples), `docs/testing.md` (the
three tiers, the manual real-environment workflow and its prerequisites),
`TROUBLESHOOTING.md` (conditions and reasons, what to check for each),
`docs/on-demand.md` updated for the admission policy and the orphan sweep,
README top section.

## 7. Out of scope

- Tagging a release (user decision pending on the API group).
- Publishing the runner image.
- OLM bundle and OperatorHub.
- Conversion webhooks (no second API version yet).
