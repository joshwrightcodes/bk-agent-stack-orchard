# bk-agent-stack-orchard — Test strategy

* Status: accepted
* Date: 2026-09-19
* Approved: 2026-09-20
* Owner: Josh Wright
* Related documents:
  * [Product requirements](../product/requirements.md): requirement identifiers such as `PR-005`
  * [System design baseline](../architecture/system-design-baseline.md): driver identifiers such as
    `QA-01`, and quality scenarios such as `QS-1`
  * [Target design](../architecture/target-design.md)
  * [Architecture decision records](../adrs/README.md): ADR-0001 to ADR-0009

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document uses ASD-STE100 Simplified Technical English and the third person.

Identifiers in this document:

* `R-nn` is a risk in section 3.
* `I-n` is an invariant in section 5.
* `T-nnn` is a test obligation in section 6.
* `E-n` is pilot evidence in section 8.

## 1. Purpose and scope

This document decides which evidence the project produces, at which level, and which gate holds
it. It does not contain test code, and it does not define the pipelines of the application that
the fleet builds.

In scope: the controller, its two API clients, the VM specification that it sends to Orchard, the
telemetry that measures the quality targets, and the release artifacts.

Out of scope: the behaviour of Tart, Orchard, Buildkite, and Apple Virtualization; the build of
the VM images; dashboards and alert rules for a specific telemetry backend (target design,
section 10.6).

No code exists. Each obligation in section 6 is therefore `ADD`. Section 9 gives the obligations
that the project does not take, with the reason.

## 2. Delivery shape and its effect on evidence

The first increment is a walking skeleton: Stack, Poller, Matcher, Launcher, and the minimum
telemetry, against `orchard dev --synthetic` and a fake Stacks API server. One job goes from
`scheduled` to a deleted VM. The full Reaper decision table (target design, section 7.2) follows
the skeleton.

A controller that creates VMs but does not delete them fills the fleet. Because of this, the
skeleton MUST contain two Reaper rows: row 2 (`maxVMLifetime`) and row 4 (the job is terminal).
These two rows are the minimum teardown, and T-005 covers them in the skeleton gate. The other
eleven rows come later.

Until all thirteen rows exist and T-020 passes, these limits apply:

* The skeleton MUST run only against `orchard dev --synthetic` or a development Mac with one
  worker, and only with a Buildkite test cluster.
* The skeleton MUST NOT run against the pilot queue, and MUST NOT run against a host that serves
  other work (PR-031).
* Each skeleton run MUST end with a check that no owned VM remains. T-060 makes this check.
* T-060 checks the stack notifications that the skeleton sends, which is "VM requested". The
  target design, section 10.5, names a notification for each later step of the VM, and W-16 owns
  them. W-16 MUST extend T-060 with each notification that it adds, so that T-060 checks every
  step when the design is whole.

| Increment | Components | Evidence that ends the increment |
|---|---|---|
| 1 — Walking skeleton | Stack, Poller, Matcher, Launcher, Telemetry, Reaper rows 2 and 4 | T-001, T-002, T-003, T-004 (capacity only), T-005 (rows 2 and 4), T-040, T-060, T-120, T-122 |
| 2 — Full lifecycle | Reaper rows 1 and 3, 5 to 13; Admission with unplaced accounting; JobStates | T-005 (all rows), T-020, T-021, T-022, T-023, T-025, T-061, T-062 |
| 3 — Operability | Readiness, token replacement, full telemetry, outage behaviour | T-008, T-024, T-041, T-063, T-064, T-121 |
| 4 — Fleet readiness | Real Tart, real Buildkite cluster, one Mac | T-080, T-100 to T-106 |

## 3. Risk map

Priority: `P1` is a risk that can lose money, credentials, or the fleet. `P2` can fail jobs or
break a quality target. `P3` degrades operation.

| ID | Protected outcome | Defect class | Impact if it happens | Basis | Priority |
|---|---|---|---|---|---|
| R-01 | A VM holds only a credential for its own job | Accepted invalid input; incorrect specification | A build reads the cluster agent token and reaches all self-hosted queues of the cluster | PR-030, SE-02, ADR-0004, ADR-0008, QS-5 | P1 |
| R-02 | A build cannot reach the host, another VM, or a private network | Incorrect specification; missing isolation field | A build reaches internal services or another build | PR-030, SE-01, QS-5 | P1 |
| R-03 | Every VM that the controller creates is deleted | Partial failure; crash between finish and delete; missed state | VMs fill the fleet; at 2 slots for each host, a few leaks stop the queue | PR-002, QA-04, QS-4 | P1 |
| R-04 | Each job has a maximum of one VM | Duplicate delivery; race between tick steps; restart | Two agents acquire one job, or a slot is lost | PR-001, PR-010, DR-01, QS-4 | P1 |
| R-05 | Owned VMs never exceed free capacity | Boundary error in capacity arithmetic; foreign VMs not counted | Orchard cannot place VMs; jobs wait without a reason | PR-005, SC-02, QS-1, ADR-0002 | P1 |
| R-06 | The controller never fails a job that an agent acquired | Incorrect success or failure; stale job state | A running build dies, and the pipeline reports a false failure | PR-008, QA-05, design principle 5 | P1 |
| R-07 | Checkouts, build data, and artifacts stay in the operating country | Wrong destination; default upload target | Source code and artifacts leave owner infrastructure | PR-032, SE-05, SE-08, QS-8 | P1 |
| R-08 | A job that cannot run fails with a message before its expiry | Rejected valid input; silent wait | A pipeline waits until the job expires, with no reason | PR-004, PR-008, QA-05, QS-3 | P2 |
| R-09 | A job that does not fit stays `scheduled` and visible | Hidden reservation; expired reservation | Jobs disappear from the queue, and other stacks cannot see them | PR-005, ADR-0002, QS-1 | P2 |
| R-10 | Jobs start in Buildkite order | Ordering error | A high-priority job waits behind an old low-priority job | PR-006 | P2 |
| R-11 | A restart does not change the result of any job | Recovery error; lost in-memory state | Duplicate VMs, or VMs of ended jobs remain | PR-010, QS-4, DR-02 | P2 |
| R-12 | A paused queue starts nothing and fails nothing | Missed state | Jobs fail while an operator holds the queue | PR-011 | P2 |
| R-13 | A cancelled job releases its VM | Cancellation race | A slot is held for a job that nobody wants | PR-007 | P2 |
| R-14 | A configuration that is not valid stops the controller | Accepted invalid input | Two profiles match one job, or a VM starts with `admin/admin` | PR-020, PR-021, ADR-0006, ADR-0009 | P2 |
| R-15 | The token replacement changes no job result | Partial failure during replacement | Jobs in progress lose their agent registration | PR-023, SE-03, ADR-0008 | P2 |
| R-16 | Telemetry measures the quality targets and contains no secret | Missing or wrong instrument; attribute leak | The pilot cannot prove QA-01, QA-02, or QA-04; a token reaches the telemetry backend | PR-024, PR-025, OP-05 | P2 |
| R-17 | The controller obeys the Stacks API rate limits | Retry storm | Buildkite limits the stack, and all jobs wait | CO-01, QS-6 | P3 |
| R-18 | The controller recovers in less than 15 minutes | Recovery error | The queue stops for longer than the target | QA-03, QS-6, QS-7 | P3 |
| R-19 | The release artifacts are reproducible and distroless | Build configuration drift | The image runs as root, or the version is not pinned | CO-04, target design section 15 | P3 |
| R-20 | The controller stays compatible with its dependencies | Compatibility drift | An Orchard, Stacks API, or agent v4 change breaks the fleet without a signal | CO-01, CO-02, CO-06, ADR-0005 | P3 |
| R-21 | The distributed work carries one licence | Licence contamination | A dependency with a field-of-use restriction enters the binary, so the project cannot publish the image under the AGPL-3.0 | ADR-0010, CO-04 | P2 |

## 4. Test levels

Each level MUST give a signal that no cheaper level gives. A behaviour that two levels prove MUST
have a different failure mode at each level.

| Level | Name | Unique signal | Environment | Runtime target |
|---|---|---|---|---|
| L0 | Static analysis and build | Configuration drift, lint rules, image properties, dependency pins | CI, Linux | 3 min |
| L1 | Unit | Decision logic in pure functions, and the generated VM specification | CI, Linux | 1 min |
| L2 | Component | The tick loop with in-memory fakes: invariants over random event sequences, restart, outages | CI, Linux | 5 min |
| L3 | Contract | The behaviour of the two clients against recorded responses and the real Orchard API | CI, Linux | 2 min |
| L4 | Integration | The real Orchard API and scheduler with synthetic workers, and a fake Stacks server | CI, Linux | 10 min |
| L5 | Load | Tick duration, API call budget, and leaks at the target scale | CI, nightly | 30 min |
| L6 | End-to-end | Real Tart VMs, the guest-agent transport, real Buildkite, and real isolation | One development Mac | 60 min |

The Matcher, the Admission function, and the Reaper decision function MUST be pure functions with
an injected clock (target design, section 12). Because of this, L1 and L2 can prove most decisions
without a VM.

## 5. Invariants

These invariants are the standing oracles. L2 asserts all of them after each event, and L4 and L6
assert them at the end of each scenario.

* **I-1:** each job UUID has a maximum of one owned VM.
* **I-2:** the number of owned VMs is not more than the free slots and not more than
  `maxInFlight`.
* **I-3:** no VM specification contains the cluster agent token or the Orchard credentials.
* **I-4:** each VM specification has `netSoftnet: true`, no host directories, and
  `restart_policy: Never`, and it has no `username` and no `password` with the guest-agent
  transport.
* **I-5:** the controller sends no finish call for a job after an agent acquired it.
* **I-6:** at the end of a scenario, no owned VM remains in Orchard.
* **I-7:** no metric attribute contains a job UUID or a VM name.
* **I-8:** an unmatched job uses no VM slot.

## 6. Test matrix

All actions are `ADD`, because the repository has no tests. "Gate" is `required` when the gate
stops a merge or a release, and `diagnostic` when it reports only.

### 6.1 Static analysis and build (L0)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-120 | R-19, CO-04 | The golangci-lint analysis, `mise run lint-format`, `mise run lint-config`, and `mise run snapshot` (`goreleaser check`, a snapshot release, then `scripts/check-image.sh`) | Each command exits 0. In CI the analysis runs through the golangci-lint action, which writes each finding as an annotation on the changed line. `scripts/check-image.sh` confirms that the snapshot image runs as user 65532 and that the configuration names the base `gcr.io/distroless/static-debian13:nonroot` | required |
| T-121 | Target design section 4 | `scripts/check-c4.sh`: `structurizr validate` on `workspace.dsl`, then `render-c4-md.sh` | The command exits 0, and the committed `c4.md` is not different from the generated file | required |
| T-124 | CO-04, R-21 | Document integrity, checked by `scripts/check-docs.py` | Every identifier that a document cites exists (`PR-`, `QA-`, `SC-`, `SE-`, `OP-`, `CO-`, `DR-`, `QS-`, `T-`, `W-`, and `ADR-`); every requirement appears in an `Implements` line of the target design; no ADR cites a later ADR; every relative link resolves; every line outside a table or a code block has 100 characters or less | required |
| T-123 | CO-04 | The commit messages of a pull request, checked with `scripts/check-commits.py` | Each message follows Conventional Commits 1.0.0, with a type of the CONTRIBUTING list, a subject line of 72 characters or less, a lowercase description with no full stop, and a body that wraps at 72 | required |
| T-122 | R-20, R-21, ADR-0005, ADR-0010 | `scripts/check-deps.py` over `go.mod` and the tree | `go.mod` has no Orchard dependency and no `replace` directive; the repository contains no file of the Orchard repository; the Go version is the version that the design states; `stacksapi`, when `go.mod` names it, is v1.2.0 or later | required |

### 6.2 Unit (L1)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-001 | R-08, PR-003, ADR-0006 | Match: exact map, extra tag, missing tag, empty `match`, the `queue` key removed, a rule that is not `key=literal`, a wildcard, and a negation | The returned profile, or the mismatch reason with the job tags and all `match` maps | required |
| T-002 | R-14, PR-020, PR-021, ADR-0009 | Configuration validation: duplicate names, equal `match` maps, an empty or `queue` match key, `startupTransport: ssh` without credentials, `tart-guest-agent` with credentials, an environment variable that is empty, a timeout sum more than `jobTokenLifetime`, an image without a digest | The controller refuses to start and names the rule. An image without a digest gives a warning only | required |
| T-003 | R-01, R-02, PR-030, ADR-0004, ADR-0009 | The VM specification builder, for each profile shape | I-3, I-4; the name matches `bk-<controllerID>-<job-uuid>` and has a maximum of 63 characters; `labels` contains only `workerLabels`; `resources` contains `tart-vms`, `logical-cores`, and `memory-mib`; the environment contains only the documented variables | required |
| T-004 | R-05, R-09, R-10, ADR-0002 | Admission: free capacity with foreign VMs on the worker, hypothetical placement of unplaced VMs, `maxInFlight: 0` and a value more than 0, a candidate that does not fit, and the order by `priority` then `scheduled_at` | The admitted list, its order, and the unmatched list. A candidate that does not fit does not stop the next candidate | required |
| T-005 | R-03, R-04, R-06, R-13, PR-002, PR-007, PR-008 | The Reaper decision function: one case for each row of the decision table, a state that matches no row, a job state that is not causally fresh, and a stale job state | The decided action and `reason` for each case. A state that matches no row gives no action and one warning record | required |
| T-006 | R-14, target design section 7.3 | Timeout arithmetic with the default values and with a profile override | `unplaced + boot + acquire` is not more than `jobTokenLifetime`, and the validation fails when it is more | required |
| T-007 | R-08, PR-004, PR-008 | The finish message: an unmatched job, a boot timeout, and an acquire timeout with startup-script output | The message contains the tags and the profiles, or the last 50 log lines. The message is cut at 4 KB on a rune boundary | required |
| T-008 | R-16, PR-025 | Telemetry construction: metric attributes, and the trace identifier of a job | I-7, and the trace identifier is the 128 bits of the job UUID | required |
| T-009 | R-08, PR-009, PR-012 | The stack notification for each step, and the finish call of a failed job | The notifications are "VM requested", "placed on <worker>", "booting", "failed: …", and "VM deleted: …". Each finish call of the controller has `exit_status -1` and `signal_reason: stack_error` | required |

### 6.3 Component (L2)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-020 | R-03, R-04, R-05, R-06, ADR-0001, ADR-0002, ADR-0003 | Property-based runs over random event sequences: jobs arrive, jobs are cancelled, workers stop, VMs fail, and the controller restarts | I-1, I-2, I-5, I-6, I-8 after each event; the controller creates a VM only for a reserved job | required |
| T-021 | R-11, PR-010, QS-4 | A stop and a start at each crash window of the target design, section 7.4 | Each job has a maximum of one VM; no VM of an ended job remains; `reservedUntil` is unknown, so the first pass extends all reservations | required |
| T-022 | R-12, PR-011 | A paused queue with waiting jobs and unmatched jobs | No VM starts, no job finishes, and VMs in progress continue | required |
| T-023 | R-13, PR-007 | A cancellation while the VM boots | The VM is deleted, and no agent acquires the job | required |
| T-024 | R-17, R-18, QS-6, QS-7 | Outages: Orchard not available, Buildkite not available, HTTP 429 with `Retry-After`, and a worker that goes offline | Admission stops; readiness fails; the controller obeys `Retry-After`; only rows 1 and 2 delete VMs with stale data; an offline worker gives rows 7 and 9 | required |
| T-025 | R-09, target design section 7.2 row 13 | Reservation renewal for many VMs in one tick | One `batch-reserve` call contains all extensions, and the renewal starts before `reservationRenewBefore` | required |

### 6.4 Contract (L3)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-040 | R-08, R-15, R-17, CO-01 | The Stacks client against recorded responses: a partial `batch-reserve` result, `not_issued` tokens, HTTP 422 on finish, HTTP 401, HTTP 429 with `Retry-After`, and an empty scheduled-jobs page | Each response gives the documented decision. HTTP 401 makes the readiness check fail. HTTP 422 marks the job as ignored and sends no second call | required |
| T-041 | R-20, ADR-0005, ADR-0010, ADR-0011 | The Orchard client of this project against `orchard dev --synthetic`: create, list with a name prefix, delete, `409 Conflict` for a name that exists, and `GET /v1/vms/{name}/events`. `go generate ./...` with a difference check compares the generated wire types with the pinned upstream version | The real API accepts the specification; a `409` counts as a successful launch; the event stream gives the script output. A field that the API rejects, or a field that upstream renames, fails the gate | required |

### 6.5 Integration (L4)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-060 | R-03, R-04, PR-009, walking skeleton | One job from `scheduled` to a deleted VM, with `orchard dev --synthetic` and the fake Stacks server | The job reaches `finished`, the VM is deleted, I-6 holds at the end, and the stack notifications contain each step | required |
| T-061 | R-05, R-09, PR-005, QS-1 | Three matched jobs and two free slots | Two VMs start, the third job stays `scheduled` and visible, and it starts when a slot is free | required |
| T-062 | R-08, PR-004, QS-3 | A job with tags that match no profile | The job finishes with `exit_status -1` at the next poll, the message lists the profiles, and no VM starts | required |
| T-063 | R-15, ADR-0008 | A revoked cluster agent token | The Stacks API returns HTTP 401, and the readiness check fails | required |
| T-064 | R-20, CO-02 | The pinned Orchard version starts, and the controller sends the current VM specification | The API accepts each field that the design uses. A rejected field fails the gate and starts a design review | required |

### 6.6 Load (L5)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-080 | R-05, R-17, SC-05, CO-01 | 50 synthetic workers with 2 slots and 500 generated jobs, with the arrival pattern of the demand model | The tick duration stays inside its budget; list and reserve stay under 10 requests each second; get-states stays under 20 requests each second; I-1, I-2, and I-6 hold; `jobs.oldest_waiting.age` follows the queue | diagnostic |

### 6.7 End-to-end (L6, one Mac)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| T-100 | R-03, ADR-0009, PR-001, PR-002, PR-026, PR-033 | A real job on a real Tart VM with the guest-agent transport, on the pilot profile | The agent starts, `xcodebuild -version` reports Xcode 27, the job finishes, the VM is deleted, the VM specification contains no credentials, and the job log is visible on the Buildkite job page | required before a release |
| T-101 | R-01, R-02, QS-5 | A job that tries to reach the host, another VM, and a private address that the profile does not permit | Each connection fails; a permitted CIDR succeeds; the VM contains no cluster agent token | required before a release |
| T-102 | R-07, PR-032, QS-8 | A job that uploads an artifact | The artifact is in owner-managed storage in the operating country, and Buildkite-managed storage has no artifact of the job | required before a release |
| T-103 | R-08, PR-008 | A profile with an image digest that does not exist, and an image whose agent does not start | The job fails after the boot timeout, and after the acquire timeout with the last log lines | required before a release |
| T-104 | R-11, R-18, QS-4, QS-6 | A controller stop during a job, and an Orchard controller restart | Jobs in progress continue; after recovery no duplicate VM exists and no VM of an ended job remains; the recovery takes less than 15 minutes | required before a release |
| T-105 | R-15, PR-023, ADR-0008 | The token replacement procedure with jobs in progress | The jobs complete, new jobs start with the new token, and no job fails because of the replacement | required before a release |
| T-106 | R-16, QA-01, QS-2 | A measurement run of 20 jobs with the image on the host | `job.phase.duration` gives a measured start time, which replaces the estimate of ADR-0001 and ADR-0003 | diagnostic |

### 6.8 Manual reviews

These obligations are reviews, not automated tests. Both come from ADR confirmations.

| ID | Basis | Review | When |
|---|---|---|---|
| T-140 | ADR-0007, PR-031 | A review of the Buildkite queue confirms that only VMs of this fleet serve it | Before the controller goes live |
| T-141 | ADR-0008, SE-03 | A review of the token confirms that `expires_at` is 90 days or less, and that `allowed_ip_addresses` contains the egress addresses of the controller and of the job VMs | Before the controller goes live, and at each replacement |

## 7. Environments, data, and secrets

| Environment | Contents | Used by |
|---|---|---|
| CI (Linux) | Go toolchain, `orchard dev --synthetic`, a fake Stacks API server, an OTLP collector that records | L0 to L5 |
| Development Mac | Tart, one Orchard worker, a Buildkite test cluster and queue, one test pipeline | L6 |
| Pilot fleet | 2 to 4 Macs, the Kubernetes reference deployment, telemetry | Section 8 |

Rules for data and secrets:

* The fake Stacks server MUST be the `FakeAgentServer` of `github.com/buildkite/agent-stack-k8s`
  (MIT licence), which Buildkite maintains against its own API and which uses the real `stacksapi`
  types. The project MUST NOT write a second fake of this API. The implementation plan, section
  3.2 gives the import and the copy option.
* Tests MUST NOT use owner source code. The end-to-end pipeline MUST use a small synthetic
  repository.
* Test credentials MUST come from CI secrets, and they MUST be separate from the pilot
  credentials. A test that writes a token to a log or to telemetry MUST fail (I-3, I-7).
* No test uses customer data, personal data, or production artifacts.
* L6 needs a Mac that can host the guest macOS version (CO-07). The pilot confirms this
  assumption.

## 8. Gates and pilot evidence

| Gate | Contents | Rule |
|---|---|---|
| Pull request | L0, L1, L2, L3, L4 | All required obligations pass. CI fails the merge if one fails |
| Nightly | L5, and long property-based runs of T-020 | A failure opens an issue. It does not stop a merge |
| Release | L6 on the development Mac | All obligations that are "required before a release" pass |
| Go live | T-140 and T-141 | Both reviews are complete |

Pilot evidence replaces estimates. It is measurement, not a test:

* **E-1:** two weeks with 0 VMs that remain after their job ends, and 0 jobs that the controller
  failed incorrectly (QA-04, requirements section 6).
* **E-2:** the measured start time and wait time against QA-01 and QA-02, with the percentile that
  the baseline assumes.
* **E-3:** the measured time from the job end to the VM deletion, which gives the value that QA-06
  does not have.
* **E-4:** the measured image count for each host, which gives the value that SC-09 does not have.

## 9. Exclusions and accepted risks

Each item is a decision, not missing work.

* **The Orchard scheduler and placement.** Orchard owns this behaviour. The controller only
  accounts for capacity, and T-004 and T-041 prove the accounting against the real API.
* **Tart, Softnet, and Apple Virtualization isolation.** The project proves that its specification
  requests isolation (T-003), and it proves the effect one time on real hardware (T-101). It does
  not test the isolation mechanism itself.
* **The Buildkite retry engine, job log storage, and the agent internals.** These are supplier
  behaviour. The project proves only its own signal (`signal_reason: stack_error`) in T-007 and
  T-062.
* **A browser, device, or operating-system matrix.** The controller has no user interface. The
  only operating-system cells are Linux for the controller and macOS for the VMs.
* **The Kubernetes chart beyond a render and one smoke deployment.** The controller MUST NOT
  depend on Kubernetes (PR-022), so the platform is not a source of product risk.
* **The host provisioning with Ansible.** One smoke run on the development Mac is sufficient
  evidence for a fixed host pool.
* **Warm pools, high availability, more than one queue, and more than one region.** These are
  product non-goals. Each needs its own ADR and its own evidence.
* **Coverage percentage as a gate.** The gates use named obligations. A coverage report is
  diagnostic only.

## 10. Review and retirement triggers

Review this strategy when one of these events occurs:

* Orchard publishes a release later than the pinned version, or it changes the VM specification.
  T-041, T-064, and T-122 change with it.
* The Buildkite Stacks API changes a limit, a state, or a status code. T-040 changes with it.
* Buildkite publishes an agent release line after v4. T-100 and the image contents change.
* The pilot gives E-1 to E-4. The estimates in ADR-0001 and ADR-0003 and the values of QA-06 and
  SC-09 then come from measurement, and T-106 can retire.
* The fleet uses a profile with the SSH transport. T-003 and T-100 then need an SSH case.
* A new Xcode or macOS image changes the pilot profile (CO-05).

## 11. Open items

Each item is an unknown that can change a level or a scenario. Each has the smallest next action.

| ID | Unknown | Effect | Next action |
|---|---|---|---|
| O-1 | The Buildkite `FakeAgentServer` is a test double, not a conformance suite. Its fidelity is unproven for this controller. The finish call of the Stacks API takes an exit status and a detail and no `signal_reason`, so the `signal_reason: stack_error` of T-009 is what Buildkite gives a job that a stack finishes, which no fake can show. | T-040 and T-060 can prove behaviour that the real API does not have. T-009 proves `exit_status -1` locally, and not `signal_reason`. | During W-24, when a Buildkite test cluster exists, record its responses, compare them with the fake, and use them as the fixtures of T-040 (owner decision, 2026-09-20). Read the `signal_reason` of a job that the controller finished there, for T-009. |
| O-2 | `orchard dev --synthetic` does not run a real startup script. | L4 proves only that the API accepts the guest-agent transport. The transport itself is unproven until L6. | Send one VM specification with `transport: tart-guest-agent` to a synthetic controller, and record whether validation accepts it. **Evidence (2026-09-27):** on the development Mac, `TestARealMachineRunsTheStartupScript` (`mise run tart-test`) ran the controller, `orchard dev` with a real Tart worker, and the agent image. The guest agent ran the startup script: `buildkite-agent` v4.0.6 started, and Buildkite refused its job token with `401 Unauthorized: Invalid job acquisition token`. The fake Stacks server issues a token that Buildkite does not know, so no job runs there. T-100 stays the only proof of a job that runs. |
| O-3 | Agent v4 support for an injected `traceparent` is not verified (target design, section 10.4). | The agent spans can stay outside the job trace. | Run one job on the development Mac with `profile.otel.enabled: true`, and examine the trace. |
| O-4 | The host macOS version that a macOS 27 guest needs is an assumption (CO-07). | L6 and the pilot can need a host update before the hosts arrive on 2026-09-22. | Start a Golden Gate VM on the development Mac before the pilot hosts arrive. **Evidence (2026-09-27):** macOS 27 Golden Gate VMs run on the macOS 27 development Mac (W-23, and the test of O-2). An older host is not tested. |
| O-5 | The tick duration budget for T-080 has no value yet. | The load test has no pass condition for the tick. It reports the tick, and it judges the invariants and the request budget. | Take the p95 of the first run of the `load` workflow on the runner as the baseline, and set the repository variable `LOAD_TICK_BUDGET` to two times that value. |
