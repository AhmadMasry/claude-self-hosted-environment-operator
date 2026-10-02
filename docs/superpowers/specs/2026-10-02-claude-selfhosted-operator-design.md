# Claude Code Self-Hosted Environment Operator: Design

Date: 2026-10-02
Status: approved for planning
Language: Go (controller-runtime, kubebuilder v4)

## 1. Purpose

A Kubernetes operator that deploys and runs Claude Code self-hosted
environments. A user creates one `ClaudeEnvironment` resource per
environment they created in claude.ai admin settings, points it at a
runner image they built and a Secret holding the environment key, and the
operator runs the fleet: either a fixed set of runners or an on-demand
orchestrator that spawns one ephemeral runner pod per session.

The project is a public open-source operator intended for other platform
teams to adopt and contribute to. It must be production ready from the first
release: it follows the Claude self-hosted production guidance, the operator
best practices the Kubernetes, Operator SDK, Red Hat and Google documents
describe, and the Restricted Pod Security Standard by default.

### Goals

- Declarative, level-triggered management of runner fleets in both modes
  the product documents: fixed fleet and on-demand.
- Production hardening defaults from the Claude deploy guide: capacity 1
  per-session pods, environment secret kept off session pods, drain-aware
  termination grace, pinned images, read-only hooks and wrapper.
- Restricted Pod Security Standard compliance for every pod the operator
  creates, enforced by the CRD schema and tested in CI.
- Operator hygiene: structural schemas with CEL validation, meaningful
  status conditions, owner references, finalizers only where ordering
  matters, least-privilege RBAC, metrics, structured logs, optional
  OpenTelemetry tracing, resource cleanup.
- Distribution as a Helm chart and kustomize manifests, images signed and
  multi-arch.

### Non-goals (v1)

- Creating or managing environments inside claude.ai. There is no public
  API; users create the environment and copy the key themselves.
- Building the runner image. Anthropic publishes none; the repo ships a
  sample Dockerfile only.
- Autoscaling a fixed fleet with HPA or KEDA. The product documents how;
  the operator exposes nothing beyond the metrics the product already
  serves.
- Conversion webhooks. They arrive with the first API version bump.
- Managing other operators or cluster add-ons such as cert-manager or
  Prometheus Operator. The chart only emits resources for them when told to.

## 2. Product facts the design depends on

Source: https://code.claude.com/docs/en/self-hosted-environments and its
quickstart, deploy, configuration and reference pages.

- The runner is the `claude` binary run as `claude self-hosted-runner`;
  the orchestrator is the same binary run as
  `claude self-hosted-runner orchestrator`. One image serves both roles.
  Requires Claude Code v2.1.224 or later.
- The environment key is a single shared secret, shown once, valid 365
  days. Runners pass it with `--environment-secret-file <path>`.
- A runner locks to the first session's owner and serves up to
  `--capacity` concurrent sessions for that owner. With the default
  `--drain-grace-sec 0` it exits when its sessions finish. Production
  guidance is capacity 1, one container per session, fresh filesystem.
- `--base-dir` and `--capacity` must be identical across all runners in
  one environment.
- Health: `GET /healthz` on `--health-port` (default 8080) returns 200
  whenever the process is alive; `/metrics` is Prometheus on the same
  port.
- Shutdown: on SIGTERM the runner drains. The drain path needs
  `--session-stop-grace-sec` (5) + `--drain-wait-sec` (0) +
  `--post-session-hook-timeout-sec` (60) + 15 s fixed, + 30 s when
  `--push-outcome-on-release` is set: 80 s at defaults. With
  `--defer-shutdown-max-min N` add N minutes plus a post-release grace of
  75 s (or `--drain-wait-sec` + 15 s when drain-wait exceeds 60 s). The
  pod's `terminationGracePeriodSeconds` must be at least that total.
- On-demand: the orchestrator polls Anthropic and runs
  `${hooks-dir}/spawn-runner` once per spawn request with the environment
  variables `CLAUDE_RUNNER_WORK_ORDER_FILE`, `CLAUDE_RUNNER_ORDER_ID`,
  `CLAUDE_RUNNER_SESSION_ID`, `CLAUDE_RUNNER_SESSION_UUID`,
  `CLAUDE_RUNNER_ATTEMPT`, `CLAUDE_RUNNER_POOL_ID`,
  `CLAUDE_RUNNER_ACCOUNT_ID`, `CLAUDE_RUNNER_ACCOUNT_EMAIL`,
  `CLAUDE_RUNNER_PRIMARY_REPO_URL`, `CLAUDE_RUNNER_PRIMARY_REPO_REVISION`,
  `CLAUDE_RUNNER_REPO_SOURCES`, `CLAUDE_RUNNER_CORRELATION_ID`,
  `CLAUDE_RUNNER_CLIENT_PLATFORM`, `CLAUDE_RUNNER_ORDER_SERVER_TIME`.
  Hook contract: idempotent on the order ID, never retry a workload,
  exit 0 = submitted, 1 = retryable, 2+ = non-retryable; stderr tail is
  shown to admins; never print the JWT or email. The spawned runner uses
  the work-order JWT in place of the environment secret, must run
  `--capacity 1`, and must never be restarted. Orchestrator flags:
  `--hooks-dir` (required), `--hook-concurrency` (4), `--hook-timeout`
  (60; timeout + 5 must be below expected-spawn), `--expected-spawn-seconds`
  (120, range 10..3600, identical on all replicas), `--min-idle` (0).
  Orchestrator `/healthz` always returns 200; readiness must be judged on
  the `connected` field in the body.
- Session customization: `--exec-path` wrapper script, `--hooks-dir`
  lifecycle hooks (`checkout`, `post-session`, `command`),
  `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR` (default `~/.claude`) seeded into
  every session. All three must be read-only to sessions.
- Secret rotation: create a new key, roll it out, revoke the old. Runners
  holding a revoked key fail their next poll and exit.
- Failed start vs normal exit: a runner that exits within about a minute
  of starting, repeatedly, is a failed start and prints `[runner:fatal]`
  or `error:`. A runner exiting after draining is normal and may show as
  CrashLoopBackOff under a Deployment.
- Network: all traffic is outbound HTTPS to `api.anthropic.com` plus the
  git host; optional hosts are listed in the deploy page. Sessions must
  not reach the cloud metadata endpoint `169.254.169.254`.

## 3. Language decision

Go, with controller-runtime and kubebuilder v4.

Reasons: the project is public and aimed at platform teams, where Go is
the lingua franca of operators; envtest gives reconciler tests against a
real API server; controller-gen produces CRDs and RBAC from types;
kubebuilder scaffolds metrics, RBAC, Helm and e2e; and the structural
reference, actions-runner-controller (ARC), is Go, so its patterns carry
over directly. Rust with kube-rs 4.x was evaluated as production-capable
with smaller static binaries and stronger typing, and rejected only for
ecosystem and contributor-pool reasons.

## 4. Architecture

Two CRDs, two reconcilers, one hook binary, one manager binary.

```
 claude.ai control plane (api.anthropic.com)
          ^ poll                        ^ poll / stream
          |                             |
 +--------+---------+        +----------+----------+
 | orchestrator pod |        | runner pod          |
 | (user image)     |        | (user image)        |
 |  claude ...      |        |  claude             |
 |  orchestrator    |        |  self-hosted-runner |
 |  --hooks-dir     |        |  --capacity 1       |
 |   spawn-runner --+--+     |  secret: JWT        |
 +------------------+  |     +---------------------+
    (env secret)       |              ^ creates Pod
                       v              |
            ClaudeRunner CR + Secret  |
                       |              |
          +------------+--------------+----------+
          |         operator manager             |
          |  ClaudeEnvironment controller        |
          |  ClaudeRunner controller             |
          +--------------------------------------+
                       ^
                       | user applies
               ClaudeEnvironment CR + Secret(env key)
```

Fixed mode has no orchestrator or ClaudeRunner: the ClaudeEnvironment
controller owns a Deployment (or StatefulSet) of runners directly.

### Why this shape

It mirrors ARC's AutoscalingRunnerSet / EphemeralRunner / listener split.
The hook is the one imperative edge, forced by the product delivering
work through a hook process rather than a watchable queue. It does a
single create and everything downstream is declarative. Idempotency on
the order ID falls out of naming the ClaudeRunner after it.

## 5. API

Group `selfhosted.claudecode.dev`, version `v1alpha1`. The group is a
placeholder until the project owns a domain; it is a one-line change
before the first release. Both kinds are namespaced. The manager runs
cluster-wide and watches all namespaces unless `--watch-namespaces` is
set.

### 5.1 ClaudeEnvironment

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeEnvironment
metadata:
  name: platform
  namespace: claude-runners
spec:
  environmentSecretRef:
    name: claude-env-secret
    key: environment-secret            # default
  runner:
    image: registry.example/claude-runner:2.1.280   # tag or digest; "latest" rejected
    capacity: 1                        # default 1
    baseDir: /workspace                # default
    settings:
      configureGit: false
      confineRepoSettings: warn        # warn | enforce | off
      drainWaitSeconds: 0              # 0..86400
      deferShutdownMaxMinutes: 0       # 0..10080
      drainGraceSeconds: 0             # 0..604800; fixed mode only
      killSessionAfterMinutes: 0       # 0..10080
      releaseIdleSessionMinutes: 0     # 0..10080
      startupTimeoutMinutes: 15        # 0..10080
      sessionStopGraceSeconds: 5
      postSessionHookTimeoutSeconds: 60
      exitIfUnusedMinutes: 0           # on-demand standby runners
      removeSessionState: false
      pushOutcomeOnRelease: false
      useAnthropicGitProxy: false      # requires capacity 1
      gitHostRewrites: []              # "from=to"
      gitSSHRewrites: []               # "host"
      lockToAccount: ""
      clientLabel: ""                  # default: pod name via downward API
      logLevel: info                   # info | debug
      healthPort: 8080
      trustWorkspace: true
    extraArgs: []                      # appended verbatim; escape hatch
    env: []                            # corev1.EnvVar; HTTPS_PROXY, telemetry, etc.
    lifecycleHooks:                    # optional; mounted read-only; sets --hooks-dir
      configMapRef: {name: runner-hooks}
    wrapperScript:                     # optional; mounted read-only; sets --exec-path
      configMapRef: {name: runner-wrapper}
      key: session-wrapper.sh
    hostConfig:                        # optional; mounted read-only;
      configMapRef: {name: runner-claude-config}   # sets SELF_HOSTED_RUNNER_HOST_CONFIG_DIR
    podTemplate: {}                    # curated, see 5.3
    terminationGracePeriodSeconds: null  # computed unless set; must be >= computed
    networkPolicy:                     # see section 7
      enabled: false
      egressCIDRs: []                  # CIDRs for api.anthropic.com, git host, internal services
  fixed:                               # exactly one of fixed | onDemand
    replicas: 3
    persistentWorkspace:               # optional; StatefulSet; requires settings.lockToAccount
      volumeClaimTemplate: {}          # corev1.PersistentVolumeClaimSpec
  onDemand:
    orchestrator:
      replicas: 2
      image: ""                        # defaults to runner.image
      expectedSpawnSeconds: 120        # 10..3600
      hookTimeoutSeconds: 60           # + 5 < expectedSpawnSeconds
      hookConcurrency: 4
      minIdle: 0
      logLevel: info
      healthPort: 8080
      env: []
      podTemplate: {}
    runnerTTLSecondsAfterFinished: 300
    maxConcurrentRunners: 0            # 0 = unlimited
status:
  observedGeneration: 3
  conditions:
    - type: Ready                      # fleet serving
    - type: SecretFound
    - type: FleetAvailable             # Deployment/StatefulSet or orchestrator available
    - type: Progressing
    - type: Degraded                   # reasons: SecretMissing, InvalidImage,
                                       #   OrchestratorUnavailable, RunnerFailedStart,
                                       #   SpawnFailures, GracePeriodTooShort
  mode: fixed | onDemand
  computedDrainBudgetSeconds: 80
  fixed:
    replicas: 3
    readyReplicas: 3
    updatedReplicas: 3
  onDemand:
    orchestratorReadyReplicas: 2
    pendingRunners: 1
    runningRunners: 4
```

Printer columns: Mode, Ready, Runners (running), Age.

### 5.2 ClaudeRunner

Internal. Created by the spawn-runner hook, never by users (documented;
not enforced in v1).

```yaml
apiVersion: selfhosted.claudecode.dev/v1alpha1
kind: ClaudeRunner
metadata:
  name: <orderID>                      # CLAUDE_RUNNER_ORDER_ID is safe for names
  namespace: claude-runners
  labels:
    selfhosted.claudecode.dev/environment: platform
    selfhosted.claudecode.dev/order-id: <orderID>
    selfhosted.claudecode.dev/session-id: <sessionID>   # absent for pre-warm
  annotations:
    selfhosted.claudecode.dev/traceparent: "00-..."     # optional, see 8.4
  ownerReferences: [ClaudeEnvironment platform]
spec:
  environmentRef: {name: platform}
  orderID: ...
  sessionID: ""                        # empty for pre-warming
  sessionUUID: ""
  attempt: 0
  clientPlatform: ""
  primaryRepoURL: ""
  accountID: ""                        # tagged account ID; email is never stored
  workOrderSecretRef: {name: <orderID>-work-order}
status:
  phase: Pending | Running | Succeeded | Failed
  podName: ...
  startedAt, finishedAt
  reason, message                      # pod-derived: ImagePullBackOff, Unschedulable, ExitCode
  conditions: [Ready]
```

Printer columns: Phase, Session, Pod, Age.

### 5.3 Curated podTemplate

A raw `corev1.PodSpec` is not exposed. Reasons: schema size (ARC strips
properties with yq to stay under limits), and Restricted PSS cannot be
guaranteed when host namespaces, hostPath, privileged or capability adds
are expressible. Fields:

- `labels`, `annotations`
- `serviceAccountName`, `automountServiceAccountToken` (default false)
- `imagePullSecrets`
- `resources` (container resources)
- `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`,
  `priorityClassName`, `runtimeClassName`, `schedulerName`
- `securityContext`: only `runAsUser`, `runAsGroup`, `fsGroup`,
  `supplementalGroups`
- `volumes`: each entry is one of configMap, secret, emptyDir, projected,
  downwardAPI, persistentVolumeClaim, ephemeral, csi
- `volumeMounts`
- `initContainers` and `sidecars` are not supported in v1

The operator always sets: `runAsNonRoot: true`, `seccompProfile:
RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities: drop
ALL`, `readOnlyRootFilesystem: true`, `hostNetwork/hostPID/hostIPC:
false`, no hostPorts. Users who need a writable root filesystem for their
image can add emptyDir mounts; the operator always provides emptyDirs for
the base dir, `/home/<user>`, and `/tmp`.

### 5.4 Validation (CEL on the CRD, no admission webhook in v1)

- Exactly one of `spec.fixed` and `spec.onDemand`.
- `onDemand` implies `runner.capacity == 1`.
- `settings.useAnthropicGitProxy` implies `runner.capacity == 1`.
- `fixed.persistentWorkspace` implies `settings.lockToAccount != ""`.
- `onDemand.orchestrator.hookTimeoutSeconds + 5 <
  onDemand.orchestrator.expectedSpawnSeconds`.
- `expectedSpawnSeconds` in 10..3600; minute fields <= 10080;
  `drainWaitSeconds` <= 86400; `drainGraceSeconds` <= 604800.
- `runner.image` must contain a tag or digest and the tag must not be
  `latest`.
- `terminationGracePeriodSeconds`, if set, must be >= the computed budget.
  This cannot be expressed in CEL across derived values, so the controller
  checks it and sets `Degraded/GracePeriodTooShort` instead of rewriting
  the user's value.
- `extraArgs` must not contain `--environment-secret-file`, `--capacity`,
  `--base-dir`, `--health-port`, `--hooks-dir`, `--exec-path`: these are
  owned by the operator.

## 6. Controllers and data flow

One manager binary, leader-elected, with two reconcilers. Each reconciler
is a thin loop over idempotent `ensureX` subroutines that each check,
apply with server-side apply, and return. Status is written once per
pass, at the end; any spec mutation (finalizer add) returns and requeues
so that a single reconcile makes one change (Red Hat practice 4). Errors
return immediately and requeue with the manager's exponential backoff;
steady state resyncs every 10 minutes.

### 6.1 ClaudeEnvironment controller

Owns, by mode:

- Always: `SecretFound` by reading `environmentSecretRef`; a content hash
  of the Secret, the runner settings and the mounted ConfigMaps is written
  as a pod-template annotation so changes roll pods. Optional
  NetworkPolicy (section 7).
- Fixed: a Deployment named `<env>-runner`, or a StatefulSet when
  `persistentWorkspace` is set. Built from the docs' recipe:
  args `self-hosted-runner --environment-secret-file /etc/claude/environment-secret
  --capacity N --base-dir ... ` plus settings, Secret mounted read-only at
  `/etc/claude`, `health` containerPort, readiness and liveness probes on
  `/healthz`, `terminationGracePeriodSeconds` = computed budget,
  `restartPolicy: Always`. Label
  `app.kubernetes.io/part-of: claude-code-self-hosted-runner` so the
  product's documented PodMonitor matches.
- OnDemand: a ServiceAccount `<env>-orchestrator`; a Role granting
  `get` on ClaudeEnvironments, `create, get` on ClaudeRunners (plus `list`
  only when `maxConcurrentRunners` is set), and `create, patch` on
  Secrets, all in the environment's namespace; a RoleBinding; a
  Deployment `<env>-orchestrator` running the user's image with args
  `self-hosted-runner orchestrator --environment-secret-file ...
  --hooks-dir /etc/claude/hooks --expected-spawn-seconds ...`. An init
  container from the operator image copies `/spawn-runner` into an
  emptyDir mounted at `/etc/claude/hooks` read-only on the main container.
  The hook needs `CLAUDE_OPERATOR_ENVIRONMENT`, namespace via downward
  API, and `CLAUDE_OPERATOR_MAX_CONCURRENT_RUNNERS`. Readiness probe is
  an exec probe that curls `/healthz` and checks `"connected":true`,
  because the HTTP status is always 200. Liveness is the plain HTTP probe.

Drain budget computation (pure function, unit-tested):

```
budget = sessionStopGraceSeconds + drainWaitSeconds + postSessionHookTimeoutSeconds + 15
if pushOutcomeOnRelease: budget += 30
if deferShutdownMaxMinutes > 0:
    postRelease = 75 if drainWaitSeconds <= 60 else drainWaitSeconds + 15
    budget += deferShutdownMaxMinutes*60 + postRelease
```

Status: `FleetAvailable` from the child workload's Available condition;
`Ready` = SecretFound && FleetAvailable && !Degraded; counts from the
child and from a label-selected list of ClaudeRunners.

Failed-start detection in fixed mode: a runner container that has
restarted more than 3 times in 10 minutes with every run shorter than 60
seconds sets `Degraded/RunnerFailedStart` with the last line matching
`[runner:fatal]` or `error:` from the previous container's termination
message or log tail. Restarts after long runs are normal drains and are
ignored.

Secret rotation: user updates the Secret; the hash changes; pods roll.
Runners holding the revoked key exit and are replaced.

Deletion: no finalizer. Owner references garbage-collect every child,
including ClaudeRunners and their Secrets and Pods.

### 6.2 spawn-runner hook

Static Go binary, `cmd/spawn-runner`, shipped in the operator image at
`/spawn-runner`. Behaviour:

1. Read the hook environment; require `CLAUDE_RUNNER_WORK_ORDER_FILE` and
   `CLAUDE_RUNNER_ORDER_ID`; read the JWT from the file.
2. Get the ClaudeEnvironment named by `CLAUDE_OPERATOR_ENVIRONMENT`. Not
   found or forbidden: exit 2 with a one-line reason.
3. If `maxConcurrentRunners > 0`, count ClaudeRunners for the environment
   in phase Pending or Running (a `list` is needed; grant `list` on
   ClaudeRunners only when the cap is set). At cap: exit 1.
4. Create Secret `<orderID>-work-order` with the JWT, then create
   ClaudeRunner `<orderID>` with `ownerReferences` to the environment;
   then patch the Secret's owner to the ClaudeRunner. AlreadyExists on
   either: exit 0 (redelivery). Timeout, 5xx, 429, connection errors:
   exit 1. 4xx other than conflict: exit 2.
5. stderr carries only the reason; stdout carries one JSON log line with
   order ID, session ID, outcome. Never the JWT or email.

The whole run is bounded by a context deadline below the orchestrator's
`--hook-timeout` (default 60 s) so the hook never gets killed mid-create.

### 6.3 ClaudeRunner controller

- Adds a finalizer on first sight.
- Ensures one Pod `<orderID>` with: the runner template from the
  referenced ClaudeEnvironment, args `self-hosted-runner
  --environment-secret-file /etc/claude/work-order/jwt --capacity 1
  --base-dir ...` plus settings, the work-order Secret mounted read-only,
  `restartPolicy: Never`, the computed grace period,
  `automountServiceAccountToken: false` unless the template overrides,
  owner reference to the ClaudeRunner.
- Phase from pod: `Pending` until `Running`; `Running`; `Succeeded` when
  the container exits 0; `Failed` otherwise, with `reason` from the
  container state. A pod not `Running` within
  `expectedSpawnSeconds` is marked `Failed/SpawnTimeout`; the control
  plane re-offers the session with a new order ID, so the pod is deleted.
- On terminal phase, record `finishedAt`; after
  `runnerTTLSecondsAfterFinished` delete the ClaudeRunner. Finalizer
  logic deletes the Pod and waits for it to disappear before removing the
  finalizer, so a runner never outlives its record.
- If the ClaudeEnvironment is gone, the ClaudeRunner is garbage-collected
  via owner reference; the finalizer still removes the Pod first.

## 7. Security and hardening

- Per-session isolation (on-demand): capacity 1, drain-grace 0,
  `restartPolicy: Never`, fresh emptyDir workspace, one pod per session.
- Environment secret stays on the orchestrator in on-demand mode. Runner
  pods only ever see a single-use JWT. In fixed mode the secret is on
  every runner because the product requires it; status carries an
  informational condition `SecretOnRunners=True` so dashboards can show it.
- Hooks, wrapper and host config mount read-only with `defaultMode` 0555
  (hooks, wrapper) and 0444 (host config). The spawn-runner binary lives
  in an emptyDir filled by an init container and mounted read-only.
- RBAC. Manager ClusterRole: full on both CRDs and their status; CRUD on
  Pods, Deployments, StatefulSets, Secrets, ConfigMaps, ServiceAccounts,
  Roles, RoleBindings, NetworkPolicies, Events; `get, list, watch` on
  Nodes is not needed and not granted; coordination Leases for leader
  election. Namespace-scoped install is supported by swapping ClusterRole
  for Roles in watched namespaces. Orchestrator Role is per environment
  and minimal (6.1). Runner pods get no token by default.
- Pod security: every pod meets the Restricted Pod Security Standard
  (section 5.3). The e2e namespace is labelled
  `pod-security.kubernetes.io/enforce=restricted` so violations fail CI.
  The Helm chart labels its namespace the same way by default.
- Egress: optional per-environment NetworkPolicy, off by default. When
  enabled: default-deny egress; allow UDP/TCP 53 to kube-dns; allow TCP
  443 to `egressCIDRs`; explicit deny of `169.254.169.254/32`.
  Hostnames cannot be expressed in NetworkPolicy, so the user supplies the
  CIDRs for `api.anthropic.com` and the git host, and the docs say how to
  do it with their CNI's FQDN policies instead when available.
- Secret hygiene: no email in any object, log or metric label. The hook
  and the manager redact anything matching the JWT prefix in error paths.
- Version pinning: image tag or digest required; `latest` rejected.
- Known friction: Claude's in-session sandbox may need capabilities
  Restricted forbids. Documented as the runner image's concern with a
  checklist for users to test.

## 8. Observability

### 8.1 Conditions and events

Conditions as in section 5 with `observedGeneration`. Normal events for
child creation and rollouts; Warning events for `SecretMissing`,
`SpawnCapReached`, `RunnerFailed`, `GracePeriodTooShort`.

### 8.2 Metrics

Manager metrics follow kubebuilder v4: served on 8443 over HTTPS with
controller-runtime's authentication and authorization filter, cert from
cert-manager when enabled or self-signed otherwise, ServiceMonitor
optional. Custom series registered on the controller-runtime registry,
following Kubernetes SIG Instrumentation naming (prefix, snake_case,
`_total`, base units, bounded labels):

- `claude_operator_environment_runners{namespace,environment,phase}` gauge
- `claude_operator_environment_ready{namespace,environment}` gauge 0/1
- `claude_operator_drain_budget_seconds{namespace,environment}` gauge
- `claude_operator_runner_spawn_duration_seconds{namespace,environment}`
  histogram, hook create to pod Running
- `claude_operator_spawn_hook_results_total{namespace,environment,result}`
  counter, result in ok|retryable|non_retryable, incremented by the
  controller from an annotation the hook writes on the ClaudeRunner
  (the hook process is short-lived and cannot be scraped)
- `claude_operator_reconcile_*` come from controller-runtime

No per-session or per-order label on any series. The product's own
per-session series already exist on the runner's `/metrics`.

A PodMonitor for orchestrator and runner pods (plain HTTP on the product's
health port) ships in the chart, matching the label and port name from the
product's documented example.

### 8.3 Logging

controller-runtime zap via logr. Flags `--log-level` and `--log-format
json|text` (JSON default). Version and commit logged at startup. Every
line carries `namespace`, `environment`, and where relevant `orderID`,
`sessionID`. The hook emits the same JSON shape. Email is never logged.

### 8.4 Tracing (optional, off by default)

OpenTelemetry SDK behind `--tracing-endpoint` (OTLP gRPC) and the standard
`OTEL_*` environment variables. Unset means no exporter, no overhead.
Root span per reconcile with child spans per ensure step and per API
write; sampled, not full rate. The hook starts a span for the spawn and
writes W3C `traceparent` into the ClaudeRunner annotation
`selfhosted.claudecode.dev/traceparent`; the ClaudeRunner controller
continues that trace so one trace covers hook, CR creation, pod creation
and pod Running. Lives in `internal/telemetry` so it can be removed without
touching reconcilers. Metrics stay Prometheus-native and are OTel
compatible via collector scraping.

## 9. Repository layout

Kubebuilder v4 layout. Module path: `github.com/OWNER/claude-self-hosted-environment-operator`
(OWNER to be set before `kubebuilder init`).

```
api/v1alpha1/                 types, deepcopy, CEL markers, docs
cmd/main.go                   manager
cmd/spawn-runner/main.go      hook
internal/controller/
  claudeenvironment/          reconciler + focused test files
  clauderunner/
internal/builders/            pure functions: env -> Deployment/StatefulSet/Pod/
                              orchestrator Deployment/RBAC/NetworkPolicy; drain budget
internal/hook/                hook logic behind a client interface (unit-testable)
internal/telemetry/           otel wiring
internal/metrics/             custom collectors
config/                       kustomize: crd, rbac, manager, prometheus, certmanager,
                              network-policy, samples
dist/chart/                   Helm chart generated by helm/v2-alpha plugin
examples/
  runner-image/Dockerfile     sample runner/orchestrator image
  fixed-fleet.yaml, on-demand.yaml, hooks-configmap.yaml, wrapper-configmap.yaml
docs/                         install, hardening, upgrade, troubleshooting, metrics
test/e2e/                     kind e2e (PR tier) and real-environment tier (nightly)
test/utils/
hack/                         scripts: setup-envtest, kind config, chart contract check
Dockerfile                    operator image: manager + spawn-runner
Makefile, PROJECT, .golangci.yaml, .github/workflows/
SECURITY.md CONTRIBUTING.md TROUBLESHOOTING.md CODEOWNERS LICENSE (Apache-2.0)
```

## 10. Testing

- Unit: builders and drain budget, table-driven; hook exit-code mapping
  against a fake client for every branch; fuzz tests for the hook's
  environment parser and the drain-budget function.
- envtest (Ginkgo, scaffolded `suite_test.go` per controller): create,
  update, secret rotation rolls pods, mode switch, deletion and GC, TTL
  cleanup, failed-start detection, spawn timeout, finalizer ordering.
- Chart contract test: rendered chart RBAC equals controller-gen RBAC;
  rendered manager Deployment is Restricted-compliant.
- e2e PR tier (kind, no credentials): install chart into a namespace
  labelled `enforce=restricted`; apply fixed-fleet sample with a stub
  image that serves `/healthz` and sleeps, assert Deployment Ready and
  grace period; apply on-demand sample with a stub orchestrator image
  that invokes `/etc/claude/hooks/spawn-runner` with a fake work order,
  assert Secret + ClaudeRunner + Pod appear, Pod reaches Running, phase
  transitions, TTL cleanup, and a redelivered order creates nothing new.
- e2e nightly tier (secret-gated): real environment key from repository
  secrets; real runner image built from `examples/runner-image`; dispatch
  a session with the product's documented CI test loop; assert a
  ClaudeRunner appears, pod exits 0, cleanup. Upgrade test: install
  previous chart release, upgrade, assert fleet rolls and environments stay
  Ready.
- Lint: golangci-lint, shellcheck on scripts and sample hooks, CodeQL.

## 11. Packaging, build, release

- Operator Dockerfile (multi-stage):

```dockerfile
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/manager ./cmd \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/spawn-runner ./cmd/spawn-runner

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/manager /manager
COPY --from=build /out/spawn-runner /spawn-runner
USER 65532:65532
ENTRYPOINT ["/manager"]
```

- Sample runner/orchestrator Dockerfile (`examples/runner-image`): one
  image for both roles.

```dockerfile
FROM debian:bookworm-slim
ARG CLAUDE_CODE_VERSION
ARG CLAUDE_ARCH=linux-x64            # or linux-arm64
ARG RUNNER_UID=10001
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION:?set with --build-arg CLAUDE_CODE_VERSION}/${CLAUDE_ARCH}/claude" \
      -o /usr/local/bin/claude \
 && chmod 0755 /usr/local/bin/claude
# Verify against the release's signed manifest (see docs/setup#binary-integrity-and-code-signing)
RUN groupadd -g ${RUNNER_UID} runner \
 && useradd -m -u ${RUNNER_UID} -g ${RUNNER_UID} -s /usr/sbin/nologin runner \
 && mkdir -p /workspace /etc/claude/hooks /home/runner/.claude \
 && chown -R runner:runner /workspace /home/runner
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'
USER ${RUNNER_UID}:${RUNNER_UID}
ENV HOME=/home/runner
ENTRYPOINT ["claude"]
```

  The operator mounts emptyDirs over `/workspace`, `/home/runner` and
  `/tmp` so the image runs with a read-only root filesystem. Users layer
  their toolchains on top. Build with
  `--build-arg CLAUDE_CODE_VERSION="$(curl -fsSL https://downloads.claude.ai/claude-code-releases/stable)"`
  or a pinned version.

- Multi-arch images (linux/amd64, linux/arm64) via buildx, pushed to
  GHCR, signed with cosign, with an SBOM attached.
- Helm chart from kubebuilder `helm/v2-alpha`, CRDs under `templates/crd`
  so upgrades update them; pushed as an OCI artifact. Kustomize manifests
  in `config/` for non-Helm users.
- CI (GitHub Actions): lint, unit + envtest, e2e PR tier on kind, chart
  lint and contract test, image build (no push) on PRs; nightly real e2e
  and upgrade test; release workflow on tags.
- Versioning: semver for the operator; Kubernetes API versioning for
  CRDs. CRD field removals only with a version bump and conversion
  webhook.

## 12. Documentation to ship

README with a 10-minute path (create environment in claude.ai, create
Secret, build image, apply sample), `docs/hardening.md` mapping each
product hardening item to the operator field that implements it,
`docs/upgrade.md`, `docs/metrics.md` with alert rule examples merged with
the product's, `TROUBLESHOOTING.md` keyed on conditions and reasons.

## 13. Open items

- `OWNER` in the module path and the API group domain. Both are
  placeholders; set before `kubebuilder init`.
- Whether to add a validating webhook in v1 for the cross-field checks
  CEL cannot express (grace period vs computed budget). Decision: no;
  Degraded condition instead.
