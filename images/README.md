# The images of the job VMs

`images/build.sh` builds the image of a job VM into a local Tart VM (W-23):

| Image | Use |
|---|---|
| `agent` | A job that needs no Xcode, every end-to-end scenario of W-24 except the iOS build, and the real-VM tests of this repository |
| `pilot` | The pilot profile (PR-026): build, test, sign, and publish an iOS or macOS app with Xcode 27 |

Both start from `ghcr.io/cirruslabs/macos-golden-gate-vanilla`, pinned by digest: macOS 27 from
the restore image of Apple, with the setup assistant done, `admin` with passwordless sudo,
automatic login, and SSH, and no Homebrew, no tools, and no Tart guest agent
([cirruslabs/macos-image-templates](https://github.com/cirruslabs/macos-image-templates),
`templates/vanilla-golden-gate.pkr.hcl`). Each image holds only what its list names (ADR-0012).
The build adds:

| Part | Image | Source |
|---|---|---|
| The Tart guest agent 0.15.0 | Both | Its release, checked against its SHA-256, in `/usr/local/bin`, with the launchd jobs of `files/tart-guest-daemon.plist` and `files/tart-guest-agent.plist`. The build puts it in place over SSH, which it turns off later |
| The Command Line Tools | `agent` | `softwareupdate`, which gives git and the compilers of Apple |
| Xcode 27.0 | `pilot` | The archive that the owner downloads (below), as `/Applications/Xcode.app`, with its license accepted and its first launch done |
| The iOS simulator runtime | `pilot` | `xcodebuild -downloadPlatform iOS`, from Apple, with no Apple ID |
| Homebrew 7.0.9 | `pilot` | The package of its release, checked against its SHA-256 |
| carthage, cocoapods, fastlane, swiftlint, xcbeautify | `pilot` | Homebrew formulae, at the versions that Homebrew gives on the day of the build |
| `buildkite-agent` 4.3.0 | Both | Its release, checked against the SHA-256 of its `SHA256SUMS`, in `/usr/local/bin` |
| The configuration of the agent | Both | `files/buildkite-agent.cfg`: the build, hooks, and plugins paths. The startup script of the controller gives the token, the job, the name, and the tags |
| The `environment` hook | Both | `files/environment`: the artifacts of a job go to the owner-managed storage (PR-032). In the pilot, it also puts Homebrew on the `PATH` and sets a UTF-8 locale |

The controller starts `buildkite-agent start` in the VM through the Tart guest agent (ADR-0009),
which runs it as `admin`, with `/usr/local/bin` but not Homebrew on its `PATH`. The hook adds
Homebrew for each job.

## The Xcode archive

Apple gives the Xcode archive only to a person who signs in with an Apple ID, so a build never
signs in to Apple: it reads a copy of the archive, checks it against `XCODE_SHA256` in `build.sh`,
and checks with `pkgutil` that Apple signed it (`xip` does not check the signature). For each new
Xcode, the owner:

1. Downloads `Xcode_<version>.xip` from <https://developer.apple.com/download/all/>.
2. Puts it in `~/XcodesCache/Xcode_<version>.xip` on the build Mac, or in the owner-managed
   storage, from which a build gets it through `XCODE_URL`, an HTTPS URL such as a presigned URL.
3. Sets `XCODE_VERSION` and `XCODE_SHA256` in `build.sh` (`shasum -a 256` of the archive).

`XCODE_ARCHIVE` names another place on the build Mac. The build expands the archive on the Mac,
because `xip` in a VM ends before the app is in place (2026-10-10). It packs Xcode into one `ditto`
archive, because the shared directory of a VM does not follow some symbolic links of Xcode, and the
VM unpacks it from a read-only directory of the Mac, so no archive stays in the image. Keep the archive and the pilot
image private: the license of Xcode limits who may receive it.

## The artifact storage

The build takes the owner-managed storage in the operating country as `ARTIFACT_DESTINATION`, for
example `s3://<bucket>/<prefix>`. The hook sends the artifacts of each job to
`<destination>/<pipeline>/<build>/<job>`, and a step may name another place inside the same
storage. An empty destination is the storage of Buildkite for `buildkite-agent`, so an image built
without a destination sets one that the agent refuses: an artifact upload fails, and nothing
reaches Buildkite. `scripts/check-hook.sh` checks these cases.

## No well-known credentials over the network

The vanilla image has the account `admin/admin`. The Tart guest agent runs in the session of the
automatic login of `admin`, and macOS 27 does not log in automatically after a change of the
password (2026-09-27: `sysadminctl -autologin set` gives `SACSetAutoLoginPassword error:22` and
exits 0, and a written `/etc/kcpassword` is ignored). The image therefore keeps the local password
and turns off every network path that would accept it: SSH (Remote Login) and Screen Sharing
(target design, section 9.2). The controller reaches the VM through the guest agent. A person who
debugs the VM uses the window of `tart run`, or `tart run --vnc-experimental`, because Tart serves
those screens itself. `tart run --vnc` does not work, because it needs Screen Sharing in the VM.

## Build

`build.sh` drives the VM with `tart exec`, through the guest agent. It uses SSH once, with the
password of the vanilla image, to put the guest agent in place. It needs an Apple silicon Mac with
Tart 2.40.1 or later: a macOS VM cannot run another macOS VM, and a hosted GitHub macOS runner is
a VM with 14 GB of disk (2026-10-10), so neither a hosted runner nor the runner VM of this
repository can build it.

The build shuts the VM down from inside, because `tart stop` does not wait for macOS to write its
caches, and a file written just before it is lost. It then starts the VM again, without the
directory of the archive, and checks it as a job VM finds it: the guest agent answers as `admin`,
`buildkite-agent` 4.3.0 runs from `/usr/local/bin`, the hook sets the destination, ports 22 and 5900
are closed, and the image holds only what its list names. The pilot also selects Xcode 27, has an
iOS simulator runtime, gives a job fastlane, CocoaPods, Carthage, xcbeautify, and SwiftLint through
the hook, and has 20 GB free. A check that fails fails the build. The ports are checked from the
Mac, so the program that runs the build needs the Local Network permission of macOS (System
Settings › Privacy & Security › Local Network).

```sh
mise run agent-image   # about 5 minutes, with the base in the cache
ARTIFACT_DESTINATION=s3://<bucket>/<prefix> mise run pilot-image   # about 20 minutes, 59 GB
tart run bkaso-pilot   # look at it
```

The build makes the local Tart VM `bkaso-<image>` (`VM_NAME` changes it). With `IMAGE_REGISTRY`, for
example `ghcr.io/joshwrightcodes`, it pushes `<registry>/bkaso-<image>:<tag>` (`IMAGE_TAG`, the
commit by default), with the credentials in `TART_REGISTRY_USERNAME` and `TART_REGISTRY_PASSWORD`.
A profile names the image by the digest that the registry gives.

## What the image holds

Each image holds only what this list names. The second boot lists each place that software installs
itself, and a name that the list lacks fails the build, so a tool that a new vanilla image adds
cannot reach a job VM unseen (ADR-0012). The list is in `build.sh` (`ALLOWED_*`):

| Place | `agent` | `pilot` |
|---|---|---|
| `/Applications` | `Safari.app` and `Utilities`, which come with macOS | Also `Xcode.app` |
| `/Library/LaunchDaemons` | `org.cirruslabs.tart-guest-daemon.plist` | The same |
| `/Library/LaunchAgents` | `org.cirruslabs.tart-guest-agent.plist` | The same |
| `~/Library/LaunchAgents` | Nothing | Nothing |
| `/usr/local/bin` | `buildkite-agent` and `tart-guest-agent` | The same |
| Homebrew formulae that were asked for (`brew leaves --installed-on-request`) | None: no Homebrew | carthage, cocoapods, fastlane, swiftlint, xcbeautify |
| Homebrew casks | None | None |

The formulae bring their own dependencies, such as Ruby, which the list does not name. The check
also fails when a job finds the agent of another CI system, an AI assistant, Java, Android, Flutter,
or `xcodes`. A job that needs another tool installs it in its own steps, or the list grows by a
change of this file and of `build.sh`.
