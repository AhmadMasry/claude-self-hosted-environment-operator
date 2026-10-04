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
pod_evidence_pid=
pod_evidence=
cleanup() {
  [ -z "$pf" ] || kill "$pf" 2>/dev/null || true
  [ -z "$runner_log_pid" ] || kill "$runner_log_pid" 2>/dev/null || true
  [ -z "$pod_evidence_pid" ] || kill "$pod_evidence_pid" 2>/dev/null || true
  [ -z "${pf_log:-}" ] || rm -f "$pf_log"
  [ -z "$runner_log" ] || rm -f "$runner_log"
  [ -z "$pod_evidence" ] || rm -f "$pod_evidence"
  [ "$workdir" = "$PWD" ] || rm -rf "$workdir"
}
trap cleanup EXIT

# redact masks the same shapes as internal/redact (JWTs, Anthropic API keys,
# environment keys and email addresses) before a log line reaches a
# (possibly public) CI log.
redact() {
  sed -E -e 's/eyJ[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+){1,2}|eyJ[A-Za-z0-9_-]{20,}/[REDACTED]/g' \
    -e 's/sk-ant-[A-Za-z0-9_-]+/[REDACTED]/g' \
    -e 's/cc(env|pool)[a-z_]*_[A-Za-z0-9_-]{8,}/[REDACTED]/g' \
    -e 's/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/[REDACTED]/g'
}

# dump_evidence prints what a failed level-2 run needs to be diagnosed: the
# ClaudeRunner status, the namespace events, the requests the sink saw, the
# runner pod log captured while it ran (the pod is garbage-collected soon
# after it finishes, so it is streamed from the moment it appears) and what
# the session saw inside the pod: the seeded config directory and the Stop
# hook's trace.
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
  if [ -n "$pod_evidence" ] && [ -s "$pod_evidence" ]; then
    echo "---- inside the runner pod: host config, seeded session config, hook trace (redacted)" >&2
    redact <"$pod_evidence" >&2
  else
    echo "---- inside the runner pod: not captured" >&2
  fi
}

# pod_evidence_script runs inside the runner pod: the host-config mount as
# the runner sees it, each session's seeded config directory under the
# default baseDir (/workspace) with its settings.json, and the Stop hook's
# trace. No secret is printed: names, the hook settings and curl's messages.
# shellcheck disable=SC2016 # the script expands inside the pod
pod_evidence_script='
echo "== /etc/claude/host-config"; ls -la /etc/claude/host-config; ls -laL /etc/claude/host-config
for d in /workspace/_sessions/*.claude-config; do
  [ -d "$d" ] || continue
  echo "== $d"; ls -la "$d"; echo "-- settings.json"; cat "$d/settings.json" 2>&1
done
echo "== /tmp/capture-reply.log"; cat /tmp/capture-reply.log 2>&1
exit 0
'

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
kubectl apply -n "$NAMESPACE" -f test/real-e2e/host-config.yaml -f test/real-e2e/wrapper.yaml
# shellcheck disable=SC2016
envsubst '$NAMESPACE' < test/real-e2e/lifecycle-hooks.yaml | kubectl apply -n "$NAMESPACE" -f -
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
  # The session's config directory appears only once the session starts and
  # the pod is gone soon after it ends, so poll while the pod runs and keep
  # the latest successful snapshot.
  pod_evidence=$(mktemp)
  (
    for _ in $(seq 1 60); do
      if out=$(kubectl exec -n "$NAMESPACE" "$runner_pod" -- sh -c "$pod_evidence_script" 2>&1); then
        printf '%s\n' "$out" >"$pod_evidence"
      fi
      sleep 5
    done
  ) &
  pod_evidence_pid=$!
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
# The wrapper script exported E2E_WRAPPER before exec-ing the binary; the Stop
# hook runs inside that process tree and reports it.
printf '%s' "$reply" | jq -e '.wrapper == "ran"' >/dev/null ||
  { echo "the wrapper script did not run: the reply's wrapper field is not \"ran\"" >&2; dump_evidence; exit 1; }

echo "== level 2: runner reaches Succeeded and is garbage collected"
kubectl wait -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s ||
  { echo "the ClaudeRunner did not reach Succeeded within 600s" >&2; exit 1; }

echo "== level 2: post-session lifecycle hook ran"
# The hook runs after the child exits, within the runner's 60s budget.
post=
for _ in $(seq 1 18); do
  if post=$(curl -sf "http://127.0.0.1:18080/$session_id-post-session"); then break; fi
  sleep 5
done
[ -n "$post" ] || { echo "no post-session record for $session_id in the sink" >&2; dump_evidence; exit 1; }
echo "$post" | jq .
printf '%s' "$post" | jq -e --arg s "$session_id" '.session_id == $s' >/dev/null ||
  { echo "the post-session record is not for $session_id" >&2; dump_evidence; exit 1; }
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
