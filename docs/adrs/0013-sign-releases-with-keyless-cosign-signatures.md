---
status: proposed
date: 2026-10-31
decision-makers: Josh Wright
---

# ADR-0013: Sign releases with keyless cosign signatures

## Summary

The repository is public. A release publishes binary archives, a container image, and a Helm
chart, and an operator runs them in a cluster with Buildkite and Orchard credentials. Without a
signature, an operator cannot prove that an artifact came from this repository. Without an SBOM,
the operator cannot find a vulnerable dependency in it. The decision is to sign each release
artifact with a keyless cosign signature from the release workflow. Each archive and each
platform image also gets an SPDX SBOM. The cost is a dependency on the public Sigstore services at
release time, and a public record of each signature.

## Context

A release tag starts a workflow that builds the archives and the image with GoReleaser and ko, and
packages the chart. Before this decision, a release had only a checksum file. A checksum file
proves that a download is complete, but not who made it: a party that can change the release can
also change the checksums.

The controller holds a Buildkite cluster agent token and an Orchard service account token
(ADR-0008). A changed image gets both, and it can start VMs on the fleet. Operators deploy the
image and the chart into Kubernetes. There, an admission controller can refuse an image without a
valid signature, if it can read the signature format.

cosign (Sigstore) signs blobs and OCI artifacts. In keyless mode, it exchanges the OIDC token of
the GitHub Actions run for a certificate from Fulcio. The certificate is valid for minutes, and it
names the workflow file and the Git ref. cosign records the signature in the Rekor transparency
log. GoReleaser signs the checksum file with cosign through its `signs` section. Its `sboms`
section makes SPDX SBOMs with syft (GoReleaser documentation, 2026-10-31). cosign 3.1.4 and syft
1.55.0 are the current releases (2026-10-31).

## Goals and non-goals

Goals:

* **Provenance.** An operator can prove that each archive, image, and chart came from the release
  workflow of this repository at a given tag.
* **No secret to keep.** One owner operates the project, and no long-lived signing key exists to
  store, rotate, or lose (OP-01).
* **Admission control.** A Kubernetes admission controller can verify the image and the chart
  with tools that operators already use.
* **Dependency inventory.** Each archive and each platform image has an SBOM that a scanner
  can read, and a signature covers it.

Non-goals:

* Signatures on the job VM images. The fleet builds them (ADR-0012), and they are not release
  artifacts.
* Signed commits and tags, which the repository rules control.
* A SLSA level. The workflow gives provenance through the certificate identity, and a later
  decision can add a SLSA provenance attestation.

## Options considered

**Checksums only.** The release publishes `checksums.txt` and nothing more. It needs no tool and
no service. However, it gives no provenance, and an admission controller has nothing to verify.

**cosign with a key pair.** The owner makes a cosign key pair, stores the private key as a
repository secret, and publishes the public key. Verification works without a network service.
However, the key is a long-lived secret: a leak lets anyone sign as the project until the owner
sees it, and the owner must rotate it and publish each new public key.

**cosign, keyless.** The release workflow signs with a short-lived certificate for its own
identity. No key exists. The verification checks the identity: the workflow file and the exact
tag. However, the signature depends on Fulcio and Rekor at release time, and Rekor makes each
signature public.

**GitHub artifact attestations.** The `actions/attest-build-provenance` action writes a Sigstore
bundle that `gh attestation verify` checks. It is also keyless. However, the bundle stays in the
attestation store of GitHub unless the workflow also pushes it. Also, fewer admission controllers
read it than read a cosign signature.

| Goal | Checksums only | cosign, key pair | cosign, keyless | GitHub attestations |
|---|---|---|---|---|
| Provenance | No | Yes, to the holder of the key | Yes, to the workflow and the tag | Yes, to the workflow and the tag |
| No secret to keep | Yes | No | Yes | Yes |
| Admission control | No | Yes | Yes | Partly |
| Dependency inventory | No | Yes, with syft | Yes, with syft | Yes, with syft |

## Decision

Sign each release artifact with a keyless cosign signature from the release workflow, and publish
an SPDX SBOM for each archive and each platform image.

* syft makes an SPDX JSON SBOM for each archive. The checksum file covers the archives, their
  SBOMs, and the chart package, and one cosign bundle (`checksums.txt.sigstore.json`) signs the
  checksum file.
* cosign signs the image index and each platform image by digest. syft makes an SPDX JSON SBOM of
  each platform image, and cosign attaches it as a signed attestation of type `spdxjson`.
* The chart goes to `ghcr.io/joshwrightcodes/charts` as an OCI artifact, and cosign signs it by
  digest.
* The certificate identity MUST be the release workflow of this repository at the release tag,
  from the OIDC issuer of GitHub Actions. A verification MUST check both values.
* The release stays a draft until the workflow has verified every signature. The repository makes
  a published release immutable.

## Consequences

Good:

* An operator can verify an archive, the image, or the chart with one cosign command.
* An admission controller can refuse an image that the release workflow did not sign.
* No signing key exists, so no key can leak, and the owner rotates nothing.
* A scanner can read the dependencies of each platform image from a signed attestation.
* A changed artifact fails verification, because a new signature names another identity.

Bad:

* A release needs Fulcio and Rekor. During an outage of Sigstore, the release fails, and the owner
  runs the workflow again later.
* Each signature is in a public transparency log. The repository is public, so this discloses
  nothing new.
* The workflow file name is part of the identity. A rename of `release.yml` changes the identity
  that operators check.
* The release workflow has `id-token: write`, `packages: write`, and `contents: write`. It runs only
  for a `v*` tag, in an environment that needs the approval of the maintainer.

## Confirmation

* `scripts/verify-release.sh <tag> dist` passes in the release workflow before the release is
  published. It verifies the checksum bundle, each image signature, each SBOM attestation, and the
  chart signature against the identity of the release workflow at that tag.
* The snapshot gate in CI builds an SBOM for each archive and a checksum file that names the
  chart package.
* `.goreleaser.yaml` signs `checksums.txt` with cosign, and `mise.toml` pins cosign and syft.
