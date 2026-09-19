# bk-agent-stack-orchard — Product requirements

* Status: accepted
* Date: 2026-09-19
* Approved: 2026-09-20
* Owner: Josh Wright
* Related documents:
  * [Target design](../architecture/target-design.md)
  * [Architecture decision records](../adrs/README.md)
  * [System design baseline](../architecture/system-design-baseline.md)

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document uses ASD-STE100 Simplified Technical English and the third person.

Each requirement has one of these status values:

* `proposed`: the owner stated this intent in a statement, an answer, or a review. The owner has
  not accepted the requirement yet.
* `accepted`: the owner accepted the requirement.
* `open`: a decision is necessary before the requirement is complete.

`UNKNOWN` shows a value that no source gives yet. Section 8 lists the questions.

## 1. Problem and intended outcome

Buildkite macOS jobs for an iOS app need an execution environment. The owner wants these jobs to
run on self-hosted Apple Silicon Macs for three reasons: cost, data sovereignty, and performance.
The comparison basis for cost and performance is the equivalent Buildkite hosted macOS agent.
Buildkite does not start agents for a self-hosted queue. Each job needs a clean macOS VM, and
each host can run only two macOS VMs at the same time.

The intended outcome has five parts:

* Pipeline authors run macOS jobs with standard Buildkite pipeline syntax.
* Each job runs in a new macOS VM on the self-hosted fleet.
* The number of VMs follows the number of scheduled jobs, up to the free capacity.
* A job that cannot run fails with a clear message in Buildkite.
* Source code stays on owner infrastructure, in the operating country.

The horizon has four phases. Development uses one Mac, and the pilot uses 2 to 4 Macs. Adoption
uses up to 10 Macs, and scale uses 10 to 50 Macs. The pilot starts in approximately 1 month, and
adoption and scale follow in approximately 3 months (from 2026-09-14).

## 2. Actors

| Actor | Description | Permissions |
|---|---|---|
| Pipeline author | Writes Buildkite pipelines that build and test an iOS app. | Sets `agents:` tags and retry rules in a pipeline. Cannot select an arbitrary VM image, and cannot change the VM profiles. |
| Fleet operator | Operates the Mac hosts, Orchard, and the controller. At present, one person does this work. | Changes the profile configuration, the credentials, and the deployment. |
| Buildkite | The SaaS CI control plane. | Schedules jobs, and shows job results and stack notifications. |

## 3. Scope

In scope:

* Execution of Buildkite macOS jobs from one queue, with one new VM for each job.
* Selection of a VM profile from the agent tags of a job.
* Visible failure of jobs that cannot run.
* Configuration, credentials, readiness, and telemetry for the fleet operator.

Non-goals:

* Pools of started, idle VMs.
* The addition or removal of Mac hosts. The host pool is fixed.
* The purchase and the budget of the hosts.
* Host directory caches, or other state that jobs share.
* Secret injection for each job. Jobs get secrets through standard Buildkite mechanisms.
* High availability of the controller or of Orchard.
* Wildcard and negated tag rules.
* More than one queue in one controller process.
* Operation in more than one region. An additional region uses a new queue with its own fleet.
* The build of the VM images.

Protected experience:

* Pipeline authors MUST NOT need pipeline syntax other than standard Buildkite `agents:` tags and
  retry rules.
* The Buildkite job page MUST stay the place where pipeline authors see job progress and failures.

## 4. Journeys

**J1: Run a macOS job.** A pipeline author adds a step with `agents: {queue: <macOS queue>}` and
optional tags. Buildkite schedules the job. If the tags match exactly one profile, a new VM starts
for the job. The agent in the VM runs the job, and the VM is deleted after the job ends. If no VM
slot is free, the job stays in the queue until a slot is free. If the job is cancelled before it
starts, its VM is deleted.

**J2: A job cannot run.** The tags of a job match no profile, or the VM of the job does not start.
The job fails with a message on the job page. If the pipeline has the recommended retry rule,
Buildkite tries the job again one time.

**J3: Change the profiles.** The fleet operator changes the configuration file and restarts the
controller. If the configuration is not valid, the controller does not start, and it reports the
rule that failed.

**J4: Replace the cluster agent token.** The fleet operator creates a new token, supplies it to the
controller, and revokes the old token when no VM from before the change exists. Jobs in progress
continue.

**J5: Recover from a failure.** The controller restarts, or Orchard or Buildkite is not available
for a period. VMs of jobs in progress continue to run. After the recovery, the controller deletes
the VMs of ended jobs and starts VMs for waiting jobs.

## 5. Requirements

### 5.1 Job execution

**PR-001: Run each job in a new VM.**

* Requirement: The product MUST run each matched job on the queue in a new macOS VM. The VM MUST
  be on a self-hosted host.
* Status: `accepted`.
* Acceptance: Given a job whose tags match one profile, and a free VM slot. When Buildkite
  schedules the job. Then an agent in a new VM acquires the job, and no other job runs in that VM.

**PR-002: Delete the VM when the job ends.**

* Requirement: The product MUST delete the VM of a job after the job reaches a terminal state:
  `finished`, `canceled`, `expired`, `timed_out`, `skipped`, or `broken`. The product MUST NOT use
  a VM for a second job.
* Status: `accepted`.
* Acceptance: Given a job that runs in a VM. When the job reaches a terminal state. Then the VM is
  deleted, and its slot becomes free for another job. The maximum time until deletion is
  `UNKNOWN` (see the system design baseline).

**PR-003: Select the VM profile by exact tag match.**

* Requirement: The product MUST select a profile only when the agent tags of the job match its
  `match` map exactly. The comparison ignores the `queue` tag.
* Status: `accepted`.
* Acceptance:
  * Given a profile with `match: {xcode: "27"}`. When a job has the tags
    `{queue: macos, xcode: "27"}`. Then the job uses that profile.
  * When a job has the tags `{queue: macos, xcode: "27", size: large}`. Then the job does not
    use that profile.
  * Given a profile with `match: {}`. When a job has only the `queue` tag. Then the job uses that
    profile.

**PR-004: Fail an unmatched job immediately.**

* Requirement: If no profile matches a job, or a tag rule is not `key=literal`, the product MUST
  fail the job immediately. The message MUST list the tags of the job and the `match` maps of all
  profiles. An unmatched job MUST NOT use a VM slot.
* Status: `accepted`.
* Acceptance: Given a job whose tags match no profile. When Buildkite schedules the job. Then the
  job fails with `exit_status -1` and the message, and no VM starts for the job.

**PR-005: Keep jobs that do not fit in the queue.**

* Requirement: The product MUST NOT start more VMs than the free VM slots. A job that does not fit
  MUST stay in the `scheduled` state, and it MUST stay visible to Buildkite.
* Status: `accepted`.
* Acceptance: Given 3 matched jobs and 2 free slots. When Buildkite schedules the jobs. Then 2 VMs
  start, and the third job stays `scheduled`. When a slot becomes free, the third job starts.

**PR-006: Start jobs in Buildkite order.**

* Requirement: When jobs wait for slots, the product MUST start them in order of Buildkite
  `priority` (highest first), and then of `scheduled_at` (oldest first).
* Status: `accepted`.
* Acceptance: Given two waiting jobs with different priorities and one free slot. When the slot
  becomes free. Then the job with the higher priority starts first.

**PR-007: Delete the VM of a cancelled job.**

* Requirement: If a job is cancelled while its VM starts, the product MUST delete the VM, and the
  job MUST NOT run.
* Status: `accepted`.
* Acceptance: Given a job whose VM is booting. When a person cancels the job. Then the VM is
  deleted, and the job does not run.

**PR-008: Fail a job whose VM does not start.**

* Requirement: Before an agent acquires a job, the product MUST fail the job with a message if its
  VM fails. The same rule applies if the VM does not start within the configured timeouts. After an
  agent acquires the job, the product MUST NOT fail the job. Buildkite then controls the result of
  the job.
* Status: `accepted`.
* Acceptance:
  * Given a profile with an image that does not exist. When a job uses the profile. Then the job
    fails with a message after the boot timeout.
  * Given an image whose agent does not start. When a job uses the image. Then the job fails after
    the acquire timeout, and the message contains the last startup-script log lines.

**PR-009: Show progress on the job page.**

* Requirement: The product MUST send a Buildkite stack notification at each step. The steps are:
  VM requested, VM placed on a host, VM booting, VM failed, and VM deleted.
* Status: `accepted`.
* Acceptance: Given a job that runs normally. When the job ends. Then its job page shows the
  notifications for each step.

**PR-010: Recover after a controller restart.**

* Requirement: After a restart, the product MUST NOT start a second VM for a job. It MUST delete
  the VMs of jobs that ended during the restart. A reserved job without a VM MAY wait for the
  remaining reservation time before it starts. The target design sets this time.
* Status: `accepted`.
* Acceptance: Given jobs in all states. When the controller stops and starts again. Then each job
  has a maximum of one VM, and no VM of an ended job remains.

**PR-011: Obey a paused queue.**

* Requirement: While the queue is `dispatch_paused`, the product MUST NOT start VMs and MUST NOT
  fail unmatched jobs. VMs of jobs in progress MUST continue.
* Status: `accepted`.
* Acceptance: Given a paused queue with waiting jobs. When time passes. Then no VM starts, and no
  job fails.

**PR-012: Give pipelines a retry signal.**

* Requirement: A job that the product fails MUST have `signal_reason: stack_error`. Pipelines on
  the queue SHOULD retry such a job one time.
* Status: `accepted`.
* Acceptance: Given a pipeline with the retry rule. When the product fails a job because a host
  went offline. Then Buildkite tries the job again one time.

**References.** ADR-0001, ADR-0002, ADR-0003, and ADR-0006 record the decisions behind these
requirements. The target design describes the job lifecycle and the failure procedures.

### 5.2 Fleet operation

**PR-020: Configure VM profiles in one file.**

* Requirement: The fleet operator MUST configure all profiles in one configuration file. A profile
  sets the match tags, image, CPU, memory, disk, worker labels, network allowlist, VM credentials,
  and timeouts. A configuration change MUST apply after a restart.
* Status: `accepted`.
* Acceptance: Given a new profile in the configuration file. When the controller restarts. Then
  jobs with the tags of the new profile use it.

**PR-021: Reject a configuration that is not valid.**

* Requirement: The controller MUST NOT start if a profile rule fails. The rules forbid duplicate
  names, equal `match` maps, and empty or `queue` match keys. They also forbid timeouts longer
  than the job token lifetime. A profile that uses the SSH startup-script transport MUST have VM
  credentials.
* Status: `accepted`.
* Acceptance: Given two profiles with equal `match` maps. When the controller starts. Then it
  stops and reports the rule that failed.

**PR-022: Supply secrets through environment variables.**

* Requirement: The configuration file MUST NOT contain secrets. The controller MUST read secrets
  from environment variables, and it MUST NOT depend on a specific platform or secret store.
* Status: `accepted`.
* Acceptance: Given the secrets as environment variables and no secrets in the file. When the
  controller starts. Then it operates.

**PR-023: Replace the cluster agent token without job failures.**

* Requirement: The fleet operator MUST replace the cluster agent token with the procedure in the
  target design. The product MUST NOT fail jobs in progress because of the replacement.
* Status: `accepted`.
* Acceptance: Given jobs in progress. When the fleet operator follows the procedure. Then the jobs
  complete, and new jobs start with the new token.

**PR-024: Report readiness.**

* Requirement: The controller MUST report that it is not ready when a data source is not
  synchronized within 30 s. It MUST also report this when the Stacks API returns HTTP 401.
* Status: `accepted`.
* Acceptance: Given a revoked cluster agent token. When the controller calls the Stacks API. Then
  the readiness check fails.

**PR-025: Provide telemetry and alert conditions.**

* Requirement: The controller MUST export metrics, traces, and logs through OpenTelemetry only.
  Any OpenTelemetry-compatible provider MAY receive the data. The target design defines the
  alert conditions.
* Source: owner statements (2026-09-13 and 2026-09-14). Status: `accepted`.
* Acceptance: Given an OpenTelemetry Collector. When jobs run. Then the collector receives the
  metrics, traces, and logs, and each job has one trace.

**PR-026: Provide a pilot profile for iOS app builds.**

* Requirement: The pilot MUST have a profile with Xcode 27 on macOS 27 (Golden Gate), for iOS app
  builds.
* Source: owner answer Q-P4 (2026-09-14); owner comment (2026-09-19). Status: `accepted`.
* Facts (as of 2026-09-19): Apple released macOS 27 (Golden Gate) and Xcode 27 (build 27A266a) on
  2026-09-14. Xcode 27 needs macOS Tahoe 26.6 or later and an Apple silicon Mac, and it contains
  the iOS 27 SDK. `ghcr.io/cirruslabs/macos-golden-gate-base:latest` gives the macOS 27 base image.
  No published Tart image has Xcode 27, so the fleet builds the Xcode layer with
  `templates/xcode.pkr.hcl` and `macos_version = golden-gate`.
* Acceptance: Given the pilot profile. When a job builds and tests the iOS app. Then the job uses
  macOS 27, Xcode 27, and the iOS 27 SDK.

**References.** ADR-0005, ADR-0008, and ADR-0009 record the decisions behind these requirements.
The target design describes the configuration, the credentials, and the observability.

### 5.3 Security and data sovereignty

**PR-030: Isolate each job VM.**

* Requirement: Each job VM MUST serve one job only, and the controller MUST NOT use it again. The
  VM MUST NOT reach the host, another VM, or a private address that its profile does not permit.
  The VM MUST NOT have host directory mounts. The VM MUST receive only a job acquisition token for
  its own job. The cluster agent token and the Orchard credentials MUST NOT go into a VM.
* Source: owner statement (2026-09-13). Status: `accepted`.
* Acceptance: Given a job that tries to reach the host, another VM, or a private address that the
  profile does not permit. When the job runs. Then the connection fails, and the VM contains no
  cluster agent token.

**PR-031: Use a dedicated queue for the macOS fleet.**

* Requirement: The macOS fleet MUST have its own Buildkite queue. Other agents MUST NOT serve this
  queue. The cluster MAY contain other queues.
* Source: owner comment (2026-09-19). Status: `accepted`.
* Acceptance: Given the Buildkite cluster. When the fleet operator reviews the queue before the
  controller goes live. Then only VMs of this fleet serve the queue.

**PR-032: Keep checkouts, build data, and artifacts in the operating country.**

* Requirement: Source code checkouts and build data MUST stay on owner infrastructure, in the
  operating country. The hosts and the job VMs MUST be in the operating country. Build artifacts
  MUST go to storage that the owner manages, in the operating country. The location of the Git
  provider is outside the scope of this product.
* Source: owner answers Q-A2 and Q-A8 (2026-09-14). Status: `accepted`.
* Acceptance: Given a pilot pipeline that uploads artifacts. When a job runs. Then the artifacts are
  in owner-managed storage in the operating country. Buildkite-managed storage has no artifacts of
  the job.

**PR-033: Store build logs in Buildkite.**

* Requirement: Build logs MAY go to Buildkite SaaS, although logs can contain parts of source code,
  for example compiler errors.
* Facts (as of 2026-09-14): Buildkite stores job logs in Amazon Web Services, and its
  documentation states no region. Private job log archiving is available only on the Enterprise
  plan, and only to Amazon S3 buckets in `us-east-1`.
* Source: owner answer Q-A7 (2026-09-14). Status: `accepted`.
* Acceptance: Given a job that writes log output. When the job runs. Then the log is visible on the
  Buildkite job page.

**References.** ADR-0004, ADR-0007, and ADR-0008 record the decisions behind these requirements.
The target design describes the trust model and the data sovereignty rules.

Quality targets that drive the architecture, such as scale, start time, and availability, are in
the [system design baseline](../architecture/system-design-baseline.md). This document does not
repeat them.

## 6. Outcome measures

Functional acceptance (section 5) shows that the product behaves as specified. The measures in
this section show whether the product gives the intended business effect.

| Measure | Baseline | Target | Window | Evidence source |
|---|---|---|---|---|
| VMs that remain after their job ends | None, new product | 0 | Pilot, 2 weeks | Telemetry `vm.deletes`; pilot exit criteria of the target design |
| Jobs that the controller fails incorrectly | None, new product | 0 | Pilot, 2 weeks | Telemetry `jobs.failed_by_controller`; pilot exit criteria of the target design |
| Time from `scheduled` to `accepted`, with the image on the host | None | 60 s | From the pilot | Telemetry `job.phase.duration` |
| Wait time of the oldest job when all slots are in use | None | 15 min at planned capacity | From the pilot | Telemetry `jobs.oldest_waiting.age` |
| Cost of one job minute, compared with Buildkite hosted macOS | Hosted Medium: 0.12 USD (A$0.17); hosted Large: 0.24 USD (A$0.34). Rate: 1 USD = 1.39842141 AUD (xe.com, 2026-09-14 04:37 UTC). Host: AU$5,099.00 each. | `UNKNOWN` | From adoption | Buildkite pricing; host cost |
| Duration of the same job, compared with Buildkite hosted macOS | `UNKNOWN` | `UNKNOWN` | Pilot | Buildkite job durations for both |

## 7. Dependencies and assumptions

Dependencies:

* The Buildkite Stacks API and job acquisition tokens operate as their documentation states (as
  of 2026-09-13).
* The VM image contains `buildkite-agent` v4. Buildkite made v4 the stable line on 2026-09-01, and
  v4.0.4 is current (2026-09-18). Agent v4 stops immediately when a flag is not defined.
* The VM image runs the Tart guest agent, which the startup-script transport needs (ADR-0009).
* Orchard 0.57.0 or later is in use. This release contains pull requests #465 and #470
  (2026-09-15).
* The Mac mini with M5 Pro ships from 2026-09-22 (Apple, as of 2026-09-14). The pilot needs the
  hosts before 2026-10-14.
* The fleet operator builds a Tart image from `ghcr.io/cirruslabs/macos-golden-gate-base` with
  Xcode 27, the `buildkite-agent` binary, and the Tart guest agent, and stores it in an OCI
  registry (PR-026). No published Tart image has Xcode 27 (as of 2026-09-19).
* The fleet operator pulls each profile image to each host before use.
* Owner-managed artifact storage exists in the operating country (PR-032).

Assumptions:

* One person operates the fleet (ADR-0001).
* The pilot uses only non-critical pipelines that opt in (target design, rollout phase 1).
* Each host is a Mac mini with an M5 Pro chip, 64 GB memory, 1 TB storage, and 10 Gb Ethernet. The
  pre-order price is AU$5,099.00 (owner answer Q-P1).

Items that can reverse the product direction:

* If Buildkite hosted macOS agents accept custom base images, ADR-0001 needs a review.
* If the fleet must run in more than one region, each region needs a new queue with its own fleet.
  The queue rule of ADR-0007 then needs a review.

## 8. Questions

Answered questions (2026-09-14):

| ID | Question | Answer |
|---|---|---|
| Q-P1 | Comparison basis for cost and performance | The equivalent Buildkite hosted macOS agent. Each host is a Mac mini with an M5 Pro chip (18-core CPU, 20-core GPU, 16-core Neural Engine), 64 GB memory, 1 TB storage, and 10 Gb Ethernet. The pre-order price is AU$5,099.00 (21:00 AEST, 2026-09-14). |
| Q-P2 | Maximum time from `scheduled` to `accepted` | 60 s |
| Q-P3 | Maximum wait time when all slots are in use | 15 min, dependent on capacity |
| Q-P4 | Pilot pipelines and versions | The latest macOS and the latest Xcode, for an iOS app. The pilot uses macOS 27 (Golden Gate) with Xcode 27. |
| Q-P5 | Dates of the phases | Pilot in 1 month; adoption and scale in 3 months |
| Q-P6 | Start point of the phase dates | Not applicable. The dates are approximate. |
| Q-P7 | Pilot profile | Xcode 27. The owner selected Xcode 26.6 on 2026-09-14 and changed this to Xcode 27 on 2026-09-19, after its release. |

No product question needs an owner decision. Three values stay `UNKNOWN` until the pilot measures
them: the maximum time from the job end to the VM deletion (PR-002, QA-06), the cost of one job
minute, and the duration of the same job (section 6).
