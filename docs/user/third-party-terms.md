# Third-party terms

A fleet that runs this controller also runs software from other vendors, under their own terms.
This page lists the terms that affect a fleet most. It is not legal advice, and it does not
replace the terms themselves. Read them, and get advice for your own case.

## macOS

The [macOS software licence agreement](https://www.apple.com/legal/sla/) limits virtual machines
in these ways:

* You can run at most two macOS VMs on one Apple computer at a time, and that computer must
  already run macOS.
* The permitted purposes are limited. Software development and testing are among them.
* The agreement restricts services that give others access to virtualised macOS, such as a
  service bureau or time-sharing. If your fleet serves other teams or other organisations, check
  whether this applies. The exception for leased Macs needs leases of at least 24 hours, so a VM
  that lives for one job does not meet it.

## Xcode and the Apple SDKs

The [Xcode and Apple SDKs agreement](https://www.apple.com/legal/sla/) is for internal use, and
it does not let you distribute Xcode or host it for others. Keep images that contain Xcode in a
private registry that only your organisation can use.

`images/build.sh` accepts the Xcode and SDK licences when it builds an image that contains Xcode.

## Tart and Orchard

Tart and Orchard use the Fair Source License, version 1.1, with Apache 2.0 as the future licence
(FSL-1.1-ALv2). It permits every purpose except a competing use: making the software available to
others in a commercial product or service that substitutes for it, or that offers the same or
similar features. Internal use is permitted. Each version changes to the Apache License 2.0 two
years after its release.

If you offer a commercial macOS build service to others, read the licence text first.

The controller does not contain Tart or Orchard code. It calls the Orchard API over the network.
[NOTICE](../../NOTICE) gives the details.

## Personal data

Job logs and build metadata can contain personal data, such as the names of committers. They go
to Buildkite, which stores them outside your network. Check your privacy and data-residency
obligations before you send them.

## Trademarks

This project is not affiliated with, or endorsed by, Buildkite, OpenAI, Cirrus Labs, or Apple.
Their names and marks belong to their owners.
