# Metrics

Two sets of series matter: the operator's own (served by the manager) and the product's (served by
every runner and orchestrator pod). This page lists both scrape targets, every operator series, alert
rules and dashboard queries.

## Scrape targets

| Target | What | Kustomize | Helm |
| :- | :- | :- | :- |
| Manager | `ServiceMonitor` `controller-manager-metrics-monitor`: HTTPS on port 8443 (`https`), path `/metrics`, bearer token; controller-runtime's authentication and authorization filter guards the endpoint, so the scraper's ServiceAccount needs the `metrics-reader` ClusterRole | Uncomment `- ../prometheus` in `config/default/kustomization.yaml` | `prometheus.enabled=true` (with `metrics.enabled`, `metrics.secure`, and `certManager.enabled` for a verified certificate) |
| Runner and orchestrator pods | `PodMonitor` `runner-metrics`: pods labelled `app.kubernetes.io/part-of=claude-code-self-hosted-runner` in any namespace, port `health`, path `/metrics`, every 30 s | Same `../prometheus` directory ([`config/prometheus/podmonitor.yaml`](../config/prometheus/podmonitor.yaml)) | Same `prometheus.enabled=true` |

Both need the Prometheus Operator CRDs. The PodMonitor follows the product's documented example; every
pod the operator creates carries the `part-of` label and a named `health` port. The ServiceMonitor ships
with `insecureSkipVerify: true` unless cert-manager is enabled (`config/prometheus/monitor_tls_patch.yaml`
for kustomize, `certManager.enabled` for Helm).

## Operator series

All six carry `namespace` and `environment` labels (the ClaudeEnvironment's namespace and name). Series
for a deleted environment are removed.

| Series | Type | Extra labels | Meaning |
| :- | :- | :- | :- |
| `claude_operator_environment_ready` | gauge | | `1` when the environment's `Ready` condition is `True`, else `0` |
| `claude_operator_drain_budget_seconds` | gauge | | The computed `terminationGracePeriodSeconds` the runner's drain path needs (`status.computedDrainBudgetSeconds`) |
| `claude_operator_fixed_fleet_replicas` | gauge | `state` (`desired`, `ready`, `updated`) | Fixed-fleet runner replicas; absent in on-demand mode |
| `claude_operator_environment_runners` | gauge | `phase` (`pending`, `running`) | On-demand ClaudeRunners by phase; absent in fixed mode |
| `claude_operator_runner_spawn_duration_seconds` | histogram | | Seconds from ClaudeRunner creation to its pod running, measured from the pod's `status.startTime` when set. Buckets 1, 2, 5, 10, 20, 30, 60, 120, 300, 600 |
| `claude_operator_runners_total` | counter | `outcome` (`created`, `succeeded`, `failed`, `spawn_timeout`) | On-demand runner lifecycle events. `failed` counts every failure once (including `WorkOrderMissing`, `EnvironmentMissing`, `EnvironmentMismatch`, `PodLost` and `SpawnTimeout`); a spawn timeout also counts `spawn_timeout` |

No label carries an email, a JWT or a Secret value.

### controller-runtime series

The manager also serves controller-runtime's defaults. The `controller` label is `claudeenvironment` or
`clauderunner`.

| Series | Use |
| :- | :- |
| `controller_runtime_reconcile_total{controller,result}` | Reconcile rate by result (`success`, `error`, `requeue`, `requeue_after`) |
| `controller_runtime_reconcile_errors_total{controller}` | Reconcile errors; alert on a sustained rate |
| `controller_runtime_terminal_reconcile_errors_total{controller}`, `controller_runtime_reconcile_panics_total{controller}`, `controller_runtime_reconcile_timeouts_total{controller}` | Rare failure modes |
| `controller_runtime_reconcile_time_seconds{controller}` | Reconcile latency histogram |
| `controller_runtime_max_concurrent_reconciles{controller}`, `controller_runtime_active_workers{controller}` | Worker pool |
| `workqueue_depth`, `workqueue_adds_total`, `workqueue_queue_duration_seconds`, `workqueue_work_duration_seconds`, `workqueue_retries_total`, `workqueue_unfinished_work_seconds`, `workqueue_longest_running_processor_seconds` | Per-controller work queues (`name` label) |
| `rest_client_requests_total{code,host,method}` and the `rest_client_*` size and retry series | API server traffic |
| `leader_election_master_status{name}` | `1` on the leader |

The Go runtime and process collectors (`go_*`, `process_*`) are registered as well.

## Product series

The runner and orchestrator series (`claude_code_self_hosted_runner_*`,
`claude_code_self_hosted_orchestrator_*`) are documented in the product
[reference](https://code.claude.com/docs/en/self-hosted-environments-reference#prometheus-metrics). The
operator does not change them. One caution from that page:
`claude_code_self_hosted_runner_locked_account{email}` carries the account email; drop or hash it at
scrape time if your metrics store is broadly readable, for example with a `metricRelabelings` entry on
the PodMonitor endpoint:

```yaml
metricRelabelings:
  - action: labeldrop
    regex: email
```

## Alert rules

The first two groups are the product's documented examples, unchanged
([source](https://code.claude.com/docs/en/self-hosted-environments-reference#prometheus-metrics)). The third
group is the operator's. Tune thresholds for your fleet.

```yaml
groups:
  - name: claude-code-self-hosted-runner
    rules:
      - alert: ClaudeRunnerPollStale
        expr: claude_code_self_hosted_runner_last_poll_age_seconds > 60
        for: 2m
        labels: {severity: warning}
        annotations:
          summary: "Runner {{ $labels.pod }} has not polled in >60s"
      - alert: ClaudeRunnerVersionDrift
        expr: count(count by (version) (claude_code_self_hosted_runner_info)) > 1
        for: 30m
        labels: {severity: info}
        annotations:
          summary: "Runners are running mixed versions"
      - alert: ClaudeRunnerInitErrorsHigh
        expr: increase(claude_code_self_hosted_runner_session_init_errors_total[10m]) > 3
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "Runner {{ $labels.pod }}: >3 session init failures in 10m (checkout hook / git / token / pre-init crash)"
      - alert: ClaudeRunnerPollErrors
        expr: sum by (pod) (rate(claude_code_self_hosted_runner_poll_errors_total[5m])) > 0
        for: 2m
        labels: {severity: warning}
        annotations:
          summary: "Runner {{ $labels.pod }}: PollWork failing ({{ $value | humanize }}/s over 5m)"
      - alert: ClaudeRunnerSessionStartHookErrors
        expr: increase(claude_code_self_hosted_runner_session_start_hook_errors_total[10m]) > 3
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "Runner {{ $labels.pod }}: >3 SessionStart hook failures in 10m"

  - name: claude-code-self-hosted-orchestrator
    rules:
      - alert: ClaudeOrchestratorDisconnected
        expr: claude_code_self_hosted_orchestrator_connected == 0
        for: 2m
        labels: {severity: critical}
        annotations:
          summary: "Orchestrator {{ $labels.pod }} cannot reach the Anthropic control plane"
      - alert: ClaudeOrchestratorPollStale
        expr: claude_code_self_hosted_orchestrator_last_poll_age_seconds > 90
        for: 2m
        labels: {severity: warning}
        annotations:
          summary: "Orchestrator {{ $labels.pod }} has not polled in >90s (poll loop waits on hook execution)"
      - alert: ClaudeOrchestratorCircuitBroken
        expr: claude_code_self_hosted_orchestrator_queue_circuit_broken_sessions > 0
        for: 1m
        labels: {severity: critical}
        annotations:
          summary: "{{ $value }} sessions circuit-broken — spawn-runner hook is repeatedly non-retryable; fix infra then retry from the Activity tab"
      - alert: ClaudeOrchestratorPollErrors
        expr: sum by (pod) (rate(claude_code_self_hosted_orchestrator_poll_errors_total[5m])) > 0
        for: 2m
        labels: {severity: warning}
        annotations:
          summary: "Orchestrator {{ $labels.pod }}: PollSpawnHints failing ({{ $value | humanize }}/s over 5m)"
      - alert: ClaudeOrchestratorSpawnHookFailing
        expr: sum by (pod) (increase(claude_code_self_hosted_orchestrator_spawn_hooks_total{result!="ok"}[5m])) > 3
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "Orchestrator {{ $labels.pod }}: >3 spawn-runner hook failures in 5m"

  - name: claude-selfhosted-operator
    rules:
      - alert: ClaudeEnvironmentNotReady
        expr: claude_operator_environment_ready == 0
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "ClaudeEnvironment {{ $labels.namespace }}/{{ $labels.environment }} has not been Ready for 10m; see kubectl describe cenv"
      - alert: ClaudeRunnersFailing
        expr: sum by (namespace, environment) (rate(claude_operator_runners_total{outcome="failed"}[15m])) > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "On-demand runners of {{ $labels.namespace }}/{{ $labels.environment }} keep failing; see kubectl get crun and their events"
      - alert: ClaudeRunnerSpawnSlow
        # 120 is the default onDemand.orchestrator.expectedSpawnSeconds; use your environment's value.
        expr: histogram_quantile(0.95, sum by (le, namespace, environment) (rate(claude_operator_runner_spawn_duration_seconds_bucket[30m]))) > 120
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "p95 runner spawn time of {{ $labels.namespace }}/{{ $labels.environment }} is above expectedSpawnSeconds; sessions will be re-offered"
      - alert: ClaudeOperatorReconcileErrors
        expr: sum by (controller) (rate(controller_runtime_reconcile_errors_total{controller=~"claudeenvironment|clauderunner"}[10m])) > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "The {{ $labels.controller }} controller keeps failing to reconcile; see the manager logs"
```

`expectedSpawnSeconds` is a spec field, not a series, so the spawn alert takes it as a literal. Keep one
rule per value if your environments differ.

## Dashboard queries

| Panel | PromQL |
| :- | :- |
| Environments not Ready | `count(claude_operator_environment_ready == 0)` |
| Fixed fleet, ready vs desired | `claude_operator_fixed_fleet_replicas{state=~"ready\|desired"}` |
| On-demand runners by phase | `sum by (namespace, environment, phase) (claude_operator_environment_runners)` |
| Runner outcomes per hour | `sum by (outcome) (increase(claude_operator_runners_total[1h]))` |
| Spawn time p50 and p95 | `histogram_quantile(0.5, sum by (le) (rate(claude_operator_runner_spawn_duration_seconds_bucket[15m])))` and the same with `0.95` |
| Drain budget | `claude_operator_drain_budget_seconds` |
| Queue depth (product) | `max by (namespace) (claude_code_self_hosted_orchestrator_pool_pending_sessions)` (environment-wide, so `max`, not `sum`, across replicas) |
| Orchestrator connected (product) | `min by (pod) (claude_code_self_hosted_orchestrator_connected)` |
| Utilization (product) | `sum(claude_code_self_hosted_runner_active_sessions) / sum(claude_code_self_hosted_runner_capacity)` |
| Reconcile errors | `sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m]))` |
| Reconcile latency p95 | `histogram_quantile(0.95, sum by (le, controller) (rate(controller_runtime_reconcile_time_seconds_bucket[5m])))` |
