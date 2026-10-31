#!/usr/bin/env bash
# Verify the signatures of a release: the checksums and the archives, the chart package, the image,
# the SBOM of each platform image, and the chart in the registry.
#
# The release workflow runs this before it publishes the draft release, and an operator can run it
# before a deployment. It needs cosign, docker with buildx, and jq. Each check requires a
# certificate for the release workflow of this repository at this exact tag, from the OIDC issuer
# of GitHub Actions.
#
# Usage: scripts/verify-release.sh <tag> [<directory with checksums.txt>]
#
# IMAGE_DIGEST and CHART_DIGEST, when set, are the digests that the release signed. The registry
# tags must then give these digests. The release workflow sets them from build/release.env.
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
  # Only the files that are present: an operator often downloads one archive. At least one file
  # must be present, because a check of nothing passes. macOS has shasum, and not sha256sum.
  present=0
  while read -r _ name; do
    [ -f "$dir/$name" ] && present=$((present + 1))
  done < "$dir/checksums.txt"
  if [ "$present" = "0" ]; then
    echo "verify-release: $dir contains no file of checksums.txt" >&2
    exit 1
  fi
  sum=(sha256sum)
  command -v sha256sum > /dev/null || sum=(shasum -a 256)
  (cd "$dir" && "${sum[@]}" --check --ignore-missing checksums.txt)

  package="$dir/bk-agent-stack-orchard-$version.tgz"
  if [ -f "$package" ]; then
    cosign verify-blob "${identity[@]}" --bundle "$package.sigstore.json" "$package"
  fi
fi

digest_of() {
  docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' | jq -r .digest
}

index="$(digest_of "$image:$tag")"
if [ -n "${IMAGE_DIGEST:-}" ] && [ "$index" != "$IMAGE_DIGEST" ]; then
  echo "verify-release: $image:$tag is $index, and the release signed $IMAGE_DIGEST" >&2
  exit 1
fi
cosign verify "${identity[@]}" "$image@$index" > /dev/null
docker buildx imagetools inspect "$image@$index" --raw |
  jq -r '.manifests[] | select(.platform.os == "linux") | .digest' |
  while read -r digest; do
    cosign verify "${identity[@]}" "$image@$digest" > /dev/null
    cosign verify-attestation "${identity[@]}" --type spdxjson "$image@$digest" > /dev/null
  done

# The chart is an OCI artifact, not an image, so cosign resolves its tag. The signed payload names
# the digest that the signature covers.
chart_digest="$(cosign verify "${identity[@]}" --output json "$chart:$version" |
  jq -r '.[0].critical.image."docker-manifest-digest"')"
if [ -n "${CHART_DIGEST:-}" ] && [ "$chart_digest" != "$CHART_DIGEST" ]; then
  echo "verify-release: $chart:$version is $chart_digest, and the release signed $CHART_DIGEST" >&2
  exit 1
fi

echo "verify-release: $tag is signed by the release workflow of $repo"
