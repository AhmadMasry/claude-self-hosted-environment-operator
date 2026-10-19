# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- The orchestrator Deployment declares its rollout: `maxSurge: 1`, `maxUnavailable: 0` and
  `minReadySeconds: 5`, so an operator upgrade (which rolls the orchestrator through its hook image) or a
  Secret rotation retires an old replica only after its replacement has passed the connected probe.
- A PodDisruptionBudget `<env>-orchestrator` with `minAvailable: 1` whenever `orchestrator.replicas` is 2 or
  more; removed when the count drops to 1. The manager role gains `policy/poddisruptionbudgets`.
- The manager releases its leader-election lease on shutdown (`LeaderElectionReleaseOnCancel`), so the
  replacement pod takes over at once instead of waiting out the lease.
- The upgrade e2e keeps an on-demand runner pod running across the upgrade and asserts it is the same pod
  afterwards, and that the orchestrator rollout completes with no surplus replicas.

### Changed

- The manager Deployment no longer sets a CPU limit by default (the 10m request and the 512Mi memory
  limit stay). CPU is compressible and a CFS quota only throttles the manager's bursts. Set
  `manager.resources.limits.cpu` in the chart, or patch the Deployment, where a namespace quota
  requires one. The CRDs and the pods the operator builds never defaulted CPU limits; only the
  install-hook init container keeps its small fixed resources, overridable via
  `spec.onDemand.orchestrator.hookInstaller.resources`.
- Toolchain: Go 1.27 (`go.mod`, the manager image and the test images build on `golang:1.27`);
  controller-tools 0.23.0, kustomize 5.8.3, golangci-lint 2.15.0 with logtools 0.11.0;
  `k8s.io/utils` refreshed. Go modules and GitHub Actions were already at their latest releases.

### Security

- `golang.org/x/net` pinned to 0.61.0: 0.59.0, pulled in indirectly, carried five HTTP/2 advisories
  (GO-2026-6603, 6610, 6611, 6612, 6617) reachable from the hook's HTTP client. `github.com/google/cel-go`
  pinned to 0.30.0 for GO-2026-6094 (imported through the Kubernetes API types, not called).
  `govulncheck -test ./...` reports no vulnerabilities.

## [0.1.2] - 2026-10-18

### Changed

- Dependencies: github.com/prometheus/client_golang 1.24.1 to 1.25.0 (with client_model, common
  and procfs) and github.com/onsi/ginkgo/v2 2.33.0 to 2.33.1. No behaviour change.

## [0.1.1] - 2026-10-15

### Added

- `spec.onDemand.orchestrator.hookInstaller.resources` replaces the install-hook init container's default
  resources (requests 10m CPU and 16Mi memory, limits 50m and 32Mi), for namespaces whose `LimitRange`
  minimums are above them. The defaults are now documented in docs/on-demand.md.
- README: an uninstall procedure, including how to free a namespace left `Terminating` on runner
  finalizers, and the environment key's 365-day expiry and rotation steps in the quickstart.

## [0.1.0] - 2026-10-13

First release. The API is `claude.cloudeng.ps/v1alpha1`; see the README's API stability
section for what that promises.

### API

- `ClaudeEnvironment` (`claude.cloudeng.ps/v1alpha1`, short name `cenv`) with two modes:
  `spec.fixed` (a Deployment, or a StatefulSet with `persistentWorkspace`) and `spec.onDemand` (an
  orchestrator that spawns one runner pod per session). Runner settings map onto the
  `claude self-hosted-runner` flags.
- `podTemplate.initContainers` on `spec.runner` and `spec.onDemand.orchestrator`: a curated init
  container (name, image, command, args, env, envFrom, volumeMounts, resources, `restartPolicy: Always`
  for sidecars) that the operator hardens like its own containers.
- `ClaudeRunner` (short name `crun`), created by the spawn hook for each on-demand session; phases
  `Pending`, `Running`, `Succeeded`, `Failed`.
- `podTemplate` exposes only fields that keep pods within the Restricted Pod Security Standard.
- CEL validation: image must carry a tag or digest and not be `:latest`; `baseDir` and `volumeMounts` may
  not target `/etc/claude`, `/home/runner` or `/tmp`; `runner.env` and `onDemand.orchestrator.env` may not set operator-owned variables;
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

[Unreleased]: https://github.com/AhmadMasry/claude-self-hosted-environment-operator/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/AhmadMasry/claude-self-hosted-environment-operator/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/AhmadMasry/claude-self-hosted-environment-operator/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/AhmadMasry/claude-self-hosted-environment-operator/releases/tag/v0.1.0
