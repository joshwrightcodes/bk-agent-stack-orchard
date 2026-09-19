#!/usr/bin/env bash
# Validate the C4 workspace and confirm that the generated views agree with it.
#
# The workspace is the source of truth for the diagrams (target design, section 4). This script
# fails when the workspace is not valid, or when docs/architecture/c4.md is older than it
# (test strategy, T-121).
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

echo "check-c4: validating the workspace"
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -v "$root/docs/architecture:/usr/local/structurizr" \
  structurizr/structurizr validate -workspace /usr/local/structurizr/workspace.dsl

echo "check-c4: regenerating the Mermaid views"
./docs/architecture/render-c4-md.sh >/dev/null

if ! git diff --quiet -- docs/architecture/c4.md; then
  echo "check-c4: docs/architecture/c4.md does not match workspace.dsl." >&2
  echo "check-c4: run ./docs/architecture/render-c4-md.sh and commit the result." >&2
  git --no-pager diff --stat -- docs/architecture/c4.md >&2
  exit 1
fi

echo "check-c4: the workspace is valid and c4.md is current"
