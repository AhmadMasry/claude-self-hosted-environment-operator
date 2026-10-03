#!/usr/bin/env bash
# post-session lifecycle hook for the real-environment test: records that the
# runner ran it from the operator's ConfigMap mount, keyed by the session id,
# with the exit reason and whether the runner's environment reached it. The
# sink URL falls back to the rendered one so a missing variable is reported
# rather than hidden. Fails open: the runner ignores its exit status anyway.
set -uo pipefail
sid=${CLAUDE_RUNNER_SESSION_ID:-}
[ -n "$sid" ] || { echo "post-session: CLAUDE_RUNNER_SESSION_ID is unset" >&2; exit 0; }
url=${E2E_REPLY_URL:-http://replysink.${NAMESPACE}.svc:8080}
jq -nc --arg sid "$sid" --arg reason "${CLAUDE_RUNNER_EXIT_REASON:-}" --arg env "${E2E_REPLY_URL:+set}" \
  '{session_id: $sid, exit_reason: $reason, env_url: (if $env == "" then "unset" else "set" end)}' \
  | curl -fsS -m 10 -X POST -H 'Content-Type: application/json' --data-binary @- "$url/$sid-post-session"
echo "post-session: reported $sid"
exit 0
