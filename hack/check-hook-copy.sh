#!/usr/bin/env bash
# Fails when a script embedded in a test/real-e2e ConfigMap differs from the
# standalone copy kept for shellcheck. Run from any directory.
set -euo pipefail
cd "$(dirname "$0")/.."
# ConfigMap manifest, data key, standalone copy.
pairs=(
  "test/real-e2e/host-config.yaml capture-reply.sh test/real-e2e/capture-reply.sh"
  "test/real-e2e/lifecycle-hooks.yaml post-session test/real-e2e/post-session.sh"
  "test/real-e2e/wrapper.yaml wrapper.sh test/real-e2e/wrapper.sh"
)
embedded_script() {
  if command -v yq >/dev/null 2>&1 && yq --version 2>&1 | grep -q mikefarah; then
    yq ".data[\"$2\"]" "$1"
  else
    python3 -c 'import sys, yaml; print(yaml.safe_load(open(sys.argv[1]))["data"][sys.argv[2]], end="")' "$1" "$2"
  fi
}
rc=0
for pair in "${pairs[@]}"; do
  read -r cm key copy <<<"$pair"
  if ! diff <(printf '%s\n' "$(embedded_script "$cm" "$key")") "$copy"; then
    echo "$cm data[$key] differs from $copy" >&2
    rc=1
  fi
done
[ "$rc" = 0 ] && echo "Hook copies match"
exit "$rc"
