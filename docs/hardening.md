# Hardening

This page maps Anthropic's production hardening checklist to what the operator does and what is left to
you, then covers the operator's own security controls: Pod Security, the admission policy, the egress
NetworkPolicy, Secret hygiene, RBAC and cache scope, and the image supply chain.

Sources: [Deploy self-hosted environments to production, "Harden your deployment"](https://code.claude.com/docs/en/self-hosted-environments-deploy#harden-your-deployment),
[Network requirements](https://code.claude.com/docs/en/self-hosted-environments-deploy#network-requirements)
and the [reference](https://code.claude.com/docs/en/self-hosted-environments-reference).

## Anthropic's checklist, item by item

| Checklist item | What the operator does | What you do |
| :- | :- | :- |
| **Ephemeral, per-session containers** (`--capacity 1`, default `--drain-grace-sec 0`, no filesystem reuse) | On-demand mode (`spec.onDemand`) creates one pod per session with `restartPolicy: Never` and deletes it after `runnerTTLSecondsAfterFinished`; CEL requires `runner.capacity == 1` in on-demand mode ("onDemand requires runner.capacity == 1"). `settings.drainGraceSeconds` defaults to `0`. In fixed mode the pods are a Deployment with `restartPolicy: Always`: a runner that exits is restarted in the same pod, so its `emptyDir` workspace survives the restart. `fixed.persistentWorkspace` reuses a PVC on purpose and CEL requires `settings.lockToAccount` with it ("fixed.persistentWorkspace requires runner.settings.lockToAccount"), so a workspace never crosses owners. | Prefer on-demand mode. In fixed mode keep `capacity: 1` and `drainGraceSeconds: 0` if you need one session per container. |
| **No broad credentials in the image** | Runner pods do not mount a ServiceAccount token unless you set `podTemplate.automountServiceAccountToken: true`. Per-session credentials go through `runner.wrapperScript` (rendered as `--exec-path`) and the `checkout` hook in `runner.lifecycleHooks` (rendered as `--hooks-dir`), or `settings.useAnthropicGitProxy`. | Build the runner image without long-lived keys or tokens. |
| **Keep the environment secret off session-running hosts** | In on-demand mode the environment Secret is mounted only on the orchestrator pod; each runner pod gets its single-use work-order JWT. The `SecretOnRunners` condition reports `False/OnDemandSecretOnOrchestrator` in on-demand mode and `True/FixedModeSecretOnPods` in fixed mode, so the posture is visible on every environment. | Prefer on-demand mode. On a fixed fleet, treat the key as readable by every session and rotate it after any suspected compromise. |
| **Default-deny network egress** | `spec.runner.networkPolicy.enabled: true` adds a default-deny egress NetworkPolicy for every pod of the environment (see [NetworkPolicy](#networkpolicy)). Off by default. | Enable it, list `egressCIDRs`, and run a CNI that enforces NetworkPolicy. Hostname allowlists need a DNS-aware policy from your CNI or an egress proxy. |
| **Least-privilege host IAM** | The runner pod's identity is `podTemplate.serviceAccountName` (default: the namespace's `default` ServiceAccount, token not mounted). The orchestrator's ServiceAccount `<env>-orchestrator` holds only the namespaced Role described in [RBAC scope](#rbac-scope-and-caches). Node IAM is outside the operator. | Give the node identity only what the kubelet needs. Use IRSA or Workload Identity through `podTemplate.serviceAccountName` when sessions need cloud access. |
| **Block the cloud metadata endpoint from sessions** | When `networkPolicy.enabled` is true, `169.254.169.254` is denied unless it falls inside an `egressCIDRs` entry, and then the operator adds `except: [169.254.169.254/32]` to that entry. | Also block it at the node: IMDSv2 with a hop limit of one, or GKE Workload Identity with metadata concealment. Whether a NetworkPolicy stops link-local traffic depends on the CNI. |
| **Per-runner filesystem isolation** (hooks dir, wrapper and `~/.claude/` read-only to the session) | Each runner pod has its own `emptyDir` workspace. The environment secret, `lifecycleHooks` (`/etc/claude/hooks`), `wrapperScript` and `hostConfig` (`/etc/claude/host-config`) are mounted read-only, and the root filesystem is read-only. CEL rejects `baseDir` and user `volumeMounts` on `/etc/claude`, `/home/runner` or `/tmp` ("volumeMounts must not target /etc/claude, /home/runner or /tmp", "baseDir must not be /etc/claude, /home/runner or /tmp"). | Ship host config through `runner.hostConfig`, not through a writable volume. |
| **Dispatch has no per-environment access control** | Not something the operator can change. `settings.lockToAccount` renders `--lock-to-account` and pre-locks fixed-mode runners to one account. | Put on a runner only data that every member who can dispatch to the environment may read. See [Personal data](#personal-data) for `lockToAccount`. |
| **Enforce the repo-settings guard** (`--confine-repo-settings`) | `settings.confineRepoSettings` renders `--confine-repo-settings`; allowed values `warn` (default, as in the product), `enforce`, `off`. | Set `confineRepoSettings: enforce` in production. |

The product also notes that your organization's IP allowlist does not cover runner traffic; the operator
does not change that.

## Threat model in brief

- **The orchestrator identity.** In on-demand mode the orchestrator pod holds the environment key and a
  ServiceAccount token that can create Secrets and ClaudeRunners in its namespace. It runs no user code.
  If it were compromised it could mint work orders and mount Secrets into runner pods; the admission
  policy below confines what it can write.
- **Runner pods.** They run model-directed code for anyone who can dispatch to the environment. They get
  no ServiceAccount token by default, cannot escalate privileges, and in on-demand mode hold only a
  single-use work order.
- **The operator.** The manager holds cluster-wide read on Secrets and ConfigMaps (unless scoped with
  `--watch-namespaces`) and write on the objects it creates. It never logs, emits or exports a Secret value,
  an email, or a JWT; Warning messages built from pod termination messages are redacted.

## Pod Security

Every pod the operator creates meets the Restricted Pod Security Standard: `runAsNonRoot`, the
`RuntimeDefault` seccomp profile, `allowPrivilegeEscalation: false`, all capabilities dropped and a
read-only root filesystem. The `podTemplate` API leaves out the fields that could break the standard
(`hostNetwork`, `hostPID`, `privileged` and others are unknown fields), and volumes are limited to the
sources the standard allows. Enforce it on the namespace:

    kubectl label namespace <ns> pod-security.kubernetes.io/enforce=restricted

The same label belongs on the operator's namespace (`claude-selfhosted-operator-system`).

## Admission policy

Two ValidatingAdmissionPolicy objects, `orchestrator-secrets` and `orchestrator-runners` (installed with the
`claude-selfhosted-operator-` prefix), confine each orchestrator identity. Source:
[`config/admission/orchestrator-policy.yaml`](../config/admission/orchestrator-policy.yaml).

They match a request only when both hold:

- the user is `system:serviceaccount:<namespace>:<env>-orchestrator`, and
- the authorizer allows that user to `get` `claudeenvironments/<env>` in that namespace. The operator's
  orchestrator Role grants exactly that, so a third-party ServiceAccount whose name merely ends in
  `-orchestrator` is not affected.

For a matched identity:

- **Secrets** (CREATE, UPDATE, DELETE; a patch arrives as UPDATE): the name must end in `-work-order`, and the
  label `selfhosted.claudecode.dev/environment` must equal `<env>` on the new object (CREATE, UPDATE) and
  on the old object (UPDATE, DELETE), and the new object's `type` must be `Opaque` (CREATE, UPDATE; an
  omitted type is defaulted to `Opaque` before admission). An orchestrator can therefore touch only its own
  environment's work orders, even in a shared namespace, and cannot mint a
  `kubernetes.io/service-account-token` Secret under a work-order name. Denial message: "an orchestrator ServiceAccount may only manage its own
  environment's Secrets named \*-work-order".
- **ClaudeRunners** (CREATE): the controller owner reference must be the ClaudeEnvironment `<env>` with
  apiVersion `selfhosted.claudecode.dev/v1alpha1`, `spec.environmentRef.name` must be `<env>`, and
  `spec.workOrderSecretRef.name` must be `metadata.name + '-work-order'`. Denial message: "an orchestrator
  may only create ClaudeRunners owned by its own ClaudeEnvironment that reference their own work order".

The policies need Kubernetes 1.30 or later (`admissionregistration.k8s.io/v1`). On an older cluster:

- Helm: `--set admissionPolicy.enabled=false`.
- kustomize: remove `- ../admission` from `config/default/kustomization.yaml`.

Without the policy, give each on-demand environment its own namespace: the orchestrator's Role can
create, patch and delete any Secret in that namespace.

## NetworkPolicy

Set `spec.runner.networkPolicy.enabled: true`. The operator then manages:

- `<env>-egress`, selecting every pod labelled `selfhosted.claudecode.dev/environment=<env>` (runners and
  the orchestrator), egress only: DNS (UDP and TCP 53) to `k8s-app=kube-dns` pods in `kube-system`, and
  TCP 443 to each entry of `spec.runner.networkPolicy.egressCIDRs`. With an empty list only DNS is allowed.
- `<env>-egress-apiserver`, in on-demand mode only, selecting the orchestrator pods: TCP to every
  address and port of the `default/kubernetes` Endpoints, so the spawn hook can reach the API server.
  The operator reads those Endpoints on every reconcile (no watch), so a control-plane IP change is picked
  up on the next reconcile of the environment (any change to it, its runners or its pods, and at the latest
the 10-minute resync); until then an enforcing CNI blocks the hook.

  When the manager flag `--apiserver-endpoints` (chart value `networkPolicy.apiServerEndpoints`, a list of
  `ip:port`, IPv6 bracketed) is set, the policy allows every listed port to every listed address instead (an address x port cross-product, so
  `10.0.0.1:6443,10.0.0.2:443` also opens 10.0.0.1:443 and 10.0.0.2:6443),
  and the Endpoints are not read. The chart's `rbac.namespaced=true` cannot grant `get` on
  `default/kubernetes`, so that mode requires the value whenever an on-demand environment enables the
  policy; a control-plane IP change then needs a value update and a manager restart.

`egressCIDRs` takes IPv4 CIDRs only, octets 0 to 255 and prefix 0 to 32 (CEL: "egressCIDRs must be IPv4 CIDRs"), at most 64 entries. A
NetworkPolicy cannot name hosts, so list the address ranges of `api.anthropic.com` and your git host
(see [Network requirements](https://code.claude.com/docs/en/self-hosted-environments-deploy#network-requirements)
for the full list, for example `downloads.claude.ai` when sessions install plugins). Ranges can change; if
your CNI offers DNS-aware policies (for example Cilium `toFQDNs` or Calico domain-based policies), use one
of those in addition to or instead of this policy, or route egress through a proxy. The metadata
exclusion is described in the checklist above.

kind enforces NetworkPolicy out of the box since v0.24.0 (kindnetd with
[kube-network-policies](https://github.com/kubernetes-sigs/kube-network-policies)), but the e2e suite
asserts only the policies' shape, not that traffic outside them is blocked.

## Secret hygiene

- **Environment key Secret.** Create it from a file written under `umask 077` (see
  [`examples/fixed-fleet.yaml`](../examples/fixed-fleet.yaml)); the key name defaults to
  `environment-secret`. The operator only references it by name and never reads its value into a log,
  event or condition.
- **Work-order Secrets.** The spawn hook creates `<orderID>-work-order` (key `jwt`), labelled with the
  environment and order ID and owned by the ClaudeEnvironment, then hands ownership to the ClaudeRunner,
  so deleting the runner deletes its work order.
- **Orphan sweep.** If the hook dies between its two creates, the Secret would hold a live JWT with no
  runner. On every on-demand reconcile the operator deletes a Secret that carries both operator labels,
  ends in `-work-order`, is controlled by the ClaudeEnvironment, is older than
  `onDemand.orchestrator.expectedSpawnSeconds`, and has no ClaudeRunner for its order (checked against the
  API server, not the cache). The delete uses UID and resourceVersion preconditions, the environment key
  Secret is excluded by name, and each deletion emits a Normal `OrphanedWorkOrderDeleted` event.
- **Operator-owned variables.** CEL rejects `runner.env` entries named `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR`,
  `SELF_HOSTED_RUNNER_CLIENT_LABEL` or `SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET` ("env may not set
  operator-owned variables"), and `extraArgs` that set operator-owned flags. It also rejects
  `onDemand.orchestrator.env` entries whose name starts with `CLAUDE_OPERATOR_` ("orchestrator.env may not
  set operator-owned variables"), because they would override the values the hook depends on.

### Personal data

`settings.lockToAccount` accepts an email address. The value is stored in the ClaudeEnvironment object
and passed to the runner as `--lock-to-account`, so treat the object as containing personal data and
limit who can read it. The runner's own `claude_code_self_hosted_runner_locked_account{email}` series
carries the account email as a label; drop or hash it at scrape time if your metrics store is broadly
readable (see [metrics](metrics.md)).

## RBAC scope and caches

The manager's ClusterRole is generated from the controllers' RBAC markers (`config/rbac/role.yaml`). Its
informers are scoped as follows:

| Object | Cached |
| :- | :- |
| Pods | Only pods with `selfhosted.claudecode.dev/role` in (`runner`, `orchestrator`) |
| ServiceAccounts, Roles, RoleBindings, NetworkPolicies | Only objects labelled `app.kubernetes.io/part-of=claude-code-self-hosted-runner` |
| Secrets, ConfigMaps | All, because users name them |

On a large cluster the Secret and ConfigMap caches cost memory and give the manager read access to every
Secret. Run the manager with `--watch-namespaces=<ns1>,<ns2>` (Helm value `watchNamespaces`) to limit every
cache to those namespaces; environments in other namespaces are then ignored. The ClusterRole itself is
not narrowed by the flag.

The orchestrator Role (one per on-demand environment, `<env>-orchestrator`) grants `get` on its own
`claudeenvironments` object, `create, get` on `clauderunners` (plus `list` when `maxConcurrentRunners` is
above 0) and `create, patch, delete` on `secrets`. The manager needs `create, patch, delete` on Secrets
because it can only grant what it holds.

## Image supply chain

- The operator image `ghcr.io/ahmadmasry/claude-self-hosted-environment-operator` is multi-arch, tagged
  `vX.Y.Z` and `vX.Y` (never `latest`), signed keyless with cosign through GitHub OIDC, and carries an SPDX
  SBOM attestation. Verify before deploying:

  ```bash
  cosign verify \
    --certificate-identity-regexp 'https://github.com/AhmadMasry/claude-self-hosted-environment-operator/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    ghcr.io/ahmadmasry/claude-self-hosted-environment-operator:vX.Y.Z
  ```

  `cosign verify-attestation --type spdxjson` with the same identity flags checks the SBOM. See
  [release](release.md).
- The runner image is yours. Anthropic publishes none and this project never publishes one, because it
  bundles Anthropic's `claude` binary. Build it from
  [`examples/runner-image`](../examples/runner-image/README.md), pin `CLAUDE_CODE_VERSION`, and reference it
  by tag or digest: CEL rejects an image without one and rejects `:latest` ("runner.image must carry a tag or
  digest and must not use :latest").
