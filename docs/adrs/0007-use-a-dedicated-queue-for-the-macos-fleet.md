---
status: accepted
date: 2026-09-19
decision-makers: Josh Wright
---

# ADR-0007: Use a dedicated queue for the macOS fleet

## Summary

The controller serves Buildkite jobs from one queue. Jobs for other agents must not reach the
macOS fleet, and macOS jobs must not go to other agents. The decision is a dedicated self-hosted
queue for the fleet. The cluster MAY contain other queues. The cost is that a leaked cluster agent
token reaches the other self-hosted queues of the cluster.

## Context

Buildkite groups queues, agent tokens, and pipelines into clusters. A cluster agent token gives
access to all self-hosted queues in its cluster. A pipeline belongs to one cluster, and its builds
run only on agents of that cluster. Buildkite has no agent token with a scope smaller than a
cluster. These facts are correct as of 2026-09-14.

The controller of ADR-0001 registers one stack for one queue, and it holds a cluster agent token.
All builds on its queue are untrusted (ADR-0004). Each additional region will use a new queue
(SE-10).

## Goals and non-goals

Goals:

* **Job separation.** Only VMs of this fleet run the jobs of this queue, and the fleet runs no
  other jobs (PR-031).
* **Pipeline compatibility.** One build can use this queue and other queues of the same cluster.
* **Small blast radius.** A leaked credential reaches as little as possible (SE-03).
* **Simple administration.** The number of clusters and queues stays small.

Non-goals:

* The credential of the controller.
* The number of regions.

## Options considered

**A dedicated queue in a shared cluster.** The fleet has its own queue, and the cluster can contain
other queues. Builds can use this queue together with other queues of the cluster. However, a
leaked cluster agent token reaches the other self-hosted queues of the cluster.

**A dedicated cluster with only this queue.** The fleet has its own queue in its own cluster. A
leaked token reaches only this queue. However, a pipeline belongs to one cluster, so a build cannot
use this queue and a self-hosted queue of another cluster. The organization also administers one
more cluster.

**A shared queue.** The fleet takes jobs from a queue that other agents also serve. No new queue is
necessary. However, another agent can take a macOS job, and the controller cannot select a VM for
that job. The untrusted-build policy of ADR-0004 then applies to agents outside the fleet.

| Goal | Dedicated queue, shared cluster | Dedicated cluster | Shared queue |
|---|---|---|---|
| Job separation | Yes | Yes | No |
| Pipeline compatibility | Yes | No | Yes |
| Small blast radius | Partly | Yes | No |
| Simple administration | Yes | Partly | Yes |

## Decision

Use a dedicated self-hosted queue for the macOS fleet. The cluster MAY contain other queues.

Only VMs of this fleet run the jobs of this queue. A leaked cluster agent token reaches the other
self-hosted queues of the same cluster, because Buildkite has no smaller token scope. The
credential decision must limit this risk with a short life and an address allowlist.

## Consequences

Good:

* Only VMs of this fleet run macOS jobs, and the fleet runs no other jobs.
* One build can use the macOS queue and other queues of the same cluster.
* Each additional region adds a new queue, and the cluster stays the same (SE-10).

Bad:

* A leaked cluster agent token reaches all self-hosted queues of the cluster.
* Untrusted macOS builds share a cluster with other work.

Review trigger:

* Review this decision if Buildkite adds an agent token with a scope smaller than a cluster.

## Confirmation

* Before the controller goes live, a review of the queue confirms that only fleet VMs serve it.
