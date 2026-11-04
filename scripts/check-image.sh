#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Checks the container image that GoReleaser and ko build.
#
# The design states a distroless base and a non-root user. An image that runs as root, or that
# uses another base, fails here rather than in a cluster. The base is checked in .goreleaser.yaml,
# and the user in the snapshot image of the last snapshot build.
#
# Usage: scripts/check-image.sh        (mise run snapshot runs it after the snapshot build)
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

readonly PROG="check-image"

say() { printf '%s: %s\n' "$PROG" "$*"; }
die() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}
usage() {
	sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

[[ $# -eq 0 ]] || usage

root="$(git rev-parse --show-toplevel)"
cd "$root"

expected_base="gcr.io/distroless/static-debian13:nonroot"
expected_user="65532"

grep -q "base_image: $expected_base" .goreleaser.yaml ||
	die ".goreleaser.yaml does not set base_image to $expected_base"

# GoReleaser loads a snapshot image into the local daemon under goreleaser.ko.local, tagged with
# the version of the snapshot. The version comes from the record of the last build, so an older
# snapshot image in the daemon is not the one checked.
[[ -f dist/metadata.json ]] ||
	die "dist/metadata.json does not exist. Run: goreleaser release --snapshot --clean"
version="$(jq -r '.version // empty' dist/metadata.json)"
[[ -n "$version" ]] || die "dist/metadata.json gives no version"

image="$(docker images --format '{{.Repository}} {{.Tag}}' |
	awk -v version="$version" '!found && $1 ~ /^goreleaser\.ko\.local/ && $2 == version {print $1 ":" $2; found = 1}')"
[[ -n "$image" ]] ||
	die "no snapshot image goreleaser.ko.local:$version found. Run: goreleaser release --snapshot --clean"

user="$(docker inspect --format '{{.Config.User}}' "$image")"
[[ "$user" == "$expected_user" ]] ||
	die "$image runs as user '${user:-root}', the design states $expected_user"

say "$image runs as $user on $expected_base"
