# Troubleshooting

Start with the environment's conditions and events, then the runners':

    kubectl -n <ns> describe cenv <env>          # conditions, Warning events
    kubectl -n <ns> get crun                     # on-demand: Phase, Session, Pod, Age
    kubectl -n <ns> describe crun <orderID>      # status.reason, status.message, events
    kubectl -n claude-selfhosted-operator-system logs deploy/claude-selfhosted-operator-controller-manager -c manager

Condition and event messages never contain the environment key, a JWT or an email; text taken from a
pod's termination message is redacted and truncated.

## Environment conditions

| Symptom | Cause | Fix |
| :- | :- | :- |
| `SecretFound=False`, reason `SecretMissing`, message `Secret "<name>" not found` | `spec.environmentSecretRef.name` names no Secret in the environment's namespace | Create the Secret in the same namespace (see the quickstart). The operator retries every 30 s and on any Secret change |
| `SecretFound=False`, reason `SecretKeyMissing`, message `Secret "<name>" has no key "<key>"` | The Secret exists but the key (default `environment-secret`) is missing or empty | Recreate it with `--from-file=environment-secret=./environment-secret`, or set `environmentSecretRef.key` |
| `Degraded=True`, message part `ConfigMapMissing: ConfigMap "<name>" not found` or `ConfigMap "<name>" has no key "<key>"` | `lifecycleHooks`, `wrapperScript` or `hostConfig` references a ConfigMap (or wrapper key) that does not exist | Create the ConfigMap or fix the reference. The `Degraded` message lists every active reason as `<Reason>: <message>` joined by `; ` |
| `Degraded=True`, `GracePeriodTooShort: terminationGracePeriodSeconds <n> is below the computed drain budget of <m> seconds` | `runner.terminationGracePeriodSeconds` is shorter than the drain path the settings need (`status.computedDrainBudgetSeconds`) | Raise it to at least the budget, or leave it unset so the operator uses the budget |
| `FleetAvailable=False`, reason `WorkloadApplyFailed`, message `could not apply <Kind> (<reason>): <error>` (the `(<reason>)` part is omitted when the error is not an API error) | The API server rejected a workload object or egress NetworkPolicy, or the operator could not read the `default/kubernetes` Endpoints for an on-demand environment's `<env>-egress-apiserver` policy. Common: a change to an immutable StatefulSet field (`fixed.persistentWorkspace.volumeClaimTemplate`); an `egressCIDRs` entry whose octets are out of range (CEL checks the shape only, so the NetworkPolicy validation rejects it and the message reads `could not apply NetworkPolicy (Invalid): ... spec.egress[1].to[<i>].ipBlock.cidr ...`); missing RBAC after a hand-edited ClusterRole. An Endpoints failure reads `could not apply NetworkPolicy (<reason>): read API server endpoints: <error>`. In on-demand mode `<env>-egress` is applied only after `<env>-egress-apiserver`, so a failure never creates the default-deny policy without the orchestrator's API server allowance | Read the `<reason>`. For a StatefulSet template change, delete the StatefulSet with `--cascade=orphan` and let the operator recreate it, or revert the change. Fix the CIDR in `spec.runner.networkPolicy.egressCIDRs`. Reapply the installer or chart for RBAC |
| `FleetAvailable=False`, reason `WorkloadUnavailable` | The fixed-fleet Deployment or StatefulSet is not yet available: pods pending, crashing or failing readiness | `kubectl -n <ns> get pods -l selfhosted.claudecode.dev/environment=<env>` and describe the pods |
| `Degraded=True`, `RunnerFailedStart: pod <pod> restarted <n> times with runs under 1m0s; last run: <line>` | A runner exits non-zero within a minute of starting, at least 3 times, most recently within 10 minutes (the product's definition of a failed start). `<line>` is the last `[runner:fatal]` or `error:` line of the termination message, redacted; or `no termination message; run kubectl logs --previous on the pod` | `kubectl -n <ns> logs <pod> -c runner --previous`. Typical: a revoked or wrong environment key, an unwritable `baseDir`, a wrapper script that exits |
| `FleetAvailable=False`, reason `HookImageUnset` (also in `Degraded`), message `operator has no hook image configured; set --hook-image or OPERATOR_IMAGE on the manager` | On-demand mode needs the operator image for the hook init container | The installer and chart set it; for a custom deployment pass `--hook-image=<operator image>` |
| `FleetAvailable=False`, reason `OrchestratorUnavailable`, message `orchestrator deployment is not yet available` | The orchestrator pod is not Ready. Its readiness probe runs `/etc/claude/hooks/spawn-runner --probe-connected http://127.0.0.1:<healthPort>/healthz` and passes only when the body has `"connected": true`. Causes: the environment key is wrong or revoked; egress to `api.anthropic.com` is blocked (a NetworkPolicy without its CIDRs, a proxy that is not configured); the `install-hook` init container failed (image pull) | `kubectl -n <ns> logs deploy/<env>-orchestrator -c orchestrator` (look for authentication or connection errors); `kubectl -n <ns> logs deploy/<env>-orchestrator -c install-hook`; `kubectl -n <ns> port-forward deploy/<env>-orchestrator 8080` and `curl localhost:8080/healthz` to read `connected`. With `spec.runner.networkPolicy.enabled`, check that `<env>-egress` lists the right CIDRs and that `<env>-egress-apiserver` exists |
| `SecretOnRunners=True`, reason `FixedModeSecretOnPods` | Expected in fixed mode: every runner pod mounts the environment key | Use on-demand mode to keep the key off runner pods (see [hardening](docs/hardening.md)) |
| The environment has no status at all | The manager does not watch its namespace (`--watch-namespaces`), or the manager is not running | See [watch-namespaces](#--watch-namespaces-mistakes) |

## On-demand runners (ClaudeRunner `status.reason`)

| Reason | Meaning | Fix |
| :- | :- | :- |
| `SpawnTimeout`, message `pod did not start within <n> seconds (<reason>: <message>)` | The pod was not Running within `onDemand.orchestrator.expectedSpawnSeconds`; the operator deleted it. The inner reason is the container's waiting reason (for example `ErrImagePull`, `ImagePullBackOff`) or the scheduling reason (`Unschedulable`), else `PodPending` | Fix scheduling or the image; raise `expectedSpawnSeconds` if pulls are slow (`hookTimeoutSeconds + 5` must stay below it) |
| `WorkOrderMissing` | The work-order Secret `<orderID>-work-order` was not found. The runner waits as `Pending` and fails with this reason once `expectedSpawnSeconds` have passed | Usually the hook died between its creates, or something deleted the Secret. Check the orchestrator log line for that `orderID` |
| `PodLost`, message `runner pod <pod> disappeared before it finished` | The pod was evicted or deleted while running. It is never re-created, because its work order is single-use | The session is re-offered by Anthropic. Look for node pressure or a manual delete |
| `EnvironmentMismatch` | No pod is created. Messages: `ClaudeRunner is not controlled by ClaudeEnvironment "<env>" named in spec.environmentRef`, `controller owner UID does not match ClaudeEnvironment "<env>"` (the environment was deleted and recreated), `ClaudeEnvironment "<env>" is not in on-demand mode` | ClaudeRunners are created by the hook only. Delete stray ones; after recreating an environment, old runners fail this way and are cleaned up by their TTL |
| `EnvironmentMissing`, message `ClaudeEnvironment "<env>" not found` | The environment was deleted | None; the runner expires after `runnerTTLSecondsAfterFinished` |
| `PodFailed`, message `exit code <n> (<reason>): <line>`, `pod failed: <reason>` or `pod phase Unknown: node unreachable` | The runner process exited non-zero (the line is the last `[runner:fatal]` or `error:` line, redacted), the pod failed, or its node became unreachable | `kubectl -n <ns> logs <pod> -c runner` while the pod still exists (until the TTL, default 300 s) |

The hook writes one JSON line per run to the orchestrator's output with `orderID`, `sessionID`,
`attempt`, `outcome` (`submitted`, `redelivered`, `at_capacity`, `error`), `exitCode` and `error`. Exit code
`1` is retryable (at capacity, transient API errors); `2` is non-retryable (the API rejected the request) and
counts towards the product's circuit breaker.

## Events

| Event | Meaning |
| :- | :- |
| Normal `OrphanedWorkOrderDeleted`, `deleted unreferenced work-order Secret <name>` | The sweep removed a work-order Secret older than `expectedSpawnSeconds` with no ClaudeRunner, left by a hook that stopped between its two creates. An occasional one is harmless; a steady stream means the hook keeps dying (orchestrator restarts, a too-low `hookTimeoutSeconds`) |
| Normal `Created`, `created <Kind> <name>` | The operator created a workload or orchestrator object |
| Normal `FleetAvailable` | `FleetAvailable` turned `True` |
| Warning `SecretMissing`, `SecretKeyMissing`, `WorkloadApplyFailed` | As the conditions above |
| Warning `ConfigMapMissing`, `GracePeriodTooShort`, `RunnerFailedStart`, `HookImageUnset` | Emitted once when the degradation starts, not on every reconcile |
| On a ClaudeRunner: Normal `PodCreated`, `RunnerSucceeded`; Warning with the failure reason | Runner lifecycle |

## Admission and validation errors

| Error | Cause |
| :- | :- |
| `ValidatingAdmissionPolicy '...orchestrator-secrets' ... denied request: an orchestrator ServiceAccount may only manage its own environment's Secrets named *-work-order` | An `<env>-orchestrator` identity wrote a Secret that is not its own work order (wrong name, label `selfhosted.claudecode.dev/environment` not equal to `<env>`, or a `type` other than `Opaque`). The operator's hook never does; investigate anything else using that ServiceAccount |
| `... orchestrator-runners ... denied request: an orchestrator may only create ClaudeRunners owned by its own ClaudeEnvironment that reference their own work order` | Same, for ClaudeRunners |
| `no matches for kind "ValidatingAdmissionPolicy"` during install | Kubernetes older than 1.30. Install with `admissionPolicy.enabled=false`, or remove `../admission` from the kustomization |
| `runner.image must carry a tag or digest and must not use :latest` | Use `<image>:<tag>` (not `latest`) or `<image>@sha256:<64 hex>` |
| `baseDir must not be /etc/claude, /home/runner or /tmp`, `volumeMounts must not target /etc/claude, /home/runner or /tmp` | These paths belong to the operator's mounts |
| `env may not set operator-owned variables` | `runner.env` names `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR`, `SELF_HOSTED_RUNNER_CLIENT_LABEL` or `SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET`. Use `runner.hostConfig`, `settings.clientLabel` and `environmentSecretRef` |
| `orchestrator.env may not set operator-owned variables` | `onDemand.orchestrator.env` names a `CLAUDE_OPERATOR_*` variable, which the operator sets for the hook. Use `onDemand.maxConcurrentRunners` and `onDemand.orchestrator.hookTimeoutSeconds` instead |
| `extraArgs may not set operator-owned flags` | Use the matching spec field instead |
| `egressCIDRs must be IPv4 CIDRs` | One entry is not `a.b.c.d/n` |
| `hookTimeoutSeconds + 5 must be below expectedSpawnSeconds`; `spec.onDemand.orchestrator.hookTimeoutSeconds` below 15 | The first mirrors the orchestrator's own startup check (the timeout plus its 5-second kill grace must stay below the spawn lease); the hook needs at least 15 s, because its deadline is the timeout minus 10 s |
| `exactly one of spec.fixed or spec.onDemand must be set`, `onDemand requires runner.capacity == 1`, `useAnthropicGitProxy requires runner.capacity == 1`, `fixed.persistentWorkspace requires runner.settings.lockToAccount` | Spec shape rules |
| `exactly one volume source must be set` | A `podTemplate.volumes` entry sets none or several sources |
| `unknown field "spec.runner.podTemplate.hostNetwork"` (or `hostPID`, `privileged`, ...) | Fields that would break Restricted Pod Security are not part of the API |

Do not set `CLAUDE_OPERATOR_*` variables in `onDemand.orchestrator.env`: no rule rejects them yet, and they
would override what the hook reads.

## Pod Security violations

`pods "<name>" is forbidden: violates PodSecurity "restricted:latest": ...` appears as a `FailedCreate`
event on the ReplicaSet (fixed fleet, orchestrator) or in the manager log for an on-demand runner pod.
The operator's pods are Restricted-compliant, so the cause is something that changes them after the
operator: a mutating webhook or sidecar injector (service mesh, security agents) adding privileged fields.
Exclude the namespace from the injector, or label the pods out of it. A container that fails with
`container has runAsNonRoot and image will run as root` is the runner image: build it with a non-root
`USER` (see `examples/runner-image`).

## `--watch-namespaces` mistakes

- An environment in a namespace not in the list is never reconciled: no status, no events, no pods.
  Add the namespace (Helm `watchNamespaces`, comma-separated) and restart the manager.
- The manager log shows `unable to get: <ns>/<name> because of unknown namespace for the cache` when it is
  asked about an object outside the list.
- The Secret and ConfigMaps an environment references must live in the environment's own namespace.
- With the chart's `rbac.namespaced=true` the manager gets a Role in the release namespace only: set
  `watchNamespaces` to that namespace and create environments there. A namespaced Role cannot grant `get`
  on the `default/kubernetes` Endpoints, so on-demand environments with `spec.runner.networkPolicy.enabled` then
  report `FleetAvailable=False`, reason `WorkloadApplyFailed`, message
  `could not apply NetworkPolicy (Forbidden): read API server endpoints: ...`.

## More detail: log level and tracing

- Manager log level: `--log-level=debug` (`debug`, `info`, `error`) and `--log-format=text` for readable
  output (default `json`). With kustomize, add the flags to the manager container args; with Helm,
  `--set 'manager.args={--leader-elect,--log-level=debug}'`.
- Runner and orchestrator log level: `runner.settings.logLevel` and `onDemand.orchestrator.logLevel`
  (`info` or `debug`).
- Tracing: set an OTLP gRPC endpoint with `--tracing-endpoint` (Helm `tracing.endpoint`; defaults to
  `OTEL_EXPORTER_OTLP_ENDPOINT`) and `--tracing-sample-ratio` (Helm `tracing.sampleRatio`, default 0.1). For
  the hook, set `OTEL_EXPORTER_OTLP_ENDPOINT` through `onDemand.orchestrator.env`; it samples every spawn,
  and the trace continues into the ClaudeRunner reconcile through the
  `selfhosted.claudecode.dev/traceparent` annotation until the runner reaches a terminal phase.
