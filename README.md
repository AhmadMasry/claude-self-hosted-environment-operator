# Claude Code self-hosted environment operator

[![Lint](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/lint.yml)
[![Tests](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test.yml)
[![E2E](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml/badge.svg)](https://github.com/AhmadMasry/claude-self-hosted-environment-operator/actions/workflows/test-e2e.yml)

A Kubernetes operator that runs [Claude Code self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments)
on your cluster. You create one `ClaudeEnvironment` per environment from
claude.ai admin settings, point it at a runner image you build and a Secret
holding the environment key, and the operator runs the fleet: a fixed set
of runners, or an orchestrator that spawns one hardened pod per session.
Every pod meets the Restricted Pod Security Standard by default.

Status: pre-release. The API is `v1alpha1` and the API group is provisional
until the first tagged release.

Install: kustomize installer (`dist/install.yaml` on each release) or the
Helm chart (`oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator`).
Docs: [quickstart](#quickstart) below, [on-demand mode](docs/on-demand.md),
[hardening](docs/hardening.md), [metrics](docs/metrics.md),
[upgrade](docs/upgrade.md), [testing](docs/testing.md),
[troubleshooting](TROUBLESHOOTING.md).

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

5. **Build, push and install the operator.** No operator image is published and a Helm chart is planned;
   for now build the image and deploy with kustomize from a checkout:

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

## Development

    make test        # unit tests (envtest)
    make lint

## Learn more

- Design: [`docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`](docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md)
- Claude docs: [self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments), [deploying them](https://code.claude.com/docs/en/self-hosted-environments-deploy)
