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

## Session audit: prompts on the pod's stdout

The operator never sees prompts: the conversation flows between the session and
Anthropic, which stores the transcript. To keep a record on your side, register
a Claude Code `UserPromptSubmit` hook through `hostConfig`. Claude Code runs it
inside the session before each prompt reaches the model, with the prompt as
JSON on stdin, and the hook inherits the session's environment, which carries
the creator's identity. The hook cannot write to its own stdout, which Claude
Code feeds back to the model as context, but the container's log stream is
file descriptor 1 of PID 1, reachable as `/proc/1/fd/1`: a line written there
appears in `kubectl logs` with no sidecar and no volume. The runner, the
session and the hook run as the same user in one container, which is what
lets the hook open another process's descriptor.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: runner-host-config
data:
  settings.json: |
    {
      "hooks": {
        "UserPromptSubmit": [
          { "hooks": [ { "type": "command", "timeout": 10, "command": "bash /etc/claude/host-config/log-prompt.sh" } ] }
        ]
      }
    }
  log-prompt.sh: |
    #!/usr/bin/env bash
    # One JSON line per prompt on the pod's stdout. Nothing on the hook's own
    # stdout, and exit 0 always: exit 2 would block the prompt.
    set -uo pipefail
    who=$(cat "${CLAUDE_CONFIG_DIR:-}/creator.json" 2>/dev/null || echo '{}')
    jq -c --argjson who "$who" --arg platform "${CLAUDE_RUNNER_CLIENT_PLATFORM:-}" \
      '{log: "prompt", ts: (now|todate), session: .session_id, creator: $who,
        platform: $platform, prompt: .prompt}' >> /proc/1/fd/1
    exit 0
```

The mount is not executable, so the hook runs through `bash`. `creator.json`
comes from a [wrapper script](#wrapper-script) that decodes the session token
once, before Claude Code starts; the runner's `decode-token` subcommand
verifies the signature against Anthropic's keys and prints the claims:

```bash
#!/usr/bin/env bash
"$CLAUDE_RUNNER_CLAUDE_BIN" self-hosted-runner decode-token \
  | jq -c '{sub: .act.sub, email: (.act.email // null)}' \
  > "$CLAUDE_CONFIG_DIR/creator.json" || true
exec "$CLAUDE_RUNNER_CLAUDE_BIN" "$@"
```

Without the wrapper, `"${CCR_SESSION_ACCOUNT_EMAIL:-}"` in the hook gives the
same email, pre-extracted by the runner without signature verification. Key
records on `act.sub`, the stable `user:<id>`: the email is absent when the
creating surface did not record one, and a session started by your
organization's service identity, as Claude Tag channel sessions are, carries
an `agent:` subject with no email, and the person who typed each Slack message
is not in the token.

A line as the hook writes it, from a session dispatched with `claude -p`
(a browser session reports `web_claude_ai`):

```json
{"log":"prompt","ts":"2026-10-20T20:48:42Z","session":"b668005d-...","creator":{"sub":"user:user_01S8...","email":"user@example.com"},"platform":"claude_code_sdk","prompt":"Reply with exactly the text: ..."}
```

What to expect:

- The lines interleave with the runner's own plain-text log on the same
  stream; the `log` field, or a parser that skips non-JSON lines, separates
  them.
- The container runtime splits a line over 16 KiB into partial records, which
  Fluent Bit, Vector and the CRI parsers reassemble; raw `kubectl logs`
  shows the breaks.
- The hook also fires for turns Claude starts on its own, such as scheduled
  tasks and subagent reports. A `Stop` hook with `last_assistant_message`
  records the replies the same way.
- This puts prompts and the creator's email into whatever already collects
  container logs, with that backend's retention and access. If the audit
  must be narrower than general log access, write to a file on a
  `podTemplate.volumes` volume and ship it with a sidecar instead.
- Never write the token itself; log the decoded claims only. A repository's
  committed settings can disable hooks, which the runner flags under its
  default `confineRepoSettings: warn`; set `enforce` if the record must not
  be skippable.
