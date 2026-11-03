# The images of the job VMs

`images/build.sh` builds the image of a job VM into a local Tart VM. It is a reference build: the
project publishes no image. An operator uses an upstream Tart image, or builds an image of its own,
and the image meets the image requirements of the target design, section 6.4. This build is one
way to meet them, and the project uses it for its own tests:

| Image | Use |
|---|---|
| `agent` | A job that needs no Xcode, every end-to-end test except the iOS build, and the real-VM tests of this repository |
| `pilot` | The pilot profile: build, test, sign, and publish an iOS or macOS app with Xcode 27 |

Both start from `ghcr.io/cirruslabs/macos-golden-gate-vanilla`, pinned by digest. This base image
is macOS 27.2 from the restore image of Apple, with the setup assistant finished. It has `admin`
with passwordless sudo, automatic login, and SSH. It has no Homebrew, no tools, and no Tart guest
agent ([cirruslabs/macos-image-templates](https://github.com/cirruslabs/macos-image-templates),
`templates/vanilla-golden-gate.pkr.hcl`). Each image holds only what its list names.
The build adds:

| Part | Image | Source |
|---|---|---|
| The Tart guest agent 0.15.0 | Both | Its release, checked against its SHA-256, in `/usr/local/bin`, with the launchd jobs of `files/tart-guest-daemon.plist` and `files/tart-guest-agent.plist`. The build puts it in place over SSH, which the build disables later |
| The Command Line Tools | `agent` | `softwareupdate`, which gives git and the compilers of Apple |
| Xcode 27.2 | `pilot` | The archive that the owner downloads (below), as `/Applications/Xcode.app`, with its license accepted and its first launch done |
| The iOS simulator runtime | `pilot` | `xcodebuild -downloadPlatform iOS`, from Apple, with no Apple ID |
| Homebrew 7.0.11 | `pilot` | The package of its release, checked against its SHA-256 |
| carthage, cocoapods, fastlane, swiftlint, xcbeautify | `pilot` | Homebrew formulae, at the versions that Homebrew gives on the day of the build |
| `buildkite-agent` 4.9.0 | Both | Its release, checked against the SHA-256 of its `SHA256SUMS`, in `/usr/local/bin`. The controller runs it only for a profile with `agent.source: image`; by default, each VM downloads the same release when it starts |
| The configuration of the agent | Both | `files/buildkite-agent.cfg`: the build, hooks, and plugins paths. The startup environment of the controller gives the token, the job, the name, and the tags |
| The agent tags of the image | Both | `/usr/local/etc/bk-agent-stack-orchard/agent-tags`: `image-name`, `image-tag`, `macos`, and `macos-build`, and in the pilot `xcode` and `xcode-build`. The build reads the values in the VM after each install, and its check reads them again |
| The `environment` hook | Both | `files/environment`: the artifacts of a job go to the optional owner-managed storage, if the build set one. In the pilot, it also puts Homebrew on the `PATH` and sets a UTF-8 locale |

The controller starts the agent in the VM through the Tart guest agent, with a startup
script that is the same for each VM (target design, section 6.4). Before the agent starts, the
script sets the host name to the name of the VM with passwordless `sudo`, so Buildkite shows which
VM ran a job. It also adds the agent tags of the image after the tags of the controller. A tag
whose key the controller already set stays as the controller set it. The guest agent runs the
script as `admin`. Its `PATH` has `/usr/local/bin` but not Homebrew. The hook adds
Homebrew for each job.

## The Xcode archive

Apple gives the Xcode archive only to a person who signs in with an Apple ID. For this reason, a
build never signs in to Apple. The build reads a copy of the archive and checks it against
`XCODE_SHA256` in `build.sh`. It also checks with `pkgutil` that Apple signed the archive (`xip`
does not check the signature). For each new Xcode, the owner does these steps:

1. Download `Xcode_<version>.xip` from <https://developer.apple.com/download/all/>.
2. Put it in `~/XcodesCache/Xcode_<version>.xip` on the build Mac. As an alternative, put it in
   the owner-managed storage. A build then downloads it through `XCODE_URL`, an HTTPS URL such as
   a presigned URL.
3. Set `XCODE_VERSION` and `XCODE_SHA256` in `build.sh` (`shasum -a 256` of the archive).

`XCODE_ARCHIVE` names another place on the build Mac. The build expands the archive on the Mac,
because `xip` in a VM ends before the app is in place (2026-10-10). It then packs Xcode into one
`ditto` archive. The shared directory of a VM does not follow some symbolic links of Xcode, and
this is the reason for the single archive. The VM unpacks the archive from a read-only directory of
the Mac. For this reason, no archive stays in the image. Keep the archive and the pilot image
private, because the license of Xcode limits who may receive it.

## The artifact storage

Buildkite-managed storage is acceptable for artifacts, so
`ARTIFACT_DESTINATION` is optional. An image built without it leaves the destination alone, and
`buildkite-agent` uploads the artifacts to the storage of Buildkite. When the build takes an
owner-managed storage as `ARTIFACT_DESTINATION`, for example `s3://<bucket>/<prefix>`, the hook
sends the artifacts of each job to `<destination>/<pipeline>/<build>/<job>`, and a step may name
another place inside the same storage. `scripts/check-hook.sh` checks these cases.

## No well-known credentials over the network

The vanilla image has the account `admin/admin`. The Tart guest agent runs in the session of the
automatic login of `admin`. After a change of the password, macOS 27 does not log in
automatically.
On 2026-09-27, `sysadminctl -autologin set` gave `SACSetAutoLoginPassword error:22` and exited 0,
and macOS ignored a written `/etc/kcpassword`. The image therefore keeps the local password. It
disables every network path that would accept the password: SSH (Remote Login) and Screen Sharing
(target design, section 9.2). The controller reaches the VM through the guest agent. A person who
debugs the VM uses the window of `tart run`, or `tart run --vnc-experimental`, because Tart serves
those screens itself. `tart run --vnc` does not work, because it needs Screen Sharing in the VM.

## Build

`build.sh` drives the VM with `tart exec`, through the guest agent. It uses SSH once, with the
password of the vanilla image, to put the guest agent in place. The build needs an Apple silicon
Mac with Tart 2.41.1 or later. A macOS VM cannot run a second macOS VM. A hosted GitHub macOS
runner is a VM with 14 GB of disk (2026-10-10). For these reasons, a hosted runner cannot build
the image.

The build stops the VM from inside the VM. `tart stop` does not wait for macOS to write its caches,
and a file that macOS wrote shortly before it is lost. The build then starts the VM again, without
the directory of the archive. It checks the VM as a job VM finds it:

- The guest agent answers as `admin`.
- `buildkite-agent` 4.9.0 runs from `/usr/local/bin`.
- The hook sets the destination.
- Ports 22 and 5900 are closed.
- The image holds only what its list names.

The pilot image has more checks. It selects Xcode 27 and has an iOS simulator runtime. It gives a
job fastlane, CocoaPods, Carthage, xcbeautify, and SwiftLint through the hook. It has 20 GB free.
A check that fails fails the build. The build checks the ports from the Mac. The program that runs
the build therefore needs the Local Network permission of macOS (System Settings › Privacy &
Security › Local Network).

```sh
mise run agent-image   # about 5 minutes, with the base in the cache
mise run pilot-image   # about 20 minutes, 59 GB
tart run bkaso-pilot   # look at it
```

The build makes the local Tart VM `bkaso-<image>` (`VM_NAME` changes it). With `IMAGE_REGISTRY`, for
example `registry.example.internal/macos` (a private registry), it pushes
`<registry>/bkaso-<image>:<tag>` (`IMAGE_TAG`, the commit by default), with the credentials in
`TART_REGISTRY_USERNAME` and `TART_REGISTRY_PASSWORD`.
A profile names the image by the digest that the registry gives.

## Known issue: boot the simulator before xcodebuild

In a job, `xcodebuild test` cannot boot an iOS simulator that is shut down. It stops with
"cannot determine the runtime bundle". The same command in the same VM worked from a terminal,
from `tart exec`, with the environment of the job, in a PTY, and in an Orchard startup script. It
failed only in a job of `buildkite-agent` (release gate, 2026-11-02). The cause is not known.

Do this in the pilot pipeline:

1. Before `xcodebuild`, boot the simulator with `xcrun simctl boot <udid>`, and wait for it with
   `xcrun simctl bootstatus <udid> -b`.
2. Give `xcodebuild` the destination `platform=iOS Simulator,id=<udid>`, not a name.

The build boots the simulator of `PILOT_SIMULATOR` (iPhone 17 by default) one time, so the image
holds its first-boot work. Because of this, the boot in a job takes about 5 seconds. One run of
the release gate also failed one time with "Channel disconnected" from the test runner, and it
passed when Buildkite ran it again.

## What the image holds

Each image holds only what this list names. The second boot lists each place where software
installs itself. A name that the list lacks fails the build. A tool that a new vanilla image adds
therefore cannot reach a job VM unseen. The list is in `build.sh` (`ALLOWED_*`):

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
