# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). No version has been released yet.

## [Unreleased]

### API

- `ClaudeEnvironment` (`selfhosted.claudecode.dev/v1alpha1`, short name `cenv`) with two modes:
  `spec.fixed` (a Deployment, or a StatefulSet with `persistentWorkspace`) and `spec.onDemand` (an
  orchestrator that spawns one runner pod per session). Runner settings map onto the
  `claude self-hosted-runner` flags.
- `ClaudeRunner` (short name `crun`), created by the spawn hook for each on-demand session; phases
  `Pending`, `Running`, `Succeeded`, `Failed`.
- `podTemplate` exposes only fields that keep pods within the Restricted Pod Security Standard.
- CEL validation: image must carry a tag or digest and not be `:latest`; `baseDir` and `volumeMounts` may
  not target `/etc/claude`, `/home/runner` or `/tmp`; `runner.env` may not set operator-owned variables;
  operator-owned `extraArgs` flags rejected; `env` and `volumeMounts` capped at 64 entries;
  `hookTimeoutSeconds` at least 15 and `hookTimeoutSeconds + 5` below `expectedSpawnSeconds`;
  `spec.runner.networkPolicy` with IPv4 `egressCIDRs`.

### Controllers

- Status conditions `Ready`, `SecretFound`, `FleetAvailable`, `Progressing`, `Degraded`,
  `SecretOnRunners`; the `Degraded` message lists every active reason as `<Reason>: <message>`, and each
  Warning is emitted once per transition. Normal `Created` events for new workload objects.
- Failed-start detection (`RunnerFailedStart`) with a redacted, truncated last fatal line.
- Drain budget computed from the runner settings and applied as `terminationGracePeriodSeconds`.
- On-demand: orchestrator Deployment with an exec readiness probe on `"connected": true`, per-environment
  ServiceAccount and least-privilege Role, `SpawnTimeout`, `PodLost`, `WorkOrderMissing`,
  `EnvironmentMismatch` (owner name and UID) handling, TTL after finish, and a sweep that deletes orphaned
  work-order Secrets (`OrphanedWorkOrderDeleted`).
- Event reason `PodFailed` replaces `RunnerFailed` for failed runner pods.
- Optional per-environment egress NetworkPolicies `<env>-egress` and `<env>-egress-apiserver`.
- Label-filtered caches for Pods, ServiceAccounts, Roles, RoleBindings and NetworkPolicies;
  `--watch-namespaces` limits every cache.
- Injectable clocks; the suites run under `-race`.
- Metrics: `claude_operator_environment_ready`, `claude_operator_drain_budget_seconds`,
  `claude_operator_fixed_fleet_replicas`, `claude_operator_environment_runners`,
  `claude_operator_runner_spawn_duration_seconds` (from the pod's start time),
  `claude_operator_runners_total`. Optional OTLP tracing.

### Hook

- `spawn-runner` hook in the operator image: idempotent on redelivery, soft `maxConcurrentRunners` cap,
  Secret then ClaudeRunner with ownership hand-off, deletion of the Secret on a non-retryable failure,
  exit codes 0, 1, 2, one redacted JSON log line per run.
- Deadline derived from `hookTimeoutSeconds` (`CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS`, minus 10 s, floor 5 s).
- Label values hashed for IDs longer than 63 characters; a bare `eyJ` JWT segment is redacted.

### Security

- Every operator-created pod is Restricted-compliant; runner pods mount no ServiceAccount token by default.
- ValidatingAdmissionPolicies `orchestrator-secrets` and `orchestrator-runners` (Kubernetes 1.30+) confine
  each orchestrator identity to its own environment's work orders.
- `SECURITY.md` with private vulnerability reporting.

### Chart

- Helm chart `claude-selfhosted-operator` generated from the kustomize installer, with
  `admissionPolicy.enabled`, `watchNamespaces`, `tracing.endpoint`, `tracing.sampleRatio`, a ServiceMonitor
  and a runner PodMonitor behind `prometheus.enabled`; the hook image follows `manager.image`.

### Testing

- envtest suites for both controllers and the CEL rules; kind e2e with a stub runner (fixed fleet,
  on-demand, admission denials, NetworkPolicy shape); chart contract test; upgrade e2e from the previous
  revision; manual real-environment test (`real-e2e`) with a registration level and a session level.

### CI and release

- Workflows `lint.yml`, `test.yml` (unit, race, chart lint and contract), `test-e2e.yml` (kind e2e and upgrade) and the manual `real-e2e.yml`; Dependabot for Go modules, Actions and Docker.
- `release.yml`: multi-arch image tagged `vX.Y.Z` and `vX.Y` (no `latest`), keyless cosign signature, SPDX
  SBOM attestation, OCI chart, GitHub Release with `install.yaml`; `dry_run` by default. The runner image is
  never published.

### Docs

- README quickstart, on-demand guide, hardening (Anthropic's checklist mapped to the operator), upgrade,
  metrics and alert rules, troubleshooting, testing, release checklist, contributing guide.

### Removed

- The manager's `--zap-*` flags; use `--log-level` and `--log-format`.
