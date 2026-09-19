---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0004: Treat all builds as untrusted and isolate each job VM

## Summary

Each job runs in a new VM (ADR-0001), but a VM can still reach the host, other VMs, private
networks, shared caches, and credentials. No process confirms that all code on the queue is
trusted. The decision is to treat all builds as untrusted. Each job VM is isolated from the host,
from other VMs, from private networks, and from long-lived credentials. The cost is slower builds,
because jobs cannot use shared host caches.

## Context

ADR-0001 gives each job a new VM. Because of this, the file systems of different jobs are already
isolated. File-system isolation alone does not stop a build from reaching other resources. If the
VM configuration permits it, a build can reach the host, other VMs, and private networks. A build
can also read the shared caches and the credentials that its VM receives.

Shared host caches make builds faster, but they also create a risk. One malicious build can corrupt
a cache, and all later builds then use the corrupted content.

No process confirms that all code on the queue is trusted. An isolation policy must also be easy to
audit, and different trust levels for different pipelines make audits more difficult.

## Goals and non-goals

Goals:

* **Containment.** A malicious build does not affect other jobs, hosts, or internal systems (SE-01,
  PR-030).
* **No long-lived credentials.** A build gets no credential with a longer life or a larger scope
  than its job (SE-02).
* **Auditability.** The isolation policy is easy to understand and to audit.
* **Build performance.** Builds are as fast as possible.

Non-goals:

* The technology that enforces the isolation.
* The Buildkite cluster that contains the queue, and the credential of the controller.
* Build caches outside the hosts.

## Options considered

**Trusted code only.** VMs use shared host caches and have network access to internal systems.
Host caches give the fastest builds, and VMs reach internal services without configuration.
However, one malicious build can corrupt shared caches or reach internal systems. Also, no process
confirms that all code on the queue is trusted.

**A trust level for each profile.** Trusted profiles use caches and internal access. Untrusted
profiles are isolated. Trusted pipelines keep the cache performance. However, two policies need
implementation, tests, and audits. A pipeline can also use a profile with an incorrect trust level.

**All builds untrusted.** All VMs are isolated, and builds use remote caches only. The policy is
secure by default, and there is only one policy. However, builds cannot use host caches.

| Goal | Trusted code only | Trust level for each profile | All builds untrusted |
|---|---|---|---|
| Containment | No | Partly | Yes |
| No long-lived credentials | — | — | Yes |
| Auditability | — | No | Yes |
| Build performance | Yes | Partly | No |

"—" shows that this ADR does not assess the option against the goal.

## Decision

Treat all builds as untrusted. It is the only option that meets the goals for containment,
credentials, and auditability. It gives up shared host caches.

Each job VM MUST have these properties:

* It MUST NOT have a network route to the host, to other VMs, or to private networks, except to
  destinations that its profile permits.
* It MUST NOT have host directory mounts.
* It MUST NOT run a second job.
* It MUST NOT get a Buildkite credential or an orchestrator credential, except a credential for
  its own job.

## Consequences

Good:

* By default, a malicious build cannot reach the host, other VMs, or private networks.
* No credential in a VM has a longer life or a larger scope than its job.
* One policy applies to all profiles.

Bad:

* Builds cannot use shared host caches. They must use remote caches.
* Access to an internal service needs an explicit entry in the profile.
* Telemetry from the agent in a VM needs a public ingest endpoint.

Review trigger:

* Review this decision if build times without host caches are a problem for pipeline authors.

## Confirmation

* Unit tests confirm that each generated VM specification has network isolation.
* Unit tests confirm that each generated VM specification has no host directories and does not
  contain the cluster agent token.
