#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Checks the environment hook of the agent image (images/files/environment) the way
# buildkite-agent runs it: the agent sources the hook, and then reads the environment. When the
# image has an owner-managed storage, the hook keeps the artifacts of a job in it. When the image
# has none, the hook leaves the destination alone, and the artifacts go to the storage of Buildkite
# (PR-032, owner decision of 2026-11-02).
#
# Usage: scripts/check-hook.sh        (mise run check-hook)
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

readonly PROG="check-hook"

usage() {
	sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

[[ $# -eq 0 ]] || usage

HOOK="$(git rev-parse --show-toplevel)/images/files/environment"
failures=0

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# destination gives what the hook leaves in BUILDKITE_ARTIFACT_UPLOAD_DESTINATION, for the storage
# that the build gives the image and the value that a pipeline step set, if it set one. The
# storages of the cases below hold no character that sed or the hook would read as syntax.
destination() {
	local owner="$1" set="$2" hook="$work/environment"
	sed "s|__ARTIFACT_DESTINATION__|${owner}|" "$HOOK" >"$hook"

	env -i PATH="$PATH" BUILDKITE_PIPELINE_SLUG=app BUILDKITE_BUILD_ID=b1 BUILDKITE_JOB_ID=j1 \
		${set:+BUILDKITE_ARTIFACT_UPLOAD_DESTINATION="$set"} \
		bash -c ". '$hook' 2>/dev/null; printf '%s' \"\${BUILDKITE_ARTIFACT_UPLOAD_DESTINATION:-}\""
}

expect() {
	local name="$1" got="$2" want="$3"

	if [[ "$got" == "$want" ]]; then
		printf '%s: %s\n' "$PROG" "$name"
	else
		printf '%s: %s: got %q, want %q\n' "$PROG" "$name" "$got" "$want" >&2
		failures=$((failures + 1))
	fi
}

expect "an image with no storage leaves the destination to buildkite-agent" \
	"$(destination "" "")" ""
expect "an image with no storage keeps a destination that a step set" \
	"$(destination "" "s3://elsewhere/x")" "s3://elsewhere/x"
expect "a job goes to its own place in the storage" \
	"$(destination "s3://acme-au/ci" "")" "s3://acme-au/ci/app/b1/j1"
expect "a step may name another place in the same storage" \
	"$(destination "s3://acme-au/ci" "s3://acme-au/ci/release")" "s3://acme-au/ci/release"
expect "a step may not name another storage" \
	"$(destination "s3://acme-au/ci" "s3://elsewhere/x")" "s3://acme-au/ci/app/b1/j1"
expect "a storage whose name starts the same is another storage" \
	"$(destination "s3://acme-au/ci" "s3://acme-au/ci-other")" "s3://acme-au/ci/app/b1/j1"

if ((failures > 0)); then
	printf '%s: %d case(s) failed\n' "$PROG" "$failures" >&2
	exit 1
fi
