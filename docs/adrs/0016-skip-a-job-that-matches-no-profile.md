---
status: accepted
date: 2026-11-03
decision-makers: Josh Wright
---

# ADR-0016: Skip a job that matches no profile

## Summary

ADR-0006 decided that a job that matches no profile fails immediately, with a message. A queue can
carry more than one stack or agent, and a job that this controller cannot carry may be the job of
another runner. The decision is to skip such a job: the controller never reserves it, never
finishes it, and gives it no VM slot. It writes one notification on the job page, one log line,
and one count for the job. The cost is that a misconfigured profile no longer gives a fast
failure: the job waits.

## Context

ADR-0006 selects a VM profile by an exact match of the agent tags. It also says that a job with no
matching profile fails at once, and that the message lists the profiles. ADR-0007 gives the fleet
a dedicated queue, but the queue is a Buildkite object, and the owner can run another stack or a
plain agent on it. Every stack of a queue reads the same list of scheduled jobs. A job that this
controller does not carry is therefore not an error of the job: another runner can carry it.

ADR-0007 and PR-031 also say that only VMs of this fleet serve the queue. Under that rule, no
other runner exists, and a skipped job can only wait until Buildkite expires it. The owner decided
on 2026-11-03 that unmatched jobs stay skipped, so that another runner can take them, and that
the macOS queue may also be served by other agents or stacks.

If this controller reserves such a job and finishes it, it ends a job that a second runner could
have run. The owner decided on 2026-11-01 (decision D1) that this controller always skips an
unmatched job. A job is unmatched if its tags match no profile, if a rule is not a literal
`key=value`, or if a tag value is one that the matcher refuses (a comma, a newline, a carriage
return, a quote, padding, or a reserved key).

Invariant I-8 stays: an unmatched job uses no VM slot. The Stacks API notification of a job
accepts a detail of up to 256 characters (read 2026-11-01), so the old message that listed the
`match` map of every profile does not fit.

## Goals and non-goals

Goals:

* **Never fail a job that another runner can carry.**
* **Tell the pipeline author.** The job page shows that this stack skipped the job, and why.
* **Use no slot and no rate limit for it.** A skipped job costs no reservation, no token, and no
  VM.
* **Stay quiet.** One notification and one log line for each job, not one for each tick.

Non-goals:

* A way to make the skip optional. The owner chose one behaviour.
* A rule that cancels an unmatched job after a time. Buildkite and the pipeline own the timeout.

## Options considered

**Fail the job (ADR-0006).** The pipeline author sees a fast failure. However, the controller
fails jobs that another runner could carry, and a queue with two stacks cannot work.

**Fail the job unless an option says to skip.** One option for each queue. However, the default
still fails jobs, and two modes double the tests and the documents.

**Skip the job, and say so once.** The job stays `scheduled`. The controller writes one
notification, one log line, and one count. However, a mistake in a profile no longer fails fast.

**Skip the job without a notification.** The simplest. However, the pipeline author then waits
with no reason on the job page.

| Goal | Fail | Fail unless option | Skip and say once | Skip silently |
|---|---|---|---|---|
| Never fail a job that another runner can carry | No | No | Yes | Yes |
| The pipeline author sees why | Yes | Yes | Yes | No |
| No slot, token, or reservation | No | No | Yes | Yes |
| Quiet | Yes | Yes | Yes, with a set of notified jobs | Yes |

## Decision

Skip a job that matches no profile.

* The controller MUST NOT reserve, finish, or give a slot to an unmatched job.
* The controller MUST write one stack notification for the job. The detail says in short why no
  profile carries the job, and it fits in 256 characters.
* The controller MUST write the full message once, at info level, in the log. It has the job
  identifier, the tags of the job, and the `match` map of each profile.
* The controller MUST count a skipped job each time that it writes the notice. The counter has no
  job identifier (§10 of the target design).
* The controller keeps the identifiers of the jobs that it notified in a bounded set in memory,
  so that it does not notify or log again at each tick. After a restart, it MAY notify a job
  once more.
* While the queue is `dispatch_paused`, the controller does nothing new.

This decision replaces only the sentence of ADR-0006 that says that a job with no matching
profile fails immediately, and the consequence "A mismatch fails immediately with a message". The
rest of ADR-0006 holds, so ADR-0006 keeps its status. ADR-0015 did the same for ADR-0010.

This decision also amends ADR-0007:

* The macOS queue MAY also be served by other agents or stacks. This replaces the rule of ADR-0007
  that only VMs of this fleet run the jobs of the queue: the goal "Job separation", its Decision
  sentence "Only VMs of this fleet run the jobs of this queue", the consequence "Only VMs of this
  fleet run macOS jobs", and its Confirmation.
* The capacity accounting of this controller MUST count only the workers that its profiles
  select. A job that another runner can take is not a concern of this controller.
* The rest of ADR-0007 holds: the fleet has a dedicated self-hosted queue, the cluster MAY contain
  other queues, the fleet runs no job of another queue, and a leaked cluster agent token reaches
  the other self-hosted queues of the cluster. ADR-0007 keeps its status.

## Consequences

Good:

* This controller never fails a job that another stack or agent of the queue can carry.
* A skipped job costs no reservation call, no token, and no VM.
* The pipeline author sees the reason on the job page.

Bad:

* A misconfigured profile no longer gives a fast failure. The pipeline author sees a notification
  on the job page, and the job waits.
* A job can wait until Buildkite times it out, or until a person cancels it.
* Jobs that never leave the list fill the head of the list that the controller reads (§6.3 of
  the target design). More than 3 pages of 1000 skipped jobs make the jobs behind them wait for
  later reads, and the counter `bkaso.queue.truncated` is the sign.
* After a restart, the controller can write a second notification for a job.
* Another agent or stack on the queue runs untrusted macOS builds as well. ADR-0004 binds the VMs
  of this fleet only. The operator decides the isolation of each other runner of the queue.
* The free slots and the waiting jobs that this controller reports cover only its own workers and
  the jobs that its profiles carry. They do not show the capacity of the other runners.

## Confirmation

* A launcher test confirms that an unmatched job is not in the reserve call, has no finish call,
  gets one notification, and is not notified at a second tick.
* A launcher test confirms the same for a rule that is not a literal, and for a refused value.
* A launcher test confirms that a batch of matched and unmatched jobs reserves only the matched
  jobs.
* A test confirms that the set of notified jobs has a bound and evicts jobs that left the list.
* The property tests (I-8) confirm that an unmatched job has no VM.
* Before the controller goes live, a review of the queue lists each agent and stack that serves
  it (T-140).
