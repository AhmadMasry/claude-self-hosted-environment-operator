#!/usr/bin/env bash
# Stop hook for the real-environment test, after the product's remote-runner
# capture hook: posts the turn's final reply to the reply sink keyed by the
# session id the dispatch CLI prints. Fails open: never blocks the session.
set -uo pipefail
[ -n "${E2E_REPLY_URL:-}" ] || exit 0
# CLAUDE_CODE_REMOTE_SESSION_ID is in cse_... form; the CLI prints session_...
sid=$(printf '%s' "${CLAUDE_CODE_REMOTE_SESSION_ID:-}" | sed 's/^cse_/session_/')
[ -n "$sid" ] || exit 0
jq -c --arg sid "$sid" '{session_id: $sid, reply: (.last_assistant_message // "")}' \
  | curl -fsS -m 10 -X POST -H 'Content-Type: application/json' --data-binary @- "$E2E_REPLY_URL/$sid" >/dev/null 2>&1
exit 0
