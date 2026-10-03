# Claude Code self-hosted environment operator

[![Lint](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml)
[![E2E](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml)

A Kubernetes operator that runs [Claude Code self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments)
on your cluster. You create one `ClaudeEnvironment` per environment from
claude.ai admin settings, point it at a runner image you build and a Secret
holding the environment key, and the operator runs the fleet: a fixed set
of runners, or an orchestrator that spawns one hardened pod per session.
Every pod meets the Restricted Pod Security Standard by default. Give each
on-demand environment its own namespace: its orchestrator can create
work-order Secrets there.

Status: pre-release. The API is `v1alpha1` and the API group is provisional
until the first tagged release.

Install: the kustomize installer (`install.yaml`, attached to each GitHub
release) or the Helm chart (`oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator`).
No release has been tagged yet.

Docs: [quickstart](#quickstart) below, [on-demand mode](docs/on-demand.md),
[hardening](docs/hardening.md), [metrics](docs/metrics.md),
[upgrade](docs/upgrade.md), [troubleshooting](TROUBLESHOOTING.md),
[testing](docs/testing.md), [releasing](docs/release.md),
[security policy](SECURITY.md), [contributing](CONTRIBUTING.md).

## Quickstart

Anthropic publishes no runner image, so you build one. Runner pods run under the Restricted Pod Security Standard.

1. **Create an environment in claude.ai.** Enable self-hosted environments, create one, and copy its
   environment key (see the [Claude docs](https://code.claude.com/docs/en/self-hosted-environments)).
2. **Create the namespace** (Restricted Pod Security labels included):

       kubectl apply -f examples/namespace.yaml

3. **Create the Secret.** Follow the commented steps at the top of
   [`examples/fixed-fleet.yaml`](examples/fixed-fleet.yaml): the key goes in via a file with `umask 077`,
   never into a committed manifest. The Secret key name is `environment-secret`.
4. **Build and push the runner image.** See [`examples/runner-image`](examples/runner-image/README.md):

       docker build --build-arg CLAUDE_CODE_VERSION=<version> -t <registry>/claude-runner:<version> examples/runner-image
       docker push <registry>/claude-runner:<version>

5. **Install the operator.** With Helm, from a release:

       kubectl create namespace claude-selfhosted-operator-system
       kubectl label namespace claude-selfhosted-operator-system pod-security.kubernetes.io/enforce=restricted
       helm install claude-selfhosted-operator oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator \
         --namespace claude-selfhosted-operator-system --version X.Y.Z

   Chart values (`dist/chart/values.yaml`): `manager.image.repository` and `manager.image.tag`,
   `manager.resources`, `rbac.namespaced`, `metrics.secure`, `certManager.enabled`, `prometheus.enabled`
   (ServiceMonitor and PodMonitor), `admissionPolicy.enabled` (default `true`; needs Kubernetes 1.30, set
   `false` on older clusters), `watchNamespaces`, `tracing.endpoint`, `tracing.sampleRatio` and
   `networkPolicy.apiServerEndpoints` (API server `ip:port` list for the orchestrator egress policy; required
   with `rbac.namespaced=true` whenever an on-demand environment enables `spec.runner.networkPolicy`). The
   hook image (`--hook-image`) always follows `manager.image`.

   Or with the installer from a release (it creates and labels the namespace itself):

       kubectl apply --server-side -f https://github.com/AhmadMasry/claude-self-hosted-environment-operator/releases/download/vX.Y.Z/install.yaml

   Or build the image yourself and deploy with kustomize from a checkout:

       make docker-build docker-push IMG=<your-registry>/claude-selfhosted-operator:<tag>
       make install
       make deploy IMG=<your-registry>/claude-selfhosted-operator:<tag>

   On-demand mode needs the manager to know its own image for the hook (`--hook-image`, default
   `OPERATOR_IMAGE`); the kustomize manifests set it automatically from `IMG`.

   On kind, replace `docker-push` with `kind load docker-image <your-registry>/claude-selfhosted-operator:<tag>`.

   The operator watches Secrets and ConfigMaps cluster-wide unless the manager runs with `--watch-namespaces`.

6. **Apply the example.** Edit `runner.image` in `examples/fixed-fleet.yaml` to your image first.

       kubectl apply -f examples/hooks-configmap.yaml -f examples/fixed-fleet.yaml

A minimal single-resource sample is in [`config/samples`](config/samples/selfhosted_v1alpha1_claudeenvironment.yaml).

## Reading status

    kubectl get cenv -A
    kubectl -n claude-runners describe cenv platform

The list shows Mode, Ready, Replicas and Age. Conditions on `.status.conditions`:

| Condition | Meaning |
|---|---|
| `Ready` | The environment is serving as configured |
| `SecretFound` | The environment Secret and its key exist (`SecretMissing`, `SecretKeyMissing`) |
| `FleetAvailable` | Enough runners are available (on-demand: `OrchestratorUnavailable` when the orchestrator is not ready, `WorkloadApplyFailed` when applying the workload failed) |
| `Progressing` | A rollout or scale change is under way |
| `Degraded` | Something needs attention, for example `ConfigMapMissing`, `GracePeriodTooShort`, `RunnerFailedStart`, `HookImageUnset` |
| `SecretOnRunners` | The Secret is mounted on runner pods; `False/OnDemandSecretOnOrchestrator` in on-demand mode |

## Configuration notes

- **Egress.** `spec.runner.networkPolicy.enabled: true` adds a default-deny egress NetworkPolicy
  (`<env>-egress`): DNS to kube-dns, TCP 443 to the IPv4 ranges in `egressCIDRs` (at most 64), with
  `169.254.169.254` always excluded, plus the standard egress rules in `additionalEgress` (at most 16) for
  in-cluster services or other ports. On-demand environments also get `<env>-egress-apiserver` so the
  orchestrator reaches the API server. Off by default; see [hardening](docs/hardening.md#networkpolicy).
- **Validation.** `runner.image` needs a tag or digest and may not be `:latest`. `baseDir` and
  `podTemplate.volumeMounts` may not use `/etc/claude`, `/home/runner` or `/tmp`. `runner.env` may not set
  `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR`, `SELF_HOSTED_RUNNER_CLIENT_LABEL` or
  `SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET` (use `runner.hostConfig`, `settings.clientLabel` and
  `environmentSecretRef`). `env`, `volumeMounts` and `onDemand.orchestrator.env` take at most 64 entries;
  `onDemand.orchestrator.hookTimeoutSeconds` is at least 15. The messages are listed in
  [troubleshooting](TROUBLESHOOTING.md#admission-and-validation-errors).
- **Manager flags.** `--watch-namespaces` (comma-separated; empty watches all), `--log-level`
  (`debug`, `info`, `error`), `--log-format` (`json`, `text`), `--tracing-endpoint` (OTLP gRPC, default
  `$OTEL_EXPORTER_OTLP_ENDPOINT`), `--tracing-sample-ratio` (default 0.1), `--hook-image`,
  `--apiserver-endpoints` (comma-separated `ip:port`; empty reads the `default/kubernetes` Endpoints). The `--zap-*` flags
  are gone.

## Hardening notes

`lockToAccount` accepts an email address. That value is stored in the ClaudeEnvironment object and passed to the
runner pod as an argument, so treat the object as containing personal data and limit who can read it. The operator
watches Secrets and ConfigMaps cluster-wide unless the manager runs with `--watch-namespaces`.
See [hardening](docs/hardening.md) for Anthropic's checklist mapped to the operator, and
[SECURITY.md](SECURITY.md) to report a vulnerability.

## Development

    make test        # unit tests (envtest)
    make lint

See [CONTRIBUTING.md](CONTRIBUTING.md) and [testing](docs/testing.md).

## Learn more

- Design: [`docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`](docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md)
- Claude docs: [self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments), [deploying them](https://code.claude.com/docs/en/self-hosted-environments-deploy)
