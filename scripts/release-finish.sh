#!/usr/bin/env bash
# Publish the draft release of a tag, after a check that its assets are the files that the workflow
# built and verified.
#
# A token with contents write can add or replace an asset on a draft. A published release is
# immutable, so a changed asset would stay. This downloads each asset of the draft and compares
# the set of names and their SHA-256 with the local files.
#
# Usage: scripts/release-finish.sh <tag>    (needs GH_TOKEN and GITHUB_REPOSITORY)
set -euo pipefail

tag="${1:?usage: release-finish.sh <tag>}"
api="repos/${GITHUB_REPOSITORY:?}"

root="$(git rev-parse --show-toplevel)"
cd "$root"

# GoReleaser makes a second draft if it does not find the draft of release-please. --slurp joins the
# pages, and the tag reaches jq as data, not as part of the filter.
releases="$(gh api "$api/releases" --paginate |
  jq --slurp --arg tag "$tag" '[.[][] | select(.tag_name == $tag)]')"
if [ "$(jq length <<< "$releases")" != "1" ]; then
  echo "release-finish: $(jq length <<< "$releases") releases have the tag $tag, and one is necessary" >&2
  exit 1
fi
if [ "$(jq -r '.[0].draft' <<< "$releases")" != "true" ]; then
  echo "release-finish: the release of $tag is not a draft" >&2
  exit 1
fi
id="$(jq -r '.[0].id' <<< "$releases")"

sha256() {
  if command -v sha256sum > /dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi |
    awk '{print $1}'
}

want="$(mktemp)"
got="$(mktemp)"
work="$(mktemp -d)"
trap 'rm -rf "$want" "$got" "$work"' EXIT

for file in dist/*.tar.gz dist/*.sbom.json dist/checksums.txt dist/checksums.txt.sigstore.json \
  build/chart/*.tgz build/chart/*.tgz.sigstore.json; do
  [ -f "$file" ] || { echo "release-finish: $file is missing" >&2; exit 1; }
  printf '%s %s\n' "$(basename "$file")" "$(sha256 "$file")" >> "$want"
done

gh api "$api/releases/$id/assets" --paginate --jq '.[] | "\(.id) \(.name)"' |
  while read -r asset name; do
    gh api -H "Accept: application/octet-stream" "$api/releases/assets/$asset" > "$work/asset"
    printf '%s %s\n' "$name" "$(sha256 "$work/asset")" >> "$got"
  done

if ! diff <(sort "$want") <(sort "$got"); then
  echo "release-finish: the assets of the draft are not the files that the workflow verified" >&2
  exit 1
fi

gh api --method PATCH "$api/releases/$id" -F draft=false > /dev/null
echo "release-finish: published $tag"
