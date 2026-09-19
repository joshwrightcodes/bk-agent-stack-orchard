---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0001: Provision macOS job VMs with a custom Buildkite stack controller

## Summary

Buildkite macOS jobs need a clean macOS VM for each job on the self-hosted Apple Silicon hosts.
Buildkite does not start agents for a self-hosted queue, and each host can run only two macOS VMs
at the same time. The decision is a custom stack controller on the Buildkite Stacks API. The
controller starts one VM for each scheduled job, and it deletes the VM when the job ends. The cost
is a controller to build and operate.

## Context

The new macOS CI fleet uses self-hosted Apple Silicon Macs. The first fleet has 2 to 10 Macs, and
the target is 10 to 50 Macs (SC-01). The hosts have no VM tooling yet.

The [product requirements](../product/requirements.md) and the
[system design baseline](../architecture/system-design-baseline.md) own the requirements and the
measurable drivers. Identifiers in this ADR, such as PR-005 and QA-01, refer to these documents.

The owner states three reasons to run the jobs on owned hardware: cost, data sovereignty, and
performance. Source code and build artefacts stay on hardware that the owner controls, and the
owner selects the hardware that runs each build. No cost or performance measurements exist yet.
One person builds and operates the whole system.

Buildkite runs the pipelines. Jobs that need macOS go to a self-hosted queue. A job stays in the
`scheduled` state until an agent acquires it, so the number of scheduled jobs shows the demand for
agents. Buildkite does not start agents for a self-hosted queue, so the fleet must supply them.

The simplest solution is a permanent agent on each host. That solution does not isolate jobs,
because each job runs directly on the host operating system. Each job therefore needs a new VM that
exists only while the job runs. Capacity makes this difficult. Apple permits a maximum of two
macOS VMs on one host at the same time. A fleet of 10 to 50 Macs has only 20 to 100 VM slots, and
a VM without a job blocks half of a host.

Two more facts affect the decision. Some jobs cannot run, for example when no VM type matches the
job, or when a VM does not start. These jobs must fail with a message, and must not wait in the
queue until they expire. The VM start time target is 60 seconds for each job (QA-01). The estimate
is 30 to 60 seconds, and telemetry will measure it after adoption.

## Goals and non-goals

Goals:

* **Follow demand.** Start a VM only for a scheduled job, and never start more VMs than the free
  slots (PR-005, QA-02).
* **Clean environment.** Run each job in a new VM that no earlier job changed (PR-001, PR-002).
* **No long-lived credentials in VMs.** Give a VM only a credential for its own job (SE-02).
* **VM selection.** Let each job ask for a VM image and size (PR-003).
* **Visible failure.** Fail a job that cannot run, with a message in Buildkite (PR-004, PR-008).
* **Self-hosted.** Run the jobs on the self-hosted Apple Silicon hosts, for cost, data sovereignty,
  and performance (BD-01, BD-02, PR-032).
* **Recovery.** Recover after a restart without a database, and remove VMs whose jobs ended
  (PR-010, QA-03).
* **Low ownership cost.** Build and operate as little software as possible, because one person
  maintains the system (OP-01).
* **Reversibility.** A later change of the approach or of the VM technology has a low cost.

Non-goals:

* The VM technology.
* The rules that map a job to a VM type.
* The Buildkite queue of the fleet, and the credential of the controller.
* The order of the capacity check and the job reservation.
* Pools of started, idle VMs.
* The addition or removal of Mac hosts. The host pool is fixed.
* Build caches.

## Options considered

**Buildkite hosted macOS agents.** Buildkite runs macOS agents on its own infrastructure, with
the instance shapes `MACOS_ARM64_M4_6X28` and `MACOS_ARM64_M4_12X56` (as of 2026-09-13). There is
no build infrastructure to operate. However, the builds and their data run on Buildkite
infrastructure, not on the self-hosted hosts, so source code checkouts leave owner infrastructure
(PR-032). The agents do not accept custom base images, and the plan vCPU limit sets the concurrency.

**A permanent agent on each host.** A Buildkite agent runs on each host and runs jobs directly on
the host operating system. This option is the simplest, and it needs no VM tooling. However, a job
can change the host for the next job. A job can also read the files and the credentials of the
host and of other jobs. The agents also hold host capacity when the queue is empty, and all jobs
get the same host environment.

**A permanent agent on each host, with a VM plugin for each step.** A plugin such as
`veertuinc/anka-buildkite-plugin` or `cirruslabs/tart-buildkite-plugin` clones a VM for each step.
Each step gets a clean VM, and no controller is necessary. However, the number of VMs does not
follow demand, because each host agent takes jobs directly. A long-lived agent token stays on each
host, and the Anka plugin mounts the host build directory into the VM. Each pipeline step must
declare the plugin. The Tart plugin has had no update since 2023-12.

**A pool autoscaler that reads queue metrics.** The autoscaler changes the size of a pool of VMs
from the number of scheduled jobs. Each VM runs `buildkite-agent --disconnect-after-job`. The
Buildkite Elastic CI Stack for AWS uses this pattern for Linux and Windows. The autoscaler is
simple, but each VM needs the cluster agent token. Queue metrics do not contain agent tags. Because
of this, the autoscaler cannot select a VM type for a job, or fail a job that no VM type can serve.
A VM can also start and get no job.

**A custom Buildkite stack controller.** The controller registers a custom stack with the
Buildkite Stacks API. With this API, it lists, reserves, and finishes jobs, and it issues a job
acquisition token that is valid for one job. The controller needs development, tests, and
operation. No public implementation for macOS VMs exists (as of 2026-09-13).

| Goal | Hosted agents | Permanent host agents | Host agent and VM plugin | Pool autoscaler | Custom controller |
|---|---|---|---|---|---|
| Follow demand | Yes | No | No | Partly | Yes |
| Clean environment | Yes | No | Yes | Yes | Yes |
| No long-lived credentials in VMs | — | No | Partly | No | Yes |
| VM selection | Partly | No | Yes | No | Yes |
| Visible failure | — | — | — | No | Yes |
| Self-hosted | No | Yes | Yes | Yes | Yes |
| Recovery | — | — | — | — | Yes |
| Low ownership cost | Yes | Yes | Yes | Partly | No |
| Reversibility | Yes | Yes | Partly | Partly | Partly |

"—" shows that this ADR does not assess the option against the goal.

## Decision

Use a custom Buildkite stack controller. It is the only option that meets the goals for demand,
credentials, VM selection, visible failure, and self-hosting.

The controller reads the scheduled jobs from the Stacks API and reserves jobs. It starts one VM for
each reserved job, and it gives the VM a job acquisition token. When the job ends, the controller
deletes the VM. When a job cannot run, the controller finishes the job with a message. The
controller uses the VM orchestrator through an interface, so the orchestrator can change without a
change to the controller.

## Consequences

Good:

* The number of VMs follows the number of scheduled jobs, up to the free slots.
* Each job gets a new VM, and only a token for that job goes into the VM.
* Jobs that cannot run fail quickly, with a message.
* After a restart, the controller gets its state again from the orchestrator and from Buildkite.

Bad:

* The controller needs development, tests, and operation.
* Each job waits for a VM to start. The target is 60 seconds (QA-01), and nobody has measured the
  estimate of 30 to 60 seconds.
* One controller replica serves one queue, so there is no high availability.
* The platform must run only one controller replica, and nothing detects a second replica with the
  same identity. Two replicas can reserve more jobs than the free slots. The unplaced-VM timeout
  then releases these jobs, but the jobs start late.

Review trigger:

* Review this decision if Buildkite hosted macOS agents accept custom base images, or if the
  reasons for owned hardware change.

## Confirmation

* Component tests confirm that owned VMs never exceed capacity.
* Component tests confirm that each job has a maximum of one VM, and that the controller deletes
  each VM that it creates.
* Component tests confirm that the controller fails a job that cannot run, with a message.
* After adoption, telemetry measures the VM start time, and the measured value replaces the
  estimate.
