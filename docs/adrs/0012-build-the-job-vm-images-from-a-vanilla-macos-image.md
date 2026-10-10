---
status: accepted
date: 2026-10-10
decision-makers: Josh Wright
---

# ADR-0012: Build the job VM images from a vanilla macOS image

## Summary

The job VM images build on the published bases of Cirrus Labs, which carry the tools of many uses:
other CI agents, AI assistants, Java, Android, and Flutter. Removing what a job does not need is a
list that must follow every change upstream, and a tool that upstream adds stays in the image
until somebody sees it. The decision is to build each image from a vanilla macOS image, with only
the software that an explicit list names, and to check the result against that list. The images
build on the vanilla image of Cirrus Labs first, and on a vanilla image of this project from the
restore image of Apple later. The cost is an Xcode archive that the owner downloads with an Apple
ID, and the upkeep of the setup of a new macOS.

## Context

A job VM runs untrusted builds (ADR-0004), with a job acquisition token for one job, and the pilot
image builds, tests, and publishes iOS and macOS apps (PR-026). A tool in the image that no job
needs is code that runs with the rights of `admin`, and that the fleet must patch.

Until this decision, `images/build.sh` built on `macos-golden-gate-base` and on
`macos-golden-gate-xcode:27` of Cirrus Labs, pinned by digest. On 2026-10-10 these bases carried the
GitHub Actions runner, `gitlab-runner`, `buildkite-agent@3`, `codex`, Claude Code, Amazon Q,
OpenJDK 17, the Android SDK and NDK, Flutter, and `xcodes`, beside the tools that an app build
uses. The build removed the ones that the owner named. Such a list of removals has two faults: a
tool that upstream adds in a new base stays in the image, and the list grows with each release of
the bases.

Cirrus Labs also publishes `macos-golden-gate-vanilla`: the restore image of Apple, with the setup
assistant done, `admin` with passwordless sudo, automatic login, SSH, and Screen Sharing, and no
Homebrew, no tools, and no Tart guest agent (`templates/vanilla-golden-gate.pkr.hcl`, 2026-10-10).
The template that makes it drives the setup assistant with keys over VNC, which can change with
each macOS release.

Xcode is the hard part of any image that starts from less. Apple gives the Xcode archive only to a
person who signs in with an Apple ID. Cirrus Labs copies a downloaded archive into its build.

## Goals and non-goals

Goals:

* **Only what a job needs.** The image contains the software of an explicit list, and nothing that
  a base adds without a decision of this project.
* **A change upstream cannot add software silently.** The build fails when the image contains
  something that the list does not name.
* **A repeatable build.** The same script builds the image on any Apple silicon Mac.
* **Little upkeep for one owner** (OP-01).

Non-goals:

* The contents of the list, which the images README records and which can change without a new
  decision.
* Where the build runs. It needs an Apple silicon Mac, which the target design states.
* The SSH transport, which ADR-0009 already leaves to a profile.

## Options considered

**Remove from the bases of Cirrus Labs.** The build starts from the full bases and removes the
tools that a job does not need. It needs no Apple ID and no setup of macOS. However, the list of
removals follows upstream, and a tool that upstream adds stays until somebody sees it.

**Build from the vanilla image of Cirrus Labs.** The build starts from the restore image of Apple
with only the setup assistant done, installs the guest agent, Xcode, the tools of the list, and the
agent, and checks the image against the list. Upstream decides only the setup of macOS, which its
template shows in one short file. However, the owner provides the Xcode archive, and the image
still depends on a base of another party.

**Build a vanilla image of this project from the restore image of Apple.** The project runs the
setup of macOS itself, and then builds as above. Nothing in the image comes from another party
except Apple. However, the setup assistant needs keys over VNC, which can break with a macOS
release, and the owner keeps that automation.

| Goal | Remove from the bases | Cirrus Labs vanilla | Vanilla of this project |
|---|---|---|---|
| Only what a job needs | No: what the list misses stays | Yes | Yes |
| No silent additions | No | Yes, but for the setup of macOS | Yes |
| Repeatable build | Yes | Yes | Yes |
| Little upkeep | Yes, until upstream changes | Yes | No: the setup assistant |
| No Apple ID step | Yes | No | No |

## Decision

Build each job VM image from a vanilla macOS image, and check it against an explicit list.

* The images build on `macos-golden-gate-vanilla` of Cirrus Labs, pinned by digest, first. This
  proves the build of the images with no change to the setup of macOS.
* The project then makes its own vanilla image from the restore image of Apple, with the same
  result as the template of Cirrus Labs, and the images build on it. Until then, the setup of macOS
  is the only part that comes from another party.
* The build installs the Tart guest agent, then the software of the list: for the pilot, Xcode from
  an archive that the owner downloads with an Apple ID, its simulator runtimes, and the tools to
  build, test, sign, and publish an iOS or macOS app.
* The build checks the image against the list: an application in `/Applications`, a launchd job,
  a program in `/usr/local/bin`, or a Homebrew that the list does not name fails the build.
* Until the build on the vanilla image works, the images build on the full bases with the removals
  and checks of 2026-10-10, as an interim.

## Consequences

Good:

* A tool that upstream adds cannot reach a job VM: the check against the list fails the build.
* The images README is the one record of what a job VM contains.
* The image is smaller, so a 1 TB host holds more images (SC-09).

Bad:

* The owner downloads each Xcode archive with an Apple ID, and keeps it where the build can read
  it. A new Xcode needs that step again.
* The build installs what the bases gave before, so it takes longer and needs the network.
* The vanilla image of this project needs the setup assistant automated with keys over VNC, which
  the owner must repair when a macOS release changes the setup assistant.
* A tool that a pipeline needs and the list lacks fails that pipeline, until the list grows.

Review trigger:

* Review this decision if Apple publishes Xcode without a sign-in, which would remove the archive
  step.
* Review it if the setup assistant gets a supported automation, such as a configuration profile,
  which would make the vanilla image of this project cheap to keep.

## Confirmation

* The build of each image on a vanilla image fails when the image contains an application in
  `/Applications`, a launchd job, a program in `/usr/local/bin`, or a Homebrew that the list in the
  images README does not name.
* The base of each image is a vanilla image, pinned by digest in `images/build.sh`, and the upstream
  watch reports a new digest.
* The pilot image reports Xcode 27 from `xcodebuild -version`, and the real-VM tests pass on the
  agent image.
