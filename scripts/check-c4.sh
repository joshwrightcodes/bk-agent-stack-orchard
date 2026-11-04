#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
# Validate the C4 workspace and confirm that the generated views agree with it.
#
# The workspace is the source of truth for the diagrams (target design, section 4). This script
# fails when the workspace is not valid, or when docs/architecture/c4.md is older than it.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

# The tag and the digest pin the image, so a new Structurizr release cannot change the result of
# this check on a pull request that did not ask for it. Renovate proposes each new release, and it
# updates this line and the same line in docs/architecture/render-c4-md.sh in one pull request.
structurizr_image="structurizr/structurizr:2026.10.26@sha256:8aebde645a7cc8d3b9e1e774cd6d75a84ce45d74ae70eb783876182fe7db31a2"

# The two scripts must run the same Structurizr, or the validation and the rendered views could
# disagree about the same workspace.
if ! grep -qF "structurizr_image=\"$structurizr_image\"" docs/architecture/render-c4-md.sh; then
	echo "check-c4: docs/architecture/render-c4-md.sh pins a different Structurizr image." >&2
	exit 1
fi

echo "check-c4: validating the workspace"
# Validation only reads the workspace, so the mount is read-only: the image cannot change a file
# of the repository. HOME points at a directory inside the container.
docker run --rm \
	--user "$(id -u):$(id -g)" \
	-e HOME=/tmp \
	-v "$root/docs/architecture:/usr/local/structurizr:ro" \
	"$structurizr_image" validate -workspace /usr/local/structurizr/workspace.dsl

echo "check-c4: regenerating the Mermaid views"
./docs/architecture/render-c4-md.sh >/dev/null

if ! git diff --quiet -- docs/architecture/c4.md; then
	echo "check-c4: docs/architecture/c4.md does not match workspace.dsl." >&2
	echo "check-c4: run ./docs/architecture/render-c4-md.sh and commit the result." >&2
	git --no-pager diff --stat -- docs/architecture/c4.md >&2
	exit 1
fi

echo "check-c4: the workspace is valid and c4.md is current"
