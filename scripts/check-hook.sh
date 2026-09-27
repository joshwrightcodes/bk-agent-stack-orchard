#!/usr/bin/env bash
# Checks the environment hook of the agent image (images/agent/files/environment) the way
# buildkite-agent runs it: the agent sources the hook, and then reads the environment. The hook
# keeps the artifacts of a job in the owner-managed storage, and never sends them to the storage of
# Buildkite (PR-032).
set -euo pipefail

HOOK="$(git rev-parse --show-toplevel)/images/agent/files/environment"
failures=0

# destination gives what the hook leaves in BUILDKITE_ARTIFACT_UPLOAD_DESTINATION, for the storage
# that the build gives the image and the value that a pipeline step set, if it set one.
destination() {
	local owner="$1" set="$2" hook
	hook="$(mktemp)"
	sed "s|__ARTIFACT_DESTINATION__|${owner}|" "$HOOK" >"$hook"

	env -i PATH="$PATH" BUILDKITE_PIPELINE_SLUG=app BUILDKITE_BUILD_ID=b1 BUILDKITE_JOB_ID=j1 \
		${set:+BUILDKITE_ARTIFACT_UPLOAD_DESTINATION="$set"} \
		bash -c ". '$hook' 2>/dev/null; printf '%s' \"\${BUILDKITE_ARTIFACT_UPLOAD_DESTINATION:-}\""

	rm -f "$hook"
}

expect() {
	local name="$1" got="$2" want="$3"

	if [[ "$got" == "$want" ]]; then
		printf 'check-hook: %s\n' "$name"
	else
		printf 'check-hook: %s: got %q, want %q\n' "$name" "$got" "$want" >&2
		failures=$((failures + 1))
	fi
}

expect "an image with no storage refuses every upload" \
	"$(destination "" "")" "none://artifact-storage-is-not-configured"
expect "a job goes to its own place in the storage" \
	"$(destination "s3://acme-au/ci" "")" "s3://acme-au/ci/app/b1/j1"
expect "a step may name another place in the same storage" \
	"$(destination "s3://acme-au/ci" "s3://acme-au/ci/release")" "s3://acme-au/ci/release"
expect "a step may not name another storage" \
	"$(destination "s3://acme-au/ci" "s3://elsewhere/x")" "s3://acme-au/ci/app/b1/j1"
expect "a storage whose name starts the same is another storage" \
	"$(destination "s3://acme-au/ci" "s3://acme-au/ci-other")" "s3://acme-au/ci/app/b1/j1"

if ((failures > 0)); then
	exit 1
fi
