#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Prepare and sign the assets of a release, in phases that run in two jobs of the release
# workflow.
#
# vex runs in the build job after GoReleaser. govulncheck writes the OpenVEX document of the
# release: for each known vulnerability in a dependency, whether the code reaches it. A
# vulnerability that the code reaches stops the release (SECURITY.md). checksums.txt then lists
# the document, so the signature of checksums.txt covers it.
#
# build runs after GoReleaser pushed the image and wrote the archives, their SBOMs, and
# checksums.txt to dist. It makes an SBOM of each platform image with syft, and packages and pushes
# the chart with the digest of the image. Its job can push to the registry and do nothing else. It
# needs a registry login for docker and for helm.
#
# sign runs in a separate job that gets the files of build as an artifact. It installs only cosign
# and jq, so the tools that build the release never run next to the OIDC token. It signs
# checksums.txt, the image, the SBOM of each platform image, and the chart, and uploads the assets
# to the draft release. Each signature is keyless: cosign exchanges the OIDC token of the workflow
# for a short-lived Sigstore certificate that names the workflow and the tag, so no signing key
# exists to protect (ADR-0013). It needs a registry login for docker.
#
# Usage: scripts/release-publish.sh vex|build|sign <tag>    (for example: sign v0.3.1)
#
# build writes build/release.env with the digests of the image and the chart. sign signs these
# digests, and scripts/verify-release.sh checks that the registry tags still give them.
set -euo pipefail

usage() {
	echo "usage: release-publish.sh vex|build|sign <tag>" >&2
	exit 2
}

repo="joshwrightcodes/bk-agent-stack-orchard"
image="ghcr.io/joshwrightcodes/bk-agent-stack-orchard"
charts="ghcr.io/joshwrightcodes/charts"

is_digest() {
	[[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]]
}

# Write "<digest> <architecture>" for each platform image under the index to a file. It fails
# unless the index holds exactly linux/amd64 and linux/arm64, because a loop over an empty list
# signs and attests nothing and still passes. An index with only attestation manifests (os
# "unknown"), or a changed ko layout, would do that. The index is addressed by its digest, so its
# content cannot change between the two phases.
platforms() {
	local index="$1" out="$2"
	docker buildx imagetools inspect "$image@$index" --raw |
		jq -r '[.manifests[] | select(.platform.os == "linux")] | sort_by(.platform.architecture)
      | if map(.platform.architecture) == ["amd64", "arm64"]
        then .[] | "\(.digest) \(.platform.architecture)"
        else error("the image has the linux platforms \(map(.platform.architecture)), and exactly amd64 and arm64 are necessary")
        end' >"$out"
}

# The loop must have handled each platform once. jq checks the list before the loop, and this
# checks what the loop did.
check_handled() {
	if [ "$1" != " amd64 arm64" ]; then
		echo "release-publish: handled the platforms '$1', and exactly amd64 and arm64 are necessary" >&2
		exit 1
	fi
}

vex() {
	local name work affected
	name="bk-agent-stack-orchard_${tag#v}.openvex.json"
	work="$(mktemp)"
	govulncheck -format openvex ./... >"$work"
	affected="$(jq -r '[.statements[]? | select(.status == "affected") | .vulnerability.name] | join(", ")' "$work")"
	if [ -n "$affected" ]; then
		rm -f "$work"
		echo "release-publish: the code reaches $affected, so this release stops. Update the dependency first" >&2
		exit 1
	fi
	jq --arg author "the release workflow of $repo" '.author = $author' "$work" >"dist/$name"
	rm -f "$work"
	(cd dist && sha256sum "$name") >>dist/checksums.txt
}

build() {
	mkdir -p build/sbom build/chart
	rm -f build/sbom/* build/chart/* build/release.env

	# The digest that GoReleaser pushed, from its own record. A lookup of the tag in the registry
	# could give an image that another workflow pushed to the same tag after GoReleaser.
	local index
	index="$(jq -r '[.[] | select(.type == "Docker Manifest") | .extra.Digest] | unique | .[]' \
		dist/artifacts.json)"
	if ! is_digest "$index"; then
		echo "release-publish: dist/artifacts.json gives no single image digest: '$index'" >&2
		exit 1
	fi

	# One SBOM for each platform image, because each one has its own base packages. The sign phase
	# attests each one to the digest of its image.
	local list digest arch handled=""
	list="$(mktemp)"
	platforms "$index" "$list"
	while read -r digest arch; do
		syft scan "registry:$image@$digest" --output "spdx-json=build/sbom/image-linux-$arch.spdx.json"
		handled="$handled $arch"
	done <"$list"
	rm -f "$list"
	check_handled "$handled"

	# The released chart refers to the image by the digest above, never by a tag. release-please
	# wrote the version into Chart.yaml.
	local work values package chart_digest
	work="$(mktemp -d)"
	cp -R deploy/helm/bk-agent-stack-orchard "$work/"
	values="$work/bk-agent-stack-orchard/values.yaml"
	if [ "$(grep -c '^  digest: ""$' "$values")" != "1" ]; then
		echo "release-publish: values.yaml has no single empty image digest to set" >&2
		exit 1
	fi
	sed -i.bak "s|^  digest: \"\"$|  digest: \"$index\"|" "$values" && rm "$values.bak"
	helm package "$work/bk-agent-stack-orchard" --destination build/chart
	rm -rf "$work"
	package="$(ls build/chart/*.tgz)"
	chart_digest="$(helm push "$package" "oci://$charts" 2>&1 | tee /dev/stderr |
		awk '/^Digest:/ {print $2}')"
	if ! is_digest "$chart_digest"; then
		echo "release-publish: helm push gave no digest for $package" >&2
		exit 1
	fi

	printf 'IMAGE_DIGEST=%s\nCHART_DIGEST=%s\n' "$index" "$chart_digest" >build/release.env
}

sign() {
	# The file comes from the build job, so this reads the two values as text, never as code: code
	# from that job must not run next to the OIDC token.
	local image_digest chart_digest
	image_digest="$(sed -n 's/^IMAGE_DIGEST=//p' build/release.env)"
	chart_digest="$(sed -n 's/^CHART_DIGEST=//p' build/release.env)"
	if ! is_digest "$image_digest" || ! is_digest "$chart_digest"; then
		echo "release-publish: build/release.env has no single valid image and chart digest" >&2
		exit 1
	fi

	# The archives, the SBOMs, and the VEX document to upload are the files that checksums.txt names,
	# and each one must
	# match its checksum. A file that the list does not name stops the run here, before anything
	# is signed: scripts/release-finish.sh would refuse it later, after the signatures exist.
	local listed present
	(cd dist && sha256sum --check --strict checksums.txt)
	listed="$(awk '{print $2}' dist/checksums.txt | sort)"
	present="$(cd dist && printf '%s\n' *.tar.gz *.sbom.json *.openvex.json | sort)"
	if [ "$listed" != "$present" ]; then
		echo "release-publish: the archives, SBOMs, and VEX document in dist are not the files of checksums.txt" >&2
		diff <(printf '%s\n' "$listed") <(printf '%s\n' "$present") >&2 || true
		exit 1
	fi

	# One signature covers checksums.txt, so it covers each archive and each SBOM (ADR-0013).
	cosign sign-blob --yes --bundle dist/checksums.txt.sigstore.json dist/checksums.txt
	local files=() name
	while read -r name; do
		files+=("dist/$name")
	done <<<"$listed"
	# --clobber replaces an asset that a failed run left on the draft.
	gh release upload "$tag" "${files[@]}" dist/checksums.txt dist/checksums.txt.sigstore.json \
		--clobber

	# --recursive also signs the image of each platform under the index.
	cosign sign --yes --recursive "$image@$image_digest"

	# The attestation binds the SBOM of each platform image to its digest, and signs it.
	local list digest arch handled="" sbom
	list="$(mktemp)"
	platforms "$image_digest" "$list"
	while read -r digest arch; do
		sbom="build/sbom/image-linux-$arch.spdx.json"
		[ -f "$sbom" ] || {
			echo "release-publish: $sbom is missing" >&2
			exit 1
		}
		cosign attest --yes --type spdxjson --predicate "$sbom" "$image@$digest"
		handled="$handled $arch"
	done <"$list"
	rm -f "$list"
	check_handled "$handled"

	# The package on the release gets its own bundle, because checksums.txt exists before the chart
	# does. The copy in the registry gets a signature on its digest.
	local package
	package="$(ls build/chart/*.tgz)"
	cosign sign-blob --yes --bundle "$package.sigstore.json" "$package"
	cosign sign --yes "$charts/bk-agent-stack-orchard@$chart_digest"
	gh release upload "$tag" "$package" "$package.sigstore.json" --clobber
}

# Sourcing the file defines the functions and runs nothing else.
[[ "${BASH_SOURCE[0]}" != "$0" ]] && return 0

[ "$#" = "2" ] || usage
phase="$1"
tag="$2"

root="$(git rev-parse --show-toplevel)"
cd "$root"

case "$phase" in
vex) vex ;;
build) build ;;
sign) sign ;;
*) usage ;;
esac
