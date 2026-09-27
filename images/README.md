# The images of the job VMs

## `agent`: the agent image without Xcode

`images/agent` builds the image of a job VM with everything but Xcode (W-23, part 1). A job that
needs no Xcode runs on it, and so does every end-to-end scenario of W-24 except the iOS build.

| Part | Source |
|---|---|
| macOS Golden Gate, Homebrew, and the Tart guest agent | `ghcr.io/cirruslabs/macos-golden-gate-base`, pinned by digest |
| `buildkite-agent` 4.0.6 | Its release, checked against its SHA-256, in `/usr/local/bin`. The `buildkite-agent@3` (3.138.0) of the base image, and its configuration, are removed |
| The configuration of the agent | `files/buildkite-agent.cfg`: the build, hooks, and plugins paths. The startup script of the controller gives the token, the job, the name, and the tags |
| The `environment` hook | `files/environment`: the artifacts of a job go to the owner-managed storage (PR-032) |

The controller starts `buildkite-agent start` in the VM through the Tart guest agent (ADR-0009),
which runs it as `admin`, with `/usr/local/bin` on its `PATH`.

### The artifact storage

The build takes the owner-managed storage in the operating country as `artifact_destination`,
for example `s3://<bucket>/<prefix>`. The hook sends the artifacts of each job to
`<destination>/<pipeline>/<build>/<job>`, and a step may name another place inside the same
storage. An empty destination is the storage of Buildkite for `buildkite-agent`, so an image built
without a destination sets one that the agent refuses: an artifact upload fails, and nothing
reaches Buildkite. `scripts/check-hook.sh` checks these cases.

### No well-known credentials over the network

The base image has the account `admin/admin`. The Tart guest agent runs in the session of the
automatic login of `admin`, and macOS 27 does not log in automatically after a change of the
password (2026-09-27: `sysadminctl -autologin set` gives `SACSetAutoLoginPassword error:22` and
exits 0, and a written `/etc/kcpassword` is ignored). The image therefore keeps the local password
and turns off every network path that would accept it: SSH (Remote Login) and Screen Sharing
(target design, section 9.2). The controller reaches the VM through the guest agent. A person who
debugs the VM uses the window of `tart run`, or `tart run --vnc-experimental`, because Tart serves
those screens itself. `tart run --vnc` does not work, because it needs Screen Sharing in the VM.

### Build

`build.sh` drives the VM with `tart exec`, through the guest agent, and not with SSH. Tart is the
only program that reaches the VM, so the build needs no Local Network permission of macOS and no
password. It needs an Apple silicon Mac itself: a macOS VM cannot run another macOS VM, so neither
a hosted macOS runner of GitHub nor the runner VM of this repository can build it.

The build shuts the VM down from inside, because `tart stop` does not wait for macOS to write its
caches, and a file written just before it is lost. It then starts the VM again and checks it as a
job VM finds it: the guest agent answers as `admin`, `buildkite-agent` 4.0.6 runs from
`/usr/local/bin`, the `buildkite-agent@3` of the base image is gone, the hook sets the destination,
and ports 22 and 5900 are closed. A check that fails fails the build.

About 10 minutes on an Apple silicon Mac that has the base image, with a 50 GB disk:

```sh
ARTIFACT_DESTINATION=s3://<bucket>/<prefix> mise run agent-image
tart run bkaso-agent   # look at it
```

The build makes the local Tart VM `bkaso-agent`. The push to the registry of the fleet, and the
image with Xcode, are part 2 of W-23.
