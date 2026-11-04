#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Verify the signatures of a release: the checksums and the archives, the chart package, the image,
# the SBOM of each platform image, and the chart in the registry. Also verify the SLSA build
# provenance of checksums.txt, the image, and the chart, which GitHub artifact attestations hold.
#
# The release workflow runs this before it publishes the draft release, and an operator can run it
# before a deployment. It needs cosign, docker with buildx, jq, curl, and gh with a login (gh auth
# login, or GH_TOKEN), because gh reads the attestations through the GitHub API. Each check
# requires a certificate for the release workflow of this repository at this exact tag, from the
# OIDC issuer of GitHub Actions, for the commit of the tag. That commit must be on main.
#
# The workflow file and its own "is the tag on main" check come from the tagged commit, so a tag on
# another commit would give signatures that pass the identity checks. The certificate also names
# the commit that the workflow ran on, and GitHub says whether main contains it. Neither comes from
# the tagged commit.
#
# Usage: scripts/verify-release.sh <tag> [<directory with checksums.txt>]
#
# IMAGE_DIGEST and CHART_DIGEST, when set, are the digests that the release signed. The registry
# tags must then give these digests. The release workflow sets them from build/release.env.
# GH_TOKEN or GITHUB_TOKEN, when set, authenticates the calls to the GitHub API, which raises its
# rate limit. Without a token the calls of curl are anonymous, which works for this public
# repository. gh uses the token too, or its own login.
set -euo pipefail

tag="${1:?usage: verify-release.sh <tag> [<directory>]}"
dir="${2:-}"
version="${tag#v}"
repo="joshwrightcodes/bk-agent-stack-orchard"
image="ghcr.io/$repo"
chart="ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard"

die() {
	echo "verify-release: $*" >&2
	exit 1
}

command -v gh >/dev/null || die "gh is necessary to verify the provenance attestations"

# github_api PATH ACCEPT: one call to the GitHub API. A token travels in the configuration of curl
# on standard input, never on its command line.
github_api() {
	local token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
	{
		printf 'header = "Accept: %s"\n' "$2"
		printf 'header = "X-GitHub-Api-Version: 2022-11-28"\n'
		if [ -n "$token" ]; then
			printf 'header = "Authorization: Bearer %s"\n' "$token"
		fi
	} | curl -fsS --retry 3 -K - "https://api.github.com/$1"
}

# The commit of the tag. Each certificate must name this commit (below), and main must contain it.
commit="$(github_api "repos/$repo/commits/refs/tags/$tag" application/vnd.github.sha)" ||
	die "could not get the commit of $tag from GitHub: check that the tag exists"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "GitHub gave no commit SHA for $tag: $commit"

# "behind" or "identical": main contains the commit. "ahead" or "diverged": it does not.
status="$(github_api "repos/$repo/compare/main...$commit?per_page=1" application/vnd.github+json |
	jq -r '.status // empty')" || status=""
case "$status" in
behind | identical) ;;
"") die "could not ask GitHub whether main contains $commit, the commit of $tag: stopping" ;;
*) die "$tag is the commit $commit, which is not on main ($status): do not use this release" ;;
esac

identity=(
	--certificate-identity "https://github.com/$repo/.github/workflows/release.yml@refs/tags/$tag"
	--certificate-oidc-issuer "https://token.actions.githubusercontent.com"
	--certificate-github-workflow-sha "$commit"
)

# The same identity for an attestation: the release workflow of this repository, which ran for the
# tag on its commit, on a runner that GitHub hosts. Without --bundle, gh reads the attestations
# from the GitHub API of this repository.
provenance=(
	--repo "$repo"
	--signer-workflow "$repo/.github/workflows/release.yml"
	--source-ref "refs/tags/$tag"
	--source-digest "$commit"
	--signer-digest "$commit"
	--deny-self-hosted-runners
)

# attested SUBJECT: SUBJECT (a file, or oci://<name>@<digest>) has a valid SLSA provenance
# attestation of the release workflow at the tag.
attested() {
	gh attestation verify "$1" "${provenance[@]}" >/dev/null ||
		die "$1 has no valid provenance attestation of the release workflow of $repo at $tag"
}

if [ -n "$dir" ]; then
	cosign verify-blob "${identity[@]}" \
		--bundle "$dir/checksums.txt.sigstore.json" "$dir/checksums.txt"
	attested "$dir/checksums.txt"
	# The release also carries the bundle of that attestation. gh checks it offline, with the same
	# identity, so a bundle on the release that is not the attestation of the workflow fails here.
	if [ -f "$dir/checksums.txt.intoto.jsonl" ]; then
		gh attestation verify "$dir/checksums.txt" --bundle "$dir/checksums.txt.intoto.jsonl" \
			"${provenance[@]}" >/dev/null ||
			die "$dir/checksums.txt.intoto.jsonl is not a valid provenance attestation of the release workflow of $repo at $tag"
	fi
	# Only the files that are present: an operator often downloads one archive. At least one file
	# must be present, because a check of nothing passes. macOS has shasum, and not sha256sum.
	present=0
	while read -r _ name; do
		[ -f "$dir/$name" ] && present=$((present + 1))
	done <"$dir/checksums.txt"
	if [ "$present" = "0" ]; then
		echo "verify-release: $dir contains no file of checksums.txt" >&2
		exit 1
	fi
	sum=(sha256sum)
	command -v sha256sum >/dev/null || sum=(shasum -a 256)
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
cosign verify "${identity[@]}" "$image@$index" >/dev/null
attested "oci://$image@$index"
# Exactly the two platforms of the release, because a loop over no platform checks nothing and
# passes. No mapfile: an operator can run this with the bash of macOS (3.2).
platforms='[.manifests[] | select(.platform.os == "linux")] | sort_by(.platform.architecture)
  | if map(.platform.architecture) == ["amd64", "arm64"] then .[].digest
    else error("got \(map("linux/" + .platform.architecture))") end'
digests="$(docker buildx imagetools inspect "$image@$index" --raw | jq -r "$platforms")" ||
	die "$image@$index must have exactly the platforms linux/amd64 and linux/arm64"
verified=0
for digest in $digests; do
	cosign verify "${identity[@]}" "$image@$digest" >/dev/null
	cosign verify-attestation "${identity[@]}" --type spdxjson "$image@$digest" >/dev/null
	verified=$((verified + 1))
done
[ "$verified" = "2" ] || die "verified $verified platform images of $image@$index, and want 2"

# The chart is an OCI artifact, not an image, so cosign resolves its tag. The signed payload names
# the digest that the signature covers.
chart_digest="$(cosign verify "${identity[@]}" --output json "$chart:$version" |
	jq -r '.[0].critical.image."docker-manifest-digest"')"
if [ -n "${CHART_DIGEST:-}" ] && [ "$chart_digest" != "$CHART_DIGEST" ]; then
	echo "verify-release: $chart:$version is $chart_digest, and the release signed $CHART_DIGEST" >&2
	exit 1
fi
[[ "$chart_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "the signature of $chart:$version names no digest"
attested "oci://$chart@$chart_digest"

echo "verify-release: $tag is commit $commit on main, signed and attested by the release workflow"
echo "verify-release: install the chart by its digest: oci://$chart@$chart_digest"
