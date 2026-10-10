# bk-agent-stack-orchard — System design baseline

* Status: accepted
* Date: 2026-09-19
* Approved: 2026-09-20
* Owner: Josh Wright
* Related documents:
  * [Product requirements](../product/requirements.md) (owner of the functional rules)
  * [Target design](target-design.md)
  * [Architecture decision records](../adrs/README.md)

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document uses ASD-STE100 Simplified Technical English and the third person.

This document is the source of truth for the quality attributes and constraints that drive the
architecture. The [product requirements](../product/requirements.md) own the functional rules. The
target design and the ADRs design the solution.

The direction of this document is from the demand to the solution. An entry MUST NOT take its
value from the target design. When an entry describes a property that only the design can give,
the source column states "Derived from the target design".

Each entry in the driver register (section 10) has these classifications:

* **Applicability:** `APPLICABLE` or `NOT_APPLICABLE`.
* **Criticality:** `DRIVER` (it shapes the architecture), `SUPPORTING` (it limits the design), or
  `INFORMATIONAL`.
* **Evidence:**
  * `CONFIRMED`: an owner statement, a document, or a verified source.
  * `ASSUMED`: a reasonable belief that nobody has confirmed.
  * `UNKNOWN`: no source gives the value.

`UNKNOWN` does not mean that an entry is not important.

Shared metadata: the owner of all entries is Josh Wright. The as-of date of all entries is
2026-09-14. An entry states a different date when its source has one.

## 1. Business context

The owner builds and tests an iOS app with Buildkite. The owner wants the macOS jobs to run on
self-hosted Apple Silicon Macs for three reasons: cost, data sovereignty, and performance. The
comparison basis is the equivalent Buildkite hosted macOS agent. No measurements of cost or
performance exist yet. The fleet is new, and the hosts have no VM tooling.

Buildkite does not start agents for a self-hosted queue. Each job needs a clean macOS VM, and Apple
permits a maximum of two macOS VMs on one host at the same time. The product is a personal project,
and one person builds and operates it.

## 2. Scope and non-goals

The [product requirements](../product/requirements.md) own the scope, the non-goals, the actors,
and the journeys. This document does not repeat them.

The architecture-relevant non-goals are:

* No pool of started, idle VMs (product requirements, section 3; ADR-0003 records the decision).
* No automatic scaling of the Mac hosts. The host pool is fixed.
* No high availability of the controller or of Orchard, provided that recovery takes less than 15
  minutes.
* No state that jobs share, such as host caches (product requirements, section 3; ADR-0004
  records the decision).
* No operation in more than one region. An additional region uses a new queue with its own fleet.

## 3. Critical quality scenarios

These scenarios are the most likely to shape the architecture.

| ID | Source | Stimulus | Environment | Artifact | Response | Measure |
|---|---|---|---|---|---|---|
| QS-1 | Pipeline authors | More matched jobs are scheduled than there are free VM slots | Peak demand, fleet at planned capacity | Controller and fleet | Jobs that do not fit stay `scheduled` and visible. VMs never exceed the free slots. | 0 moments with more VMs than slots. Oldest waiting job: 15 min or less. |
| QS-2 | Pipeline author | A matched job is scheduled | A free slot exists, and the image is on the host | Controller, Orchard, job VM | An agent in a new VM acquires the job | Time from `scheduled` to `accepted`: 60 s or less |
| QS-3 | Pipeline author | A job cannot run: no profile matches, or its VM does not start | Normal operation | Controller | The job fails with a message before its scheduled-job expiry | Unmatched job: fails at the next poll. VM failure: fails within the configured timeouts. |
| QS-4 | Fault | The controller stops and starts again | Jobs in all states | Controller | No second VM for a job. VMs of ended jobs are deleted. | 0 duplicate VMs. Delay for a reserved job without a VM: a maximum of 300 s. |
| QS-5 | Malicious build | A build tries to reach the host, another VM, a private network, or a long-lived credential | Normal operation | Job VM | The attempt fails | 0 reachable destinations outside the profile allowlist. 0 VM specifications with the cluster agent token. |
| QS-6 | Fault | The controller or the Orchard controller stops | Jobs in progress | Controller, Orchard | No new VMs start. VMs in progress continue. Service returns after recovery. | Recovery time: less than 15 min |
| QS-7 | Fault | A Mac host goes offline | Jobs in progress on that host | Orchard, controller | Reserved jobs on the host fail with a message. Buildkite handles jobs that an agent acquired. | Detection after the Orchard worker offline timeout (3 min) |
| QS-8 | Pipeline author | A job clones source code and uploads artifacts | Normal operation | Job VM, artifact storage | Checkouts, build data, and artifacts stay on owner infrastructure, in the operating country | 0 artifacts in Buildkite-managed storage. Job logs in Buildkite SaaS are accepted. |

## 4. Workload and data

### 4.1 Fleet and hosts

* **Fleet size:** development on 1 Mac; pilot on 2 to 4 Macs; first fleet of 2 to 10 Macs; target
  of 10 to 50 Macs. The pilot starts in approximately 1 month, and adoption and scale follow in
  approximately 3 months (from 2026-09-14).
* **Host model:** Mac mini with an M5 Pro chip: 18-core CPU, 20-core GPU, and 16-core Neural
  Engine. Each host has 64 GB memory, 1 TB storage, and 10 Gb Ethernet. Apple ships it from
  2026-09-22.
* **Host price:** AU$5,099.00 for each host. This is the pre-order price at 21:00 AEST on
  2026-09-14 (owner statement).
* **VM capacity:** 2 macOS VMs on each host. The first fleet has 4 to 20 VM slots, and the target
  fleet has 20 to 100 VM slots.
* **VM size:** if each VM uses half of a host, a VM has approximately 9 CPU cores and 32 GB
  memory (`ASSUMED`). The nearest Buildkite hosted shapes are Medium (6 vCPU, 28 GB) and Large (12
  vCPU, 56 GB). The hosted shapes use M4 chips.

### 4.2 Job demand (assumed model)

No demand data exists. This model describes a generic enterprise-grade iOS app. All values are
`ASSUMED`. Public data points put enterprise iOS pipelines at approximately 10 to 30 minutes
(Bitrise 2025; Cash App 2023; Duolingo 2024; Lyft 2023).

| # | Parameter | Value | Calculation |
|---|---|---|---|
| D1 | iOS engineers | 100 | Assumption |
| D2 | Pull request builds for each engineer, each workday | 2 | Assumption |
| D3 | Main-branch and release builds, each workday | 20 | Assumption |
| D4 | Builds each workday | 220 | D1 × D2 + D3 |
| D5 | macOS jobs for each build | 4 | Build and unit tests, 2 UI test shards, static checks |
| D6 | macOS jobs each workday | 880 | D4 × D5 |
| D7 | Mean job duration | 15 min | Assumption, within the public data points |
| D8 | Longest job | 60 min | Assumption: release archive or full UI test suite |
| D9 | VM start and teardown for each job | 1.5 min | 60 s start target and 30 s teardown |
| D10 | Mean slot occupancy for each job | 16.5 min | D7 + D9 |
| D11 | Share of jobs in a 10-hour working window | 80 % | Assumption |
| D12 | Mean arrival rate in the window | 70.4 jobs each hour | D6 × D11 ÷ 10 |
| D13 | Peak arrival rate | 140.8 jobs each hour | 2 × D12 (assumed peak factor) |
| D14 | Mean concurrent VMs in the window | 19.4 | D12 × D10 ÷ 60 |
| D15 | Peak concurrent VMs | 38.7 | D13 × D10 ÷ 60 |
| D16 | Macs for peak demand without a wait | 20 | D15 ÷ 2, rounded up |

With this model, a fleet of 10 Macs (20 slots) serves the mean demand of the window, but jobs wait
at peak. A fleet of approximately 20 Macs serves the peak without a wait. The 15-minute wait target
can permit a smaller fleet at peak. The pilot must measure the real arrival rate and job duration.

### 4.3 Data and images

* **Controller:** stores no durable data.
* **Orchard:** stores the state of VMs, workers, and service accounts.
* **Job VM:** holds source code and build data until the VM is deleted.
* **Buildkite SaaS:** receives job status, timing, and job logs. Job logs are stored in Amazon Web
  Services, and the documentation states no region (as of 2026-09-14). The owner accepts this
  storage (Q-A7).
* **Artifacts:** owner-managed storage in the operating country (PR-032).
* **Pilot image:** macOS 27 (Golden Gate) with Xcode 27 (owner comments 2026-09-19). Apple
  released macOS 27 and Xcode 27 (build 27A266a) on 2026-09-14. Xcode 27 needs macOS Tahoe 26.6 or
  later and an Apple silicon Mac, and it contains the iOS 27 SDK.
* **Image availability:** `cirruslabs` publishes `ghcr.io/cirruslabs/macos-golden-gate-vanilla`
  (tags `27.0` and `latest`) and `ghcr.io/cirruslabs/macos-golden-gate-base` (tag `latest`, 30.6
  GiB compressed, 39.9 GB disk, uploaded 2026-08-19). It publishes no `macos-golden-gate-xcode`
  image: the `xcode:N` variants exist only for `tahoe`, `sequoia`, and `sonoma`, and the newest
  released tag of `macos-tahoe-xcode` is 26.5 (the tags `27-beta-4` and `27-beta-6` are
  prereleases). The templates repository builds a new `xcode:N` image on the weekend after an
  Xcode release, so a macOS 27 image with Xcode 27 can come later. The fleet therefore builds its
  own image with `templates/xcode.pkr.hcl` and `macos_version = golden-gate`. This template uses
  `macos-golden-gate-base:latest` as its base, and its default disk is 140 GB (as of 2026-09-19).
* **Host storage:** each host has 1 TB storage, so the number of images that a host can hold is
  limited. The limit is `UNKNOWN` (SC-09).

## 5. Quality targets

* **Correctness:** 0 VMs that remain after their job ends, and 0 jobs that the controller fails
  incorrectly, during a two-week pilot (PR-001, PR-002, PR-008). At all times, each job has a
  maximum of one VM, and owned VMs do not exceed capacity.
* **Start time:** 60 s or less from `scheduled` to `accepted`, when the image is on the host. The
  owner did not state a percentile. This baseline assumes the 95th percentile (`ASSUMED`).
* **Wait time:** 15 minutes or less for the oldest waiting job, at planned capacity. The design
  alert uses the same value. It gives an alert when the oldest waiting job is more than 15 minutes
  old.
* **Recovery time:** less than 15 minutes for the controller and for the Orchard controller.
* **Failure visibility:** a job that cannot run MUST fail with a message before its scheduled-job
  expiry.

## 6. Recovery

* **Controller:** it has no durable state. After a restart, it gets its state from Orchard and
  Buildkite. A reserved job without a VM waits a maximum of the reservation expiry (300 s). The
  recovery time MUST be less than 15 minutes.
* **Orchard controller:** one instance with an embedded database. The loss of its data is
  recoverable, because workers register again and the controller removes VMs in progress. No
  backup is necessary. The recovery time MUST be less than 15 minutes.
* **Recovery point objective:** `NOT_APPLICABLE` for the controller, because it stores no data.

## 7. Consistency

* Each job MUST have a maximum of one VM. The Buildkite reservation is the arbiter between stacks.
* The controller MUST fail a job only before an agent acquires the job.
* A job that does not fit MUST stay `scheduled` and visible (ADR-0002).

## 8. Security and data sovereignty

* **Trust:** all builds are untrusted (ADR-0004).
* **Credentials in VMs:** only a job acquisition token for the job of the VM, with a lifetime of 30
  minutes or less.
* **Controller credential:** a dedicated cluster agent token with an expiry of 90 days or less.
  Its IP allowlist contains the controller and the job VMs (ADR-0008).
* **Queue separation:** the fleet has a dedicated queue (ADR-0007). The cluster may contain other
  queues. A leaked cluster agent token reaches all self-hosted queues of the cluster.
* **Orchard role:** the `compute:write` role also permits the deletion of workers. This risk is
  accepted.
* **Source code:** checkouts, build data, and artifacts MUST stay on owner infrastructure, in the
  operating country. Self-hosted agents clone repositories inside the owner network, and Buildkite
  does not receive source code (Buildkite documentation, as of 2026-09-14).
* **Build logs:** job logs MAY go to Buildkite SaaS (owner answer Q-A7).
* **Git provider:** job VMs clone from a Git provider. The location of the Git provider is outside
  the scope of this system (owner answer Q-A8).
* **Regions:** the fleet operates in one region. Each additional region uses a new queue with its
  own Orchard controller, controller, and hosts (owner answer Q-A9). Each new queue can use the
  same cluster (ADR-0007).
* **Telemetry:** MUST NOT contain source code, because the telemetry provider can be outside the
  owner infrastructure.

## 9. Cost and operations

* **Ownership:** one person builds and operates the system (`ASSUMED`).
* **Licences:** Tart and Orchard have no licence cost for this use (as of 2026-09-13). This
  project uses the AGPL-3.0, so the controller contains no Orchard code (ADR-0010).
* **Cost envelope:** `NOT_APPLICABLE`. The purchase of the hosts is outside this system (owner
  answer Q-A4).
* **Cost comparison:** Buildkite hosted macOS costs 0.12 USD each minute for Medium and 0.24 USD
  each minute for Large (as of 2026-09-14). With the demand model, 880 jobs of 15 minutes use
  13,200 job minutes each workday. At hosted Medium prices, that is 1,584 USD each workday. The
  host price is AU$5,099.00 each, so 20 hosts cost AU$101,980.00.
* **Exchange rate:** 1 USD = 1.39842141 AUD. This is the xe.com mid-market rate at 04:37 UTC on
  2026-09-14. The host price is from 11:00 UTC on the same day.
* **Hosted cost in AUD:** Medium costs A$0.1678 each minute, and Large costs A$0.3356 each minute.
  With the demand model, hosted Medium costs A$2,215.10 each workday. Hosted Large costs
  A$4,430.20 each workday.
* **Host cost in USD:** one host costs 3,646.25 USD, and 20 hosts cost 72,925.08 USD.
* **Break-even (estimate):** the purchase price of 20 hosts equals 46.0 workdays of hosted Medium
  cost, or 23.0 workdays of hosted Large cost. The estimate excludes power, colocation, network,
  and operator time. The period of use for the hosts is `UNKNOWN`.
* **Host location:** co-located with the controllers, in the operating country (`ASSUMED`, owner
  answer Q-A5).
* **Deployment platform:** the controller MUST NOT depend on a specific platform. Kubernetes is the
  reference deployment.
* **Telemetry:** OpenTelemetry only, to any OpenTelemetry-compatible provider (owner answer Q-A6).
  The retention period is `NOT_APPLICABLE` to the architecture.

## 10. Driver register

| ID | Theme | Parameter | Applicability | Criticality | Evidence | Value or measure | Source | Review trigger |
|---|---|---|---|---|---|---|---|---|
| BD-01 | Business | Execution location | APPLICABLE | DRIVER | CONFIRMED | Self-hosted Apple Silicon hosts | Owner statement (2026-09-13); ADR-0001 | Hosted macOS agents accept custom base images |
| BD-02 | Business | Reasons for self-hosting | APPLICABLE | DRIVER | CONFIRMED | Cost, data sovereignty, performance. No measurements. | Owner statement (2026-09-13) | Measurements become available |
| BD-03 | Business | Comparison basis | APPLICABLE | SUPPORTING | CONFIRMED | Equivalent Buildkite hosted macOS agent: Medium 0.12 USD/min, Large 0.24 USD/min | Owner answer Q-P1; Buildkite pricing (2026-09-14) | Buildkite price change |
| BD-04 | Business | Workload type | APPLICABLE | SUPPORTING | CONFIRMED | iOS app builds and tests | Owner answer Q-P4 | New workload type |
| BD-05 | Business | Exchange rate | APPLICABLE | SUPPORTING | CONFIRMED | 1 USD = 1.39842141 AUD, mid-market | xe.com (2026-09-14 04:37 UTC) | A cost comparison at a later date |
| SC-01 | Scale | Fleet size | APPLICABLE | DRIVER | CONFIRMED | 1 Mac (development), 2 to 4 (pilot), 2 to 10 (first fleet), 10 to 50 (target) | Owner statement (2026-09-13) | Need for more than 50 Macs |
| SC-02 | Scale | VM slots on each host | APPLICABLE | DRIVER | CONFIRMED | 2 (hard limit) | Apple macOS licence; Orchard `tart-vms` resource (2026-09-13) | Apple licence change |
| SC-03 | Scale | Dates of the phases | APPLICABLE | SUPPORTING | CONFIRMED | Pilot in approximately 1 month; adoption and scale in approximately 3 months (from 2026-09-14) | Owner answers Q-P5 and Q-P6 | A change of plan |
| SC-04 | Scale | Host model | APPLICABLE | DRIVER | CONFIRMED | Mac mini, M5 Pro: 18-core CPU, 20-core GPU, 16-core Neural Engine; 64 GB; 1 TB; 10 Gb Ethernet; AU$5,099.00 pre-order (2026-09-14 21:00 AEST); ships 2026-09-22 | Owner answers Q-P1 (2026-09-14); Apple (2026-09-14) | New host model |
| SC-05 | Demand | Job arrival rate and peak concurrency | APPLICABLE | DRIVER | ASSUMED | 880 jobs each workday; peak 38.7 concurrent VMs (section 4.2) | Demand model; owner answer Q-A1 | Pilot telemetry |
| SC-06 | Demand | Job duration and longest job | APPLICABLE | SUPPORTING | ASSUMED | Mean 15 min; longest 60 min; safety limit `maxVMLifetime` 25 h | Demand model (section 4.2) | Pilot telemetry |
| SC-07 | Data | Image size | APPLICABLE | SUPPORTING | CONFIRMED | `macos-golden-gate-base`: 30.6 GiB compressed, 39.9 GB disk. Xcode images: 64.1 GiB compressed and 140 GB disk for `macos-tahoe-xcode:26.5`; the template disk is 140 GB | `ghcr.io` manifests; `cirruslabs/macos-image-templates` (2026-09-19) | First Xcode 27 image build |
| SC-08 | Data | VM size | APPLICABLE | SUPPORTING | ASSUMED | Half of a host: approximately 9 CPU cores, 32 GB memory | Inference from SC-02 and SC-04 | Pilot measurements |
| SC-09 | Data | Host storage for images | APPLICABLE | SUPPORTING | UNKNOWN | 1 TB for each host; image disk 140 GB; number of images for each host not measured | SC-04; SC-07 | Pilot measurement |
| QA-01 | Quality | Start time, `scheduled` to `accepted` | APPLICABLE | DRIVER | CONFIRMED | 60 s or less, image on the host; 95th percentile (`ASSUMED`) | Owner answer Q-P2 | Measured start time exceeds the target |
| QA-02 | Quality | Wait time when all slots are in use | APPLICABLE | DRIVER | CONFIRMED | 15 min or less, at planned capacity | Owner answer Q-P3 | Sustained waits over 15 min |
| QA-03 | Quality | Recovery time | APPLICABLE | DRIVER | CONFIRMED | Less than 15 min for the controller and Orchard; no high availability | Owner answer Q-A3 | A recovery takes 15 min or more |
| QA-04 | Quality | Correctness | APPLICABLE | DRIVER | CONFIRMED | 0 leaked VMs and 0 incorrect failures in the pilot; 1 VM maximum for each job | PR-001, PR-002, PR-008; owner statement (2026-09-13) | A failed invariant |
| QA-05 | Quality | Failure visibility | APPLICABLE | DRIVER | CONFIRMED | Fail with a message before scheduled-job expiry | ADR-0001; PR-004; PR-008 | — |
| QA-06 | Quality | Time from job end to VM deletion | APPLICABLE | SUPPORTING | UNKNOWN | — | PR-002 (the requirement states no value) | Slot shortage in the pilot |
| DR-01 | Consistency | One VM for each job | APPLICABLE | DRIVER | CONFIRMED | Maximum of one | PR-001, PR-002; ADR-0002 | — |
| DR-02 | Recovery | Delay after a controller crash | APPLICABLE | SUPPORTING | CONFIRMED | Maximum of 300 s | Derived from the target design (reservation expiry) | — |
| DR-03 | Recovery | Orchard data loss | APPLICABLE | INFORMATIONAL | CONFIRMED | Recoverable; no backup | Orchard architecture: one instance with an embedded database | Orchard stores more state |
| DR-04 | Recovery | Recovery point objective of the controller | NOT_APPLICABLE | — | CONFIRMED | The controller stores no data | Derived from the target design (no durable controller state) | The controller stores data |
| SE-01 | Security | Trust of builds | APPLICABLE | DRIVER | CONFIRMED | All builds untrusted | ADR-0004 | A trusted queue becomes necessary |
| SE-02 | Security | Credentials in VMs | APPLICABLE | DRIVER | CONFIRMED | Job acquisition token only; 30 min or less | PR-030; ADR-0004 | — |
| SE-03 | Security | Controller Buildkite credential | APPLICABLE | DRIVER | CONFIRMED | Dedicated token; 90 days or less; allowlist | ADR-0008 | Short-lived Stacks API credential |
| SE-04 | Security | Queue separation | APPLICABLE | SUPPORTING | CONFIRMED | Dedicated queue for the fleet; the cluster may contain other queues | Owner comment (2026-09-19); ADR-0007 | Tokens with a smaller scope |
| SE-05 | Sovereignty | Source code location | APPLICABLE | DRIVER | CONFIRMED | Owner infrastructure, in the operating country | Owner answer Q-A2 | Change of operating country |
| SE-06 | Security | Orchard role scope | APPLICABLE | SUPPORTING | CONFIRMED | `compute:write` also permits worker deletion; accepted | Orchard 0.57.0 role list (2026-09-19) | A narrower Orchard role |
| SE-07 | Sovereignty | Build log location | APPLICABLE | SUPPORTING | CONFIRMED | Buildkite SaaS in Amazon Web Services, region not stated; accepted by the owner | Buildkite documentation (2026-09-14); owner answer Q-A7 | Logs must stay in the operating country |
| SE-08 | Sovereignty | Artifact location | APPLICABLE | SUPPORTING | CONFIRMED | Owner-managed storage in the operating country | Owner answer Q-A2; PR-032 | — |
| SE-09 | Sovereignty | Git provider location | NOT_APPLICABLE | — | CONFIRMED | Outside the scope of this system | Owner answer Q-A8 | — |
| SE-10 | Sovereignty | Regions | APPLICABLE | SUPPORTING | CONFIRMED | One region now; each additional region uses a new queue with its own fleet | Owner answers Q-A2 and Q-A9 | A second region |
| OP-01 | Operations | Ownership and capacity | APPLICABLE | DRIVER | ASSUMED | One person | ADR-0001 | More people join |
| OP-02 | Operations | Cost envelope | NOT_APPLICABLE | — | CONFIRMED | Host purchase is outside the system | Owner answer Q-A4 | — |
| OP-03 | Operations | Host location | APPLICABLE | SUPPORTING | ASSUMED | Co-located with the controllers, in the operating country | Owner answer Q-A5 | Hosts at a different site |
| OP-04 | Operations | Deployment platform | APPLICABLE | SUPPORTING | CONFIRMED | Platform neutral; Kubernetes reference | Owner statement (2026-09-13) | — |
| OP-05 | Operations | Telemetry provider | APPLICABLE | SUPPORTING | CONFIRMED | Any OpenTelemetry-compatible provider; no source code in telemetry | Owner answers (2026-09-13 and Q-A6) | — |
| OP-06 | Operations | Delivery cadence | APPLICABLE | INFORMATIONAL | UNKNOWN | No constraint assumed | — | A release schedule is set |
| CO-01 | Constraint | Buildkite Stacks API limits | APPLICABLE | SUPPORTING | CONFIRMED | 10 requests/s for list and reserve; 20 requests/s for get-states; reservation 900 s default, 3600 s maximum (2026-09-13) | Buildkite Stacks API documentation (2026-09-13) | Buildkite API change |
| CO-02 | Constraint | Orchard | APPLICABLE | SUPPORTING | CONFIRMED | One controller; minimum server version 0.57.0, which contains pull requests #465 and #470 (2026-09-15); FSL-1.1-ALv2, so the controller contains no Orchard code | ADR-0005; ADR-0010 | Orchard release |
| CO-03 | Constraint | Host scaling | NOT_APPLICABLE | — | CONFIRMED | The host pool is fixed | Product requirements, section 3 | Cloud Macs become necessary |
| CO-04 | Constraint | Implementation conventions | APPLICABLE | INFORMATIONAL | CONFIRMED | Go 1.27; `mise` for tool versions and tasks; GoReleaser and ko; distroless image; golangci-lint; AGPL-3.0 | Owner comments (2026-09-13 and 2026-09-14) | — |
| CO-05 | Constraint | macOS and Xcode | APPLICABLE | SUPPORTING | CONFIRMED | Pilot: macOS 27 (Golden Gate) with Xcode 27, Apple silicon only, iOS 27 SDK; the fleet builds the Xcode image from `macos-golden-gate-base` | Owner comments (2026-09-19); Apple Developer; `ghcr.io` (2026-09-19) | A published macOS 27 image with Xcode, or a new Xcode version |
| CO-06 | Constraint | Buildkite agent | APPLICABLE | SUPPORTING | CONFIRMED | Agent v4 in the VM image; v4.0.0 stable 2026-09-01, v4.3.0 current (2026-10-08, checked 2026-10-10); v4 rejects unknown flags and removes the OpenTracing and Datadog backends | Owner comment (2026-09-19); `buildkite/agent` releases (2026-09-19) | A v5 release line |
| CO-07 | Constraint | Host macOS version | APPLICABLE | SUPPORTING | ASSUMED | A host runs a macOS version that can host the guest version. For a macOS 27 guest, the assumption is a macOS 27 host | Inference from Apple Virtualization; `vanilla-golden-gate.pkr.hcl` states macOS 27 for its build step (2026-09-19) | Apple or Tart states the host rule, or a macOS 27 guest starts on an older host |

## 11. Assumptions and questions

Assumptions:

* One person operates the system (OP-01).
* The demand model in section 4.2 describes a generic enterprise-grade iOS app (SC-05, SC-06).
* Each VM uses half of a host (SC-08).
* The start-time target applies to the 95th percentile (QA-01).
* The hosts are co-located with the controllers, in the operating country (OP-03).
* A macOS 27 guest needs a macOS 27 host (CO-07). The pilot confirms this value.

Answered questions (2026-09-14):

| ID | Question | Answer |
|---|---|---|
| Q-A1 | Expected job demand | Unknown. Use assumptions for a generic enterprise-grade iOS app (section 4.2). |
| Q-A2 | Data sovereignty details | Source code stays on owner infrastructure, in the operating country. The fleet may operate in more than one region. |
| Q-A3 | Acceptable recovery time | Less than 15 minutes |
| Q-A4 | Budget | Not applicable. The purchase of the hosts is a separate concern. |
| Q-A5 | Host location | Not applicable. Assume that the hosts are co-located with the controllers. |
| Q-A6 | Telemetry backend | Not applicable. Assume an OpenTelemetry-compatible provider. |
| Q-A7 | Job logs in Buildkite SaaS | Yes |
| Q-A8 | Repository location | A Git provider. Its location is outside the scope of this system. |
| Q-A9 | Layout for more than one region | Each region has its own fleet and a new queue. Assume one region for now. |

No question needs an owner decision. Three values stay `UNKNOWN`, and each has an owner and a
trigger: the number of images for each host (SC-09, pilot measurement), the time from the job end
to the VM deletion (QA-06, pilot measurement), and the delivery cadence (OP-06, informational).

## 12. Review triggers

Review this baseline when one of these events occurs:

* The owner answers a question in section 11 or in the product requirements.
* Pilot telemetry gives measured values for start time, wait time, or job demand.
* The hosts arrive, the Xcode 27 image is built, `cirruslabs` publishes a macOS 27 image with
  Xcode, or Apple releases a new Xcode version.
* A review trigger in the driver register occurs.
* An ADR changes status, or a new ADR supersedes an ADR.
