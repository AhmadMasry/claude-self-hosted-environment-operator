# Customizing runner and orchestrator pods

Four places let you run your own code, each at a different moment. Pick by
when it must run and what it must see.

| Hook | When it runs | Runs as | Typical use |
|---|---|---|---|
| Image entrypoint | Once per pod, before the runner or orchestrator process | Pod user | Node-level setup, exporting pod-wide variables |
| `podTemplate.initContainers` | Once per pod, before the main container, in a container of its own | Pod user | Fetching or generating files into a shared volume |
| `spec.runner.wrapperScript` | Once per session, in place of the Claude Code binary | Runner user, session environment | Session credentials, per-session variables |
| `spec.runner.lifecycleHooks` | `checkout` per repository, `command` per session, `post-session` after it | Runner user | Custom clone, cleanup, reporting |

Sessions share the runner's user, so anything a hook leaves in the pod is
readable by Claude's session. Give sessions short-lived, narrowly scoped
credentials; keep anything that can mint credentials outside the pod, for
example in [External Secrets Operator](https://external-secrets.io/) writing a
Secret the pod mounts.

## Image entrypoint

The operator sets only the container arguments, never the command, so the
image's `ENTRYPOINT` runs first. Both runners and the orchestrator use the
same image; `$2` is `orchestrator` on the orchestrator pod.

```dockerfile
COPY entrypoint.sh /usr/local/bin/entrypoint
ENTRYPOINT ["/usr/local/bin/entrypoint"]
```

```bash
#!/usr/bin/env bash
set -euo pipefail
if [ "${2:-}" != orchestrator ]; then
  export SOME_SETTING=value        # inherited by the runner, every session and every hook
fi
exec claude "$@"
```

The root filesystem is read-only; write under `/tmp` or `$HOME`, which are
`emptyDir` volumes of the pod.

## Init containers

`podTemplate.initContainers` on `spec.runner` and on `spec.onDemand.orchestrator`
takes a curated container: `name`, `image`, `imagePullPolicy`, `command`,
`args`, `workingDir`, `env`, `envFrom`, `volumeMounts`, `resources` and
`restartPolicy: Always` for a sidecar. The operator sets the security context
(no privilege escalation, all capabilities dropped, read-only root filesystem),
and the pod's `runAsNonRoot` and seccomp profile apply, so the Restricted Pod
Security Standard still holds. On the orchestrator, your containers run after
the operator's own one that installs the spawn hook.

Hand results over through the operator's volumes, mounted by name:
`workspace` (the runner's `baseDir`), `home` (`/home/runner`) and `tmp` on runner
pods; `home` and `tmp` on the orchestrator. Your own `podTemplate.volumes` are
available too.

```yaml
spec:
  runner:
    podTemplate:
      initContainers:
        - name: fetch-tools
          image: registry.example.com/tools:1.4.0
          command: ["/bin/sh", "-c", "cp /opt/tools/* /home/runner/bin/"]
          volumeMounts:
            - {name: home, mountPath: /home/runner}
```

## Wrapper script

`spec.runner.wrapperScript` names a ConfigMap key the operator mounts under
`/etc/claude/wrapper` and passes to the runner as `--exec-path`. The runner
starts it in place of the Claude Code binary, once per session, with the
session's environment (`CLAUDE_CODE_REMOTE_SESSION_ID`, `CLAUDE_CONFIG_DIR`,
`CLAUDE_RUNNER_CLAUDE_BIN` and the rest; see the
[product reference](https://code.claude.com/docs/en/self-hosted-environments-configuration)).
Two rules from the product docs: end with `exec "$CLAUDE_RUNNER_CLAUDE_BIN" "$@"`
so signals, exit codes, stdin (the runner's control channel) and file
descriptor 3 propagate, and never drop or rewrite `"$@"`, which carries the
session's prompt-file flags.

[`examples/wrapper-configmap.yaml`](../examples/wrapper-configmap.yaml) turns a
short-lived GitHub token, delivered as a Secret file, into git credentials for
the session. Wire it up with a Secret mounted as a directory (not `subPath`, so
a rotated token appears in place):

```yaml
spec:
  runner:
    wrapperScript:
      name: runner-wrapper
      key: wrapper.sh
    podTemplate:
      volumes:
        - name: github-token
          secret:
            secretName: github-token        # for example written by External Secrets Operator
      volumeMounts:
        - {name: github-token, mountPath: /var/run/github, readOnly: true}
```

```bash
kubectl apply -f examples/wrapper-configmap.yaml
```

The real-environment test runs a wrapper from the same kind of mount on every
run and asserts it executed (see [testing](testing.md)).

## Lifecycle hooks

`spec.runner.lifecycleHooks` names a ConfigMap mounted executable at
`/etc/claude/hooks` and passed as `--hooks-dir`. The runner looks for
`checkout`, `command` and `post-session` by name; any that is absent falls back
to the built-in behaviour. `checkout` replaces the built-in clone and receives
the repository URL, ref, checkout path and session id, which makes it the
product's intended place for custom git authentication. `post-session` runs
after the Claude Code child exits, with the session id and exit reason, and
never affects the session's outcome. `command` is `wrapperScript` by another
route and loses when both are set.

## Session configuration

`spec.runner.hostConfig` names a ConfigMap whose files the runner seeds into
each session's configuration directory: `settings.json`, `CLAUDE.md`, skills
and Claude Code hooks such as `SessionStart`, which run inside the session
itself rather than around it.
