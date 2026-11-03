---
status: accepted
date: 2026-11-03
decision-makers: Josh Wright
---

# ADR-0020: Leave the job VM images to the operator

## Summary

ADR-0012 decided how the project builds the job VM images, and PR-026 asked the project for a
pilot profile with Xcode 27. The project now delivers a controller to operators who run their own
fleets, and it runs no pilot. The decision is that the project publishes no job VM image. Each
operator builds the images of its fleet, and `images/` is a reference build for an operator who
builds its own. The quick start keeps an upstream image as its default, for evaluation only. This
ADR replaces the parts of ADR-0012 that assume that the project builds and delivers the images of
the fleet, and ADR-0012 keeps its status. The cost is that each operator owns the security and
the upkeep of its images.

## Context

ADR-0012 builds each image from a vanilla macOS image and checks the result against an explicit
list. It also plans a vanilla image of this project from the restore image of Apple, and it names
the pilot image with Xcode 27 (PR-026). Those parts assume that the project builds the images that
a fleet runs.

The owner decided on 2026-11-03 that the project publishes no VM image and runs no pilot. The
implementation plan put W-23 (the pilot image) and W-25 (the pilot start) out of scope on the same
day. ADR-0017 lets a VM download its agent, so an image with only the Tart guest agent can serve a
job, and an operator does not need an image of this project to try the controller.

The upstream image `ghcr.io/cirruslabs/macos-golden-gate-base` runs the Tart guest agent, and the
quick start uses it, pinned by digest. It keeps the well-known account `admin` with the password
`admin`, it accepts that password over SSH (Remote Login), and it gives `admin` passwordless
`sudo` (ADR-0017, spike of 2026-11-03; owner statement, 2026-11-03). It also carries tools that no
job needs (ADR-0012). Target design §9.2 forbids an image that accepts a well-known credential
over the network, and it asks an image for the guest-agent transport to turn off SSH and Screen
Sharing.

An Xcode archive comes from Apple only after a sign-in with an Apple ID. The Xcode and Apple SDKs
agreement is for internal use, and it does not permit a party to distribute Xcode or to host it
for others (`docs/user/third-party-terms.md`, which links the agreement, read on 2026-11-03). A
project that published an image with Xcode would distribute that software.

## Goals and non-goals

Goals:

* **The project ships what it can maintain.** It publishes the controller, its chart, and its
  documents, and no image that it cannot patch for each fleet.
* **An operator can still start quickly.** A trial needs no image build.
* **A production image is safe by default.** The documents state the rules that a production image
  meets, and the quick start says that its default does not meet them.
* **The image method of ADR-0012 stays available.** An operator who builds an image can start from
  a build that already checks its contents.

Non-goals:

* The contents of a production image. Each operator decides them.
* A registry or a signature for images. The operator stores and pulls its own images.
* The setup of macOS from the restore image of Apple.

## Options considered

**The project builds and publishes the images (ADR-0012 as written).** An operator pulls a ready
image. However, the project then patches images for fleets that it does not run, it distributes
Xcode, and it keeps a pilot that no work unit delivers.

**The project publishes no image, and keeps `images/` as a reference build.** Each operator builds
its images, and `images/build.sh` shows a build that starts from a vanilla image and checks the
result against a list. The quick start uses an upstream image for a trial. However, each operator
owns the security and the upkeep of its images.

**The project publishes no image, and removes `images/`.** The repository is smaller. However, an
operator loses a build that already meets the rules of §9.2, and the real-VM tests lose their
agent image.

| Goal | Publish images | Reference build | No build |
|---|---|---|---|
| The project ships what it can maintain | No | Yes | Yes |
| An operator can start quickly | Yes | Yes, with the quick start | Yes, with the quick start |
| A production image is safe by default | Yes, for the images of the project | Yes, with the rules and the label of the quick start | Partly: rules only |
| The image method stays available | Yes | Yes | No |

## Decision

Publish no job VM image, and keep `images/` as a reference build.

* The project MUST NOT publish a job VM image. The release publishes the controller binaries, its
  container image, and its chart only (ADR-0013).
* The fleet operator supplies each image of its fleet. A production image MUST meet the image
  requirements of target design §5 and the image rules of target design §9.2.
* `images/build.sh` is a reference for an operator who builds its own images. It keeps the method
  of ADR-0012: a vanilla base pinned by digest, an explicit list, and a check that fails on
  anything else. The real-VM tests MAY use its images.
* The quick start MAY use the upstream base image, pinned by digest, as its default. The quick
  start guide and the comment of its script MUST say that the default is for evaluation only, that
  it does not meet the image rules of §9.2, and that production uses an image that the operator
  builds.
* PR-026, the pilot profile with Xcode 27, is out of the scope of the project.

This decision amends ADR-0012 by partial replacement, as ADR-0016 does for ADR-0006. It replaces
these parts of ADR-0012: the assumption that the project builds the images of the fleet, the plan
to make a vanilla image of this project from the restore image of Apple, and the bullet that names
the pilot image and its Xcode (PR-026). The method of ADR-0012 stays the method of the reference
build, so ADR-0012 keeps its status. A full supersession would drop that method, which `images/`
still follows.

## Consequences

Good:

* The project patches no image for a fleet that it does not run, and it distributes no Apple
  software.
* The rules for a production image are in the target design, where an operator reads them.
* An operator can try the controller with no image build, and can start a production image from a
  build that already checks its contents.

Bad:

* Each operator owns the security and the upkeep of its images, including each new Xcode.
* The default of the quick start does not meet the image rules of §9.2. A person who copies the
  quick start into production runs an image with a well-known credential that SSH accepts. The
  label of the quick start is the only control.
* The values that a pilot of the project was to measure (requirements section 6, test strategy
  section 8) come only from the release gate and from operators who share their measurements.

Review trigger:

* Review this decision if Apple permits the distribution of a macOS image with Xcode.
* Review it if an upstream image with the Tart guest agent turns off SSH and drops the well-known
  credential, which would let the quick start default meet §9.2.

## Confirmation

* The release workflow publishes no job VM image.
* The quick start guide and `scripts/quickstart.sh` label the default image as evaluation only,
  and name the rules of §9.2 that it does not meet.
* PR-026 has the status `out of scope` in the requirements.
