# Upgrading

## Supported path

Upgrade one release at a time, from N-1 to N. CI runs the upgrade e2e (`make test-e2e-upgrade`, job
"Upgrade from the previous revision" in `test-e2e.yml`) on every push and pull request: it builds the
previous revision (the merge-base with `master`, or `HEAD~1` on `master`) in a temporary worktree,
installs its installer with `kubectl apply --server-side`, creates a fixed-fleet environment, applies the
candidate's manifests over it, and asserts that:

- the manager rolls to the new image;
- the CRDs carry the candidate's CEL rules;
- the existing runner Deployment converges and the environment stays `Ready`;
- no `FailedCreate` events appear;
- a spec the new rules reject, which the previous revision accepted, is now rejected.

The e2e covers the kustomize installer path. The Helm chart is generated from the same manifests and its
CRDs, RBAC and Restricted settings are checked against them by the chart contract test (`make test-chart`),
but `helm upgrade` itself is not exercised in CI.

## Installer users

Apply the new release's installer over the old one:

    kubectl apply --server-side -f https://github.com/AhmadMasry/claude-self-hosted-environment-operator/releases/download/vX.Y.Z/install.yaml

The installer lists the CRDs before the manager Deployment, so the new schema is in place before the new
manager starts. `--server-side` avoids the client-side annotation size limit on the CRDs. If a field
manager conflict is reported for a field you changed by hand, decide whether to keep your change and
rerun with `--force-conflicts` only if the installer's value should win.

## Helm users

    helm upgrade claude-selfhosted-operator oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator \
      --namespace claude-selfhosted-operator-system --version X.Y.Z --reuse-values

Helm does not upgrade CRDs placed in a chart's `crds/` directory. This chart renders its CRDs from
`templates/crd/` instead (behind `crd.enabled`, default `true`), so `helm upgrade` updates them. They carry
`helm.sh/resource-policy: keep` while `crd.keep` is `true` (the default), so `helm uninstall` leaves the CRDs
and your ClaudeEnvironments in place.

Check `dist/chart/values.yaml` of the new version for new values before reusing old ones. The values the
operator adds to the plugin's defaults are `admissionPolicy.enabled`, `watchNamespaces`,
`tracing.endpoint` and `tracing.sampleRatio`; the hook image (`--hook-image`) always follows
`manager.image.repository` and `manager.image.tag`.

## When the CRD schema gets stricter

A release can add validation, as the hardening release did (image tag or digest, reserved mount paths and
env names, `hookTimeoutSeconds` at least 15, IPv4 `egressCIDRs`). Kubernetes does not re-validate stored
objects when a CRD changes: existing ClaudeEnvironments stay in place, keep reconciling and stay `Ready`.
The new rules apply when an object is created or updated. With CRD validation ratcheting (on by default
since Kubernetes 1.30) an update that leaves an offending field unchanged is still accepted; an update that
touches it must satisfy the new rule. Fix such objects at your next edit; `kubectl apply --dry-run=server`
shows the CEL message without changing anything.

The operator re-renders its workloads after an upgrade. A change in the rendered pod template (new flags,
labels or defaults) rolls the runner Deployment or StatefulSet and the orchestrator Deployment once, under
the same drain budget as any other rollout.

### Notes for the hardening release

- The manager no longer accepts the `--zap-*` flags. Logging is set with `--log-format` (`json` or `text`)
  and `--log-level` (`debug`, `info` or `error`). Remove any `--zap-*` entry from custom manager args
  before upgrading, or the manager exits with "flag provided but not defined".
- The admission policies need Kubernetes 1.30 or later. On older clusters upgrade with
  `admissionPolicy.enabled=false`, or remove `../admission` from the kustomization.
- The orchestrator gets `CLAUDE_OPERATOR_HOOK_TIMEOUT_SECONDS`; an orchestrator still running an older
  pod template without it uses a 60-second timeout, so its hook deadline is 50 seconds until it rolls.

## Rollback

- Installer: apply the previous release's `install.yaml` with `kubectl apply --server-side`. The CRDs
  return to the old schema.
- Helm: `helm rollback claude-selfhosted-operator <revision> -n claude-selfhosted-operator-system`.

Status fields the older version does not know are pruned the next time it writes an object's status;
spec fields that only the newer schema knows are pruned on the next write of the object, so remove them
from your manifests first. The older manager re-renders the workloads, which rolls the pods once.
