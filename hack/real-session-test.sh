#!/usr/bin/env bash
# Real-environment test. Level 1 (always): the orchestrator registers with
# Anthropic using CLAUDE_ENVIRONMENT_KEY and the environment reports Ready.
# Level 2 (when CLAUDE_CODE_OAUTH_REFRESH_TOKEN is set, or `claude auth login`
# already happened on this machine): start a session routed to
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

if [ -z "${CLAUDE_CODE_OAUTH_REFRESH_TOKEN:-}" ] && ! claude auth status >/dev/null 2>&1; then
  echo "== level 2 skipped: no CLAUDE_CODE_OAUTH_REFRESH_TOKEN and no claude login on this machine"
  exit 0
fi
if [ -n "${CLAUDE_CODE_OAUTH_REFRESH_TOKEN:-}" ]; then
  : "${CLAUDE_CODE_OAUTH_SCOPES:?CLAUDE_CODE_OAUTH_SCOPES is required with CLAUDE_CODE_OAUTH_REFRESH_TOKEN}"
  echo "== level 2: logging in with the refresh token"
  claude auth login >/dev/null
fi

# The CLI has no repository flag: it reads the repository from the origin
# remote of the checkout it runs in, and --ref names the branch to check out.
workdir=$PWD
if [ -n "$TEST_REPO" ]; then
  workdir=$(mktemp -d)
  git clone -q --depth 1 ${TEST_REF:+--branch "$TEST_REF"} "https://github.com/$TEST_REPO.git" "$workdir"
fi
TEST_REF=${TEST_REF:-$(git -C "$workdir" rev-parse --abbrev-ref HEAD)}
echo "repository: $(git -C "$workdir" remote get-url origin) ref: $TEST_REF"

echo "== level 2: starting a session"
pf_log=$(mktemp)
kubectl port-forward -n "$NAMESPACE" svc/replysink 18080:8080 >"$pf_log" 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
sleep 2
marker="operator-real-e2e-$(date +%s)"
result=$(cd "$workdir" && claude -p "Reply with exactly the text: $marker" --environment "$CLAUDE_ENVIRONMENT_ID" --ref "$TEST_REF" --output-format json)
echo "create: $result"
session_id=$(printf '%s' "$result" | jq -er '.session_id')
echo "session $session_id"

echo "== level 2: runner lifecycle"
phase=
for _ in $(seq 1 60); do
  phase=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ -n "$phase" ] && break
  sleep 5
done
echo "runner phase: ${phase:-none}"
[ -n "$phase" ]

echo "== level 2: reply captured"
reply=
for _ in $(seq 1 60); do
  if reply=$(curl -sf "http://127.0.0.1:18080/$session_id"); then break; fi
  sleep 5
done
[ -n "$reply" ] || { echo "no reply for $session_id in the sink" >&2; cat "$pf_log" >&2; exit 1; }
echo "$reply" | jq .
printf '%s' "$reply" | jq -e --arg m "$marker" '.reply | contains($m)' >/dev/null

echo "== level 2: runner reaches Succeeded and is garbage collected"
kubectl wait -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s
n=
for _ in $(seq 1 40); do
  n=$(kubectl get -n "$NAMESPACE" clauderunners -l selfhosted.claudecode.dev/environment=real -o name | wc -l | tr -d ' ')
  [ "$n" = 0 ] && break
  sleep 5
done
[ "$n" = 0 ]
echo "== real-environment test passed"
