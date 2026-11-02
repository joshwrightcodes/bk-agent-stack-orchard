# bk-agent-stack-orchard — Product requirements

* Status: accepted
* Date: 2026-09-19
* Approved: 2026-09-20
* Owner: the maintainers
* Related documents:
  * [Target design](../architecture/target-design.md)
  * [Architecture decision records](../adrs/README.md)
  * [System design baseline](../architecture/system-design-baseline.md)

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document follows the [writing guide](../writing-guide.md), in the third person.

Each requirement has one of these status values:

* `proposed`: the owner stated this intent in a statement, an answer, or a review. The owner has
  not accepted the requirement yet.
* `accepted`: the owner accepted the requirement.
* `open`: a decision is necessary before the requirement is complete.

`UNKNOWN` shows a value that no source gives yet. Section 8 lists the questions.

## 1. Problem and intended outcome

Buildkite macOS jobs for an iOS app need an execution environment. The owner wants these jobs to
run on self-hosted Apple silicon Macs for three reasons: cost, data sovereignty, and performance.
The comparison basis for cost and performance is the equivalent Buildkite hosted macOS agent.
Buildkite does not start agents for a self-hosted queue. Each job needs a clean macOS VM, and
each host can run only two macOS VMs at the same time.

The intended outcome has five parts:

* Pipeline authors run macOS jobs with standard Buildkite pipeline syntax.
* Each job runs in a new macOS VM on the self-hosted fleet.
* The number of VMs follows the number of scheduled jobs, up to the free capacity.
* A job whose VM cannot start fails with a clear message in Buildkite. A job that no profile
  carries stays scheduled, with a notification on its job page, for another runner of the queue.
* Source code stays on owner infrastructure, in the operating country.

The horizon has four phases. Development uses one Mac, and the pilot uses 2 to 4 Macs. Adoption
uses up to 10 Macs, and scale uses 10 to 50 Macs. The dates of the phases are `UNKNOWN`
(checked 2026-11-01). The maintainers own the dates. They become known when a Buildkite
test cluster and the Mac hosts are ready. The plan of 2026-09-14 estimated that the pilot starts
in approximately 1 month, and that adoption and scale follow in approximately 3 months.

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
* Visible failure of jobs whose VM cannot start, and a visible skip of jobs that no profile carries.
* Configuration, credentials, readiness, and telemetry for the fleet operator.
* The build of the job VM images. This was a non-goal until the owner decided on 2026-10-10 that
  the project builds them (ADR-0012).

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

**J2: A job cannot run.** The VM of the job does not start. The job fails with a message on the
job page. The Buildkite platform generates the `signal_reason` `stack_error` for it (PR-012), and
the automatic retry rule of the pipeline decides if Buildkite tries the job again. If the tags of
a job match no profile, the controller skips the job (PR-004): it writes one notification on the
job page, and the job stays scheduled for another stack or agent of the queue.

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

**PR-004: Skip an unmatched job.**

* Requirement: If no profile matches a job, or a tag rule is not `key=literal` (a key or a value
  with a comma, a newline, a carriage return, or a quote is not a literal), the product MUST
  skip the job. The product MUST NOT reserve the job, MUST NOT finish the job, and MUST NOT give
  it a VM slot. The job stays `scheduled`, so that another stack or agent of the queue can carry
  it. The product MUST write one stack notification on the job page, which says in short why no
  profile carries the job. The product MUST write that notification once for a job while the job
  stays listed and the process runs. The product MAY write it again after a restart, after
  the job left the list and came back, and when the number of jobs that it remembers passes the
  cap of the design (10 000). The product MUST write the full message, with the tags of
  the job and the `match` maps of all profiles, in the log each time that it writes the
  notification. The product MUST count a skipped job each time that it writes the notification.
  A job can have at most 100 notifications, so the product keeps the number of notices low.
* Status: `accepted`. Decision D1 of the owner, 2026-11-01, replaced "fail immediately" with
  "skip". ADR-0016 records it.
* Acceptance: Given a job whose tags match no profile, and a free VM slot. When Buildkite
  schedules the job, and the controller runs 2 ticks. Then the controller does not reserve the
  job, does not finish it, and starts no VM for it. The job page has one notification, and the
  counter of skipped jobs rises by 1. A matched job in the same tick starts as normal.

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
  write new notifications for unmatched jobs. VMs of jobs in progress MUST continue.
* Status: `accepted`.
* Acceptance: Given a paused queue with waiting jobs. When time passes. Then no VM starts, and no
  job fails. No new notification appears for an unmatched job.

**PR-012: Give pipelines a retry signal.**

* Requirement: The product MUST finish a job that it fails with exit status -1 and a detail. The
  Buildkite platform then generates the `signal_reason` `stack_error` for the job. The product
  cannot set the `signal_reason` itself, because the finish request has no such field. Pipelines
  on the queue use `signal_reason: stack_error` with `limit: 0` in their automatic retry rules
  (Buildkite Stacks API documentation, read on 2026-11-01). Whether the platform generates
  `stack_error` for every job that the product finishes is `UNKNOWN` (owner: the maintainers;
  trigger: the first runs of T-100 to T-105).
* Status: `accepted`.
* Acceptance: Given a pipeline with the retry rule. When the product fails a job because a host
  went offline. Then the job has `signal_reason: stack_error`, and the retry rule of the pipeline
  applies.

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
  than the job token lifetime. The controller MUST also refuse a reservation expiry that is not a
  whole number of seconds from 1 to 3600, which are the limits of the Stacks API. It MUST refuse
  a job token lifetime that is not a whole number of seconds from 1 to 1800, because the token
  goes into a VM that runs untrusted code. A profile MUST NOT use `queue`, `profile`, or
  `agent-stack` as a `match` key. No `match` value and no `buildkite.queue` MAY have a comma, a
  quote, a newline, or white space at an end. A `softnetAllow` entry MUST be an IPv4 CIDR range
  that is not wider than /16. A profile that uses the SSH startup-script transport MUST have VM
  credentials. A profile MUST name only variables that start with `VM_` as the source of a value
  for its VM, because the controller pod holds other secrets. A profile MUST NOT send agent
  telemetry over plain HTTP to a non-loopback address.
* Status: `accepted`.
* Acceptance: Given two profiles with equal `match` maps. When the controller starts. Then it
  stops and reports the rule that failed.
* Acceptance: Given a profile whose `otel.headersEnv` names `HTTPS_PROXY` or
  `OTEL_EXPORTER_OTLP_HEADERS`. When the controller starts. Then it stops and names the profile,
  the field, and the variable.

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
  synchronized within `stateFreshness`. It MUST also report this when the Stacks API returns
  HTTP 401.
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
  the iOS 27 SDK. No published Tart image had Xcode 27 on that date. Since 2026-10-10, the project
  builds the pilot image from `ghcr.io/cirruslabs/macos-golden-gate-vanilla` and an Xcode archive
  (ADR-0012).
* Acceptance: Given the pilot profile. When a job builds and tests the iOS app. Then the job uses
  macOS 27, Xcode 27, and the iOS 27 SDK.

**References.** ADR-0005, ADR-0008, and ADR-0009 record the decisions behind these requirements.
The target design describes the configuration, the credentials, and the observability.

### 5.3 Security and data sovereignty

**PR-030: Isolate each job VM.**

* Requirement: Each job VM MUST serve one job only, and the controller MUST NOT use it again. The
  VM MUST NOT reach the host, another VM, or a private address that its profile does not permit.
  The VM MUST NOT have host directory mounts. The VM MUST receive only a job acquisition token for
  its own job. The cluster agent token and the Orchard credentials MUST NOT go into a VM. The
  controller MUST refuse a profile that names a variable without the `VM_` prefix as the source of
  a VM value, and it MUST refuse any `BK_AGENT_STACK_ORCHARD_` or `OTEL_` name with a message
  that names the controller.
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

**PR-032: Keep checkouts and build data in the operating country.**

* Requirement: Source code checkouts and build data MUST stay on owner infrastructure, in the
  operating country. The hosts and the job VMs MUST be in the operating country. Build artifacts
  (for example, the files of `artifact_paths`) MAY go to Buildkite-managed storage. The location of
  the Git provider is outside the scope of this product.
* Source: owner answers Q-A2 and Q-A8 (2026-09-14); owner statement (2026-11-02): Buildkite-managed
  artifact storage is acceptable. Status: `accepted`.
* History: until 2026-11-02, this requirement stated that build artifacts MUST go to storage that
  the owner manages, in the operating country. The owner statement of 2026-11-02 replaced that
  rule and closed the open question about artifact credentials.
* Acceptance: Given a pilot pipeline that uploads artifacts. When a job runs. Then the artifact
  upload succeeds, and the artifact is visible on the job page. Checkouts and build data do not
  leave the host of the VM, except through the log and artifact services of Buildkite.

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
| VMs that remain after their job ends | None, new product | 0 | Pilot, 2 weeks | Telemetry `bk_agent_stack_orchard.vm.leftover`; pilot exit criteria of the target design (E-1) |
| Jobs that the controller fails incorrectly | None, new product | 0 | Pilot, 2 weeks | Telemetry `bk_agent_stack_orchard.job.failed`; pilot exit criteria of the target design (E-1) |
| Time from `scheduled` to `accepted`, with the image on the host | None | 60 s | From the pilot | Telemetry `bk_agent_stack_orchard.job.phase.duration` |
| Wait time of the oldest job when all slots are in use | None | 15 min at planned capacity | From the pilot | Telemetry `bk_agent_stack_orchard.job.waiting.oldest.age` |
| Cost of one job minute, compared with Buildkite hosted macOS | Hosted Medium: 0.12 USD (A$0.17); hosted Large: 0.24 USD (A$0.34). Rate: 1 USD = 1.39842141 AUD (xe.com, 2026-09-14 04:37 UTC). Host: AU$5,099.00 each. | `UNKNOWN` | From adoption | Buildkite pricing; host cost |
| Duration of the same job, compared with Buildkite hosted macOS | `UNKNOWN` | `UNKNOWN` | Pilot | Buildkite job durations for both |

The gauge `bk_agent_stack_orchard.vm.leftover` counts the VMs of this controller whose job is
in a terminal state. It is 0, except for the short window before the reaper deletes a VM, which
the target design defines. A deletion counter cannot show a VM that remains, because a VM that
remains is not deleted. The counter `bk_agent_stack_orchard.job.failed` counts every job that the
controller failed, so the pilot checks each increase against its job to decide whether the failure
was wrong.

## 7. Dependencies and assumptions

Dependencies:

* The Buildkite Stacks API and job acquisition tokens operate as their documentation states (as
  of 2026-09-13).
* Each VM runs `buildkite-agent` v4: a pinned release that the VM downloads when it starts, or the
  agent of the image (target design, section 5; ADR-0017). Buildkite made v4 the stable line on
  2026-09-01, and v4.9.0 is current (2026-10-30, checked 2026-11-01). Agent v4 stops immediately
  when a flag is not defined. The release archives are on GitHub, and a VM that downloads the agent
  reaches GitHub or a mirror that the operator sets.
* The VM image runs the Tart guest agent, which the startup-script transport needs (ADR-0009).
* Orchard 0.58.0 or later is in use (Orchard release 0.58.0, 2026-10-19; target design, section
  9.2). Release 0.57.0 contains pull requests #465 and #470 (2026-09-15), and release 0.58.0
  contains them too.
* The Mac mini with M5 Pro ships from 2026-09-22 (Apple, as of 2026-09-14). The pilot needs the
  hosts. The date at which the hosts are ready is `UNKNOWN` (checked 2026-11-01). The owner is the
  maintainer. The date becomes known when the Mac hosts are ready. The plan of 2026-09-14 estimated
  that the pilot needs the hosts before 2026-10-14.
* The fleet operator supplies each Tart image, and stores it in an OCI registry. The project
  publishes no image (owner decision, 2026-11-03). The operator uses an upstream Tart image, or
  builds an image of its own, and the image meets the image requirements of the target design,
  section 5: the Tart guest agent, and the `buildkite-agent` v4 binary when the profile uses the
  agent of the image. `images/` is a reference
  build of such an image, from `ghcr.io/cirruslabs/macos-golden-gate-vanilla`, with Xcode 27 for
  the pilot profile (PR-026, ADR-0012).
* The fleet operator pulls each profile image to each host before use.
* Buildkite-managed artifact storage is available to the organisation (PR-032; owner statement,
  2026-11-02).

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
| Q-P5 | Dates of the phases | Pilot in 1 month; adoption and scale in 3 months. History: on 2026-11-01 the dates became `UNKNOWN`, with the maintainers as owner and a trigger (section 1) |
| Q-P6 | Start point of the phase dates | Not applicable. The dates are approximate. |
| Q-P7 | Pilot profile | Xcode 27. The owner selected Xcode 26.6 on 2026-09-14 and changed this to Xcode 27 on 2026-09-19, after its release. |

No product question needs an owner decision. Three values stay `UNKNOWN` until the pilot measures
them: the maximum time from the job end to the VM deletion (PR-002, QA-06), the cost of one job
minute, and the duration of the same job (section 6). Two more values stay `UNKNOWN` until a
trigger occurs: the dates of the phases (section 1; trigger: a Buildkite test cluster and the Mac
hosts are ready), and the date at which the Mac hosts are ready (section 7; trigger: the Mac hosts
are ready).
