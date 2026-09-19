---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0002: Check free capacity before the controller reserves a job

## Summary

The controller of ADR-0001 must reserve a job before it can issue a job acquisition token for that
job. A reserved job disappears from the scheduled-jobs list until the reservation ends. The
decision is to calculate the free VM capacity first, and to reserve only the jobs that fit. The
cost is capacity accounting in the controller that can disagree with the orchestrator for a short
time.

## Context

The Buildkite Stacks API lists the scheduled jobs of a queue, and a stack reserves a job before it
runs the job. A reserved job does not appear in the scheduled-jobs list. A reservation lasts 900
seconds by default and 3600 seconds at most. When a reservation expires, the job becomes
`scheduled` again. These values are correct as of 2026-09-13.

A stack must reserve a job before it can request a job acquisition token for the job. The token
expires early if the reservation expires.

`agent-stack-k8s`, the Buildkite stack controller for Kubernetes, reserves jobs first, and then
waits for free capacity. A code comment in `agent-stack-k8s` accepts that reservations can expire
while jobs wait. Each macOS host has only two VM slots (ADR-0001), so free slots are few. With the
`agent-stack-k8s` order, a job can stay reserved and hidden while it waits. Its token can also
expire before a slot is free.

## Goals and non-goals

Goals:

* **No hidden waiting jobs.** A job that cannot start stays visible in the `scheduled` state
  (PR-005).
* **Valid tokens.** A VM gets a job acquisition token that is still valid when the agent starts
  (SE-02).
* **Buildkite order.** Jobs start in Buildkite priority order, and then oldest first (PR-006).
* **Low complexity.** The controller logic stays small (OP-01).

Non-goals:

* The host on which a VM runs.
* The values of the reservation and token timeouts.

## Options considered

**Reserve first, then wait for capacity.** The controller reserves all matching jobs, and a
limiter starts them when slots become free. `agent-stack-k8s` uses this order. The logic is simple.
However, reserved jobs are hidden while they wait, and reservations and tokens can expire before a
slot is free.

**Check capacity, then reserve.** The controller calculates the free slots from the orchestrator
inventory, and it reserves only the jobs that fit. Jobs that do not fit stay scheduled. The
controller issues the token just before it creates the VM. However, the controller must count the
capacity itself, and its count can disagree with the orchestrator for a short time.

**Reserve nothing.** The controller starts a VM, and the agent in the VM takes any scheduled job
from the queue. No reservation is necessary. However, a job acquisition token needs a reservation,
so each VM would need the cluster agent token. The agent can also take a job that needs a different
VM type.

| Goal | Reserve first | Check capacity first | Reserve nothing |
|---|---|---|---|
| No hidden waiting jobs | No | Yes | Yes |
| Valid tokens | Partly | Yes | No |
| Buildkite order | Yes | Yes | — |
| Low complexity | Yes | Partly | Yes |

"—" shows that this ADR does not assess the option against the goal.

## Decision

Check free capacity before a reservation. It is the only option that keeps waiting jobs visible and
gives each VM a valid job acquisition token.

The controller renews the reservation of each job while its VM starts. The [target
design](../architecture/target-design.md) gives the capacity calculation.

## Consequences

Good:

* Jobs that do not fit stay scheduled and visible.
* Each VM gets its token just before the VM is created.
* Other stacks on the queue can still see the jobs that this controller cannot start.

Bad:

* The controller duplicates the capacity accounting of the orchestrator. The two counts can
  disagree for a short time, so a VM can wait without a host and needs a timeout.
* The controller must read the orchestrator inventory before each reservation.

Review trigger:

* Review this decision if free capacity is usually available, or if the Stacks API adds a way to
  hold a job without hiding it.

## Confirmation

* Component tests confirm that the controller never reserves more jobs than the free slots.
* Component tests confirm that jobs that do not fit stay in the `scheduled` state.
