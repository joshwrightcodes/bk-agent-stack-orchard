---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0003: Start job VMs on demand, without a pool of idle VMs

## Summary

ADR-0001 starts one VM for each job, so each job waits for a VM to start. A pool of started, idle
VMs would remove most of this wait. The decision is to start VMs only when jobs need them, and to
keep no idle VMs. The cost is a VM start time for each job, with a target of 60 seconds and an estimate of 30 to 60
seconds.

## Context

Each macOS host can run only two VMs at the same time (ADR-0001). The first fleet has 2 to 10 Macs,
so it has only 4 to 20 VM slots (SC-01, SC-02). An idle VM in a pool holds one of these slots, and no job can use
that slot.

The VM start time target is 60 seconds (QA-01). The estimate is 30 to 60 seconds, and nobody has
measured it. The controller records the duration of each start phase, and this data will show the
real value.

## Goals and non-goals

Goals:

* **Capacity for jobs.** Every VM slot is available for a job (SC-02, QA-02).
* **Acceptable start time.** The job start time meets the 60-second target (QA-01).
* **Simplicity.** The controller stays small (OP-01).

Non-goals:

* The time to pull a VM image on a host for the first time.

## Options considered

**Start VMs on demand.** The controller starts a VM only for a reserved job. All slots stay
available for jobs. However, each job waits for its VM to start.

**Keep a pool of idle VMs.** The controller keeps started VMs for each VM profile, and gives an
idle VM to a new job. Jobs start almost immediately. However, idle VMs hold slots that jobs cannot
use, and the controller must decide the pool size for each profile.

**Start VMs on demand, with a design ready for a pool.** The controller starts VMs on demand, but
its internal design separates VM creation from job assignment. A pool can then be added later.
However, this design adds work for a pool that no measurement supports.

| Goal | On demand | Pool of idle VMs | On demand, ready for a pool |
|---|---|---|---|
| Capacity for jobs | Yes | No | Yes |
| Acceptable start time | Yes | Yes | Yes |
| Simplicity | Yes | No | Partly |

## Decision

Start job VMs on demand, and keep no idle VMs. It is the only option that meets all goals.

## Consequences

Good:

* Every slot is available for a job.
* The controller has no pool logic.

Bad:

* Each job waits for its VM to start. The upper estimate equals the target, so a small delay can
  make a job miss the target (QA-01).
* A pool needs a new ADR that supersedes this ADR, and a change to the controller.

Review trigger:

* Review this decision if measured start times are a problem for pipeline authors.

## Confirmation

* Component tests confirm that the controller creates a VM only for a reserved job.
* After adoption, telemetry records the duration of each VM start phase, and the measured value
  replaces the estimate.
