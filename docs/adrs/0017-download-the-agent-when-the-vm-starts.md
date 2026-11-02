---
status: proposed
date: 2026-11-03
decision-makers: maintainers
---

# ADR-0017: Download the agent when the VM starts

## Summary

Each job VM needs `buildkite-agent` v4. Until now, the image had to contain it, so the version of
the agent was a property of the image, and no upstream Tart image could serve a job: the upstream
base image carries agent v3. The decision is that the startup script downloads a pinned agent
release, checks its SHA-256, and runs it. A profile can turn this off and run the agent of its
image instead. The cost is one download of about 31 MB for each job, and a dependency on the
server that holds the release.

## Context

ADR-0012 builds the job VM images from a vanilla macOS image, and the image build installs the
Tart guest agent and `buildkite-agent` 4.9.0. The fleet operator supplies each image, and the
project publishes no image (owner decision, 2026-11-03). An operator can use an upstream Tart image
or build one.

A spike on 2026-11-03 checked the upstream images on a Mac with Softnet on, as the fleet runs them:

* `ghcr.io/cirruslabs/macos-golden-gate-vanilla` has no Tart guest agent, so the controller cannot
  run its startup script in it.
* `ghcr.io/cirruslabs/macos-golden-gate-base` has the Tart guest agent 0.15.0, passwordless `sudo`
  for `admin`, and `buildkite-agent` 3.138.2 from Homebrew, first on the `PATH` of the guest agent.
* The release archive `buildkite-agent-darwin-arm64-4.9.0.tar.gz` is 31.3 MB. In the VM, the
  download, the SHA-256 check, and the extraction took 1 to 2 seconds. A VM boot takes 30 to 55
  seconds.
* Buildkite publishes a `SHA256SUMS` file with each release, and does not sign it. The binary is ad
  hoc signed, and `curl` sets no quarantine attribute, so macOS runs it with no prompt.
* The downloaded agent read the configuration file of the Homebrew agent of the image. The
  environment of the startup script took precedence over the file.

ADR-0004 treats each build as untrusted, and the target design gives a VM no host directory.
ADR-0009 runs the startup script over the Tart guest agent.

## Goals and non-goals

Goals:

* **An upstream image with the guest agent can serve a job.** The operator does not have to build
  an image to try the controller.
* **The agent version is configuration.** A new agent does not need a new image.
* **The binary that runs is the binary that the operator pinned.** A changed file fails the job.
* **An image can still carry its own agent.** An operator with no access to the release server
  keeps a working option.
* **No new path from the VM to the host.**

Non-goals:

* A cache of the agent on the hosts. A cache needs a host directory or a service that the VMs can
  reach.
* A choice of agent version by the job. The profile decides.

## Options considered

**The agent in the image.** The current state. No network is needed at the start of a job.
However, the agent version and the image are one thing, and no upstream image works: the base
image has agent v3.

**Download at the start.** The startup script downloads the pinned archive, checks its SHA-256,
and runs the binary from a new temporary directory. Any image with the guest agent works, and the
agent version changes with the configuration. However, each job downloads about 31 MB, and the
release server, or a mirror that the operator sets, must be reachable from the VM.

**Mount the agent from the host.** A read-only Orchard host directory that holds only the agent.
No download, and one copy for each host. However, it gives the untrusted build a file-sharing path
to the host, which the target design forbids, and each host must hold the right binary.

| Goal | In the image | Download at the start | Mount from the host |
|---|---|---|---|
| An upstream image can serve a job | No | Yes | Yes |
| The agent version is configuration | No | Yes | Partly: each host holds a version |
| The pinned binary runs | Yes, if the image build checks it | Yes, the script checks the SHA-256 | Yes, if the host setup checks it |
| An image can carry its own agent | Yes | Yes, as an option | Yes, as an option |
| No new path from the VM to the host | Yes | Yes | No |

## Decision

Download the agent when the VM starts, and let a profile use the agent of its image instead.

* The configuration MUST have an agent source for each profile: `download` or `image`. The default
  is `download`.
* For `download`, the configuration MUST have an `https` URL of a release archive and the SHA-256
  of that archive. The controller MUST carry a default URL and SHA-256: the release that the
  controller release was tested with. The controller MUST refuse a URL that is not `https`, and a
  SHA-256 that is not 64 lower-case hexadecimal characters.
* For `download`, the startup script MUST download the archive to a new temporary directory, check
  its SHA-256, extract only `buildkite-agent`, and run that file. If a step fails, the script MUST
  stop before an agent starts, with a message that names the step.
* For `download`, the script MUST set the build, hooks, and plugins paths of the agent, so that the
  result does not depend on a configuration file in the image.
* For `image`, the script MUST run `buildkite-agent` from the `PATH`, and MUST stop with a message
  if its major version is not 4.
* The URL and the SHA-256 MUST travel in the environment of the startup script, not in its text.

## Consequences

Good:

* An upstream image with the Tart guest agent, such as the base image, can serve a job.
* An agent upgrade is a change of configuration, or of the controller default.
* A changed archive fails the job before an agent starts.
* The `image` source catches an image with agent v3 before the agent runs.

Bad:

* Each job downloads about 31 MB. At 1,000 jobs a day, that is about 31 GB a day from the release
  server, and a mirror is then the better choice.
* A job fails if the release server or the mirror is not reachable. A mirror in a private range
  also needs an entry in `softnetAllow`.
* The SHA-256 is the only anchor of trust, because Buildkite does not sign its checksums. The
  operator, or the controller release, pins it.
* The controller default and the pin of the image build are two values that change together.

## Confirmation

* A test of the VM specification builder confirms that each source gives its script, and that the
  URL and the SHA-256 are in the environment and not in the script.
* A test runs the script of each source in `sh` and `zsh`, with fake tools: a matching archive
  starts the downloaded agent with the build paths; a wrong SHA-256, a failed download, or an
  archive without the agent stops with a message and starts no agent; an image agent of v3 stops
  with a message.
* A configuration test confirms the default source, the default URL and SHA-256, a profile that
  overrides them, and the refusal of a URL that is not `https` or of a SHA-256 in the wrong form.
* A test on a real Tart VM confirms that a job runs on the upstream base image with the
  downloaded agent. It also confirms that a wrong SHA-256 fails the job before an agent acquires
  it, and that the job page shows the line of the script that names the failed check.
