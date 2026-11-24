---
status: accepted
date: 2026-09-19
approved: 2026-09-20
owner: the maintainers
---
# bk-agent-stack-orchard — Test strategy

## Related documents

* [Product requirements](../product/requirements.md): requirement identifiers such as `PR-005`
* [System design baseline](../architecture/system-design-baseline.md): driver identifiers such as
  `QA-01`, and quality scenarios such as `QS-1`
* [Target design](../architecture/target-design.md)
* [Architecture decision records](../adrs/README.md): [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md) to [ADR-0022](../adrs/0022-authenticate-the-checkout-with-files-of-a-read-only-mount.md)

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

This document follows the [writing guide](../writing-guide.md), in the third person.

Identifiers in this document:

* `R-nn` is a risk in section 3.
* `I-n` is an invariant in section 5.
* `T-nnn` is a test obligation in section 6.
* `E-n` is fleet evidence in section 8.

## 1. Purpose and scope

This document decides which evidence the project produces, at which level, and which gate holds
it. It does not contain test code, and it does not define the pipelines of the application that
the fleet builds.

In scope: the controller, its two API clients, the VM specification that it sends to Orchard, the
telemetry that measures the quality targets, the release artifacts, and the reference build of
the job VM images in `images/`. The project publishes no job VM image, and the fleet operator
builds the images of its fleet (owner decision, 2026-11-03, [ADR-0020](../adrs/0020-leave-the-job-vm-images-to-the-operator.md)).

Out of scope: the behaviour of Tart, Orchard, Buildkite, and Apple Virtualization; dashboards and
alert rules for a specific telemetry backend (target design, section 10.6).

Section 6 gives each obligation as `ADD` because the repository had no tests when the maintainers
accepted this strategy (2026-09-20). The implementation plan records which work unit added each
test. Section 9 gives the obligations that the project does not take, with the reason.

## 2. Delivery shape and its effect on evidence

The first increment is a walking skeleton: Stack, Poller, Matcher, Launcher, and the minimum
telemetry, against `orchard dev --synthetic` and a fake Stacks API server. One job goes from
`scheduled` to a deleted VM. The full Reaper decision table (target design, section 7.2) follows
the skeleton.

A controller that creates VMs but does not delete them fills the fleet. Because of this, the
skeleton MUST contain two Reaper rows: row 2 (`maxVMLifetime`) and row 4 (the job is terminal).
These two rows are the minimum teardown, and [T-005](#t-005) covers them in the skeleton gate. The other
eleven rows come later.

Until all thirteen rows exist and [T-020](#t-020) passes, these limits apply:

* The skeleton MUST run only against `orchard dev --synthetic` or a development Mac with one
  worker, and only with a Buildkite test cluster.
* The skeleton MUST NOT run against a production queue, and MUST NOT run against a host that serves
  other work ([PR-031](../product/requirements.md#pr-031)).
* Each skeleton run MUST end with a check that no owned VM remains. [T-060](#t-060) makes this check.
* [T-060](#t-060) checks the stack notifications that the skeleton sends: "VM requested", "placed on
  <worker>", "booting", and "VM deleted: job_terminal". The target design, section 10.5, names a
  notification for each step of the VM, and [W-16](../delivery/implementation-plan.md#w-16) owns them. [W-16](../delivery/implementation-plan.md#w-16) MUST extend [T-060](#t-060) with each
  notification that it adds, so that [T-060](#t-060) checks every step when the design is whole.

| Increment | Components | Evidence that ends the increment |
|---|---|---|
| 1 — Walking skeleton | Stack, Poller, Matcher, Launcher, Telemetry, Reaper rows 2 and 4 | [T-001](#t-001), [T-002](#t-002), [T-003](#t-003), [T-004](#t-004) (capacity only), [T-005](#t-005) (rows 2 and 4), [T-040](#t-040), [T-060](#t-060), [T-120](#t-120), [T-122](#t-122) |
| 2 — Full lifecycle | Reaper rows 1 and 3, 5 to 13; Admission with unplaced accounting; JobStates | [T-005](#t-005) (all rows), [T-020](#t-020), [T-021](#t-021), [T-022](#t-022), [T-023](#t-023), [T-025](#t-025), [T-061](#t-061), [T-062](#t-062) |
| 3 — Operability | Readiness, token replacement, full telemetry, outage behaviour | [T-008](#t-008), [T-009](#t-009), [T-024](#t-024), [T-041](#t-041), [T-063](#t-063), [T-064](#t-064), [T-121](#t-121) |
| 4 — Fleet readiness | Real Tart, real Buildkite cluster, one Mac | [T-080](#t-080), [T-100](#t-100) to [T-107](#t-107), [T-140](#t-140), [T-141](#t-141) |

## 3. Risk map

Priority: `P1` is a risk that can lose money, credentials, or the fleet. `P2` can fail jobs or
break a quality target. `P3` degrades operation.

| ID | Protected outcome | Defect class | Impact if it happens | Basis | Priority |
|---|---|---|---|---|---|
| <a id="r-01"></a>R-01 | A VM holds only a credential for its own job | Accepted invalid input; incorrect specification | A build reads the cluster agent token and reaches all self-hosted queues of the cluster | [PR-030](../product/requirements.md#pr-030), [SE-02](../architecture/system-design-baseline.md#se-02), [ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md), [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md), [QS-5](../architecture/system-design-baseline.md#qs-5) | P1 |
| <a id="r-02"></a>R-02 | A build cannot reach the host, another VM, or a private network | Incorrect specification; missing isolation field | A build reaches internal services or another build | [PR-030](../product/requirements.md#pr-030), [SE-01](../architecture/system-design-baseline.md#se-01), [QS-5](../architecture/system-design-baseline.md#qs-5) | P1 |
| <a id="r-03"></a>R-03 | Every VM that the controller creates is deleted | Partial failure; crash between finish and delete; missed state | VMs fill the fleet; at 2 slots for each host, a few leaks stop the queue | [PR-002](../product/requirements.md#pr-002), [QA-04](../architecture/system-design-baseline.md#qa-04), [QS-4](../architecture/system-design-baseline.md#qs-4) | P1 |
| <a id="r-04"></a>R-04 | Each job has a maximum of one VM | Duplicate delivery; race between tick steps; restart | Two agents acquire one job, or a slot is lost | [PR-001](../product/requirements.md#pr-001), [PR-010](../product/requirements.md#pr-010), [DR-01](../architecture/system-design-baseline.md#dr-01), [QS-4](../architecture/system-design-baseline.md#qs-4) | P1 |
| <a id="r-05"></a>R-05 | Owned VMs never exceed free capacity | Boundary error in capacity arithmetic; foreign VMs not counted | Orchard cannot place VMs; jobs wait without a reason | [PR-005](../product/requirements.md#pr-005), [SC-02](../architecture/system-design-baseline.md#sc-02), [QS-1](../architecture/system-design-baseline.md#qs-1), [ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md) | P1 |
| <a id="r-06"></a>R-06 | The controller never fails a job that an agent acquired, and never deletes its VM while it runs | Incorrect success or failure; stale job state; an API answer that leaves jobs out | A running build dies, and the pipeline reports a false failure | [PR-008](../product/requirements.md#pr-008), [QA-05](../architecture/system-design-baseline.md#qa-05), design principle 5 | P1 |
| <a id="r-07"></a>R-07 | Checkouts and build data stay in the operating country; artifacts MAY use Buildkite-managed storage | Data leaves the host of the VM by a path other than the Buildkite log and artifact services | Source code or build data leaves owner infrastructure | [PR-032](../product/requirements.md#pr-032), [SE-05](../architecture/system-design-baseline.md#se-05), [SE-08](../architecture/system-design-baseline.md#se-08), [QS-8](../architecture/system-design-baseline.md#qs-8) | P1 |
| <a id="r-08"></a>R-08 | A job whose VM cannot start fails with a message before its expiry, and a job that no profile carries gets a notification | Rejected valid input; silent wait | A pipeline waits until the job expires, with no reason | [PR-004](../product/requirements.md#pr-004), [PR-008](../product/requirements.md#pr-008), [QA-05](../architecture/system-design-baseline.md#qa-05), [QS-3](../architecture/system-design-baseline.md#qs-3) | P2 |
| <a id="r-09"></a>R-09 | A job that does not fit stays `scheduled` and visible | Hidden reservation; expired reservation | Jobs disappear from the queue, and other stacks cannot see them | [PR-005](../product/requirements.md#pr-005), [ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md), [QS-1](../architecture/system-design-baseline.md#qs-1) | P2 |
| <a id="r-10"></a>R-10 | Jobs start in Buildkite order | Ordering error | A high-priority job waits behind an old low-priority job | [PR-006](../product/requirements.md#pr-006) | P2 |
| <a id="r-11"></a>R-11 | A restart does not change the result of any job | Recovery error; lost in-memory state | Duplicate VMs, or VMs of ended jobs remain | [PR-010](../product/requirements.md#pr-010), [QS-4](../architecture/system-design-baseline.md#qs-4), [DR-02](../architecture/system-design-baseline.md#dr-02) | P2 |
| <a id="r-12"></a>R-12 | A paused queue starts nothing and fails nothing | Missed state | Jobs fail while an operator holds the queue | [PR-011](../product/requirements.md#pr-011) | P2 |
| <a id="r-13"></a>R-13 | A cancelled job releases its VM | Cancellation race | A slot is held for a job that nobody wants | [PR-007](../product/requirements.md#pr-007) | P2 |
| <a id="r-14"></a>R-14 | A configuration that is not valid stops the controller | Accepted invalid input | Two profiles match one job, or a VM starts with `admin/admin` | [PR-020](../product/requirements.md#pr-020), [PR-021](../product/requirements.md#pr-021), [ADR-0006](../adrs/0006-route-jobs-to-vm-profiles-by-agent-tags.md), [ADR-0009](../adrs/0009-run-startup-scripts-over-the-tart-guest-agent.md) | P2 |
| <a id="r-15"></a>R-15 | The token replacement changes no job result | Partial failure during replacement | Jobs in progress lose their agent registration | [PR-023](../product/requirements.md#pr-023), [SE-03](../architecture/system-design-baseline.md#se-03), [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md) | P2 |
| <a id="r-16"></a>R-16 | Telemetry measures the quality targets and contains no secret | Missing or wrong instrument; attribute leak | A fleet cannot prove [QA-01](../architecture/system-design-baseline.md#qa-01), [QA-02](../architecture/system-design-baseline.md#qa-02), or [QA-04](../architecture/system-design-baseline.md#qa-04); a token reaches the telemetry backend | [PR-024](../product/requirements.md#pr-024), [PR-025](../product/requirements.md#pr-025), [OP-05](../architecture/system-design-baseline.md#op-05) | P2 |
| <a id="r-17"></a>R-17 | The controller obeys the Stacks API rate limits | Retry storm | Buildkite limits the stack, and all jobs wait | [CO-01](../architecture/system-design-baseline.md#co-01), [QS-6](../architecture/system-design-baseline.md#qs-6) | P3 |
| <a id="r-18"></a>R-18 | The controller recovers in less than 15 minutes | Recovery error | The queue stops for longer than the target | [QA-03](../architecture/system-design-baseline.md#qa-03), [QS-6](../architecture/system-design-baseline.md#qs-6), [QS-7](../architecture/system-design-baseline.md#qs-7) | P3 |
| <a id="r-19"></a>R-19 | The release artifacts are reproducible and distroless | Build configuration drift | The image runs as root, or the version is not pinned | [CO-04](../architecture/system-design-baseline.md#co-04), target design section 15 | P3 |
| <a id="r-20"></a>R-20 | The controller stays compatible with its dependencies | Compatibility drift | An Orchard, Stacks API, or agent v4 change breaks the fleet without a signal | [CO-01](../architecture/system-design-baseline.md#co-01), [CO-02](../architecture/system-design-baseline.md#co-02), [CO-06](../architecture/system-design-baseline.md#co-06), [ADR-0005](../adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md) | P3 |
| <a id="r-21"></a>R-21 | The distributed work carries one licence | Licence contamination | A dependency with a field-of-use restriction enters the binary, so the project cannot publish the image under the AGPL-3.0 | [ADR-0010](../adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md), [CO-04](../architecture/system-design-baseline.md#co-04) | P2 |
| <a id="r-22"></a>R-22 | A job behind a long head of the queue still starts | A flood of jobs that never leave the list, such as jobs that no profile carries | One pipeline upload stops the fleet for every pipeline of the queue | [QA-02](../architecture/system-design-baseline.md#qa-02), [ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md), [ADR-0019](../adrs/0019-sweep-the-queue-behind-a-long-head.md) | P2 |

## 4. Test levels

Each level MUST give a signal that no cheaper level gives. A behaviour that two levels prove MUST
have a different failure mode at each level.

| Level | Name | Unique signal | Environment | Runtime target |
|---|---|---|---|---|
| L0 | Static analysis and build | Configuration drift, lint rules, image properties, dependency pins | CI, Linux | 3 min |
| L1 | Unit | Decision logic in pure functions, and the generated VM specification | CI, Linux | 1 min |
| L2 | Component | The tick loop with in-memory fakes: invariants over random event sequences, restart, outages | CI, Linux | 5 min |
| L3 | Contract | The behaviour of the two clients against recorded responses and the real Orchard API | CI, Linux; CI, macOS arm64 for the Orchard client | 2 min |
| L4 | Integration | The real Orchard API and scheduler with synthetic workers, and a fake Stacks server | CI, macOS arm64 | 10 min |
| L5 | Load | Tick duration, API call budget, and leaks at the target scale | CI, macOS arm64, nightly | 30 min |
| L6 | End-to-end | Real Tart VMs, the guest-agent transport, real Buildkite, and real isolation | One development Mac | 60 min |

The Matcher, the Admission function, and the Reaper decision function MUST be pure functions with
an injected clock (target design, section 12). Because of this, L1 and L2 can prove most decisions
without a VM.

## 5. Invariants

These invariants are the standing oracles. L2 asserts all of them after each event, and L4 and L6
assert them at the end of each scenario.

* <a id="i-1"></a>**I-1:** each job UUID has a maximum of one owned VM.
* <a id="i-2"></a>**I-2:** the number of scheduled owned VMs is not more than the free slots, and the number of
  owned VMs is not more than `maxInFlight`. A failed VM is not scheduled, and Orchard frees its
  resources, so it takes no slot while it waits for the Reaper. It still counts against
  `maxInFlight`.
* <a id="i-3"></a>**I-3:** no VM specification contains the cluster agent token or the Orchard credentials.
* <a id="i-4"></a>**I-4:** each VM specification has `netSoftnet: true`, only the host directories
  of its profile, each with `ro: true`, and `restart_policy: Never`, and it has no `username` and
  no `password` with the guest-agent transport.
* <a id="i-5"></a>**I-5:** the controller sends no finish call for a job after an agent acquired it.
* <a id="i-6"></a>**I-6:** at the end of a scenario, no owned VM remains in Orchard.
* <a id="i-7"></a>**I-7:** no metric attribute contains a job UUID or a VM name.
* <a id="i-8"></a>**I-8:** an unmatched job uses no VM slot, and the controller never reserves it or finishes it.
* <a id="i-9"></a>**I-9:** the controller deletes the VM of a job that an agent acquired, and that has not ended,
  only by row 2 (the maximum lifetime) or by row 7 (the VM failed) of the Reaper decision table.

## 6. Test matrix

All actions are `ADD`, as section 1 explains. The
[implementation plan](../delivery/implementation-plan.md) records which work unit added each test.
"Gate" is `required` when the gate stops a merge or a release, and `diagnostic`
when it reports only.

### 6.1 Static analysis and build (L0)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-120"></a>T-120 | [R-19](#r-19), [CO-04](../architecture/system-design-baseline.md#co-04) | The golangci-lint analysis, `mise run lint-format`, `mise run lint-config`, and `mise run snapshot` (`goreleaser check`, a snapshot release, then `scripts/check-image.sh`) | Each command exits 0. In CI the analysis runs through the golangci-lint action, which writes each finding as an annotation on the changed line. `scripts/check-image.sh` confirms that the snapshot image runs as user 65532 and that the configuration names the base `gcr.io/distroless/static-debian13:nonroot` | required |
| <a id="t-121"></a>T-121 | Target design section 4 | `scripts/check-c4.sh`: `structurizr validate` on `workspace.dsl` | The command exits 0 | required |
| <a id="t-124"></a>T-124 | [CO-04](../architecture/system-design-baseline.md#co-04), [R-21](#r-21) | Document integrity, checked by `scripts/check-docs.py` | Every identifier that a document cites exists, for each family of the glossary; every requirement appears in an `Implements` line of the target design; no ADR cites a later ADR; every relative link resolves; every line outside a table or a code block has 100 characters or less, and a URL in the line does not count; the file name of each ADR agrees with its title, and its status is a lifecycle value; a range from `ADR-0001` reaches the highest ADR that exists, except in an ADR file, because an accepted ADR keeps its meaning, and in a sentence that has a date, because that sentence is history; the glossary lists each identifier family that the script knows, with its number of digits | required |
| <a id="t-123"></a>T-123 | [CO-04](../architecture/system-design-baseline.md#co-04) | The commit messages of a pull request, checked with `scripts/check-commits.py` | Each message follows Conventional Commits 1.0.0, with a type of the CONTRIBUTING list, a subject line of 72 characters or less, a lowercase description with no full stop, and a body that wraps at 72 | required |
| <a id="t-122"></a>T-122 | [R-20](#r-20), [R-21](#r-21), [ADR-0005](../adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md), [ADR-0010](../adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md) | The tests of `internal/repocheck` over `go.mod` and the tree | `go.mod` has no Orchard dependency and no `replace` directive; the repository contains no file of the Orchard repository; the Go version is the version that the design states; `stacksapi`, when `go.mod` names it, is v1.2.0 or later | required |

### 6.2 Unit (L1)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-001"></a>T-001 | [R-08](#r-08), [PR-003](../product/requirements.md#pr-003), [ADR-0006](../adrs/0006-route-jobs-to-vm-profiles-by-agent-tags.md) | Match: exact map, extra tag, missing tag, empty `match`, the `queue` key removed, a rule that is not `key=literal`, a wildcard, and a negation | The returned profile, or the mismatch reason with the job tags and all `match` maps | required |
| <a id="t-002"></a>T-002 | [R-14](#r-14), [PR-020](../product/requirements.md#pr-020), [PR-021](../product/requirements.md#pr-021), [ADR-0009](../adrs/0009-run-startup-scripts-over-the-tart-guest-agent.md) | Configuration validation: duplicate names, equal `match` maps, an empty or `queue` match key, `startupTransport: ssh` without credentials, `tart-guest-agent` with credentials, an environment variable that is empty, a name for the VM that does not start with `VM_` or that starts with `OTEL_` or the controller prefix, `otel.endpoint` without https (loopback excepted), a `reservationExpiry` outside 1 to 3600 s or a `jobTokenLifetime` outside 1 to 1800 s (whole seconds only), a `softnetAllow` entry that is not an IPv4 CIDR with a prefix or that is too wide, a `match` key `queue`, `profile`, or `agent-stack`, a `match` value or a `buildkite.queue` with a comma, a quote, a newline, a carriage return, or padding, a negative `unplaced`, `boot`, or `acquire` timeout, a timeout sum that with one minute is more than `jobTokenLifetime`, an image without a digest, an `agent.source` that is not `download` or `image`, an `agent.url` that is not https, an `agent.sha256` that is not 64 lower-case hexadecimal characters, an agent pair with one value | The controller refuses to start and names the rule. An image without a digest gives a warning only; for `hostDirs`: a `name` that does not match `^[a-z0-9-]+$`, a duplicate `name`, a `path` that is not absolute and clean, that contains `..` anywhere, `:`, or a control character, `/`, a URL, and more than 8 entries; a `checkout` field that names no `hostDirs` entry of its profile, and `checkout: {}` that sets nothing; the warnings for `hostDirs` and `checkout` | required |
| <a id="t-003"></a>T-003 | [R-01](#r-01), [R-02](#r-02), [PR-030](../product/requirements.md#pr-030), [ADR-0004](../adrs/0004-treat-all-builds-as-untrusted.md), [ADR-0021](../adrs/0021-mount-operator-files-read-only-in-job-vms.md), [ADR-0022](../adrs/0022-authenticate-the-checkout-with-files-of-a-read-only-mount.md), [ADR-0009](../adrs/0009-run-startup-scripts-over-the-tart-guest-agent.md) | The VM specification builder, for each profile shape | [I-3](#i-3), [I-4](#i-4); the name matches `bk-<controllerID>-<job-uuid>` and has a maximum of 63 characters; `labels` contains only `workerLabels`; `resources` contains `tart-vms`, `logical-cores`, and `memory-mib`; the environment contains only the documented variables; each agent source gives its script, and the URL and the SHA-256 of a download are in the environment, not in the script; the script of each source, run in `sh` and `zsh` with fake tools, starts the downloaded agent only after a matching SHA-256, stops before an agent starts when the download, the check, or the extraction fails, and refuses an image agent that is not v4; with two `credential.helper` values in the global git configuration, the script removes them and says so, and with one value or none it changes nothing; with `BKASO_HOST_DIRS` set and a mount missing, the script names the mount and stops before an agent starts; with `BKASO_SSH_FROM` set, the script copies only the named SSH files into `~/.ssh` with modes `0700` and `0600`, writes their names and not their contents, and stops when the mount holds none of them; with `BKASO_GIT_CREDENTIALS_FROM` set, the helper answers `get` from `.git-credentials` of the mount and ignores `store` and `erase`, and the script stops when the file is missing | required |
| <a id="t-004"></a>T-004 | [R-05](#r-05), [R-09](#r-09), [R-10](#r-10), [ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md) | Admission: free capacity with foreign VMs on the worker (a failed VM that is not scheduled holds no slot), hypothetical placement of unplaced VMs, `maxInFlight: 0` and a value more than 0, a candidate that does not fit, and the order by `priority` then `scheduled_at` | The admitted list, its order, and the list of unmatched jobs that the launcher skips. A candidate that does not fit does not stop the next candidate. The open slots of each pool are `reserved` for unplaced and requested VMs, `unavailable` on a paused worker or one that no profile has the labels of, and `free` for the rest | required |
| <a id="t-005"></a>T-005 | [R-03](#r-03), [R-04](#r-04), [R-06](#r-06), [R-11](#r-11), [R-13](#r-13), [PR-002](../product/requirements.md#pr-002), [PR-007](../product/requirements.md#pr-007), [PR-008](../product/requirements.md#pr-008) | The Reaper decision function: one case for each row of the decision table, a state that matches no row, a job state that is not causally fresh, and a stale job state. The timeouts of a VM that this process did not start, from the profile that its tags name, and from the widest timeouts when they name no profile of the configuration | The decided action and `reason` for each case. A row that finishes a job acts only on a state of this tick. The ages of rows 10 to 12 count on the clock of the controller, so an Orchard clock that runs half a day behind or ahead changes none of these decisions. Row 10 counts from the token call of the job, and from the first listing when this process sent no token call or sent it after that listing. The lifetime of row 2 counts from `createdAt`, or from the first listing when `createdAt` is zero, so a restart that lists an old VM again deletes it at once. A row that reads the phase of a VM waits for a fresh snapshot. A finish that Buildkite refuses keeps the VM for that pass, and the next state decides without a second finish. Row 6 deletes the VM of a `reserved` job whose extension Buildkite answered with `not_reserved`, with no finish, also for a failed VM or a timeout, once a state asked for after the refusal arrives; an acquired state keeps the VM. A state that matches no row gives no action and one warning record | required |
| <a id="t-006"></a>T-006 | [R-14](#r-14), target design section 7.3 | Timeout arithmetic with the default values and with a profile override | `unplaced + boot + acquire` and one minute is not more than `jobTokenLifetime`, and the validation fails when it is more, also when the sum alone fits; for a profile with `hostDirs`, the resolved `timeouts.hostDirs` is a whole number of seconds, at least 1 s, and less than the resolved `acquire`, with the default and with a profile override, and a profile without `hostDirs` ignores it | required |
| <a id="t-007"></a>T-007 | [R-08](#r-08), [PR-004](../product/requirements.md#pr-004), [PR-008](../product/requirements.md#pr-008) | The finish message of a boot timeout and of an acquire timeout with startup-script output. The skip notification of an unmatched job | The finish message contains the last 50 log lines, and it is cut at 4 KB on a rune boundary. The skip notification says in short why no profile carries the job, and it fits in 256 characters | required |
| <a id="t-008"></a>T-008 | [R-16](#r-16), [PR-025](../product/requirements.md#pr-025) | Telemetry construction: metric attributes, the trace identifier of a job, the spans of a call for several jobs, and a span that failed | [I-7](#i-7), and the trace identifier is the 128 bits of the job UUID. Each job of a reserve, token, or skip-notice call has its own span of the call in its own trace, with its answer, and the log record about the job carries that trace. A span that records an error has the status Error. Each series of a configured profile, of a closed set of values, and of a source exists from the first observation, also while the stack registers, and a series that falls to zero reports 0. While the gate of admission is closed, `admission.blocked` is 1 for the reason of the gate, and the waiting jobs of the last queue read are still counted | required |
| <a id="t-009"></a>T-009 | [R-08](#r-08), [PR-009](../product/requirements.md#pr-009), [PR-012](../product/requirements.md#pr-012) | The stack notification for each step, and the finish call of a failed job | The notifications are "VM requested", "VM request failed", "placed on <worker>", "booting", "failed: …", and "VM deleted: …". Each start of a reserved job counts one outcome in `vm.starts`. Each finish call of the controller has `exit_status -1` and a detail, and no `signal_reason`, because the finish request has no such field. The platform generates `stack_error` ([PR-012](../product/requirements.md#pr-012)), which no fake can show | required |

### 6.3 Component (L2)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-020"></a>T-020 | [R-03](#r-03), [R-04](#r-04), [R-05](#r-05), [R-06](#r-06), [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md), [ADR-0002](../adrs/0002-check-capacity-before-reserving-jobs.md), [ADR-0003](../adrs/0003-start-job-vms-on-demand.md) | Property-based runs over random event sequences: jobs arrive, jobs are cancelled, workers stop, VMs fail, and the controller restarts | [I-1](#i-1), [I-2](#i-2), [I-5](#i-5), [I-6](#i-6), [I-8](#i-8), [I-9](#i-9) after each event; the controller creates a VM only for a reserved job | required |
| <a id="t-021"></a>T-021 | [R-11](#r-11), [PR-010](../product/requirements.md#pr-010), [QS-4](../architecture/system-design-baseline.md#qs-4) | A stop and a start at each crash window of the target design, section 7.4 | Each job has a maximum of one VM; no VM of an ended job remains; `reservedUntil` is unknown, so the first pass extends all reservations | required |
| <a id="t-022"></a>T-022 | [R-12](#r-12), [PR-011](../product/requirements.md#pr-011) | A paused queue with waiting jobs and unmatched jobs | No VM starts, no job finishes, no new notification appears for an unmatched job, and VMs in progress continue | required |
| <a id="t-023"></a>T-023 | [R-13](#r-13), [PR-007](../product/requirements.md#pr-007) | A cancellation while the VM boots | The VM is deleted, and no agent acquires the job | required |
| <a id="t-024"></a>T-024 | [R-06](#r-06), [R-17](#r-17), [R-18](#r-18), [QS-6](../architecture/system-design-baseline.md#qs-6), [QS-7](../architecture/system-design-baseline.md#qs-7) | Outages: Orchard not available, only the lists of Orchard failing, Buildkite not available, only the get-states call not available, Buildkite or Orchard that accepts calls and does not answer, HTTP 429 with `Retry-After`, and a worker that goes offline | Admission stops, also on old job states while owned VMs exist; readiness fails; each call ends at its time limit, so rows 1 and 2 continue while Buildkite hangs and the reservations are extended while Orchard hangs; while Orchard hangs during a burst of jobs, the tick ends inside a few call limits, the controller reserves only the jobs that one step can start, and the first create that hangs stops the others; the controller obeys `Retry-After`; only rows 1 and 2 delete VMs with stale job states; while the lists of Orchard fail for longer than `stateFreshness`, the controller finishes no job on the old phase of a VM, extends the reservations of the VMs that start, and still deletes by row 4; an offline worker gives rows 7 and 9 | required |
| <a id="t-025"></a>T-025 | [R-09](#r-09), target design section 7.2 row 13 | Reservation renewal for many VMs in one tick | One `batch-reserve` call contains all extensions, and the renewal starts before `reservationRenewBefore` | required |
| <a id="t-026"></a>T-026 | [R-22](#r-22), [R-17](#r-17), [ADR-0019](../adrs/0019-sweep-the-queue-behind-a-long-head.md) | 6500 unmatched jobs at the head of the queue, and one matched job behind them | The matched job gets a VM within 10 s, and the list calls stay inside the request budget of the Stacks API | required |
| <a id="t-027"></a>T-027 | [R-06](#r-06), [R-03](#r-03), target design section 7.1 rule 4 | A get-states answer that names none of the jobs, for longer than `stateFreshness`; an answer that leaves one job out once; and a job that Buildkite no longer knows | [I-9](#i-9): no VM of a running job is deleted; the empty answers make the job states stale and readiness fail; the job that two answers in a row leave out loses its VM by row 5 | required |
| <a id="t-028"></a>T-028 | [R-06](#r-06), target design section 7.1 rules 3 and 4 | An agent acquires a job after the state that a finish of row 12 read, and Buildkite refuses the finish | [I-9](#i-9): the refused finish does not delete the VM; the next state shows the agent, the VM stays until the job ends, and the controller sends no second finish | required |
| <a id="t-029"></a>T-029 | [R-06](#r-06), [R-08](#r-08), target design section 7.1 rule 1 | The clock of Orchard runs 10 min behind the clock of the controller, and 10 min ahead | A VM whose agent is slow keeps its job inside the acquire timeout, and the job fails when the timeout ends, on the clock of the controller | required |
| <a id="t-030"></a>T-030 | [R-08](#r-08), [R-09](#r-09), target design sections 7.1 rule 1 and 7.3 | A slow tick puts two minutes between the token call of a job and the first listing of its VM, and no worker has room for the VM | Row 10 deletes the VM at the end of `unplaced` from the token call, and not from the first listing. The token then has at least `boot + acquire` left, and the job returns to the queue without a finish | required |

### 6.4 Contract (L3)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-040"></a>T-040 | [R-08](#r-08), [R-15](#r-15), [R-17](#r-17), [CO-01](../architecture/system-design-baseline.md#co-01) | The Stacks client against recorded responses: a partial `batch-reserve` result, `not_issued` tokens, HTTP 422 on finish, HTTP 401, HTTP 429 with `Retry-After`, and an empty scheduled-jobs page | Each response gives the documented decision. HTTP 401 makes the readiness check fail. HTTP 422 on a finish is a refusal that the Reaper does not send again | required |
| <a id="t-041"></a>T-041 | [R-20](#r-20), [ADR-0005](../adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md), [ADR-0010](../adrs/0010-call-the-orchard-api-with-a-client-of-this-project.md), [ADR-0015](../adrs/0015-generate-only-the-orchard-fields-the-controller-uses.md) | The Orchard client of this project against `orchard dev --synthetic`: create, list with a name prefix, delete, `409 Conflict` for a name that exists, and `GET /v1/vms/{name}/events`. `go generate ./...` with a difference check compares the generated wire types with the pinned upstream version | The real API accepts the specification; a `409` counts as a successful launch; the event stream gives the script output. A field that the API rejects, or a field that upstream renames, fails the gate | required |

### 6.5 Integration (L4)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-060"></a>T-060 | [R-03](#r-03), [R-04](#r-04), [PR-009](../product/requirements.md#pr-009), walking skeleton | One job from `scheduled` to a deleted VM, with `orchard dev --synthetic` and the fake Stacks server | The job reaches `finished`, the VM is deleted, [I-6](#i-6) holds at the end, and the stack notifications contain each step | required |
| <a id="t-061"></a>T-061 | [R-05](#r-05), [R-09](#r-09), [PR-005](../product/requirements.md#pr-005), [QS-1](../architecture/system-design-baseline.md#qs-1) | Three matched jobs and two free slots | Two VMs start, the third job stays `scheduled` and visible, and it starts when a slot is free | required |
| <a id="t-062"></a>T-062 | [R-08](#r-08), [PR-004](../product/requirements.md#pr-004), [QS-3](../architecture/system-design-baseline.md#qs-3) | A job with tags that match no profile, over 2 ticks | The controller does not reserve the job and does not finish it. The job stays `scheduled`, it has one notification, no VM starts, and the counter of skipped jobs rises by 1 | required |
| <a id="t-063"></a>T-063 | [R-15](#r-15), [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md) | A revoked cluster agent token | The Stacks API returns HTTP 401, and the readiness check fails | required |
| <a id="t-064"></a>T-064 | [R-20](#r-20), [CO-02](../architecture/system-design-baseline.md#co-02) | The pinned Orchard version starts, and the controller sends the current VM specification | The API accepts each field that the design uses. A rejected field fails the gate and starts a design review | required |

### 6.6 Load (L5)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-080"></a>T-080 | [R-05](#r-05), [R-17](#r-17), [SC-05](../architecture/system-design-baseline.md#sc-05), [CO-01](../architecture/system-design-baseline.md#co-01) | 50 synthetic workers with 2 slots and 500 generated jobs, with the arrival pattern of the demand model | The tick duration stays inside its budget; list and reserve stay under 10 requests each second; get-states stays under 20 requests each second; [I-1](#i-1), [I-2](#i-2), and [I-6](#i-6) hold; `bk_agent_stack_orchard.job.waiting.oldest.age` follows the queue | diagnostic |

### 6.7 End-to-end (L6, one Mac)

| ID | Basis | Scenario | Oracle | Gate |
|---|---|---|---|---|
| <a id="t-100"></a>T-100 | [R-03](#r-03), [ADR-0009](../adrs/0009-run-startup-scripts-over-the-tart-guest-agent.md), [PR-001](../product/requirements.md#pr-001), [PR-002](../product/requirements.md#pr-002), [PR-033](../product/requirements.md#pr-033) | A real job on a real Tart VM with the guest-agent transport, on the Xcode 27 reference image of `images/` | The agent starts, `xcodebuild -version` reports Xcode 27, the job finishes, the VM is deleted, the VM specification contains no credentials, and the job log is visible on the Buildkite job page | required before a release |
| <a id="t-101"></a>T-101 | [R-01](#r-01), [R-02](#r-02), [QS-5](../architecture/system-design-baseline.md#qs-5) | A job that tries to reach the host, another VM, and a private address that the profile does not permit. A preview without a job runs on the development Mac: `mise run softnet-test` probes from a VM of the controller specification (2026-09-27: it found that Softnet lets a VM reach the host through the gateway, which the pf rules of the hosts now close). The pf rules of that Mac permit the ranges from which Softnet picks the network of each VM, so the probe also checks a `softnetAllow` that holds the gateway | Each connection fails; a permitted CIDR succeeds; the VM contains no cluster agent token | required before a release |
| <a id="t-102"></a>T-102 | [R-07](#r-07), [PR-032](../product/requirements.md#pr-032), [QS-8](../architecture/system-design-baseline.md#qs-8) | A job that uploads an artifact | The artifact upload succeeds, and the artifact is visible on the job page. Checkouts and build data do not leave the host of the VM, except through the log and artifact services of Buildkite | required before a release |
| <a id="t-103"></a>T-103 | [R-08](#r-08), [PR-008](../product/requirements.md#pr-008) | A profile with an image digest that does not exist, and an image whose agent does not start: an agent that exits, and an agent that runs and acquires nothing. A preview runs on the development Mac (`mise run failures-test`, 2026-09-27) | The job fails, within the boot timeout, with the reason of the pull (on real Tart a missing digest fails the VM at once, `MANIFEST_UNKNOWN`); with the status and the last log lines of the agent when the agent exits; and after the acquire timeout with the last log lines when it acquires nothing. No VM remains | required before a release |
| <a id="t-104"></a>T-104 | [R-11](#r-11), [R-18](#r-18), [QS-4](../architecture/system-design-baseline.md#qs-4), [QS-6](../architecture/system-design-baseline.md#qs-6) | A controller stop during a job, and an Orchard controller restart. A preview of the controller crash runs on the development Mac (`mise run restart-test`); `orchard dev` runs its worker in its own process, so the Orchard restart needs the real hosts | Jobs in progress continue; after recovery no duplicate VM exists and no VM of an ended job remains; the recovery takes less than 15 minutes | required before a release |
| <a id="t-105"></a>T-105 | [R-15](#r-15), [PR-023](../product/requirements.md#pr-023), [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md) | The token replacement procedure with jobs in progress | The jobs complete, new jobs start with the new token, and no job fails because of the replacement | required before a release |
| <a id="t-107"></a>T-107 | [R-08](#r-08), [PR-008](../product/requirements.md#pr-008), [ADR-0017](../adrs/0017-download-the-agent-when-the-vm-starts.md) | A job on the upstream base image with the downloaded agent, a job on an image of this project with the downloaded agent, a profile with a wrong `agent.sha256`, and a profile with `agent.source: image` on the upstream base image | On the upstream base image, agent v4 runs the job, not the v3 of the image, and the checkout with the repository credentials of Buildkite passes. On the image of this project, the job passes. With the wrong SHA-256 or the v3 agent of the image, no agent acquires the job, the job ends with `-1` and the line of the script that names the failed check or the version, and the VM is deleted | required |
| <a id="t-106"></a>T-106 | [R-16](#r-16), [QA-01](../architecture/system-design-baseline.md#qa-01), [QS-2](../architecture/system-design-baseline.md#qs-2) | A measurement run of 20 jobs with the image on the host | `job.phase.duration` gives a measured start time, which replaces the estimate of [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md) and [ADR-0003](../adrs/0003-start-job-vms-on-demand.md) | diagnostic |
| <a id="t-108"></a>T-108 | [PR-030](../product/requirements.md#pr-030), [ADR-0021](../adrs/0021-mount-operator-files-read-only-in-job-vms.md) | A profile with two host directories on a real Tart VM, with Orchard started with `--insecure-allow-host-dirs` and a read-only policy for each path, and the same profile on a host that lacks one directory | The job reads a file of each mount at `/Volumes/My Shared Files/<name>`, a write to each mount fails, `ssh -G -F` reads an SSH configuration of a mount, and a signed arm64 binary of a mount runs, or the record of the spike says why not. A create with `ro: false` for a policy path fails in Orchard. On the host without the directory, no agent acquires the job, the job ends with `-1`, and the VM is deleted | required before a release |
| <a id="t-109"></a>T-109 | [PR-030](../product/requirements.md#pr-030), [ADR-0022](../adrs/0022-authenticate-the-checkout-with-files-of-a-read-only-mount.md) | On a real Tart VM: a profile with `checkout.sshFrom` and a pipeline of a private repository with an SSH URL, and a profile with `checkout.gitCredentialsFrom` and a pipeline of a private repository with an HTTPS URL, both on a Git host that the checkout tokens of Buildkite do not cover. The deploy key and the credential stay outside the repository | Both checkouts pass. `~/.ssh` has mode `0700` and each copied file `0600`, `BUILDKITE_NO_SSH_KEYSCAN` is `true`, a host that is not in `known_hosts` is refused, the helper list of git starts with an empty value, and the job log holds the names of the copied files and no part of a credential | required before a release |
| <a id="t-110"></a>T-110 | [ADR-0021](../adrs/0021-mount-operator-files-read-only-in-job-vms.md), target design section 9.2 | The Job of the policies, in the kind smoke deployment, against Orchard with cluster settings that a person changed | Each `hostDirPolicies` entry has `ro` and is a path of `HOST_DIR_POLICIES`; the Job replaces the list, keeps `schedulerProfile` and every other field, changes nothing on a second run, clears the list for an empty value, and refuses `/`, a path that is not clean, `..`, `:`, a control character, and a URL | required |
| <a id="t-111"></a>T-111 | [PR-030](../product/requirements.md#pr-030), [ADR-0021](../adrs/0021-mount-operator-files-read-only-in-job-vms.md) | The VM specification of a profile with `hostDirs` against `orchard dev --insecure-allow-host-dirs` with one `ro` policy, and against Orchard without the flag, in CI with no Mac | A create with `ro: true` and a policy path passes; a create with `ro: false`, of a path that no policy allows, or without the flag fails with 400, which the controller counts as `invalid_request`; an Orchard controller that starts without the flag fails a VM with `hostDirs` that runs | required |

### 6.8 Manual reviews

These obligations are reviews, not automated tests. Both come from ADR confirmations.

| ID | Basis | Review | When |
|---|---|---|---|
| <a id="t-140"></a>T-140 | [ADR-0007](../adrs/0007-use-a-dedicated-queue-for-the-macos-fleet.md), [ADR-0016](../adrs/0016-skip-a-job-that-matches-no-profile.md), [PR-031](../product/requirements.md#pr-031) | A review of the Buildkite queue lists each agent and stack that serves it | Before the controller goes live |
| <a id="t-141"></a>T-141 | [ADR-0008](../adrs/0008-use-a-dedicated-cluster-agent-token.md), [SE-03](../architecture/system-design-baseline.md#se-03) | A review of the token confirms that `expires_at` is 90 days or less, and that `allowed_ip_addresses` contains the egress addresses of the controller and of the job VMs | Before the controller goes live, and at each replacement |

## 7. Environments, data, and secrets

| Environment | Contents | Used by |
|---|---|---|
| CI (Linux) | Go toolchain, a fake Stacks API server | L0 to L3 |
| CI (macOS arm64) | Go toolchain, `orchard dev --synthetic`, a fake Stacks API server, an OTLP collector that records | L3 (the Orchard client) to L5 |
| Development Mac | Tart, one Orchard worker, a Buildkite test cluster and queue, one test pipeline | L6 |
| Operator fleet | The fleet of an operator who shares its measurements, with telemetry | Section 8 |

Rules for data and secrets:

* The fake Stacks server MUST be the `FakeAgentServer` of `github.com/buildkite/agent-stack-k8s`
  (MIT licence), which Buildkite maintains against its own API and which uses the real `stacksapi`
  types. The project MUST NOT write a second fake of this API. The implementation plan, section
  3.2 gives the import and the copy option.
* Tests MUST NOT use owner source code. The end-to-end pipeline MUST use a small synthetic
  repository.
* Test credentials MUST come from CI secrets, and they MUST be separate from the credentials of
  any fleet. A test that writes a token to a log or to telemetry MUST fail ([I-3](#i-3), [I-7](#i-7)).
* No test uses customer data, personal data, or production artifacts.
* L6 needs a Mac that can host the guest macOS version ([CO-07](../architecture/system-design-baseline.md#co-07)). The release gate confirms this
  assumption ([O-4](#o-4)).

## 8. Gates and fleet evidence

| Gate | Contents | Rule |
|---|---|---|
| Pull request | L0, L1, L2, L3, L4 | All required obligations pass. CI fails the merge if one fails |
| Nightly | L5, and long property-based runs of [T-020](#t-020) | A failure opens an issue. It does not stop a merge |
| Release | L6 on the development Mac | All obligations that are "required before a release" pass |
| Go live | [T-140](#t-140) and [T-141](#t-141) | Both reviews are complete |

Fleet evidence replaces estimates. It is measurement, not a test. The project runs no pilot
([ADR-0020](../adrs/0020-leave-the-job-vm-images-to-the-operator.md)): the release gate gives the first values, and the fleet of an operator gives the rest
when the operator shares its measurements. The quality targets are product goals. They are not
the thresholds of the alerts of an operator (target design, section 10.6).

* <a id="e-1"></a>**E-1:** two weeks with `bk_agent_stack_orchard.vm.leftover` at 0, except for the window in which
  the reaper did not delete a VM whose job it saw as terminal, and 0 jobs that the controller failed
  incorrectly ([QA-04](../architecture/system-design-baseline.md#qa-04), requirements section 6). The gauge counts the VMs whose job is terminal, so
  it shows a VM that remains. The deletion counter `bk_agent_stack_orchard.vm.deletions` does not,
  because it counts only the VMs that were deleted. The window is 10 consecutive ticks, which is 10
  s with the default poll interval (target design, section 10.1). Two exports in a row that are more
  than 0 are a leak. A Buildkite outage keeps the gauge above 0 for longer than the window, because
  the reaper waits for fresh job states (target design, section 7.2, row 3). [E-1](#e-1) excludes such
  periods, and the staleness signal of [E-5](#e-5) shows them. The jobs that the controller failed
  incorrectly are the increases of `bk_agent_stack_orchard.job.failed`, each one checked against its
  job.
* <a id="e-2"></a>**E-2:** the measured start time and wait time against [QA-01](../architecture/system-design-baseline.md#qa-01) and [QA-02](../architecture/system-design-baseline.md#qa-02), with the percentile that
  the baseline assumes. The load condition is the one of [T-080](#t-080): 50 synthetic workers of 2 slots,
  500 generated jobs, and the arrival pattern of the demand model (baseline section 4.2, [SC-05](../architecture/system-design-baseline.md#sc-05)),
  with the tick duration at or below `LOAD_TICK_BUDGET`, which is 34 ms ([O-5](#o-5)). The 34 ms is the
  budget of [T-080](#t-080) against local fakes only. It does not apply to a real fleet, because a real tick
  makes calls to the Buildkite API and to Orchard. The tick budget of a real fleet: `UNKNOWN`;
  owner: the maintainers; trigger: the first week of `bk_agent_stack_orchard.tick.duration` data
  that an operator shares. The start time includes the starts that pulled an image, because the
  controller cannot tell them apart (cold or warm start: `UNKNOWN`; owner: the maintainers; trigger:
  [O-7](#o-7); target design, section 10.1).
* <a id="e-3"></a>**E-3:** the measured time from the job end to the VM deletion, which gives the value that [QA-06](../architecture/system-design-baseline.md#qa-06)
  does not have. It is the histogram `bk_agent_stack_orchard.job.phase.duration` with the phase
  value `finished_to_deleted`. The histogram starts at the first tick in which the reaper saw the
  job as terminal, so the true time from the job end to the deletion is not the histogram value.
  It is between the histogram value and the histogram value plus `jobStatesInterval`.
* <a id="e-4"></a>**E-4:** the measured image count for each host, which gives the value that [SC-09](../architecture/system-design-baseline.md#sc-09) does not have.
* <a id="e-5"></a>**E-5:** the measured recovery time against [QA-03](../architecture/system-design-baseline.md#qa-03), which is less than 15 min. The measure uses
  the existing signals. A recovery starts at the last answer of a source that was good. That time is
  the export time minus the value of `bk_agent_stack_orchard.source.staleness` in the first export
  that is more than `stateFreshness` (60 s). For a restart of the controller, the recovery starts at
  the last export before the gap. It ends at the first export in which the
  `bk_agent_stack_orchard.source.staleness` of all three sources is at or below `stateFreshness` and
  the readiness probe (gRPC health, `Check`) gives `SERVING`. The measure needs a record of each
  outage of Orchard, each outage of Buildkite, and each restart of the controller, and the time of
  each one, from the fleet that gives it.

## 9. Exclusions and accepted risks

Each item is a decision, not missing work.

* **The Orchard scheduler and placement.** Orchard owns this behaviour. The controller only
  accounts for capacity, and [T-004](#t-004) and [T-041](#t-041) prove the accounting against the real API.
* **Tart, Softnet, and Apple Virtualization isolation.** The project proves that its specification
  requests isolation ([T-003](#t-003)), and it proves the effect one time on real hardware ([T-101](#t-101)). It does
  not test the isolation mechanism itself.
* **The Buildkite retry engine, job log storage, and the agent internals.** These are supplier
  behaviour. The project proves only that its finish call has `exit_status -1` and a detail, in
  [T-007](#t-007) and [T-009](#t-009). The platform generates the `signal_reason` ([PR-012](../product/requirements.md#pr-012)).
* **A browser, device, or operating-system matrix.** The controller has no user interface. The
  only operating-system cells are Linux for the controller and macOS for the VMs.
* **The Kubernetes chart beyond a render and one smoke deployment.** The controller MUST NOT
  depend on Kubernetes ([PR-022](../product/requirements.md#pr-022)), so the platform is not a source of product risk.
* **The host provisioning with Ansible.** One smoke run on the development Mac is sufficient
  evidence for a fixed host pool.
* **Warm pools, high availability, more than one queue, and more than one region.** These are
  product non-goals. Each needs its own ADR and its own evidence.
* **Coverage percentage as evidence of an obligation.** The gates use named obligations. CI also
  fails when the total statement coverage is below 85 % (`scripts/coverage.sh`), as a floor.

## 10. Review and retirement triggers

The maintainers review this strategy when one of these events occurs:

* Orchard publishes a release later than the pinned version, or it changes the VM specification.
  [T-041](#t-041), [T-064](#t-064), and [T-122](#t-122) change with it.
* The Buildkite Stacks API changes a limit, a state, or a status code. [T-040](#t-040) changes with it.
* Buildkite publishes an agent release line after v4. [T-100](#t-100) and the image contents change.
* A fleet gives [E-1](#e-1) to [E-5](#e-5). The estimates in [ADR-0001](../adrs/0001-provision-macos-job-vms-with-a-custom-stack-controller.md) and [ADR-0003](../adrs/0003-start-job-vms-on-demand.md) and the values of [QA-06](../architecture/system-design-baseline.md#qa-06) and
  [SC-09](../architecture/system-design-baseline.md#sc-09) then come from measurement, and [T-106](#t-106) can retire.
* The fleet uses a profile with the SSH transport. [T-003](#t-003) and [T-100](#t-100) then need an SSH case.
* A new Xcode or macOS changes the reference build of `images/` ([CO-05](../architecture/system-design-baseline.md#co-05)).

## 11. Open items

Each item is an unknown that can change a level or a scenario. Each has the smallest next action.

| ID | Unknown | Effect | Next action |
|---|---|---|---|
| <a id="o-1"></a>O-1 | The Buildkite `FakeAgentServer` is a test double, not a conformance suite. Its fidelity is unproven for this controller. The finish call of the Stacks API takes an exit status and a detail and no `signal_reason`, so the `signal_reason: stack_error` of [T-009](#t-009) is what Buildkite gives a job that a stack finishes, which no fake can show. | [T-040](#t-040) and [T-060](#t-060) can prove behaviour that the real API does not have. [T-009](#t-009) proves `exit_status -1` locally, and not `signal_reason`. | During [W-24](../delivery/implementation-plan.md#w-24), when a Buildkite test cluster exists, record its responses, compare them with the fake, and use them as the fixtures of [T-040](#t-040) (owner decision, 2026-09-20). Read the `signal_reason` of a job that the controller finished there, for [T-009](#t-009). |
| <a id="o-2"></a>O-2 | `orchard dev --synthetic` does not run a real startup script. | L4 proves only that the API accepts the guest-agent transport. The transport itself is unproven until L6. | Send one VM specification with `transport: tart-guest-agent` to a synthetic controller, and record whether validation accepts it. **Evidence (2026-09-27):** on the development Mac, `TestARealMachineRunsTheStartupScript` (`mise run tart-test`) ran the controller, `orchard dev` with a real Tart worker, and the agent image. The guest agent ran the startup script: `buildkite-agent` v4.0.6 started, and Buildkite refused its job token with `401 Unauthorized: Invalid job acquisition token`. The fake Stacks server issues a token that Buildkite does not know, so no job runs there. [T-100](#t-100) stays the only proof of a job that runs. |
| <a id="o-3"></a>O-3 | Agent v4 support for an injected `traceparent` is not verified (target design, section 10.4). | The agent spans can stay outside the job trace. | Run one job on the development Mac with `profile.otel.enabled: true`, and examine the trace. |
| <a id="o-4"></a>O-4 | The host macOS version that a macOS 27 guest needs is an assumption ([CO-07](../architecture/system-design-baseline.md#co-07)). | L6 and the pilot can need a host update before the pilot hosts are ready. The date of the hosts is `UNKNOWN` (owner: the maintainers; trigger: the Mac hosts are ready). On 2026-09-14 the plan estimated that the hosts ship on 2026-09-22. | Start a Golden Gate VM on the development Mac before the pilot hosts arrive. **Evidence (2026-09-27):** macOS 27 Golden Gate VMs run on the macOS 27 development Mac ([W-23](../delivery/implementation-plan.md#w-23), and the test of [O-2](#o-2)). An older host is not tested. **Evidence (2026-11-03):** the release gate ran [T-100](#t-100) to [T-105](#t-105) with job VMs of macOS 27.2 on an Orchard worker on macOS 27.2. |
| <a id="o-5"></a>O-5 | The tick duration budget for [T-080](#t-080) had no value. **Closed on 2026-11-02.** | The load test judges the tick, together with the invariants and the request budget, when `LOAD_TICK_BUDGET` parses as a duration. | **Evidence (2026-11-01):** four runs of the `load` workflow on `macos-latest` (run IDs 41276671046, 41276715340, 41292677418, and 41297506926) gave a p95 of 17, 17, 15, and 16 ms. The budget is two times the highest p95, 17 ms, which is 34 ms. The repository variable `LOAD_TICK_BUDGET` is `34ms` (set 2026-11-02). |
| <a id="o-6"></a>O-6 | The Stacks API answers for the edge cases are not recorded: a finish of a job that is acquired, terminal, scheduled, or unknown, a reserve of a job that is running or terminal, and a get-states request for a job that Buildkite does not know, and the largest list that get-states accepts. `test/fakestacks` assumes 422 and `not_reserved`. | The controller can treat a real answer incorrectly, and the fake hides it. | Record the real answers on the Buildkite test cluster, and put them in the fake. |
| <a id="o-7"></a>O-7 | Orchard 0.58.0 gives no event for an image pull (checked 2026-11-02, tag 0.58.0), so the start phase does not carry a cold or warm attribute. | The start time of [QA-01](../architecture/system-design-baseline.md#qa-01) includes the starts that pulled an image. | Record the events and the status messages of real VMs on a real host, and add the attribute if a pull shows. |
| <a id="o-8"></a>O-8 | Orchard 0.58.0 exports `org.cirruslabs.orchard.controller.worker_resource` with a `worker` attribute in its source (read 2026-11-02), but nobody has seen the series in a collector. The alert "Unknown worker" (target design, section 10.6) depends on it. | Without the series, nothing shows a worker name that no account of the list owns. | When the first host is registered ([W-22](../delivery/implementation-plan.md#w-22); owner: the maintainers), read the series in the collector. If it is missing, write the alert on the worker list of the Orchard API, and record the result here. |
| <a id="o-9"></a>O-9 | Who rotates the tokens of the Orchard accounts (the bootstrap admin, the controller, the operator, and each Mac host) and the cluster agent token. **Closed on 2026-11-03.** | None for the tests. Secret rotation is the responsibility of the fleet operator, and the project does not automate it (owner decision, 2026-11-03). The runbooks describe the procedures of the operator, and the project has no goal to automate them. | None. Machine identity, such as SPIFFE and SPIRE in front of Orchard, is out of the scope of this project (owner decision, 2026-11-03). An operator MAY put a machine-identity system in front of Orchard. The ADR index keeps the number 0018 reserved. |
