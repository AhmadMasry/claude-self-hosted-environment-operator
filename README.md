# claude-selfhosted-operator

A Kubernetes operator that runs [Claude Code self-hosted environment](https://code.claude.com/docs/en/self-hosted-environments) runners for you.
You declare a `ClaudeEnvironment` (short name `cenv`); the operator deploys and supervises the runner
fleet, wires in your environment Secret, hooks and settings, drains runners safely on rollout, and reports
health through status conditions and metrics.

API: `selfhosted.claudecode.dev/v1alpha1` (provisional until v1.0.0).

**Status.** Fixed-fleet mode (`spec.fixed.replicas`) works. On-demand mode is declared in the API but is
coming in a later release; for now such a resource reports `Degraded` with reason `UnsupportedMode`.

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

5. **Install the operator.** A Helm chart is planned; for now use kustomize from a checkout:

       make install
       make deploy IMG=<operator image>

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
| `FleetAvailable` | Enough runners are available |
| `Progressing` | A rollout or scale change is under way |
| `Degraded` | Something needs attention, for example `ConfigMapMissing`, `GracePeriodTooShort`, `RunnerFailedStart`, `UnsupportedMode` |
| `SecretOnRunners` | The Secret is mounted on runner pods |

## Development

    make test        # unit tests (envtest)
    make lint

## Learn more

- Design: [`docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md`](docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md)
- Claude docs: [self-hosted environments](https://code.claude.com/docs/en/self-hosted-environments), [deploying them](https://code.claude.com/docs/en/self-hosted-environments-deploy)
