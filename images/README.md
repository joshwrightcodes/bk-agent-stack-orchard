# The images of the job VMs

`images/build.sh` builds the image of a job VM into a local Tart VM (W-23):

| Image | Base, pinned by digest | Use |
|---|---|---|
| `agent` | `ghcr.io/cirruslabs/macos-golden-gate-vanilla` (macOS 27 from the restore image of Apple, with the setup assistant done) | A job that needs no Xcode, every end-to-end scenario of W-24 except the iOS build, and the real-VM tests of this repository |
| `pilot` | `ghcr.io/cirruslabs/macos-golden-gate-xcode:27` (macOS 27, Xcode 27 and its simulator runtimes), for now | The pilot profile (PR-026) |

Both bases come from [cirruslabs/macos-image-templates](https://github.com/cirruslabs/macos-image-templates).
ADR-0012 decides that each image builds from a vanilla macOS image, with only what a list names.
The agent image does that now. The pilot image builds on the Xcode base until its build from the
vanilla image works, because that build needs an Xcode archive that the owner downloads with an
Apple ID.

The vanilla image has `admin` with passwordless sudo, automatic login, and SSH, and no Homebrew,
no tools, and no Tart guest agent. The build adds:

| Part | Image | Source |
|---|---|---|
| The Tart guest agent 0.15.0 | `agent` | Its release, checked against its SHA-256, in `/usr/local/bin`, with the launchd jobs of `files/tart-guest-daemon.plist` and `files/tart-guest-agent.plist`. The build puts it in place over SSH, which it turns off later. The Xcode base has its own |
| The Command Line Tools | `agent` | `softwareupdate`, which gives git and the compilers of Apple |
| `buildkite-agent` 4.3.0 | Both | Its release, checked against the SHA-256 of its `SHA256SUMS`, in `/usr/local/bin`. The `buildkite-agent@3` of the Xcode base, and its configuration, are removed |
| The configuration of the agent | Both | `files/buildkite-agent.cfg`: the build, hooks, and plugins paths. The startup script of the controller gives the token, the job, the name, and the tags |
| The `environment` hook | Both | `files/environment`: the artifacts of a job go to the owner-managed storage (PR-032) |

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

`build.sh` drives the VM with `tart exec`, through the guest agent. Only the agent image uses SSH,
once, with the password of the vanilla image, to put the guest agent in place. It needs an Apple silicon Mac with Tart 2.40.1 or later: a macOS VM cannot run another
macOS VM, and a hosted GitHub macOS runner is a VM with 14 GB of disk (2026-10-10), so neither a
hosted runner nor the runner VM of this repository can build it.

The build shuts the VM down from inside, because `tart stop` does not wait for macOS to write its
caches, and a file written just before it is lost. It then starts the VM again and checks it as a
job VM finds it: the guest agent answers as `admin`, `buildkite-agent` 4.3.0 runs from
`/usr/local/bin`, no `buildkite-agent@3` remains, the hook sets the destination, ports 22 and 5900
are closed, the agent image holds only what its list names, and the pilot selects Xcode 27. A check that fails fails the build. The
ports are checked from the Mac, so the program that runs the build needs the Local Network
permission of macOS (System Settings › Privacy & Security › Local Network).

```sh
mise run agent-image   # about 5 minutes, with the base in the cache
ARTIFACT_DESTINATION=s3://<bucket>/<prefix> mise run pilot-image   # about 160 GB of disk
tart run bkaso-pilot   # look at it
```

The build makes the local Tart VM `bkaso-<image>` (`VM_NAME` changes it). With `IMAGE_REGISTRY`, for
example `ghcr.io/joshwrightcodes`, it pushes `<registry>/bkaso-<image>:<tag>` (`IMAGE_TAG`, the
commit by default), with the credentials in `TART_REGISTRY_USERNAME` and `TART_REGISTRY_PASSWORD`.
A profile names the image by the digest that the registry gives.

## What the image holds

The agent image holds only what this list names. The second boot lists each place that software
installs itself, and a name that the list lacks fails the build, so a tool that a new vanilla image
adds cannot reach a job VM unseen (ADR-0012). The list is in `build.sh` (`ALLOWED_*`):

| Place | The agent image holds |
|---|---|
| `/Applications` | `Safari.app` and `Utilities`, which come with macOS |
| `/Library/LaunchDaemons` | `org.cirruslabs.tart-guest-daemon.plist` |
| `/Library/LaunchAgents` | `org.cirruslabs.tart-guest-agent.plist` |
| `~/Library/LaunchAgents` | Nothing |
| `/usr/local/bin` | `buildkite-agent` and `tart-guest-agent` |
| `/opt/homebrew` | Nothing: the image has no Homebrew |

It also checks that `git` comes from the Command Line Tools. A job that needs another tool installs
it in its own steps, or the list grows by a change of this file and of `build.sh`.

The pilot image builds on the Xcode base for now, so the build removes what that base installs for
other uses (2026-10-10):

| Removed | Kept |
|---|---|
| The GitHub Actions runner (`~/actions-runner`) and `gitlab-runner`, the agents of other CI systems; `codex`, Claude Code, and Amazon Q; OpenJDK 17, the Android SDK, Flutter, `xcodes`, and their lines in `~/.zprofile` | Xcode 27 and its simulator runtimes, the Apple WWDR certificates, the build tools of the base (git, git-lfs, jq, yq, gh, Node, Ruby, mise, awscli, cmake, and the like), fastlane, CocoaPods, Carthage, Tuist, Mint, xcbeautify, xcpretty, SwiftLint, SwiftFormat, SwiftGen, LicensePlist, applesimutils, libimobiledevice, ios-deploy, GnuPG, ImageMagick |

The second boot checks that the removed tools are gone, and that the pilot still has fastlane,
CocoaPods, Carthage, xcbeautify, and SwiftLint.
