---
status: accepted
date: 2026-09-19
decision-makers: Josh Wright
---

# ADR-0009: Run VM startup scripts over the Tart guest agent

## Summary

The Orchard worker runs a startup script in each job VM, and this script starts the Buildkite
agent. Until now the worker reached the VM through SSH, so each VM specification contained a
username and a password. Orchard 0.57.0 adds a second transport that uses the Tart guest agent.
The decision is to use the guest-agent transport. The cost is a new image requirement: each image
must run the Tart guest agent.

## Context

The controller creates one VM for each job and gives it a startup script (ADR-0001). The script
receives the job acquisition token in its environment, and it starts `buildkite-agent` with
`--acquire-job`. The worker, not the controller, runs the script.

With the SSH transport, the VM specification must contain a username and a password for the image.
The controller reads these values from its environment and writes them into each VM specification.
Orchard service accounts with the `compute:read` role can read VM specifications, so the image
credentials have a larger audience than the job. The credentials are also long-lived, because they
belong to the image and not to the job. This conflicts with the rule that a VM gets only a
credential for its own job (ADR-0004, PR-030). Images that keep the well-known `admin/admin`
credentials make the problem worse.

Orchard 0.57.0 (2026-09-15) adds the field `startup_script.transport` with the values `ssh` (the
default) and `tart-guest-agent`. With the guest-agent transport, the worker connects to the guest
agent through the VM control socket and starts a login shell. The transport operates only on the
Tart runtime. The worker builds the same script for both transports: `set -e`, one `export` line
for each environment entry, then the script content. The event log of the VM is also the same, so
the acquire-timeout procedure that reads the last log lines does not change.

The guest agent is a separate program in the guest. The base template of
`cirruslabs/macos-image-templates` installs it from the `openai/tools` Homebrew tap and adds its
launch agent (as of 2026-09-19). Images that the fleet builds from this template therefore have
the agent. An older image, or an image from a different source, can have no guest agent.

## Goals and non-goals

Goals:

* **No standing credential in a VM specification.** A VM specification contains only a credential
  for its own job (PR-030, SE-02).
* **No image credential management.** The fleet operator does not create, store, or rotate image
  credentials (OP-01).
* **Same failure information.** The startup-script log stays available for the acquire timeout and
  for the job failure message (PR-008).
* **Reversibility.** A profile can return to SSH without a change to the controller.

Non-goals:

* The contents of the startup script.
* The build of the VM images.
* The network policy of the VM, which Softnet sets (ADR-0004).

## Options considered

**SSH transport.** The worker opens an SSH session with the username and the password of the VM
specification. This transport is the Orchard default, and it operates with every image. However,
each VM specification carries image credentials that outlive the job, and every reader of the VM
resource can see them.

**Tart guest agent transport.** The worker connects to the guest agent through the VM control
socket. The VM specification carries no credentials, and the VM needs no SSH server and no
password. However, the image must run the guest agent, and the transport needs Orchard 0.57.0 or
later.

**SSH transport with a credential for each VM.** The controller generates a password for each VM
and writes it into the specification and into the guest with a cloud-init-like mechanism. The
credential then has the life of the job. However, macOS images have no such mechanism, so the
image must contain a program that reads the password. This is more work than the guest agent, and
the credential is still in the specification.

| Goal | SSH | Tart guest agent | SSH with a credential for each VM |
|---|---|---|---|
| No standing credential in a VM specification | No | Yes | Partly |
| No image credential management | No | Yes | No |
| Same failure information | Yes | Yes | Yes |
| Reversibility | Yes | Yes | Partly |

## Decision

Use the `tart-guest-agent` transport for startup scripts. It is the only option that removes the
image credentials from the VM specification without new code in the image.

Each profile has a `startupTransport` value. The default is `tart-guest-agent`. A profile MAY
select `ssh` when its image does not run the guest agent. A profile with the SSH transport MUST
have VM credentials, and a profile with the guest-agent transport MUST NOT have them. Each image
for the guest-agent transport MUST run the Tart guest agent.

## Consequences

Good:

* VM specifications contain no image credentials, so a reader with `compute:read` sees only the
  job acquisition token of that job.
* The fleet operator manages no image passwords.
* The VM needs no SSH server for the startup script.

Bad:

* Each image must run the Tart guest agent. An image without it needs the SSH transport.
* The minimum Orchard version is 0.57.0 for the workers and the controller.
* A guest agent that does not start gives a different failure than an SSH failure, and the
  acquire timeout is the only signal.

Review trigger:

* Review this decision if Orchard removes a transport, or if the fleet must use images that
  cannot run the guest agent.

## Confirmation

* Unit tests confirm that a generated VM specification with the guest-agent transport has no
  `username` and no `password`.
* Unit tests confirm that the configuration validation rejects a profile with the guest-agent
  transport and VM credentials, and a profile with the SSH transport and no credentials.
* The end-to-end scenarios run with the guest-agent transport and confirm that the agent starts.
