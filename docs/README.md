# Documents

This directory holds the document set of `bk-agent-stack-orchard`. The documents decide the
behaviour of the project, and the code follows them. A change starts in a document, as
[CONTRIBUTING](../CONTRIBUTING.md) says.

## Reading order for the first day

1. The [main README](../README.md). It says what the project does and what it does not do.
2. The [glossary](glossary.md). It gives the terms and the identifier families that the other
   documents use.
3. The [product requirements](product/requirements.md), sections 1 and 2 and the journeys. They
   say what the product does, and for whom.
4. The [target design](architecture/target-design.md), sections 4 to 7. They give the components,
   the job lifecycle, and the Reaper decision table.
5. The [decision records](adrs/README.md). Read ADR-0001, ADR-0002, ADR-0004, and ADR-0006 first.
6. The [development guide](development.md). Run the tools, and follow the quick start "Run the
   controller locally".
7. The [test strategy](quality/test-strategy.md), sections 3 to 5. They give the risks and the
   invariants.
8. The [implementation plan](delivery/implementation-plan.md), to learn which work is open.

Read the [system design baseline](architecture/system-design-baseline.md) when a question is about
a number, such as the demand model or a quality target.

## Document map

| Document | Question that it answers |
|---|---|
| [Product requirements](product/requirements.md) | What must the product do? |
| [System design baseline](architecture/system-design-baseline.md) | Which drivers, targets, and assumptions limit the design? |
| [Target design](architecture/target-design.md) | How does the controller work? |
| [C4 model](architecture/c4.md) | What are the levels of the architecture? The file is generated from [`workspace.dsl`](architecture/workspace.dsl). |
| [Decision records](adrs/README.md) | Why did the project choose this approach? |
| [Test strategy](quality/test-strategy.md) | Which risks exist, and which tests cover them? |
| [Implementation plan](delivery/implementation-plan.md) | In which order does the work go, and what ends each unit? |
| [Development guide](development.md) | How does a developer set up a Mac, and run the gates, the tests, and the controller? |
| [Testing on real VMs](testing-on-real-vms.md) | How does a developer run the tests that start real Tart VMs? |
| [Releasing](releasing.md) | How are versions calculated, and how do the maintainers make a release? |
| [Writing guide](writing-guide.md) | How does a contributor write a document? |
| [Glossary](glossary.md) | What does a term or an identifier mean? |
| [Quick start](user/quickstart.md) | How does an operator see one real job run in a Tart VM on one Mac? |
| [Install the controller](user/install.md) | What does an operator need, and how does an operator install, verify, and upgrade a release? |
| [Configuration reference](user/configuration.md) | Which keys does the configuration file have, with their types and defaults? |
| [Third-party terms](user/third-party-terms.md) | Which terms of Apple, Tart, and Orchard affect a fleet? |
| [Runbook: replace the agent token](runbooks/token-replacement.md) | How does an operator replace the cluster agent token? |
| [Runbook: retire and rotate the Orchard bootstrap admin](runbooks/orchard-bootstrap-admin.md) | How does an operator take the `bootstrap-admin` token out of the Orchard pod and rotate it? |
| [Runbook: Orchard worker accounts](runbooks/orchard-worker-accounts.md) | How does an operator add, rotate, and revoke the account of one Mac host? |

The direction of the set is from the requirements to the design, the tests, and the plan.
`mise run check-docs` checks that each cited identifier exists, and that each link resolves.

## Access checklist

A new contributor needs these items. The maintainers grant them. Ask in a GitHub issue or
discussion, and do not send a secret in it.

* [ ] **A Buildkite test cluster.** The cluster gives a queue and a cluster agent token for the
  controller. Only a run against real Buildkite needs it. The local quick start does not.
* [ ] **The Mac lab.** A Mac with Apple silicon, with Tart and Orchard, runs the real-VM tests. The
  maintainers hold the lab.
* [ ] **The secret source.** The maintainers give each secret through the secret manager of the
  team. Never put a secret in the repository, a log, or a document.
* [ ] **The GitHub team.** Membership gives review rights. Until a team exists, the maintainers own
  each path (`.github/CODEOWNERS`).
* [ ] **The `release` environment reviewer.** A release publishes only after a reviewer of the
  `release` environment of the repository approves it. Only the maintainers take this role.
