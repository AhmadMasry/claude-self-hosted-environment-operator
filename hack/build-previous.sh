#!/usr/bin/env bash
# Builds the operator image and installer of a previous git revision into
# an out-of-tree worktree, for the upgrade e2e. Usage:
#   hack/build-previous.sh <rev> <image> <out-dir>
set -euo pipefail
rev=$1; image=$2; out=$3
wt=$(mktemp -d)
trap 'git worktree remove --force "$wt"' EXIT
git worktree add --detach "$wt" "$rev"
mkdir -p "$out"
make -C "$wt" docker-build build-installer IMG="$image"
cp "$wt/dist/install.yaml" "$out/install.yaml"
