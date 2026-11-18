#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
# Validate the C4 workspace.
#
# The workspace is the model of the diagrams (target design, section 4). This script fails when the
# workspace is not valid.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

# The tag and the digest pin the image, so a new Structurizr release cannot change the result of
# this check on a pull request that did not ask for it. Renovate proposes each new release.
structurizr_image="structurizr/structurizr:2026.10.26@sha256:8aebde645a7cc8d3b9e1e774cd6d75a84ce45d74ae70eb783876182fe7db31a2"

echo "check-c4: validating the workspace"
# Validation only reads the workspace, so the mount is read-only: the image cannot change a file
# of the repository. HOME points at a directory inside the container.
docker run --rm \
	--user "$(id -u):$(id -g)" \
	-e HOME=/tmp \
	-v "$root/docs/architecture:/usr/local/structurizr:ro" \
	"$structurizr_image" validate -workspace /usr/local/structurizr/workspace.dsl

echo "check-c4: the workspace is valid"
