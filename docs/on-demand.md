# On-demand mode

In on-demand mode the operator runs no runner fleet. It runs an orchestrator that holds the environment
secret, and each incoming session spawns one runner pod with a single-use work order. See
[`examples/on-demand.yaml`](../examples/on-demand.yaml). Set `spec.onDemand` instead of `spec.fixed`.

## What the operator creates

For an environment named `<env>` it creates a ServiceAccount, Role, RoleBinding and Deployment, all named
`<env>-orchestrator`. The Role grants `get` on its own `claudeenvironments` object, `create, get` on `clauderunners`
(plus `list` only when `maxConcurrentRunners` is above 0), and `create, patch, delete` on `secrets`.
Because the manager can only grant what it holds, the manager itself needs `create, patch, delete` on Secrets.

Give each on-demand environment its own namespace. The orchestrator ServiceAccount can create, patch and
delete any Secret in its namespace, so anything else in that namespace, another environment's secret
included, is within its reach. The operator narrows what a ClaudeRunner can do with that reach: the API
requires `workOrderSecretRef.name` to be `<orderID>-work-order`, and the controller starts a pod only for a
ClaudeRunner controlled by the on-demand ClaudeEnvironment its `environmentRef` names (otherwise it fails
with `EnvironmentMismatch`). On Kubernetes 1.30 or later the [admission policy](#admission-policy) also
confines the orchestrator to its own environment's work orders.
The operator watches Secrets and ConfigMaps cluster-wide unless the manager runs with `--watch-namespaces`.
The condition `SecretOnRunners` is `False` with reason `OnDemandSecretOnOrchestrator`: the environment
secret is mounted on the orchestrator only; runner pods get just their work-order JWT.

## Hook image

An init container `install-hook` runs the operator image with `--install /hooks`, which copies the hook
binary (`/spawn-runner` in the operator image) into a shared volume the orchestrator mounts at
`/etc/claude/hooks`. The manager finds its own image through `--hook-image`, which defaults to the
`OPERATOR_IMAGE` environment variable. The kustomize manifests fill `OPERATOR_IMAGE` from the manager
container image (a `replacements` entry in `config/default/kustomization.yaml`). Without a hook image the
environment is `Degraded` with reason `HookImageUnset`.

## Orchestrator probes

Readiness is an exec probe, `/etc/claude/hooks/spawn-runner --probe-connected http://127.0.0.1:<healthPort>/healthz`,
which exits 0 only when the response body has `"connected": true`, so a replica that has not reached
Claude receives no sessions. Liveness is an HTTP GET on `/healthz`.

## Hook contract

The orchestrator runs the hook once per work order. In order:

1. If a ClaudeRunner named after the order ID exists, exit 0 with outcome `redelivered`.
2. If `maxConcurrentRunners` is reached, exit 1 with outcome `at_capacity`. The cap is soft: concurrent hook runs can briefly overshoot it.
3. Create Secret `<orderID>-work-order` (key `jwt`), then the ClaudeRunner, and make the ClaudeRunner owner of the Secret.
4. If the runner create fails with a non-retryable error, delete the Secret.

The hook's owner references set `controller: true` and leave `blockOwnerDeletion` unset, because the
`OwnerReferencesPermissionEnforcement` admission plugin (on by default on OpenShift) would otherwise require
`update` on the owner's `finalizers` subresource, which the orchestrator Role does not grant.

Exit codes: `0` success or redelivered, `1` retryable (at capacity, or a transient API or transport error), `2` non-retryable (the API rejected the request). The hook's
deadline is `onDemand.orchestrator.hookTimeoutSeconds` (minimum 15, default 60) minus 10 seconds, never below
5 seconds, so it finishes before the orchestrator kills it; the operator passes the timeout as
`CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS`, and the hook assumes 60 when the variable is absent. The hook writes
one JSON line to stdout with `ts`, `orderID`, `sessionID`, `attempt`, `outcome`,
`exitCode`, `runner`, `warning`, `error`; token values are redacted. It reads `CLAUDE_OPERATOR_ENVIRONMENT`,
`CLAUDE_OPERATOR_NAMESPACE`, `CLAUDE_OPERATOR_MAX_CONCURRENT_RUNNERS` and
`CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS`; do not set these names in `onDemand.orchestrator.env`, which would
override them. It never reads
`CLAUDE_RUNNER_ACCOUNT_EMAIL`, so no account email is stored in the cluster.

## ClaudeRunner lifecycle

ClaudeRunners are created by the hook, never by users. Phases: `Pending`, `Running`, `Succeeded`, `Failed`.

| Reason | Meaning |
|---|---|
| `PodPending` / `PodRunning` | Pod created, or running |
| `PodSucceeded` / `PodFailed` | Pod finished |
| `SpawnTimeout` | Pod not Running within `onDemand.orchestrator.expectedSpawnSeconds` of creation; the pod is deleted |
| `WorkOrderMissing` | The work-order Secret was not found before the pod was created; the runner stays `Pending` with this reason and fails with it once `expectedSpawnSeconds` have passed |
| `PodLost` | The runner pod disappeared (evicted or deleted) before it finished; it is never re-created, because its work order is single-use |
| `EnvironmentMissing` | The ClaudeEnvironment no longer exists |
| `EnvironmentMismatch` | The ClaudeRunner is not controlled by the on-demand ClaudeEnvironment its `environmentRef` names; no pod is created |

The JWT is mounted at `/etc/claude/environment-secret`, the same volume path as in fixed mode. The
finalizer `selfhosted.claudecode.dev/runner-pod` makes deletion remove the pod first.
`runnerTTLSecondsAfterFinished` (default 300) deletes a finished ClaudeRunner after that many seconds.

    kubectl get crun -A     # columns: Phase, Session, Pod, Age

## Orphaned work orders

If the hook stops between creating the work-order Secret and the ClaudeRunner, the Secret would hold a live
JWT that nothing uses. On every on-demand reconcile the operator deletes a Secret that carries the
environment and order-id labels, ends in `-work-order`, is controlled by the ClaudeEnvironment, is older than
`expectedSpawnSeconds`, and has no ClaudeRunner for its order (checked against the API server). The
environment key Secret is never deleted. Each deletion emits a Normal `OrphanedWorkOrderDeleted` event.

## Admission policy

Two ValidatingAdmissionPolicy objects, `orchestrator-secrets` and `orchestrator-runners` (`config/admission`,
Kubernetes 1.30+), apply to a request when the user is `system:serviceaccount:<ns>:<env>-orchestrator` and
the authorizer allows that user to `get` `claudeenvironments/<env>` in that namespace, which the operator's
orchestrator Role grants. For such an identity:

- Secrets (CREATE, UPDATE, DELETE): the name must end in `-work-order` and the label
  `selfhosted.claudecode.dev/environment` must equal `<env>`, on the new object for CREATE and UPDATE and on
  the old object for UPDATE and DELETE. Each orchestrator can only touch its own environment's work orders.
- ClaudeRunners (CREATE): the controller owner must be the ClaudeEnvironment `<env>`
  (`selfhosted.claudecode.dev/v1alpha1`), `spec.environmentRef.name` must be `<env>`, and
  `spec.workOrderSecretRef.name` must be the runner's name plus `-work-order`.

On older clusters remove `../admission` from `config/default/kustomization.yaml`, or install the chart with
`admissionPolicy.enabled=false`. See [hardening](hardening.md#admission-policy) for the denial messages.

## Egress policy

With `spec.runner.networkPolicy.enabled: true` the operator adds `<env>-egress` (DNS plus TCP 443 to
`egressCIDRs` for every pod of the environment) and, in on-demand mode, `<env>-egress-apiserver`, which lets
the orchestrator reach the API server addresses listed in the `default/kubernetes` Endpoints. See
[hardening](hardening.md#networkpolicy).

## Environment status

Conditions: `FleetAvailable` has reasons `WorkloadAvailable`, `OrchestratorUnavailable`, `HookImageUnset`
and `WorkloadApplyFailed`. Status fields: `onDemand.orchestratorReadyReplicas`, `pendingRunners`,
`runningRunners`.

## Metrics

| Metric | Labels |
|---|---|
| `claude_operator_environment_runners` | `namespace`, `environment`, `phase` (`pending`, `running`) |
| `claude_operator_runner_spawn_duration_seconds` (histogram) | `namespace`, `environment` |
| `claude_operator_runners_total` | `namespace`, `environment`, `outcome` (`created`, `succeeded`, `failed`, `spawn_timeout`) |

The fixed-mode series (`claude_operator_environment_ready`, `claude_operator_fixed_fleet_replicas`,
`claude_operator_drain_budget_seconds`) are unchanged. See [metrics](metrics.md) for types, alert rules and
the PodMonitor.

## Tracing

Optional OTLP tracing. The manager takes `--tracing-endpoint` (default `OTEL_EXPORTER_OTLP_ENDPOINT`) and
`--tracing-sample-ratio` (default 0.1). The hook reads `OTEL_EXPORTER_OTLP_ENDPOINT` from the orchestrator
container environment, which you set through `onDemand.orchestrator.env`, and samples every spawn. The
trace continues into the ClaudeRunner reconcile through the `selfhosted.claudecode.dev/traceparent`
annotation on the ClaudeRunner.
