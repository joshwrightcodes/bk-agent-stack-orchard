---
status: accepted
date: 2026-09-13
decision-makers: Josh Wright
---

# ADR-0006: Route jobs to VM profiles by their agent tags

## Summary

Jobs need different VMs, but the Stacks API does not route jobs by their agent tags. The decision
is one Buildkite queue, where the agent tags of each job select a VM profile by exact match. A job
that matches no profile fails with a message. The cost is matching logic in the controller, and a
profile for each tag combination in use.

## Context

Jobs need different VMs, with different Xcode images, CPU and memory sizes, and network
allowlists. A VM profile describes one such VM.

The Stacks API gives all scheduled jobs of a queue, and it does not filter them by agent tags. The
agent in the VM also ignores agent tags when it acquires its job. Because of this, the controller
must map each job to a profile.

Two earlier decisions limit the choice. Hosts must pull each image before use (ADR-0005), so the
set of images must be finite. Builds are untrusted (ADR-0004), so pipelines must not select any
image that they want.

## Goals and non-goals

Goals:

* **Standard tags.** Pipeline authors state requirements with standard `agents:` tags (PR-003).
* **Predictable mapping.** Each job maps to exactly one profile, or it fails with a clear message
  (PR-003, PR-004).
* **Finite images.** The set of images is finite, so hosts can pull them before use (SC-07, SC-09).
* **No arbitrary images.** Pipelines cannot select an arbitrary image (SE-01).
* **Few queues.** The number of Buildkite queues stays small.
* **Reversibility.** A later change of the routing model needs few pipeline changes.

Non-goals:

* The contents of each VM image.
* Wildcard and negated tag rules.

## Options considered

**One queue for each profile.** Each Buildkite queue has exactly one VM profile. The mapping is
the simplest, and the image set is finite. However, each new profile needs a new Buildkite queue,
and pipelines state queue names instead of requirements.

**One queue, and agent tags select a profile.** The controller compares the agent tags of each job
with the configured profiles. Pipelines use standard tags, one queue serves all profiles, and the
profiles are a finite, reviewed list. However, the match logic is in the controller.

**The pipeline sets the image.** A plugin or an environment variable in the pipeline gives an image
reference. Pipeline authors get maximum flexibility. However, untrusted pipelines can select any
image. The image set also has no limit, so hosts cannot pull the images before use.

| Goal | One queue for each profile | One queue, tags select a profile | Pipeline sets the image |
|---|---|---|---|
| Standard tags | No | Yes | No |
| Predictable mapping | Yes | Yes | — |
| Finite images | Yes | Yes | No |
| No arbitrary images | Yes | Yes | No |
| Few queues | No | Yes | — |
| Reversibility | No | Partly | No |

"—" shows that this ADR does not assess the option against the goal.

## Decision

Use one Buildkite queue, and let the agent tags of each job select a VM profile. It is the only
option that meets all goals.

The match is exact. The agent tags of the job, without `queue`, must be equal to the `match` map of
one profile. A subset match or a best-fit match could let a job with an extra tag run on a less
specific profile. Exact match prevents this. A job that matches no profile fails immediately, and
the message lists the available profiles. The [target design](../architecture/target-design.md)
gives the full algorithm.

## Consequences

Good:

* Pipelines use standard `agents:` tags on one queue.
* The image set is finite, so hosts can pull all images before use.
* A mismatch fails immediately with a message.

Bad:

* The controller must parse and compare tags.
* Each tag combination in use needs a profile.
* Wildcard and negated tag rules do not work.

Review trigger:

* Review this decision if the number of profiles makes exact match difficult to maintain.

## Confirmation

* Unit tests cover exact match, extra tags, missing tags, and an empty `match` map.
* Unit tests cover rules with an incorrect format and duplicate `match` maps.
