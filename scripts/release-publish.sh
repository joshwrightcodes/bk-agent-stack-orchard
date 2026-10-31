#!/usr/bin/env bash
# Sign the image of a release, attest the SBOM of each platform image, and package, sign, and push
# the chart.
#
# The release workflow runs this after GoReleaser pushed the image and uploaded the archives to the
# draft release. Each signature is keyless: cosign exchanges the OIDC token of the workflow for a
# short-lived Sigstore certificate that names the workflow and the tag, so no signing key exists to
# protect (ADR-0013). It needs a registry login for docker and for helm.
#
# Usage: scripts/release-publish.sh <tag>    (for example v0.3.1)
#
# It writes build/release.env with the digests that it signed, for scripts/verify-release.sh.
set -euo pipefail

tag="${1:?usage: release-publish.sh <tag>}"
image="ghcr.io/joshwrightcodes/bk-agent-stack-orchard"
charts="ghcr.io/joshwrightcodes/charts"

root="$(git rev-parse --show-toplevel)"
cd "$root"
mkdir -p build/sbom build/chart

# The digest that GoReleaser pushed, from its own record. A lookup of the tag in the registry could
# give an image that another workflow pushed to the same tag after GoReleaser.
index="$(jq -r '[.[] | select(.type == "Docker Manifest") | .extra.Digest] | unique | .[]' \
  dist/artifacts.json)"
if ! [[ "$index" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "release-publish: dist/artifacts.json gives no single image digest: '$index'" >&2
  exit 1
fi

# --recursive also signs the image of each platform under the index.
cosign sign --yes --recursive "$image@$index"

# One SBOM for each platform image, because each one has its own base packages. The attestation
# binds the SBOM to the digest and signs it.
docker buildx imagetools inspect "$image@$index" --raw |
  jq -r '.manifests[] | select(.platform.os == "linux") | "\(.digest) \(.platform.architecture)"' |
  while read -r digest arch; do
    sbom="build/sbom/image-linux-$arch.spdx.json"
    syft scan "registry:$image@$digest" --output "spdx-json=$sbom"
    cosign attest --yes --type spdxjson --predicate "$sbom" "$image@$digest"
  done

# The released chart refers to the image by the digest above, never by a tag. release-please wrote
# the version into Chart.yaml.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -R deploy/helm/bk-agent-stack-orchard "$work/"
values="$work/bk-agent-stack-orchard/values.yaml"
if [ "$(grep -c '^  digest: ""$' "$values")" != "1" ]; then
  echo "release-publish: values.yaml has no single empty image digest to set" >&2
  exit 1
fi
sed -i.bak "s|^  digest: \"\"$|  digest: \"$index\"|" "$values" && rm "$values.bak"
rm -f build/chart/*
helm package "$work/bk-agent-stack-orchard" --destination build/chart
package="$(ls build/chart/*.tgz)"

# The package on the release gets its own bundle, because the checksums of GoReleaser exist before
# the chart does. The copy in the registry gets a signature on its digest.
cosign sign-blob --yes --bundle "$package.sigstore.json" "$package"
digest="$(helm push "$package" "oci://$charts" 2>&1 | tee /dev/stderr | awk '/^Digest:/ {print $2}')"
if ! [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "release-publish: helm push gave no digest for $package" >&2
  exit 1
fi
cosign sign --yes "$charts/bk-agent-stack-orchard@$digest"
gh release upload "$tag" "$package" "$package.sigstore.json" --clobber

printf 'IMAGE_DIGEST=%s\nCHART_DIGEST=%s\n' "$index" "$digest" > build/release.env
