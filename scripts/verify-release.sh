#!/usr/bin/env bash
# Verify the signatures of a release: the checksums, the image, the SBOM of each platform image,
# and the chart.
#
# The release workflow runs this before it publishes the draft release, and an operator can run it
# before a deployment. It needs cosign, docker with buildx, and jq. Each check requires a
# certificate for the release workflow of this repository at this exact tag, from the OIDC issuer
# of GitHub Actions.
#
# Usage: scripts/verify-release.sh <tag> [<directory with checksums.txt>]
set -euo pipefail

tag="${1:?usage: verify-release.sh <tag> [<directory>]}"
dir="${2:-}"
version="${tag#v}"
repo="joshwrightcodes/bk-agent-stack-orchard"
image="ghcr.io/$repo"
chart="ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard"

identity=(
  --certificate-identity "https://github.com/$repo/.github/workflows/release.yml@refs/tags/$tag"
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
)

if [ -n "$dir" ]; then
  cosign verify-blob "${identity[@]}" \
    --bundle "$dir/checksums.txt.sigstore.json" "$dir/checksums.txt"
  # Only the files that are present: an operator often downloads one archive. macOS has shasum,
  # and not sha256sum.
  sum=(sha256sum)
  command -v sha256sum > /dev/null || sum=(shasum -a 256)
  (cd "$dir" && "${sum[@]}" --check --ignore-missing checksums.txt)
fi

index="$(docker buildx imagetools inspect "$image:$tag" --format '{{json .Manifest}}' | jq -r .digest)"
cosign verify "${identity[@]}" "$image@$index" > /dev/null
docker buildx imagetools inspect "$image@$index" --raw |
  jq -r '.manifests[] | select(.platform.os == "linux") | .digest' |
  while read -r digest; do
    cosign verify "${identity[@]}" "$image@$digest" > /dev/null
    cosign verify-attestation "${identity[@]}" --type spdxjson "$image@$digest" > /dev/null
  done

cosign verify "${identity[@]}" "$chart:$version" > /dev/null

echo "verify-release: $tag is signed by the release workflow of $repo"
