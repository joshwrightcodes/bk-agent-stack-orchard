---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0005: Run and schedule the job VMs with Tart and Orchard

## Summary

The controller from ADR-0001 needs software that runs macOS VMs on the self-hosted hosts and
schedules the VMs across the hosts. The decision is to use Tart to run the VMs and Orchard to
schedule them. Both have no licence cost for this use, and Orchard can run tests with synthetic
workers instead of Mac hosts. The cost is a dependency on one Orchard controller and on Orchard
fixes that are not released yet.

## Context

ADR-0001 decides that a controller creates one VM for each job through a VM orchestrator. No
orchestrator exists yet.

Apple permits a maximum of two macOS VMs on one host. The orchestrator must enforce this limit and
report the free capacity to the controller. It must also isolate the network of each VM and keep
host directories out of the VM (ADR-0004). Finally, it must start the Buildkite agent in the VM.

The controller is a Go program, so a Go client or a documented REST API decreases the integration
work. The tests of the controller run in CI, where no Mac hosts exist.

## Goals and non-goals

Goals:

* **Capabilities.** An API creates, lists, and deletes VMs, and gives data for free capacity
  (SC-02).
* **Isolation.** Each VM can have an isolated network and no host mounts (PR-030).
* **Low licence cost.** The software has a low licence cost for this use (CO-02).
* **Testability.** Tests can use the real orchestrator API without Mac hosts.
* **Integration.** A Go client or a documented REST API exists (CO-04).
* **Low ownership cost.** As little orchestration logic as possible needs development (OP-01).
* **Reversibility.** A later change of the orchestrator has a low cost.

Non-goals:

* The rules that select a VM for a job.
* The build of the VM images.
* High availability of the orchestrator.

## Options considered

**Tart and Orchard.** Tart runs macOS VMs with Apple Virtualization.framework. Orchard schedules
Tart VMs on a cluster of hosts. The latest releases are Tart 2.40.1 (2026-09-30) and Orchard 0.57.0
(2026-09-15), checked 2026-10-10. Since 2026-06-05, both use the FSL-1.1-ALv2 licence, which permits
this use without cost. The Orchard REST API creates, lists, and deletes VMs, and workers report
their resources. Softnet blocks access to private ranges and to the other VMs. It lets a VM reach
the gateway of its network, which is the host, so the hosts add pf rules (target design, §9.1).
`orchard dev --synthetic` runs workers with in-memory VMs, and Orchard has a Go client. However, the
Orchard controller is one instance with an embedded database. Orchard releases before 0.57.0 can
release worker capacity before a VM stops, and Orchard does not delete failed VMs.

**Tart without an orchestrator.** The controller runs `tart` commands on each host through SSH.
There is no orchestrator to operate. However, the controller must implement host inventory,
placement, VM lifecycle, and host failure detection. The controller also needs SSH access to each
host, and tests need real Mac hosts or a custom fake.

**Anka Build Cloud.** Veertu Anka Build Cloud has a controller with a web UI and a REST API, a
registry, and virtualization nodes. A vendor supports the product. However, it needs a commercial
licence for hosts or cores, and the trial is 30 days (as of 2026-09-13). No source confirmed a test
mode without Mac hosts, or network isolation for each VM.

**MacStadium Orka.** Orka is a Kubernetes-native macOS orchestrator with a REST API and open-source
plugins. It runs on the MacStadium cloud, on a private cloud, or on Mac hardware of the customer. A
vendor supports the product. However, it is a commercial product with pricing on request (as of
2026-09-13). No source confirmed a test mode without Mac hosts, or network isolation for each VM.

| Goal | Tart and Orchard | Tart without an orchestrator | Anka Build Cloud | MacStadium Orka |
|---|---|---|---|---|
| Capabilities | Yes | Partly | Yes | Yes |
| Isolation | Yes | Yes | Not verified | Not verified |
| Low licence cost | Yes | Yes | No | No |
| Testability | Yes | No | Not verified | Not verified |
| Integration | Yes | Partly | Yes | Yes |
| Low ownership cost | Yes | No | Yes | Yes |
| Reversibility | Yes | Partly | Partly | Partly |

"Not verified" shows that no source confirmed the capability.

## Decision

Use Tart to run the job VMs and Orchard to schedule them. It is the only option that meets all
goals. It is also the only option with a verified way to test without Mac hosts.

The [target design](../architecture/target-design.md) gives the Orchard-specific rules: resource
requests, the VM name format, the minimum Orchard version, and the Go module path.

## Consequences

Good:

* The software has no licence cost for this use.
* Orchard enforces the two-VM limit through worker resources.
* Softnet isolates the network of each VM.
* CI can test against the real Orchard API with synthetic workers.
* The Cirrus Labs image templates build Tart images with a selected Xcode version, such as the
  Xcode 27 pilot image (PR-026).

Bad:

* The Orchard controller is one instance with no high availability. Its recovery must take less
  than 15 minutes (QA-03).
* The worker capacity and overcommit corrections need Orchard 0.57.0 or later. This release
  contains pull requests #465 and #470.
* The startup script uses SSH, so the VM specification contains VM credentials. Orchard 0.57.0
  adds a Tart Guest Agent transport that removes this need.
* The controller needs the Orchard `compute:write` role, and this role also permits the creation
  and deletion of workers. Orchard 0.57.0 has no narrower role.
* Each Xcode image needs a disk of 85 to 140 GB, and each host has 1 TB storage. A host can hold
  only a few images (SC-07, SC-09).

Review trigger:

* Review this decision if Orchard development stops, if its licence changes, or if the orchestrator
  needs high availability.

## Confirmation

* Integration tests run against `orchard dev --synthetic`.
* A CI check confirms that the fleet runs the minimum Orchard version.
