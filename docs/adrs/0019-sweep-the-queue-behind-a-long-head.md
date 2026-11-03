---
status: proposed
date: 2026-11-03
decision-makers: maintainers
---

# ADR-0019: Sweep the queue behind a long head

## Summary

The controller reads at most 3 pages of 1000 scheduled jobs in one tick, from the head of the
list. ADR-0016 leaves a job that matches no profile in the list. A pipeline author can therefore
put more than 3000 such jobs at the head of the queue, and every job behind them is never read.
The fleet then stays idle for every pipeline of the queue. The decision is to read 2 pages from
the head and 1 page from a sweep. The sweep moves through the rest of the list, one page in each
tick, and starts again after the head at the end of the list. A read still makes at most 3 list
requests. A longer flood makes a job behind it wait longer, but it cannot hide the job. The cost
is that a job behind the head is in one read of each sweep, not in every read.

## Context

The scheduled-jobs call of the Stacks API takes a page size (`limit`, at most 1000) and a cursor
(`after`). The answer orders the jobs by `scheduled_at`, oldest first. The call has no filter by
tag or pipeline, no other order, and no cursor that reads backwards (`buildkite/stacksapi` v1.2.0
`scheduled_jobs.go`, read on 2026-11-03; Stacks API documentation, read on 2026-11-01). The only
way to a job is to read every page in front of it, from the head or from a cursor that an earlier
answer gave.

The list limit of the Stacks API is 10 requests each second, and the controller reads the queue
at each tick of 1 s. The time budget of a tick (§4 of the target design) counts the read of the
queue as one call that can hang, because a page that fails ends the read. These two facts limit
the requests of one read to a small, fixed number.

ADR-0016 keeps an unmatched job in the list until another runner of the queue takes it, Buildkite
times it out, or a person cancels it. Such a job stays at the head, because new jobs arrive behind
it. A security review on 2026-11-03 found that one pipeline upload of 3000 or more steps with tags
that no profile carries hides every later job of the queue. The counter `bkaso.queue.truncated` and
a warning existed, but no alert used them, and nothing recovered without a person.

## Goals and non-goals

Goals:

* **No hidden job.** A job behind any number of jobs that never leave the list reaches admission
  in a time that grows with the length of the list, and never stops.
* **A fixed cost.** One read makes at most 3 list requests, whatever the length of the queue, so
  the rate limit and the time budget of a tick hold.
* **No gain from a longer flood.** More jobs at the head can make a job wait longer, but cannot
  hide it.
* **The same whole listing.** The launcher forgets notified jobs only after a whole listing. The
  meaning of a whole listing does not change.
* **A clear signal.** An operator learns that the queue is longer than one read, and which
  pipeline is the likely source.

Non-goals:

* Finishing or cancelling unmatched jobs. ADR-0016 holds.
* Fair shares between pipelines. Admission keeps the order by priority and age.
* A filter on the server side. The API has none.

## Options considered

**Keep 3 pages from the head, and alert.** No change of code. However, a flood of 3000 jobs still
hides every job behind it until a person acts.

**Read more pages until the free slots are full.** The poll reads until enough matched jobs fill
the free slots. However, without a cap the requests grow with the flood, and with a cap a longer
flood defeats it in the same way as today. The poll would also need the Matcher.

**Read from the tail when the head is full.** New jobs are at the tail. However, the API has no
other order and no backward cursor, so the only way to the tail is to read every page before it.

**Cap the unmatched jobs of each pipeline.** Admission considers at most a number of unmatched
jobs from each pipeline. However, the hidden jobs are hidden because the poll never reads them,
and a cap on what admission considers does not read one more page. A flood from several
pipelines also passes a cap for each pipeline.

**Read more pages from the head.** For example 10 pages. However, the limit only moves, the
requests of each tick grow, and the time budget of a tick shrinks.

**Read 2 pages from the head and 1 page of a sweep.** The poll keeps, in memory, the cursor where
the last read stopped, and continues from it. At the end of the list, the sweep starts again after
the head. However, a job behind the head is in one read of each sweep, and every read sees 2000
head jobs, not 3000.

| Goal | Alert only | Read until full | Read the tail | Cap each pipeline | More head pages | Head and sweep |
|---|---|---|---|---|---|---|
| No hidden job | No | Only without a cap | Not possible with the API | No | No | Yes |
| A fixed cost | Yes | Only with a cap | Not possible with the API | Yes | Yes, higher | Yes |
| No gain from a longer flood | No | No | Not possible with the API | No | No | Yes |
| The same whole listing | Yes | Yes | Not possible with the API | Yes | Yes | Yes |
| A clear signal | Yes | Yes | Not possible with the API | Yes | Yes | Yes |

## Decision

Read 2 pages from the head and 1 page of a sweep. Alert on a read that is not whole.

* One read of the queue MUST make at most 3 list requests (`MaxPages`).
* A read MUST read the first 2 pages from the head of the list (`HeadPages`).
* If the head has a next page, the read MUST read 1 more page from the sweep cursor. If the sweep
  cursor is empty, the read MUST use the cursor that the head gave.
* The controller MUST keep the sweep cursor in memory from one read to the next. It MUST empty
  the cursor when the sweep reaches the end of the list, when the head reaches the end of the
  list, and when a read fails.
* If the sweep page fails, the read MUST keep the head pages as its answer, which is not whole,
  and MUST write a warning that gives the error. The head pages arrived whole, and a read that
  kept nothing would fail at every other tick when Buildkite refuses an old cursor.
* A read is whole only when its pages follow one another from the head to the end of the list.
  Each other read MUST write a warning, and MUST add 1 to `bkaso.queue.truncated`. The warning
  SHOULD name the pipeline that has the most jobs of the read.
* An operator MUST alert when `bkaso.queue.truncated` increases for 10 min (§10.6 of the target
  design).

The decision changes only how the poll reads the list. ADR-0016 still decides what the controller
does with an unmatched job.

## Consequences

Good:

* No number of jobs at the head can hide a job behind them. With a tick of 1 s, a job behind N
  jobs reaches a read in about (N − 2000) / 1000 s.
* A read makes at most 3 list requests, as before, so the rate limit and the time budget of a
  tick do not change.
* A queue of up to 3000 jobs gives the same read as before: 3 pages in sequence, which is a whole
  listing.
* The warning names the likely source of a flood, and the alert tells an operator to act.

Bad:

* Every read sees 2000 jobs from the head, not 3000. In a queue of more than 3000 jobs, a job at a
  place from 2001 to 3000 is in the first read of each sweep only.
* A job behind the head is in one read of each sweep. If no slot is free at that read, the job
  waits for the next sweep. Each 1000 jobs of a flood add about 1 s to that wait.
* Whether Buildkite accepts a cursor in a later tick is `UNKNOWN` (owner: the maintainers; trigger:
  the pilot, or the first failed read of a sweep page). If it does not, each sweep page with a
  cursor of an earlier read fails. That read keeps the 2000 head jobs, and the next sweep starts
  again after the head, with a cursor of the same read. The controller then sees 3000 jobs at
  every other read and 2000 jobs at the others, so no job behind the first 3000 jobs reaches a
  read. `cicd.system.errors` and the warning of the read then show the failed calls.
* The sweep gives the launcher new unmatched jobs at each read. With more than 10 000 unmatched
  jobs in the list (`NotifiedLimit`), the launcher forgets a job before the sweep returns to it.
  The job then gets its notice again at each sweep, and it counts again in `bkaso.job.skipped`.
  This costs at most one notification call of 1000 lines in each tick. Buildkite refuses a notice
  after the 100th of a job (§3 of the target design).
* A flood still slows the fleet. The alert is the protection, and a person cancels the build.

## Confirmation

* Unit tests of `internal/stack` confirm that a job behind 40 pages of jobs that stay in the list
  arrives at the read that the sweep reaches it, and that each read makes at most `MaxPages`
  calls.
* Unit tests confirm the cursors of each read, the start of a new sweep at the end of the list,
  after a failed read, and after a short queue, and that only a read of the pages in sequence to
  the end is whole. A unit test confirms that a failed sweep page leaves the head pages as the
  answer.
* A unit test confirms that the warning names the pipeline with the most jobs of the read.
* The component test T-026 confirms that a job behind 6500 unmatched jobs gets a VM, inside the
  request budget of the Stacks API.
