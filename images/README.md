# The images of the job VMs

`images/build.sh` builds the image of a job VM into a local Tart VM (W-23):

| Image | Base, pinned by digest | Use |
|---|---|---|
| `agent` | `ghcr.io/cirruslabs/macos-golden-gate-base` (macOS 27, the release of 2026-10-03) | A job that needs no Xcode, every end-to-end scenario of W-24 except the iOS build, and the real-VM tests of this repository |
| `pilot` | `ghcr.io/cirruslabs/macos-golden-gate-xcode:27` (macOS 27, Xcode 27 and its simulator runtimes) | The pilot profile (PR-026) |

Both bases come from [cirruslabs/macos-image-templates](https://github.com/cirruslabs/macos-image-templates).
The Xcode base is the agent base with Xcode 27 (`templates/xcode.pkr.hcl`), so the image needs no
Xcode build of its own. Each base carries Homebrew, the Tart guest agent, and `admin` with
passwordless sudo. The build adds:

| Part | Source |
|---|---|
| `buildkite-agent` 4.3.0 | Its release, checked against the SHA-256 of its `SHA256SUMS`, in `/usr/local/bin`. The `buildkite-agent@3` of the base, and its configuration, are removed |
| The configuration of the agent | `files/buildkite-agent.cfg`: the build, hooks, and plugins paths. The startup script of the controller gives the token, the job, the name, and the tags |
| The `environment` hook | `files/environment`: the artifacts of a job go to the owner-managed storage (PR-032) |

The controller starts `buildkite-agent start` in the VM through the Tart guest agent (ADR-0009),
which runs it as `admin`, with `/usr/local/bin` on its `PATH`.

## The artifact storage

The build takes the owner-managed storage in the operating country as `ARTIFACT_DESTINATION`, for
example `s3://<bucket>/<prefix>`. The hook sends the artifacts of each job to
`<destination>/<pipeline>/<build>/<job>`, and a step may name another place inside the same
storage. An empty destination is the storage of Buildkite for `buildkite-agent`, so an image built
without a destination sets one that the agent refuses: an artifact upload fails, and nothing
reaches Buildkite. `scripts/check-hook.sh` checks these cases.

## No well-known credentials over the network

The base image has the account `admin/admin`. The Tart guest agent runs in the session of the
automatic login of `admin`, and macOS 27 does not log in automatically after a change of the
password (2026-09-27: `sysadminctl -autologin set` gives `SACSetAutoLoginPassword error:22` and
exits 0, and a written `/etc/kcpassword` is ignored). The image therefore keeps the local password
and turns off every network path that would accept it: SSH (Remote Login) and Screen Sharing
(target design, section 9.2). The controller reaches the VM through the guest agent. A person who
debugs the VM uses the window of `tart run`, or `tart run --vnc-experimental`, because Tart serves
those screens itself. `tart run --vnc` does not work, because it needs Screen Sharing in the VM.

## Build

`build.sh` drives the VM with `tart exec`, through the guest agent, and not with SSH, so it needs
no password. It needs an Apple silicon Mac with Tart 2.40.1 or later: a macOS VM cannot run another
macOS VM, and a hosted GitHub macOS runner is a VM with 14 GB of disk (2026-10-10), so neither a
hosted runner nor the runner VM of this repository can build it.

The build shuts the VM down from inside, because `tart stop` does not wait for macOS to write its
caches, and a file written just before it is lost. It then starts the VM again and checks it as a
job VM finds it: the guest agent answers as `admin`, `buildkite-agent` 4.3.0 runs from
`/usr/local/bin`, the `buildkite-agent@3` of the base is gone, the hook sets the destination, ports
22 and 5900 are closed, and the pilot selects Xcode 27. A check that fails fails the build. The
ports are checked from the Mac, so the program that runs the build needs the Local Network
permission of macOS (System Settings › Privacy & Security › Local Network).

```sh
mise run agent-image   # about 10 minutes, with the base in the cache
ARTIFACT_DESTINATION=s3://<bucket>/<prefix> mise run pilot-image   # about 160 GB of disk
tart run bkaso-pilot   # look at it
```

The build makes the local Tart VM `bkaso-<image>` (`VM_NAME` changes it). With `IMAGE_REGISTRY`, for
example `ghcr.io/joshwrightcodes`, it pushes `<registry>/bkaso-<image>:<tag>` (`IMAGE_TAG`, the
commit by default), with the credentials in `TART_REGISTRY_USERNAME` and `TART_REGISTRY_PASSWORD`.
A profile names the image by the digest that the registry gives.

## What the image keeps

A job VM runs Buildkite jobs, and the pilot builds, tests, and publishes iOS and macOS apps, so the
build removes what the bases install for other uses (2026-10-10):

| Image | Removed | Kept |
|---|---|---|
| Both | The GitHub Actions runner (`~/actions-runner`) and `gitlab-runner`, the agents of other CI systems; `codex`, Claude Code, and Amazon Q | The build tools of the base: git, git-lfs, jq, yq, gh, Node, Ruby, mise, awscli, cmake, and the like |
| `pilot` | OpenJDK 17, the Android SDK, Flutter, `xcodes`, and their lines in `~/.zprofile` | Xcode 27 and its simulator runtimes, the Apple WWDR certificates, fastlane, CocoaPods, Carthage, Tuist, Mint, xcbeautify, xcpretty, SwiftLint, SwiftFormat, SwiftGen, LicensePlist, applesimutils, libimobiledevice, ios-deploy, GnuPG, ImageMagick |

The second boot checks that the removed tools are gone, and that the pilot still has fastlane,
CocoaPods, Carthage, xcbeautify, and SwiftLint.
