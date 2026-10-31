#!/usr/bin/env bash
# Sign the image of a release, attest the SBOM of each platform image, and push and sign the chart.
#
# The release workflow runs this after GoReleaser pushed the image. Each signature is keyless:
# cosign exchanges the OIDC token of the workflow for a short-lived Sigstore certificate that names
# the workflow and the tag, so no signing key exists to protect (ADR-0013). It needs a registry
# login for docker and for helm.
#
# Usage: scripts/release-publish.sh <tag>    (for example v0.3.1)
set -euo pipefail

tag="${1:?usage: release-publish.sh <tag>}"
version="${tag#v}"
image="ghcr.io/joshwrightcodes/bk-agent-stack-orchard"
charts="ghcr.io/joshwrightcodes/charts"

root="$(git rev-parse --show-toplevel)"
cd "$root"

# Sign by digest, never by tag: a tag can move, a digest cannot. --recursive also signs the image
# of each platform under the index.
index="$(docker buildx imagetools inspect "$image:$tag" --format '{{json .Manifest}}' | jq -r .digest)"
cosign sign --yes --recursive "$image@$index"

# One SBOM for each platform image, because each one has its own base packages. The attestation
# binds the SBOM to the digest and signs it, which an SBOM that only sits next to the image does
# not do.
mkdir -p build/sbom
docker buildx imagetools inspect "$image@$index" --raw |
  jq -r '.manifests[] | select(.platform.os == "linux") | "\(.digest) \(.platform.architecture)"' |
  while read -r digest arch; do
    sbom="build/sbom/image-linux-$arch.spdx.json"
    syft scan "registry:$image@$digest" --output "spdx-json=$sbom"
    cosign attest --yes --type spdxjson --predicate "$sbom" "$image@$digest"
  done

package="build/chart/bk-agent-stack-orchard-$version.tgz"
digest="$(helm push "$package" "oci://$charts" 2>&1 | tee /dev/stderr | awk '/^Digest:/ {print $2}')"
if [ -z "$digest" ]; then
  echo "release-publish: helm push gave no digest for $package" >&2
  exit 1
fi
cosign sign --yes "$charts/bk-agent-stack-orchard@$digest"
