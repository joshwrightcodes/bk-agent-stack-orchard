# Install the controller

To try the controller on one Mac first, follow the [quick start](quickstart.md).

This guide tells you what the controller needs, how to install a release, and how to verify and
upgrade it. It does not tell you how to set up Buildkite, Orchard, or your Mac hosts: use the
setup that suits you.

## Before you start

You need these items:

* **A Buildkite cluster** with a self-hosted queue for the macOS jobs, and a cluster agent token
  for that queue. Use a queue that only this controller serves. Give the token an expiry and an
  IP allowlist.
* **Apple silicon Mac hosts** that run an Orchard worker and Tart 2.39.0 or later.
* **An Orchard controller**, version 0.58.0 or later, and an Orchard service account for this
  controller.
* **A Tart image for each profile.** The image must run the Tart guest agent. The upstream
  `ghcr.io/cirruslabs/macos-golden-gate-base` image does. By default, each VM downloads a pinned
  `buildkite-agent` release when it starts, so the image needs no agent, and the VMs must reach
  GitHub or a mirror that you set (see [The agent in each VM](#the-agent-in-each-vm)).
* **An OpenTelemetry collector** that receives OTLP, if you want metrics, traces, and logs.
* **A place to run the controller.** The examples below use Kubernetes and Helm. The controller is
  one binary, so other platforms work too.

The repository has examples for some of these items. They show one way to do it, and you do not
have to use them:

* [`deploy/orchard`](../../deploy/orchard/README.md): an Orchard controller on Kubernetes.
* [`deploy/hosts`](../../deploy/hosts/README.md): an Ansible playbook for the Mac hosts.
* [`images/`](../../images/README.md): a build of the job VM images.

## Install with Helm

Each release has a signed Helm chart in an OCI registry. The
[Releases page](https://github.com/joshwrightcodes/bk-agent-stack-orchard/releases) lists the
releases.

1. [Verify the release](#verify-a-release).
2. Create a namespace and a Secret. The keys of the Secret are environment variables of the
   controller. The chart does not create a Secret, so no secret goes through the values of a
   release. Read each value from your secret store, and do not write it to a file.

   ```sh
   kubectl create namespace bk-agent-stack-orchard
   kubectl --namespace bk-agent-stack-orchard create secret generic bk-agent-stack-orchard \
     --from-literal=BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN="$BUILDKITE_AGENT_TOKEN" \
     --from-literal=BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN="$ORCHARD_TOKEN"
   ```

   If Orchard uses a private certificate authority, also add
   `BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT`.

3. Write a values file. Set `config.controllerID`, `config.buildkite.queue`,
   `config.orchard.url`, and `secret.name`, and add at least one entry to `config.profiles`.
   `values.yaml` in the chart describes each value, and `values.schema.json` rejects a value that
   is not valid.

   A profile field that names an environment variable (`vmCredentials.usernameEnv`,
   `vmCredentials.passwordEnv`, and `otel.headersEnv`) must name a variable that starts with
   `VM_`. Add the variable with `extraEnv`. Only a `VM_` variable can go into a VM, because the
   controller pod holds other secrets.

4. Install the chart. Replace `<version>` with an exact version:

   ```sh
   helm install bk-agent-stack-orchard \
     oci://ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard --version <version> \
     --namespace bk-agent-stack-orchard --values values.yaml
   ```

The [chart README](../../deploy/helm/bk-agent-stack-orchard/README.md) describes the values, the
NetworkPolicy, and what to do when the Secret changes. Section 8 of the
[target design](../architecture/target-design.md) lists every configuration key and environment
variable.

## The agent in each VM

By default, the startup script of each VM downloads the `buildkite-agent` release that this
controller was tested with, checks its SHA-256, and runs it. A changed archive fails the job before
an agent starts, and the job page says which step failed.

To use another release, or your own mirror, set the URL and the SHA-256 of the archive together:

```yaml
defaults:
  agent:
    url: https://mirror.example.com/buildkite-agent-darwin-arm64-4.9.0.tar.gz
    sha256: "faaf0084edf08a92a877253620604fbd6ce496d2851a7ae5126dd19f27429e76"
```

Put the SHA-256 in quotes. YAML reads a value of digits, or of digits and one `e`, as a number,
and the controller then refuses it.

If the mirror has a private address, also add it to `softnetAllow` of each profile, because the
VMs cannot reach private ranges otherwise.

To use the agent of the image instead, for example when the VMs cannot reach GitHub, set
`agent.source: image` in the defaults or in one profile. The image must then contain
`buildkite-agent` v4 on the `PATH`. If it has another major version, the job fails with a message
that names the version.

Each download is about 31 MB. If your fleet runs many jobs a day, use a mirror.

### Git credential helpers in the image

Git Credential Manager writes two `credential.helper` lines in `~/.gitconfig`: an empty one, then
the manager. The upstream `macos-golden-gate-base` image has them. `buildkite-agent` v4.9.0 cannot
replace two helpers with its own, so a checkout with the repository credentials of Buildkite would
fail with `cannot overwrite multiple values with a single value`.

When the image has more than one helper, the startup script removes them before the agent starts,
and the event log of the VM says so. Git Credential Manager then does not run in the VM. A VM serves
one job, so the change does not outlast the job.

## Run the binary

Each release has archives for Linux (amd64 and arm64) and macOS (arm64). Download the archive for
your platform, [verify it](#verify-a-release), and start the controller with a configuration file:

```sh
bk-agent-stack-orchard -config config.yaml
```

Give the tokens in the environment variables above, not in the file.

## Build from source

You need [mise](https://mise.jdx.dev) and Docker.

```sh
mise install       # install the pinned tool versions
mise run build     # build the binary
mise run snapshot  # build the release archives and the image, without publishing them
```

The image is `goreleaser.ko.local` in your local Docker daemon. Push it to your registry, and set
`image.repository` and `image.digest` in the values of the chart.

## Verify a release

The release workflow of this repository signs each release with a keyless
[cosign](https://docs.sigstore.dev/cosign/) signature.

| Artifact | Where | Signature | SBOM |
|---|---|---|---|
| Archives | The GitHub release | `checksums.txt.sigstore.json` signs `checksums.txt`, which names each archive | `<archive>.sbom.json` (SPDX), in `checksums.txt` |
| Image | `ghcr.io/joshwrightcodes/bk-agent-stack-orchard`, tags `v<version>` and `<version>` | On the index and on each platform image | An attestation of type `spdxjson` on each platform image |
| Chart | `oci://ghcr.io/joshwrightcodes/charts/bk-agent-stack-orchard`, and the GitHub release | On the chart in the registry, and `<chart>.tgz.sigstore.json` on the release | None. The chart refers to the image by its digest |

To verify all the artifacts of a release, put the downloaded files in one directory, and run this
command. It needs cosign, docker with buildx, and jq.

```sh
scripts/verify-release.sh v0.1.0 ./downloads
```

To verify one artifact, require the identity of the release workflow at the tag:

```sh
cosign verify \
  --certificate-identity "https://github.com/joshwrightcodes/bk-agent-stack-orchard/.github/workflows/release.yml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/joshwrightcodes/bk-agent-stack-orchard:v0.1.0
```

## Versions and upgrades

The project uses [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). A release tag
has the form `vMAJOR.MINOR.PATCH`, for example `v0.3.1`. The binary, the container image, and the
Helm chart have the same version.

Until version 1.0:

* A minor release can contain a breaking change. The release notes identify each one, and tell
  you what to change.
* A patch release contains only fixes, and no breaking change.
* Pin an exact version or an image digest. Do not use a version range.

`CHANGELOG.md` lists the changes in each release. Read it before you upgrade.
