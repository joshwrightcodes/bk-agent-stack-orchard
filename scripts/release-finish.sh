#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Publish the draft release of a tag, after a check that its assets are the files that the workflow
# built and verified.
#
# A token with contents write can add or replace an asset on a draft, or change its notes. A
# published release is immutable, so a change would stay. This downloads each asset of the draft
# and compares the set of names and their SHA-256 with the local files. Just before the publish, it
# lists the assets again: an upload or a replacement gets a new asset ID, so a change during the
# check stops the publish. A change can still fall between that list and the publish, so after the
# publish it downloads the assets again and reads the notes again. The release is then immutable,
# so a match proves what the release holds for good, and a mismatch fails the run.
#
# Usage: scripts/release-finish.sh <tag>    (needs GH_TOKEN and GITHUB_REPOSITORY)
set -euo pipefail

# Publish the draft. The tag decides the pre-release flag, because a published release is immutable.
# Every 0.x release is a pre-release until 1.0.0, and it is not the latest release. A tag of another
# major version is neither. The draft holds whatever flag release-please gave it, so this PATCH is
# the one place that decides. -F sends the booleans as JSON. make_latest is a string enum of the API
# ("true", "false", or "legacy"), so -f sends it as a raw string.
publish_release() {
	local api="$1" id="$2" tag="$3"
	local flags
	case "$tag" in
	v0.*) flags=(-F prerelease=true -f make_latest=false) ;;
	*) flags=(-F prerelease=false) ;;
	esac
	gh api --method PATCH "$api/releases/$id" -F draft=false "${flags[@]}" >/dev/null
	echo "release-finish: published $tag (${flags[*]})"
}

sha256() {
	if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi |
		awk '{print $1}'
}

# Download each asset of a release. Write "<name> <SHA-256>" of each one to the file $3, and
# "<asset ID> <name>" to the file $4, both sorted. $5 is a scratch file. Call it as a plain command,
# never in a condition or a command substitution, so that a failed download stops the script.
asset_sums() {
	local api="$1" id="$2" sums="$3" ids="$4" scratch="$5"
	: >"$sums"
	: >"$ids"
	gh api "$api/releases/$id/assets" --paginate --jq '.[] | "\(.id) \(.name)"' |
		while read -r asset name; do
			gh api -H "Accept: application/octet-stream" "$api/releases/assets/$asset" >"$scratch"
			printf '%s %s\n' "$name" "$(sha256 "$scratch")" >>"$sums"
			printf '%s %s\n' "$asset" "$name" >>"$ids"
		done
	sort -o "$sums" "$sums"
	sort -o "$ids" "$ids"
}

# Write "<asset ID> <name>" of each asset of a release to the file $3, sorted.
asset_ids() {
	local api="$1" id="$2" ids="$3"
	gh api "$api/releases/$id/assets" --paginate --jq '.[] | "\(.id) \(.name)"' | sort >"$ids"
}

# The fields of a release that a reader sees, other than the assets.
notes() {
	jq -S '{tag_name, name, body}'
}

# Sourcing the file defines the functions and runs nothing else.
[[ "${BASH_SOURCE[0]}" != "$0" ]] && return 0

tag="${1:?usage: release-finish.sh <tag>}"
api="repos/${GITHUB_REPOSITORY:?}"

root="$(git rev-parse --show-toplevel)"
cd "$root"

# The workflow uploads to the draft of release-please, and a second draft with the same tag would
# make the choice unclear. --slurp joins the pages, and the tag reaches jq as data, not as part of
# the filter.
releases="$(gh api "$api/releases" --paginate |
	jq --slurp --arg tag "$tag" '[.[][] | select(.tag_name == $tag)]')"
if [ "$(jq length <<<"$releases")" != "1" ]; then
	echo "release-finish: $(jq length <<<"$releases") releases have the tag $tag, and one is necessary" >&2
	exit 1
fi
if [ "$(jq -r '.[0].draft' <<<"$releases")" != "true" ]; then
	echo "release-finish: the release of $tag is not a draft" >&2
	exit 1
fi
id="$(jq -r '.[0].id' <<<"$releases")"
notes_before="$(jq '.[0]' <<<"$releases" | notes)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

for file in dist/*.tar.gz dist/*.sbom.json dist/*.openvex.json dist/checksums.txt \
	dist/checksums.txt.sigstore.json dist/checksums.txt.intoto.jsonl build/chart/*.tgz \
	build/chart/*.tgz.sigstore.json; do
	[ -f "$file" ] || {
		echo "release-finish: $file is missing" >&2
		exit 1
	}
	printf '%s %s\n' "$(basename "$file")" "$(sha256 "$file")" >>"$work/want"
done
sort -o "$work/want" "$work/want"

asset_sums "$api" "$id" "$work/got" "$work/ids" "$work/asset"
if ! diff "$work/want" "$work/got"; then
	echo "release-finish: the assets of the draft are not the files that the workflow verified" >&2
	exit 1
fi

# The download takes a while. An asset that changed during it has a new ID now.
asset_ids "$api" "$id" "$work/ids-now"
if ! diff "$work/ids" "$work/ids-now"; then
	echo "release-finish: the assets of the draft changed during the check, so it stays a draft" >&2
	exit 1
fi

publish_release "$api" "$id" "$tag"

# The release is immutable from here, so this reads what it holds for good.
asset_sums "$api" "$id" "$work/published" "$work/ids-published" "$work/asset"
notes_after="$(gh api "$api/releases/$id" | notes)"
failed=0
if ! diff "$work/want" "$work/published"; then
	echo "release-finish: the published release of $tag holds assets that the workflow did not verify" >&2
	failed=1
fi
if [ "$notes_before" != "$notes_after" ]; then
	echo "release-finish: the notes of $tag changed between the check and the publish" >&2
	diff <(printf '%s\n' "$notes_before") <(printf '%s\n' "$notes_after") >&2 || true
	failed=1
fi
if [ "$failed" = "1" ]; then
	echo "release-finish: $tag is published and immutable. Check the release now, and tell operators not to use it if it is wrong" >&2
	exit 1
fi
echo "release-finish: the published assets and notes of $tag are the ones that the workflow checked"
