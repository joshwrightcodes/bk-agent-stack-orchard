# Glossary

This glossary gives the meaning of each term and each identifier family as this project uses them.
The linked document is the authority. When a document and this glossary differ, the document is
correct, and this glossary needs a correction.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## Terms

| Term | Meaning in this project | Defined in |
|---|---|---|
| Stacks API | The Buildkite API that a stack controller uses. It lists the scheduled jobs of a queue, reserves jobs, issues job acquisition tokens, gets job states, finishes jobs, and sends notifications. It accepts only a cluster agent token. | [Target design](architecture/target-design.md), section 3 |
| Cluster agent token | The Buildkite token (`bkct_`) of the controller. It lets the controller call the Stacks API. It MUST NOT enter a VM. | [Target design](architecture/target-design.md), section 9.2; [ADR-0008](adrs/0008-use-a-dedicated-cluster-agent-token.md); [runbook](runbooks/token-replacement.md) |
| Job acquisition token | The Buildkite token (`bkjat_`) that connects one agent to one reserved job. The controller gets it for each job. Only this token goes into a VM. | [Target design](architecture/target-design.md), sections 3 and 9.1 |
| Reservation | The claim that the controller makes on a scheduled job, so that no other stack takes it. It lasts a limited time, and the controller extends it while the job waits. An expired reservation returns the job to `scheduled`. | [Target design](architecture/target-design.md), sections 3 and 6.4 |
| Profile | A named set of VM settings: the tags that it matches (the match map), the image, the CPU, the memory, the disk, and options such as the startup transport. The controller maps each job to one profile. | [Target design](architecture/target-design.md), section 5; [ADR-0006](adrs/0006-route-jobs-to-vm-profiles-by-agent-tags.md) |
| Admission | The pure function that decides which scheduled jobs start now. It uses the scheduled jobs, the Inventory, the profiles, and `maxInFlight`, and it never exceeds the free capacity. It also lists the jobs that no profile matches. | [Target design](architecture/target-design.md), sections 4 and 6.3; [ADR-0002](adrs/0002-check-capacity-before-reserving-jobs.md) |
| Inventory | The component that lists the Orchard workers and VMs at each interval, selects the VMs that this controller owns by their name prefix, and parses the job UUIDs from the names. | [Target design](architecture/target-design.md), section 4 |
| Reaper | The pure decision function for each owned VM, and its executor. Its actions are to delete the VM, finish the job, extend the reservation, or send a notification. It follows the decision table. | [Target design](architecture/target-design.md), section 7.2 |
| Launcher | The component that, for each admitted job, reserves the job, gets a job acquisition token, creates the VM in Orchard, and sends notifications. | [Target design](architecture/target-design.md), sections 4 and 6.4 |
| Softnet | The Tart network filter that keeps a VM away from private ranges and from other VMs. It does not block the host: a VM can reach the gateway of its network, which is the host. The pf anchor on each host closes that path. Each job VM uses it (`netSoftnet: true`). | [ADR-0004](adrs/0004-treat-all-builds-as-untrusted.md); [Target design](architecture/target-design.md), section 9.1; [Testing on real VMs](testing-on-real-vms.md), section "The Tart tests" |
| Guest agent | The Tart guest agent, a program in the VM image. The startup script of a job runs over it, so the image needs no SSH credentials. A profile can select SSH instead (`startupTransport`). | [ADR-0009](adrs/0009-run-startup-scripts-over-the-tart-guest-agent.md) |
| Worker | An Orchard process on a Mac host. It runs the VMs that Orchard places on it, with a limit of two macOS VMs at the same time. Do not confuse it with a Buildkite agent. | [Target design](architecture/target-design.md), section 3; [ADR-0005](adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md) |
| Synthetic mode | The mode of `orchard dev --synthetic`, in which workers hold in-memory VMs and no VM runs. The integration tests and the local quick start use it. | [ADR-0005](adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md); [Development guide](development.md), section "Run the controller locally" |

## Identifier families

Each family has a prefix and a number. Each identifier is defined in one document only, and
`mise run check-docs` fails when a document cites an identifier that no document defines.

| Family | Meaning | Defined in |
|---|---|---|
| `PR-nnn` | A product requirement | [Product requirements](product/requirements.md) |
| `BD-nn` | A business driver | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `SC-nn` | A scale driver: the fleet, the hosts, and the demand | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `QA-nn` | A quality attribute target, such as the start time | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `DR-nn` | A data and consistency driver, such as one VM for each job | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `SE-nn` | A security driver | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `OP-nn` | An operations driver | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `CO-nn` | A constraint, such as a limit of the Stacks API | [System design baseline](architecture/system-design-baseline.md), section 10 |
| `QS-n` | A quality scenario | [System design baseline](architecture/system-design-baseline.md), section 3 |
| `R-nn` | A risk | [Test strategy](quality/test-strategy.md), section 3 |
| `I-n` | An invariant that the tests assert | [Test strategy](quality/test-strategy.md), section 5 |
| `T-nnn` | A test obligation | [Test strategy](quality/test-strategy.md), section 6 |
| `E-n` | A piece of pilot evidence | [Test strategy](quality/test-strategy.md), section 8 |
| `O-n` | An open item of the test strategy | [Test strategy](quality/test-strategy.md), section 11 |
| `W-nn` | A work unit | [Implementation plan](delivery/implementation-plan.md), section 4 |
| `D-n` | A delivery risk | [Implementation plan](delivery/implementation-plan.md), section 8 |
| `ADR-nnnn` | An architecture decision record | [Decision records](adrs/README.md) |

The product requirements and the system design baseline also hold owner answers with the
identifiers `Q-P1` and `Q-A1`. The prefix `Q-P` marks a question of the product requirements,
and `Q-A` a question of the system design baseline, section 11. No check reads these two
prefixes.
