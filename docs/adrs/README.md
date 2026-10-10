# Architecture decision records

This directory contains the architecture decision records (ADRs) of `bk-agent-stack-orchard`.

## Format

Each ADR records one decision, and its title states the decision. The structure follows the GIG
Cymru NHS Wales decision record template (summary, drivers, options, analysis, recommendation,
consequences, confirmation). The comparison table follows the Gareth Morgan template. The front
matter follows MADR 4.

Each ADR has these sections:

1. **Summary.** The problem, the decision, and the main cost, in a few sentences.
2. **Context.** The situation and the forces, as connected prose.
3. **Goals and non-goals.** Named goals that the options must meet, and topics outside the decision.
4. **Options considered.** One paragraph for each option, and one table that compares the options
   against the goals.
5. **Decision.** The chosen option and the reason for it.
6. **Consequences.** The good and the bad results of the decision.
7. **Confirmation.** The checks that show that the implementation follows the decision.

## Writing rules

* ADRs use ASD-STE100 Simplified Technical English and the third person.
* An ADR references only earlier ADRs. The status fields `superseded by` are the only exception.
* Facts that can change, such as versions, prices, and vendor terms, have a date.
* An ADR records the reasons for a decision. The target design
  (`docs/architecture/target-design.md`) contains the detailed design.
* Identifiers such as `PR-005` refer to the [product requirements](../product/requirements.md).
  Identifiers such as `QA-01` and `SC-02` refer to the
  [system design baseline](../architecture/system-design-baseline.md).

## File names

A file name is a four-digit sequence number and a present-tense imperative phrase, in lowercase
with dashes. Example: `0001-provision-macos-job-vms-with-a-custom-stack-controller.md`. The title
of the ADR uses the same number, for example `ADR-0001`.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in these documents are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

ADRs use these key words only for hard constraints that the decision sets.

## Lifecycle

The `status` field has one of these values:

* `proposed`: the ADR is in review.
* `accepted`: the decision-maker approved the ADR.
* `rejected`: the decision-maker did not approve the ADR.
* `deprecated`: the decision no longer applies, and no new decision replaces it.
* `superseded by ADR-NNNN`: a newer ADR replaces this decision.

The content of an accepted ADR does not change. To replace a decision:

1. Create a new ADR with the new decision. Add `supersedes: ADR-NNNN` to its front matter.
2. Change the status of the old ADR to `superseded by ADR-NNNN`.

## Index

The decision-maker accepted every decision below on 2026-09-20. The content of an accepted ADR
does not change: a new ADR supersedes it.

| ADR | Decision | Status |
|---|---|---|
| [ADR-0001](0001-provision-macos-job-vms-with-a-custom-stack-controller.md) | Provision macOS job VMs with a custom Buildkite stack controller | accepted |
| [ADR-0002](0002-check-capacity-before-reserving-jobs.md) | Check free capacity before the controller reserves a job | accepted |
| [ADR-0003](0003-start-job-vms-on-demand.md) | Start job VMs on demand, without a pool of idle VMs | accepted |
| [ADR-0004](0004-treat-all-builds-as-untrusted.md) | Treat all builds as untrusted and isolate each job VM | accepted |
| [ADR-0005](0005-run-and-schedule-vms-with-tart-and-orchard.md) | Run and schedule the job VMs with Tart and Orchard | accepted |
| [ADR-0006](0006-route-jobs-to-vm-profiles-by-agent-tags.md) | Route jobs to VM profiles by their agent tags | accepted |
| [ADR-0007](0007-use-a-dedicated-queue-for-the-macos-fleet.md) | Use a dedicated queue for the macOS fleet | accepted |
| [ADR-0008](0008-use-a-dedicated-cluster-agent-token.md) | Use a dedicated cluster agent token with an expiry and an IP allowlist | accepted |
| [ADR-0009](0009-run-startup-scripts-over-the-tart-guest-agent.md) | Run VM startup scripts over the Tart guest agent | accepted |
| [ADR-0010](0010-call-the-orchard-api-with-a-client-of-this-project.md) | Call the Orchard API with a client of this project | accepted |
| [ADR-0011](0011-generate-the-orchard-wire-types.md) | Generate the Orchard wire types from the pinned version | accepted |
| [ADR-0012](0012-build-the-job-vm-images-from-a-vanilla-macos-image.md) | Build the job VM images from a vanilla macOS image | accepted |
