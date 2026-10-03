#!/usr/bin/env bash
# Fails when the capture-reply.sh embedded in test/real-e2e/host-config.yaml
# differs from test/real-e2e/capture-reply.sh. Run from any directory.
set -euo pipefail
cd "$(dirname "$0")/.."
cm=test/real-e2e/host-config.yaml
copy=test/real-e2e/capture-reply.sh
if command -v yq >/dev/null 2>&1 && yq --version 2>&1 | grep -q mikefarah; then
  embedded=$(yq '.data["capture-reply.sh"]' "$cm")
else
  embedded=$(python3 -c 'import sys, yaml; print(yaml.safe_load(open(sys.argv[1]))["data"]["capture-reply.sh"], end="")' "$cm")
fi
if ! diff <(printf '%s\n' "$embedded") "$copy"; then
  echo "$cm data[capture-reply.sh] differs from $copy" >&2
  exit 1
fi
echo "Hook copy matches"
