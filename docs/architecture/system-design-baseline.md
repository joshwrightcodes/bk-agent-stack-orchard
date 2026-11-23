---
status: accepted
date: 2026-09-19
approved: 2026-09-20
owner: the maintainers
---
# bk-agent-stack-orchard — System design baseline

## Related documents

* [Product requirements](../product/requirements.md) (owner of the functional rules)
* [Target design](target-design.md)
* [Architecture decision records](../adrs/README.md)

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document follows the [writing guide](../writing-guide.md), in the third person.

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

Shared metadata: the maintainers own all entries. The as-of date of all entries is
2026-09-14. An entry states a different date when its source has one.

## 1. Business context

The owner builds and tests an iOS app with Buildkite. The owner wants the macOS jobs to run on
self-hosted Apple silicon Macs for three reasons: cost, data sovereignty, and performance. The
comparison basis is the equivalent Buildkite hosted macOS agent. No measurements of cost or
performance exist yet. The fleet is new, and the hosts have no VM tooling.

Buildkite does not start agents for a self-hosted queue. Each job needs a clean macOS VM, and Apple
permits a maximum of two macOS VMs on one host at the same time. The product is a personal project,
and one person builds and operates it.

## 2. Scope and non-goals

The [product requirements](../product/requirements.md) own the scope, the non-goals, the actors,
and the journeys. This document does not repeat them.

The architecture-relevant non-goals are:

* No pool of started, idle VMs (product requirements, section 3; [ADR-0003](../adrs/0003-start-job-vms-on-demand.md) records the decision).
* No automatic scaling of the Mac hosts. The host pool is fixed.
* No high availability of the controller or of Orchard, provided that recovery takes less than 15
  minutes.
* No state that jobs share, such as host caches (product requirements, section 3; [ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md)
  records the decision).
* No operation in more than one region. An additional region uses a new queue with its own fleet.

## 3. Critical quality scenarios

These scenarios are the most likely to shape the architecture.

| ID | Source | Stimulus | Environment | Artifact | Response | Measure |
|---|---|---|---|---|---|---|
| <a id="qs-1"></a>QS-1 | Pipeline authors | More matched jobs are scheduled than there are free VM slots | Peak demand, fleet at planned capacity | Controller and fleet | Jobs that do not fit stay `scheduled` and visible. VMs never exceed the free slots. | 0 moments with more VMs than slots. Oldest waiting job: 15 min or less. |
| <a id="qs-2"></a>QS-2 | Pipeline author | A matched job is scheduled | A free slot exists, and the image is on the host | Controller, Orchard, job VM | An agent in a new VM acquires the job | Time from `scheduled` to `accepted`: 60 s or less |
| <a id="qs-3"></a>QS-3 | Pipeline author | A job cannot run: no profile matches, or its VM does not start | Normal operation | Controller | A job with no profile stays scheduled, with one notification. A job whose VM does not start fails with a message before its scheduled-job expiry | Unmatched job: one notification at the next poll, and no reservation. VM failure: fails within the configured timeouts. |
| <a id="qs-4"></a>QS-4 | Fault | The controller stops and starts again | Jobs in all states | Controller | No second VM for a job. VMs of ended jobs are deleted. | 0 duplicate VMs. Delay for a reserved job without a VM: a maximum of 300 s. |
| <a id="qs-5"></a>QS-5 | Malicious build | A build tries to reach the host, another VM, a private network, or a long-lived credential | Normal operation | Job VM | The attempt fails | 0 reachable destinations outside the profile allowlist. 0 VM specifications with the cluster agent token. |
| <a id="qs-6"></a>QS-6 | Fault | The controller or the Orchard controller stops | Jobs in progress | Controller, Orchard | No new VMs start. VMs in progress continue. Service returns after recovery. | Recovery time: less than 15 min |
| <a id="qs-7"></a>QS-7 | Fault | A Mac host goes offline | Jobs in progress on that host | Orchard, controller | Reserved jobs on the host fail with a message. Buildkite handles jobs that an agent acquired. | Detection after the Orchard worker offline timeout (3 min) |
| <a id="qs-8"></a>QS-8 | Pipeline author | A job clones source code and uploads artifacts | Normal operation | Job VM, artifact storage | Checkouts and build data stay on owner infrastructure, in the operating country. Artifacts go to Buildkite-managed storage (owner statement, 2026-11-02). | 0 checkouts and 0 build data outside the host of the VM, except through Buildkite log and artifact services. Job logs and artifacts in Buildkite SaaS are accepted. |

## 4. Workload and data

### 4.1 Fleet and hosts

* **Fleet size:** development on 1 Mac; pilot on 2 to 4 Macs; first fleet of 2 to 10 Macs; target
  of 10 to 50 Macs. The dates of the pilot and of adoption and scale are `UNKNOWN` (owner: the
  maintainer; trigger: a Buildkite test cluster and the Mac hosts are ready). On 2026-09-14, the
  plan estimated the pilot start in approximately 1 month and adoption and scale in approximately
  3 months.
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
can permit a smaller fleet at peak. Measurements of a fleet give the real arrival rate and job
duration.

### 4.3 Data and images

* **Controller:** stores no durable data.
* **Orchard:** stores the state of VMs, workers, and service accounts.
* **Job VM:** holds source code and build data until the controller deletes the VM.
* **Buildkite SaaS:** receives job status, timing, and job logs. Buildkite stores job logs in
  Amazon Web Services, and its documentation states no region (as of 2026-09-14). The owner
  accepts this storage (Q-A7).
* **Artifacts:** Buildkite-managed storage, in the region of Buildkite. The owner accepts this
  storage (owner statement, 2026-11-02; [PR-032](../product/requirements.md#pr-032)).
* **Xcode image:** macOS 27 (Golden Gate) with Xcode 27 (owner comments 2026-09-19). The project
  publishes no image, and the fleet operator builds its own (owner decision, 2026-11-03, [ADR-0020](../adrs/0020-leave-the-job-vm-images-to-the-operator.md)).
  `images/` builds this image as a reference. Apple
  released macOS 27 and Xcode 27 (build 27A266a) on 2026-09-14. Xcode 27 needs macOS Tahoe 26.6 or
  later and an Apple silicon Mac, and it contains the iOS 27 SDK.
* **Image availability:** `cirruslabs` publishes `ghcr.io/cirruslabs/macos-golden-gate-vanilla`
  (tags `27.0` and `latest`) and `ghcr.io/cirruslabs/macos-golden-gate-base` (tag `latest`, 30.6
  GiB compressed, 39.9 GB disk, uploaded 2026-08-19). It publishes no `macos-golden-gate-xcode`
  image: the `xcode:N` variants exist only for `tahoe`, `sequoia`, and `sonoma`, and the newest
  released tag of `macos-tahoe-xcode` is 26.5 (the tags `27-beta-4` and `27-beta-6` are
  prereleases). The templates repository builds a new `xcode:N` image on the weekend after an
  Xcode release, so a macOS 27 image with Xcode 27 can come later. The plan of 2026-09-19 was to
  build the image with the template `templates/xcode.pkr.hcl` and `macos_version = golden-gate`.
  That template uses `macos-golden-gate-base:latest` as its base, and its default disk is 140 GB
  (as of 2026-09-19). The project does not use the template now ([ADR-0012](../adrs/0012-build-the-job-vm-images-from-a-vanilla-macos-image.md)).
  Since 2026-09-22, Cirrus Labs publishes `macos-golden-gate-xcode:27` from that template: 68.9 GB
  compressed and an 85.4 GB disk (checked 2026-10-10). Since 2026-10-10, the pilot image builds
  from `macos-golden-gate-vanilla` and an Xcode archive instead ([ADR-0012](../adrs/0012-build-the-job-vm-images-from-a-vanilla-macos-image.md)): a 104 GB disk, 59 GB on
  the host, and 40 GB free in the VM.
* **Host storage:** each host has 1 TB storage, so the number of images that a host can hold is
  limited. The limit is `UNKNOWN` ([SC-09](#sc-09)).

## 5. Quality targets

* **Correctness:** 0 VMs that remain after their job ends, and 0 jobs that the controller fails
  incorrectly, over two weeks of a fleet ([PR-001](../product/requirements.md#pr-001), [PR-002](../product/requirements.md#pr-002), [PR-008](../product/requirements.md#pr-008)). At all times, each job has a
  maximum of one VM, and owned VMs do not exceed capacity.
* **Start time:** 60 s or less from `scheduled` to `accepted`, when the image is on the host. The
  owner did not state a percentile. This baseline assumes the 95th percentile (`ASSUMED`).
* **Wait time:** 15 minutes or less for the oldest waiting job, at planned capacity. The design
  gives the signal of this wait (target design, section 10.6). The target is a product goal, and
  the operator sets the threshold of its own alert.
* **Recovery time:** less than 15 minutes for the controller and for the Orchard controller.
* **Failure visibility:** a job whose VM cannot start MUST fail with a message before its
  scheduled-job expiry. A job that no profile carries MUST get one notification ([ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md)).

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
* A job that does not fit MUST stay `scheduled` and visible ([ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md)).

## 8. Security and data sovereignty

* **Trust:** all builds are untrusted ([ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md)).
* **Credentials in VMs:** only a job acquisition token for the job of the VM, with a lifetime of 30
  minutes or less.
* **Controller credential:** a dedicated cluster agent token with an expiry of 90 days or less.
  Its IP allowlist contains the controller and the job VMs ([ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md)).
* **Queue separation:** the fleet has a dedicated queue ([ADR-0007](../adrs/0007-use-a-dedicated-queue-for-the-macos-fleet.md)). Other agents or stacks can also
  serve the queue, and the capacity of the controller counts only its own workers ([ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md)). The
  cluster can contain other queues. A leaked cluster agent token reaches all self-hosted queues of
  the cluster.
* **Orchard role:** the `compute:write` role also permits the deletion of workers. This risk is
  accepted.
* **Source code:** checkouts and build data MUST stay on owner infrastructure, in the
  operating country. Self-hosted agents clone repositories inside the owner network, and Buildkite
  does not receive source code (Buildkite documentation, as of 2026-09-14).
* **Build logs:** job logs MAY go to Buildkite SaaS (owner answer Q-A7).
* **Git provider:** job VMs clone from a Git provider. The location of the Git provider is outside
  the scope of this system (owner answer Q-A8).
* **Regions:** the fleet operates in one region. Each additional region uses a new queue with its
  own Orchard controller, controller, and hosts (owner answer Q-A9). Each new queue can use the
  same cluster ([ADR-0007](../adrs/0007-use-a-dedicated-queue-for-the-macos-fleet.md)).
* **Telemetry:** MUST NOT contain source code, because the telemetry provider can be outside the
  owner infrastructure.

## 9. Cost and operations

* **Ownership:** one person builds and operates the system (`ASSUMED`).
* **Licences:** Tart and Orchard have no licence cost for this use (as of 2026-09-13). This
  project uses the AGPL-3.0, so the controller imports no Orchard package and has no Orchard
  module dependency ([ADR-0010](../adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md)). The project generates only the field names, the JSON tags, and the
  types of the Orchard fields that the controller uses, and the generated file carries no Orchard
  logic ([ADR-0015](../adrs/0015-generate-only-the-orchard-fields-the-controller-uses.md)).
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
| <a id="bd-01"></a>BD-01 | Business | Execution location | APPLICABLE | DRIVER | CONFIRMED | Self-hosted Apple silicon hosts | Owner statement (2026-09-13); [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md) | Hosted macOS agents accept custom base images |
| <a id="bd-02"></a>BD-02 | Business | Reasons for self-hosting | APPLICABLE | DRIVER | CONFIRMED | Cost, data sovereignty, performance. No measurements. | Owner statement (2026-09-13) | Measurements become available |
| <a id="bd-03"></a>BD-03 | Business | Comparison basis | APPLICABLE | SUPPORTING | CONFIRMED | Equivalent Buildkite hosted macOS agent: Medium 0.12 USD/min, Large 0.24 USD/min | Owner answer Q-P1; Buildkite pricing (2026-09-14) | Buildkite price change |
| <a id="bd-04"></a>BD-04 | Business | Workload type | APPLICABLE | SUPPORTING | CONFIRMED | iOS app builds and tests | Owner answer Q-P4 | New workload type |
| <a id="bd-05"></a>BD-05 | Business | Exchange rate | APPLICABLE | SUPPORTING | CONFIRMED | 1 USD = 1.39842141 AUD, mid-market | xe.com (2026-09-14 04:37 UTC) | A cost comparison at a later date |
| <a id="sc-01"></a>SC-01 | Scale | Fleet size | APPLICABLE | DRIVER | CONFIRMED | 1 Mac (development), 2 to 4 (pilot), 2 to 10 (first fleet), 10 to 50 (target) | Owner statement (2026-09-13) | Need for more than 50 Macs |
| <a id="sc-02"></a>SC-02 | Scale | VM slots on each host | APPLICABLE | DRIVER | CONFIRMED | 2 (hard limit) | Apple macOS licence; Orchard `tart-vms` resource (2026-09-13) | Apple licence change |
| <a id="sc-03"></a>SC-03 | Scale | Dates of the phases | APPLICABLE | SUPPORTING | UNKNOWN | `UNKNOWN`, owner: the maintainers, trigger: a Buildkite test cluster and the Mac hosts are ready. History: on 2026-09-14 the plan estimated the pilot in approximately 1 month and adoption and scale in approximately 3 months | Owner answers Q-P5 and Q-P6 | The trigger occurs, or a change of plan |
| <a id="sc-04"></a>SC-04 | Scale | Host model | APPLICABLE | DRIVER | CONFIRMED | Mac mini, M5 Pro: 18-core CPU, 20-core GPU, 16-core Neural Engine; 64 GB; 1 TB; 10 Gb Ethernet; AU$5,099.00 pre-order (2026-09-14 21:00 AEST); ships 2026-09-22 | Owner answers Q-P1 (2026-09-14); Apple (2026-09-14) | New host model |
| <a id="sc-05"></a>SC-05 | Demand | Job arrival rate and peak concurrency | APPLICABLE | DRIVER | ASSUMED | 880 jobs each workday; peak 38.7 concurrent VMs (section 4.2) | Demand model; owner answer Q-A1 | Pilot telemetry |
| <a id="sc-06"></a>SC-06 | Demand | Job duration and longest job | APPLICABLE | SUPPORTING | ASSUMED | Mean 15 min; longest 60 min; safety limit `maxVMLifetime` 25 h | Demand model (section 4.2) | Pilot telemetry |
| <a id="sc-07"></a>SC-07 | Data | Image size | APPLICABLE | SUPPORTING | CONFIRMED | `macos-golden-gate-base`: 30.6 GiB compressed, 39.9 GB disk. Xcode images: 64.1 GiB compressed and 140 GB disk for `macos-tahoe-xcode:26.5`; the template disk is 140 GB; `macos-golden-gate-xcode:27`: 68.9 GB compressed and 85.4 GB disk. The pilot image, `images/build.sh pilot` on `macos-golden-gate-vanilla`: 104 GB disk, 59 GB on the host | `ghcr.io` manifests; `cirruslabs/macos-image-templates` (2026-09-19, 2026-10-10); the pilot build (2026-10-10) | A new Xcode or vanilla image |
| <a id="sc-08"></a>SC-08 | Data | VM size | APPLICABLE | SUPPORTING | ASSUMED | Half of a host: approximately 9 CPU cores, 32 GB memory | Inference from [SC-02](#sc-02) and [SC-04](#sc-04) | Fleet measurements |
| <a id="sc-09"></a>SC-09 | Data | Host storage for images | APPLICABLE | SUPPORTING | UNKNOWN | 1 TB for each host; disk of the Xcode 27 reference image 104 GB, 59 GB on the host ([SC-07](#sc-07)); number of images for each host not measured | [SC-04](#sc-04); [SC-07](#sc-07) | Fleet measurement |
| <a id="qa-01"></a>QA-01 | Quality | Start time, `scheduled` to `accepted` | APPLICABLE | DRIVER | CONFIRMED | 60 s or less, image on the host; 95th percentile (`ASSUMED`) | Owner answer Q-P2 | Measured start time exceeds the target |
| <a id="qa-02"></a>QA-02 | Quality | Wait time when all slots are in use | APPLICABLE | DRIVER | CONFIRMED | 15 min or less, at planned capacity | Owner answer Q-P3 | Sustained waits over 15 min |
| <a id="qa-03"></a>QA-03 | Quality | Recovery time | APPLICABLE | DRIVER | CONFIRMED | Less than 15 min for the controller and Orchard; no high availability | Owner answer Q-A3 | A recovery takes 15 min or more |
| <a id="qa-04"></a>QA-04 | Quality | Correctness | APPLICABLE | DRIVER | CONFIRMED | 0 leaked VMs and 0 incorrect failures over two weeks of a fleet; 1 VM maximum for each job | [PR-001](../product/requirements.md#pr-001), [PR-002](../product/requirements.md#pr-002), [PR-008](../product/requirements.md#pr-008); owner statement (2026-09-13) | A failed invariant |
| <a id="qa-05"></a>QA-05 | Quality | Failure visibility | APPLICABLE | DRIVER | CONFIRMED | A VM failure fails the job with a message before scheduled-job expiry; an unmatched job gets one notification | [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md); [ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md); [PR-004](../product/requirements.md#pr-004); [PR-008](../product/requirements.md#pr-008) | — |
| <a id="qa-06"></a>QA-06 | Quality | Time from job end to VM deletion | APPLICABLE | SUPPORTING | UNKNOWN | — | [PR-002](../product/requirements.md#pr-002) (the requirement states no value) | Slot shortage in a fleet |
| <a id="dr-01"></a>DR-01 | Consistency | One VM for each job | APPLICABLE | DRIVER | CONFIRMED | Maximum of one | [PR-001](../product/requirements.md#pr-001), [PR-002](../product/requirements.md#pr-002); [ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md) | — |
| <a id="dr-02"></a>DR-02 | Recovery | Delay after a controller crash | APPLICABLE | SUPPORTING | CONFIRMED | Maximum of 300 s | Derived from the target design (reservation expiry) | — |
| <a id="dr-03"></a>DR-03 | Recovery | Orchard data loss | APPLICABLE | INFORMATIONAL | CONFIRMED | Recoverable; no backup | Orchard architecture: one instance with an embedded database | Orchard stores more state |
| <a id="dr-04"></a>DR-04 | Recovery | Recovery point objective of the controller | NOT_APPLICABLE | — | CONFIRMED | The controller stores no data | Derived from the target design (no durable controller state) | The controller stores data |
| <a id="se-01"></a>SE-01 | Security | Trust of builds | APPLICABLE | DRIVER | CONFIRMED | All builds untrusted | [ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md) | A trusted queue becomes necessary |
| <a id="se-02"></a>SE-02 | Security | Credentials in VMs | APPLICABLE | DRIVER | CONFIRMED | Job acquisition token only; 30 min or less | [PR-030](../product/requirements.md#pr-030); [ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md) | — |
| <a id="se-03"></a>SE-03 | Security | Controller Buildkite credential | APPLICABLE | DRIVER | CONFIRMED | Dedicated token; 90 days or less; allowlist | [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md) | Short-lived Stacks API credential |
| <a id="se-04"></a>SE-04 | Security | Queue separation | APPLICABLE | SUPPORTING | CONFIRMED | Dedicated queue for the fleet; other agents or stacks can also serve it; the cluster can contain other queues | Owner comment (2026-09-19); owner decision (2026-11-03); [ADR-0007](../adrs/0007-use-a-dedicated-queue-for-the-macos-fleet.md); [ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md) | Tokens with a smaller scope |
| <a id="se-05"></a>SE-05 | Sovereignty | Source code location | APPLICABLE | DRIVER | CONFIRMED | Owner infrastructure, in the operating country | Owner answer Q-A2 | Change of operating country |
| <a id="se-06"></a>SE-06 | Security | Orchard role scope | APPLICABLE | SUPPORTING | CONFIRMED | `compute:write` also permits worker deletion; accepted | Orchard 0.57.0 role list (2026-09-19) | A narrower Orchard role |
| <a id="se-07"></a>SE-07 | Sovereignty | Build log location | APPLICABLE | SUPPORTING | CONFIRMED | Buildkite SaaS in Amazon Web Services, region not stated; accepted by the owner | Buildkite documentation (2026-09-14); owner answer Q-A7 | A need to keep logs in the operating country |
| <a id="se-08"></a>SE-08 | Sovereignty | Artifact location | APPLICABLE | SUPPORTING | CONFIRMED | Buildkite-managed storage, in the Buildkite region; accepted by the owner (changed 2026-11-02; before: owner-managed storage in the operating country) | Owner statement (2026-11-02); [PR-032](../product/requirements.md#pr-032) | A need to keep artifacts in the operating country |
| <a id="se-09"></a>SE-09 | Sovereignty | Git provider location | NOT_APPLICABLE | — | CONFIRMED | Outside the scope of this system | Owner answer Q-A8 | — |
| <a id="se-10"></a>SE-10 | Sovereignty | Regions | APPLICABLE | SUPPORTING | CONFIRMED | One region now; each additional region uses a new queue with its own fleet | Owner answers Q-A2 and Q-A9 | A second region |
| <a id="op-01"></a>OP-01 | Operations | Ownership and capacity | APPLICABLE | DRIVER | ASSUMED | One person | [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md) | More people join |
| <a id="op-02"></a>OP-02 | Operations | Cost envelope | NOT_APPLICABLE | — | CONFIRMED | Host purchase is outside the system | Owner answer Q-A4 | — |
| <a id="op-03"></a>OP-03 | Operations | Host location | APPLICABLE | SUPPORTING | ASSUMED | Co-located with the controllers, in the operating country | Owner answer Q-A5 | Hosts at a different site |
| <a id="op-04"></a>OP-04 | Operations | Deployment platform | APPLICABLE | SUPPORTING | CONFIRMED | Platform neutral; Kubernetes reference | Owner statement (2026-09-13) | — |
| <a id="op-05"></a>OP-05 | Operations | Telemetry provider | APPLICABLE | SUPPORTING | CONFIRMED | Any OpenTelemetry-compatible provider; no source code in telemetry | Owner answers (2026-09-13 and Q-A6) | — |
| <a id="op-06"></a>OP-06 | Operations | Delivery cadence | APPLICABLE | INFORMATIONAL | ASSUMED | No fixed schedule. release-please calculates each version from the commits, as [ADR-0014](../adrs/0014-calculate-releases-with-release-please.md) decides (2026-11-03) | [ADR-0014](../adrs/0014-calculate-releases-with-release-please.md) | A release schedule is set |
| <a id="co-01"></a>CO-01 | Constraint | Buildkite Stacks API limits | APPLICABLE | SUPPORTING | CONFIRMED | 10 requests/s for list and reserve; 20 requests/s for get-states; reservation 900 s default, 3600 s maximum (2026-09-13) | Buildkite Stacks API documentation (2026-09-13) | Buildkite API change |
| <a id="co-02"></a>CO-02 | Constraint | Orchard | APPLICABLE | SUPPORTING | CONFIRMED | One controller; minimum server version 0.58.0 (2026-10-19), after 0.57.0 added pull requests #465 and #470; FSL-1.1-ALv2, so the controller imports no Orchard package, and the project generates only the field names, JSON tags, and types of the Orchard fields that the controller uses | [ADR-0005](../adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md); [ADR-0010](../adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md); [ADR-0015](../adrs/0015-generate-only-the-orchard-fields-the-controller-uses.md) | Orchard release |
| <a id="co-03"></a>CO-03 | Constraint | Host scaling | NOT_APPLICABLE | — | CONFIRMED | The host pool is fixed | Product requirements, section 3 | Cloud Macs become necessary |
| <a id="co-04"></a>CO-04 | Constraint | Implementation conventions | APPLICABLE | INFORMATIONAL | CONFIRMED | Go 1.27; `mise` for tool versions and tasks; GoReleaser and ko; distroless image; golangci-lint; AGPL-3.0 | Owner comments (2026-09-13 and 2026-09-14) | — |
| <a id="co-05"></a>CO-05 | Constraint | macOS and Xcode | APPLICABLE | SUPPORTING | CONFIRMED | macOS 27 (Golden Gate) with Xcode 27, Apple silicon only, iOS 27 SDK; the fleet operator builds its images, and the reference build makes the Xcode image from `macos-golden-gate-vanilla` and an Xcode archive that the operator downloads ([ADR-0012](../adrs/0012-build-the-job-vm-images-from-a-vanilla-macos-image.md), [ADR-0020](../adrs/0020-leave-the-job-vm-images-to-the-operator.md)) | Owner comments (2026-09-19, 2026-10-10); owner decision (2026-11-03); Apple Developer; `ghcr.io` (2026-09-19) | A published macOS 27 image with Xcode, or a new Xcode version |
| <a id="co-06"></a>CO-06 | Constraint | Buildkite agent | APPLICABLE | SUPPORTING | CONFIRMED | Agent v4 in each VM, downloaded at the start or from the image (2026-11-03); v4.0.0 stable 2026-09-01, v4.12.0 current (2026-11-19, checked 2026-11-23); v4.7.0 and later reject compromised eMudhra certificate chains (CVE-2026-111943); v4 rejects unknown flags and removes the OpenTracing and Datadog backends | Owner comment (2026-09-19); `buildkite/agent` releases (2026-09-19) | A v5 release line |
| <a id="co-07"></a>CO-07 | Constraint | Host macOS version | APPLICABLE | SUPPORTING | ASSUMED | A host runs a macOS version that can host the guest version. The guests run macOS 27.2, and the hosts run macOS 27.2 or later (owner decision, 2026-11-01) | Inference from Apple Virtualization; `vanilla-golden-gate.pkr.hcl` states macOS 27 for its build step (2026-09-19) | Apple or Tart states the host rule, or a macOS 27 guest starts on an older host |

## 11. Assumptions and questions

Assumptions:

* One person operates the system ([OP-01](#op-01)).
* The demand model in section 4.2 describes a generic enterprise-grade iOS app ([SC-05](#sc-05), [SC-06](#sc-06)).
* Each VM uses half of a host ([SC-08](#sc-08)).
* The start-time target applies to the 95th percentile ([QA-01](#qa-01)).
* The hosts are co-located with the controllers, in the operating country ([OP-03](#op-03)).
* A macOS 27.2 guest needs a host on macOS 27.2 or later ([CO-07](#co-07)). The release gate confirms this
  value for a host on macOS 27.2 ([O-4](../quality/test-strategy.md#o-4)).

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
trigger: the number of images for each host ([SC-09](#sc-09), fleet measurement), the time from the job
end to the VM deletion ([QA-06](#qa-06), fleet measurement), and the dates of the phases ([SC-03](#sc-03), a Buildkite
test cluster and the Mac hosts are ready). The delivery cadence ([OP-06](#op-06), informational)
follows [ADR-0014](../adrs/0014-calculate-releases-with-release-please.md).

## 12. Review triggers

Review this baseline when one of these events occurs:

* The owner answers a question in section 11 or in the product requirements.
* Fleet telemetry gives measured values for start time, wait time, or job demand.
* The hosts arrive, the Xcode 27 image is built, `cirruslabs` publishes a macOS 27 image with
  Xcode, or Apple releases a new Xcode version.
* A review trigger in the driver register occurs.
* An ADR changes status, or a new ADR supersedes an ADR.
