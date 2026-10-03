#!/usr/bin/env bash
# Real-environment test. Level 1 (always): the orchestrator registers with
# Anthropic using CLAUDE_ENVIRONMENT_KEY and the environment reports Ready.
# Level 2 (when CLAUDE_CODE_OAUTH_REFRESH_TOKEN is set, or `claude auth status`
# reports a claude.ai login on this machine): start a session routed to
# CLAUDE_ENVIRONMENT_ID and check the reply, the ClaudeRunner lifecycle and GC.
# Needs: kubectl against a cluster running the operator, envsubst, jq, curl,
# git, and claude v2.1.224 or later for level 2. Run from the repository root.
# Never enable `set -x` here: the environment key passes through envsubst.
set -euo pipefail
: "${CLAUDE_ENVIRONMENT_KEY:?}" "${CLAUDE_ENVIRONMENT_ID:?}"
NAMESPACE=${NAMESPACE:-claude-real-e2e}
RUNNER_IMAGE=${RUNNER_IMAGE:-example.com/claude-runner:real-e2e-test}
OPERATOR_NAMESPACE=${OPERATOR_NAMESPACE:-claude-selfhosted-operator-system}
# TEST_REPO (owner/name) is cloned anonymously from GitHub; empty means the
# current checkout, whose origin remote the CLI uses to pick the repository.
TEST_REPO=${TEST_REPO:-}
TEST_REF=${TEST_REF:-}
export NAMESPACE RUNNER_IMAGE
workdir=$PWD
pf=
runner_log_pid=
runner_log=
cleanup() {
  [ -z "$pf" ] || kill "$pf" 2>/dev/null || true
  [ -z "$runner_log_pid" ] || kill "$runner_log_pid" 2>/dev/null || true
  [ -z "${pf_log:-}" ] || rm -f "$pf_log"
  [ -z "$runner_log" ] || rm -f "$runner_log"
  [ "$workdir" = "$PWD" ] || rm -rf "$workdir"
}
trap cleanup EXIT

# redact masks anything that looks like a JWT or an environment key before a
# log line reaches a (possibly public) CI log.
redact() { sed -E 's/eyJ[A-Za-z0-9_-]{20,}(\.[A-Za-z0-9_-]+){0,2}/[REDACTED]/g; s/cc(env|pool)[a-z_]*_[A-Za-z0-9_-]{8,}/[REDACTED]/g'; }

# dump_evidence prints what a failed level-2 run needs to be diagnosed: the
# ClaudeRunner status, the namespace events, the keys the sink holds and the
# runner pod log captured while it ran (the pod is garbage-collected soon
# after it finishes, so it is streamed from the moment it appears).
dump_evidence() {
  echo "---- ClaudeRunners ($NAMESPACE)" >&2
  kubectl get -n "$NAMESPACE" clauderunners -o yaml 2>&1 | grep -vE 'resourceVersion|managedFields|uid:' | redact >&2 || true
  echo "---- events ($NAMESPACE)" >&2
  kubectl get events -n "$NAMESPACE" --sort-by=.lastTimestamp 2>&1 | tail -40 | redact >&2 || true
  echo "---- reply sink log" >&2
  kubectl logs -n "$NAMESPACE" deployment/replysink --tail=50 2>&1 | redact >&2 || true
  if [ -n "$runner_log" ] && [ -s "$runner_log" ]; then
    echo "---- runner pod log (redacted, last 200 lines)" >&2
    tail -n 200 "$runner_log" | redact >&2
  else
    echo "---- runner pod log: not captured" >&2
  fi
}

# claude_ai_login succeeds when `claude auth status` (JSON by default) reports
# a logged-in claude.ai account; an API-key login cannot start a session.
claude_ai_login() {
  claude auth status --json 2>/dev/null | jq -e '.loggedIn == true and .authMethod == "claude.ai"' >/dev/null 2>&1
}

echo "== operator ready"
kubectl wait --for=condition=Established crd/claudeenvironments.selfhosted.claudecode.dev crd/clauderunners.selfhosted.claudecode.dev --timeout=60s
kubectl rollout status -n "$OPERATOR_NAMESPACE" deployment/claude-selfhosted-operator-controller-manager --timeout=180s

echo "== namespace and sink"
kubectl create ns "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl label ns "$NAMESPACE" pod-security.kubernetes.io/enforce=restricted --overwrite
kubectl apply -n "$NAMESPACE" -f test/real-e2e/replysink.yaml
kubectl rollout status -n "$NAMESPACE" deployment/replysink --timeout=120s
kubectl apply -n "$NAMESPACE" -f test/real-e2e/host-config.yaml
# shellcheck disable=SC2016 # envsubst takes the literal variable names
envsubst '$CLAUDE_ENVIRONMENT_KEY' < test/real-e2e/orchestrator-secret.yaml.tmpl | kubectl apply -n "$NAMESPACE" -f - >/dev/null
# shellcheck disable=SC2016
envsubst '$RUNNER_IMAGE $NAMESPACE' < test/real-e2e/environment.yaml | kubectl apply -n "$NAMESPACE" -f -

echo "== level 1: orchestrator connected"
kubectl wait -n "$NAMESPACE" claudeenvironment/real --for=condition=Ready=True --timeout=300s
kubectl get -n "$NAMESPACE" claudeenvironment/real -o jsonpath='{.status.conditions}' | jq .

if [ -n "${CLAUDE_CODE_OAUTH_REFRESH_TOKEN:-}" ]; then
  : "${CLAUDE_CODE_OAUTH_SCOPES:?CLAUDE_CODE_OAUTH_SCOPES is required with CLAUDE_CODE_OAUTH_REFRESH_TOKEN}"
  echo "== level 2: logging in with the refresh token"
  claude auth login >/dev/null
elif ! claude_ai_login; then
  echo "== level 2 skipped: no CLAUDE_CODE_OAUTH_REFRESH_TOKEN and no claude.ai login on this machine"
  exit 0
fi
if ! claude_ai_login; then
  echo "level 2: claude auth status does not report a claude.ai login" >&2
  exit 1
fi

# The CLI has no repository flag: it reads the repository from the origin
# remote of the checkout it runs in, and --ref names the branch to check out.
if [ -n "$TEST_REPO" ]; then
  workdir=$(mktemp -d)
  git clone -q --depth 1 ${TEST_REF:+--branch "$TEST_REF"} "https://github.com/$TEST_REPO.git" "$workdir"
fi
if [ -z "$TEST_REF" ]; then
  TEST_REF=$(git -C "$workdir" rev-parse --abbrev-ref HEAD)
  # A detached HEAD reports "HEAD": use the commit instead.
  [ "$TEST_REF" != HEAD ] || TEST_REF=$(git -C "$workdir" rev-parse HEAD)
fi
echo "repository: ${TEST_REPO:-the current checkout} ref: $TEST_REF"

echo "== level 2: starting a session"
pf_log=$(mktemp)
kubectl port-forward -n "$NAMESPACE" svc/replysink 18080:8080 >"$pf_log" 2>&1 &
pf=$!
sleep 2
marker="operator-real-e2e-$(date +%s)"
result=$(cd "$workdir" && claude -p "Reply with exactly the text: $marker" --environment "$CLAUDE_ENVIRONMENT_ID" --ref "$TEST_REF" --output-format json)
echo "create: $result"
session_id=$(printf '%s' "$result" | jq -er '.session_id') || { echo "claude -p returned no session_id; output above" >&2; exit 1; }
echo "session $session_id"

echo "== level 2: runner lifecycle"
phase=
for _ in $(seq 1 60); do
  phase=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ -n "$phase" ] && break
  sleep 5
done
echo "runner phase: ${phase:-none}"
[ -n "$phase" ] || { echo "no ClaudeRunner appeared for session $session_id within 300s" >&2; dump_evidence; exit 1; }
# Stream the runner pod's log from now on; it is only printed, redacted, if
# the run fails.
runner_pod=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o jsonpath='{.items[0].status.podName}' 2>/dev/null || true)
if [ -n "$runner_pod" ]; then
  runner_log=$(mktemp)
  kubectl logs -n "$NAMESPACE" -f "$runner_pod" --all-containers >"$runner_log" 2>&1 &
  runner_log_pid=$!
fi

echo "== level 2: reply captured"
reply=
for _ in $(seq 1 60); do
  if reply=$(curl -sf "http://127.0.0.1:18080/$session_id"); then break; fi
  sleep 5
done
[ -n "$reply" ] || { echo "no reply for $session_id in the sink" >&2; cat "$pf_log" >&2; dump_evidence; exit 1; }
echo "$reply" | jq .
printf '%s' "$reply" | jq -e --arg m "$marker" '.reply | contains($m)' >/dev/null ||
  { echo "the sink's reply for $session_id does not contain the marker $marker" >&2; dump_evidence; exit 1; }

echo "== level 2: runner reaches Succeeded and is garbage collected"
kubectl wait -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s ||
  { echo "the ClaudeRunner did not reach Succeeded within 600s" >&2; exit 1; }
# A failed kubectl call is not "zero runners": keep polling until a call succeeds and lists none.
n=unknown
for _ in $(seq 1 40); do
  if out=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o name 2>/dev/null); then
    n=$(printf '%s' "$out" | grep -c . || true)
    [ "$n" = 0 ] && break
  fi
  sleep 5
done
[ "$n" = 0 ] || { echo "ClaudeRunner objects remain after 200s (count: $n)" >&2; exit 1; }
echo "== real-environment test passed"
