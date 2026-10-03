# Testing

The operator is tested at five levels. The first four run in CI on every push and pull request and never
contact Anthropic. The fifth, the real-environment test, is manual and uses a real self-hosted environment.

| Level | What it covers | Command | CI |
| :- | :- | :- | :- |
| Unit | Builders, hook, reply sink, helpers | `make test` | `test.yml` |
| envtest | Controllers against a real API server and etcd (CEL rules, conditions, garbage collection) | `make test` | `test.yml` |
| Chart contract | `dist/chart` matches the kustomize installer | `make helm-lint test-chart` | `test.yml` |
| kind e2e | Installer on a kind cluster with the stub runner image (fixed fleet, on-demand, PSS Restricted) | `make test-e2e` | `test-e2e.yml` |
| Upgrade e2e | Install the previous revision, upgrade to the candidate, environments stay valid and Ready | `make test-e2e-upgrade` | `test-e2e.yml` |
| Real environment, level 1 | The orchestrator registers with Anthropic using the environment key | `make real-session-test` | `real-e2e.yml` (manual) |
| Real environment, level 2 | A real session runs on an operator-spawned runner and its reply is read back | `make real-session-test` | `real-e2e.yml` (manual) |

## Unit and envtest

`make test` runs every package except `test/e2e` with envtest (a real `kube-apiserver` and `etcd`, no
kubelet). It downloads the envtest binaries into `bin/` on first use.

## kind e2e and upgrade e2e

Both need Docker and kind, and use the Makefile-owned cluster `claude-selfhosted-operator-test-e2e`
(`KIND_CLUSTER`), which they create if missing and delete at the end. They use the stub runner image
(`test/stubrunner`), never Anthropic's binary, and make no outbound calls.

```bash
make test-e2e
make test-e2e-upgrade                       # UPGRADE_FROM defaults to the merge-base with master
make test-e2e-upgrade UPGRADE_FROM=<git-ref>
```

## Real-environment test

The real-environment test deploys the operator to a kind cluster and points an on-demand `ClaudeEnvironment`
at a real self-hosted environment. It follows the product's recipe in
[Test self-hosted environments end to end](https://code.claude.com/docs/en/self-hosted-environments-testing),
using its remote-runner variant: the runner's Stop hook posts each turn's final reply to an in-cluster reply
sink instead of a local file.

### What each level proves

**Level 1, registration** (always runs; needs `CLAUDE_ENVIRONMENT_KEY` and `CLAUDE_ENVIRONMENT_ID`). The
script creates the namespace (PSS `restricted`), the reply sink (`test/real-e2e/replysink.yaml`), the host
config ConfigMap with the Stop hook (`test/real-e2e/host-config.yaml`), the environment-key Secret and the
`ClaudeEnvironment` (`test/real-e2e/environment.yaml`), then waits for the environment to be `Ready`. Ready
in on-demand mode requires the orchestrator pod to be ready, and its readiness probe requires the health
endpoint to report the orchestrator connected, so this proves it authenticated to Anthropic with the key. No session is created and nothing is billed.

**Level 2, session** (runs only when `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` is set, or `claude auth status`
reports a claude.ai login on the machine; the script reads its default JSON output and requires
`loggedIn == true` and `authMethod == "claude.ai"`, so an API-key login skips level 2; otherwise the script prints that level 2 was skipped and exits 0). The script
starts one session routed to the environment, and asserts that:

1. the CLI returns a `session_id`;
2. the orchestrator's spawn hook created a `ClaudeRunner` (labelled
   `selfhosted.claudecode.dev/environment=real`);
3. the runner's Stop hook posted a reply containing the sentinel to the reply sink;
4. the `ClaudeRunner` reaches `Succeeded` once the idle session is released
   (`releaseIdleSessionMinutes: 1`);
5. the `ClaudeRunner` is garbage-collected after `runnerTTLSecondsAfterFinished` (120 s).

### How the session is started

The commands come from the product docs:

- `claude -p "<prompt>" --environment <environment-id> --ref <branch> --output-format json` creates a
  session on the environment, prints one line of JSON containing `session_id`, and exits without waiting for
  the reply. `--environment` and `--ref` need Claude Code v2.1.224 or later.
- There is no repository flag. The CLI detects the repository from the `origin` remote of the git checkout
  it runs in. The script therefore runs `claude` in the current checkout, or, when `TEST_REPO` is set, in an
  anonymous shallow clone of `https://github.com/<TEST_REPO>.git` (so `TEST_REPO` must be public; for a
  private repository, run the script from your own checkout of it with `TEST_REPO` unset).
- `TEST_REF` defaults to the branch checked out, which must exist on the remote.
- The Stop hook reads `last_assistant_message` from its input and the session id from
  `CLAUDE_CODE_REMOTE_SESSION_ID`, rewriting the `cse_` prefix to the `session_` prefix the CLI prints.

The hook reaches the runner through `spec.runner.hostConfig`: the ConfigMap is mounted read-only at
`/etc/claude/host-config` and the operator sets `SELF_HOSTED_RUNNER_HOST_CONFIG_DIR`, which the runner seeds
into each session's config directory. The mount is not executable, so `settings.json` runs the hook as
`bash /etc/claude/host-config/capture-reply.sh`. `E2E_REPLY_URL` is set through `spec.runner.env`.
`test/real-e2e/capture-reply.sh` is a copy of the embedded script kept for `shellcheck`; keep the two
identical.

When level 2 fails, the script prints redacted evidence to stderr: the `ClaudeRunner` objects, the
namespace events, the sink's request log, the runner pod's log (streamed from the moment the pod appears,
because the pod is garbage-collected soon after the session ends) and, read through `kubectl exec` while the
pod ran, the host-config mount, each session's seeded config directory under `/workspace/_sessions` with its
`settings.json`, and the hook's trace. The hook appends that trace to `/tmp/capture-reply.log` in the pod:
whether it ran, which session id and sink URL it saw, the keys of its input and curl's result.

### Prerequisites

- A self-hosted environment in a Team or Enterprise organization with **Allow self-hosted environments**
  turned on, used only for this test. Do not point the test at a production environment: its runners carry
  the capture hook, and its sessions consume the organization's usage.
- For level 2, the organization's GitHub connection must cover the repository the session checks out
  (`test_repo`, or this repository by default).
- Repository secrets (Settings > Secrets and variables > Actions):
  - `CLAUDE_ENVIRONMENT_KEY`: the environment key shown once when the environment was created. Only the
    orchestrator pod uses it.
  - `CLAUDE_ENVIRONMENT_ID`: the environment's `ccpool_...` ID, shown in the environment's detail dialog on
    the Cloud environments admin page.
- Optional, for level 2 in CI:
  - `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`: an OAuth refresh token for a claude.ai account. When it is set,
    `claude auth login` exchanges it directly instead of opening a browser.
  - `CLAUDE_CODE_OAUTH_SCOPES`: the space-separated scopes the token was issued with, for example
    `user:profile user:inference user:sessions:claude_code`. Required with the refresh token.

#### Minting the refresh token

From the product docs ([Authenticate from CI](https://code.claude.com/docs/en/self-hosted-environments-testing#authenticate-from-ci)
and [environment variables](https://code.claude.com/docs/en/env-vars)):

- Session creation with `--environment` authenticates with a claude.ai OAuth login. API keys are not
  accepted, and neither is the environment key.
- `claude setup-token` does not work for this: it mints a one-year inference-only token, and the scope that
  grants cloud-session control, `user:sessions:claude_code`, is capped server-side at 30 days.
- Use a dedicated automation account. Run `claude auth login` with it interactively; on Linux the login is
  stored in `~/.claude/.credentials.json`, on macOS in the Keychain.
- Provide that login's refresh token and scopes to CI as `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and
  `CLAUDE_CODE_OAUTH_SCOPES`. Where the refresh token is read from in the stored login is not documented.
- The refresh grant is capped at 30 days from the initial login: log in again and replace both secrets
  every 30 days. Anthropic's docs say to contact your account team for a machine identity that is not bound
  to a human account.

### Running it from GitHub Actions

Run the `real-e2e` workflow from the Actions tab (Run workflow). Inputs:

| Input | Default | Meaning |
| :- | :- | :- |
| `test_repo` | empty | `owner/name` the level 2 session checks out; empty means this repository |
| `ref` | empty | Branch for the session; empty means the branch the workflow runs on, or `test_repo`'s default branch |
| `keep_cluster` | `false` | Keep the kind cluster after the run. Only useful on a self-hosted Actions runner: GitHub-hosted runners are discarded when the job ends |

The workflow fails early with a clear message if either required secret is missing. It installs kind and
the Claude Code CLI (`curl -fsSL https://claude.ai/install.sh | bash`), builds the operator, reply sink and
runner images, loads them into the kind cluster `claude-selfhosted-operator-real-e2e`, deploys the operator
and runs `hack/real-session-test.sh`. On failure it prints the resources and the operator, environment and
reply-sink logs.

### Running it by hand

On a machine with Docker, kind, kubectl, envsubst, jq, git and the Claude Code CLI:

```bash
claude auth login                          # interactive, instead of the refresh token
export CLAUDE_ENVIRONMENT_KEY=...          # read it from your secret store; do not type it into history
export CLAUDE_ENVIRONMENT_ID=ccpool_...
make real-session-test IMG=example.com/claude-selfhosted-operator:real-e2e   # optional: TEST_REPO=owner/name TEST_REF=branch
make cleanup-test-e2e                      # delete the kind cluster afterwards
git checkout config/manager/kustomization.yaml   # `make deploy` rewrote the image
```

Pass a non-`latest` `IMG`: the image is loaded into kind, and a `:latest` tag would make the kubelet try to
pull it.

`make real-session-test` creates or reuses the Makefile-owned kind cluster (`KIND_CLUSTER`), builds and loads
the images, deploys the operator and runs the script. To run only the script against a cluster that already
runs the operator and has the images, use `RUNNER_IMAGE=<image> hack/real-session-test.sh` from the
repository root.

### Secret handling

The environment key is rendered by `envsubst` straight into `kubectl apply -f -`; it is never written to
disk or printed, and the script never enables `set -x`. The workflow reads secrets only in step `env:`
blocks.

### Images

`make real-runner-image` builds `examples/runner-image` with the current `stable` Claude Code version
(override with `CLAUDE_CODE_VERSION=<x.y.z>`) and adds `jq` for the Stop hook
(`test/real-e2e/runner.Dockerfile`). The image bundles Anthropic's binary: it is built locally or in the
job, loaded into kind, and never pushed or published. The reply sink image (`make replysink-image`) is
likewise local only.

The `real-e2e` workflow's diagnostics step prints the resource listing and the manager logs only; runner,
orchestrator and reply sink logs are not printed, because a session's content could appear in them. The
reply sink and runner base images in `test/replysink/Dockerfile` are pinned by digest; the example runner
image picks the Claude binary from `TARGETARCH` (override with `--build-arg CLAUDE_ARCH`). Resolving
`CLAUDE_CODE_VERSION` to an empty string (the stable-release lookup failing) fails `make real-runner-image`
immediately. `hack/check-hook-copy.sh` (run by `make lint` and the `test` workflow) fails when the Stop hook
embedded in `test/real-e2e/host-config.yaml` differs from `test/real-e2e/capture-reply.sh`.

### Cost

Level 1 creates no session. Level 2 runs one short session (a single one-line reply) per run, which
consumes the organization's Claude Code usage like any other session, and the runner pod is limited to
2 CPU and 4 GiB.
