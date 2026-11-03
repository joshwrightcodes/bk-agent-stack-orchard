# Troubleshooting

Start with the job page in Buildkite. The controller writes a line there at each step of a VM:
"VM requested", "placed on" a host, "booting", and "VM deleted" with a reason. A job that fails
before its agent starts shows why, and the last lines of the startup script of the VM.

Then read the log of the controller. Each time it acts on a VM, it writes one record with the
reason. [Telemetry](telemetry.md) lists the reasons and the metrics.

## A job stays scheduled

| What you see | Cause | What to do |
|---|---|---|
| The job page says that no profile carries the job | The tags of the job, without `queue`, equal no `match` map. The match is exact. | Change the `agents:` of the step, or add a profile with that `match` map. |
| No line on the job page | The controller does not serve the queue, or does not run. | Check `buildkite.queue`, and that the controller is ready. |
| "VM requested", then nothing | No Mac host has a free slot, or none has the `workerLabels` of the profile. | Check `bk_agent_stack_orchard.slot.count` with the state `free`, the workers in Orchard, and their labels. After `unplaced` (2 min), the VM is deleted and the job waits again. |
| The job waits while other jobs run | Each Mac host runs at most two VMs. | Add hosts, or wait. `bk_agent_stack_orchard.job.waiting.oldest.age` shows the wait. |
| The log warns that the queue is longer than one read | Many jobs that never leave the queue, such as jobs that no profile carries, are at the head of the queue. Each tick, the controller reads the head and one more page, so a job behind them waits longer. | The warning names the pipeline that has the most jobs of the read. Cancel the build of that pipeline, or fix the `agents:` of its steps. |
| The controller log says that it is not ready | Buildkite or Orchard does not answer, or Buildkite refused the agent token. | Fix the connection or the token. The log names the source. |
| `cicd.system.errors` with `orchard` and `unauthorized` goes up | Orchard refused the token of the service account of the controller, for example after a rotation. The inventory then gets old, and admission stops. | Give the controller the current token of its Orchard account, and restart it. |
| No job starts, and `bk_agent_stack_orchard.admission.blocked` is 1 for a reason | The reason says why admission stopped: a queue that the controller has not read or that an operator paused, or a source that has not answered or whose answer is old. | For `queue_paused`, resume the queue in Buildkite. For a source, fix the connection to Buildkite or Orchard, and read `bk_agent_stack_orchard.source.staleness`. |

## A job fails before it runs

| What the job page shows | Cause | What to do |
|---|---|---|
| A boot timeout | The image pull, the clone, or the start of the VM took longer than `boot` (15 min). A first pull of a large image can. | Pull the image to each host first, or raise `timeouts.boot` of the profile. |
| An acquire timeout, with lines of the startup script | macOS booted, but the agent did not take the job within `acquire` (5 min). | Read the lines. Check that the image runs the Tart guest agent, and that the VM reaches Buildkite. |
| `could not download buildkite-agent` | The VM cannot reach `agent.url`. | Check the network of the hosts, or set a mirror. A mirror in a private range needs `softnetAllow`. |
| `does not match agent.sha256` | The archive at `agent.url` is not the one that `agent.sha256` names. | Set the pair of the release that you want, together. |
| `the image has "buildkite-agent version 3…"` | The profile uses the agent of the image (`agent.source: image`), and the image has another major version. | Put agent v4 in the image, or use `agent.source: download`. |
| The reason that Orchard gives for a failed VM | Orchard or Tart could not run the VM: a missing image, a disk smaller than the image, or a host problem. | Read the reason. Check `diskSizeGB`, the image reference, and the log of the Orchard worker. |
| Failures of many jobs on one Mac host | `bk_agent_stack_orchard.job.failed` or `bk_agent_stack_orchard.vm.deletions` goes up for one `bk_agent_stack_orchard.worker.name`. A host with a full disk or a broken Tart fails each VM fast, frees its slot at once, and so gets many new VMs. | Pause scheduling on the worker in Orchard, so that admission skips it, and repair the host. |
| Failures of many jobs of one profile | The same counters go up for one `bk_agent_stack_orchard.profile` on several hosts. | Check the image and the timeouts of the profile. |

## A job fails while it runs

After the agent takes the job, Buildkite decides the result, as for any agent. The controller only
deletes the VM when the job ends. A VM that fails while its job runs gives the reason on the job
page, and Buildkite marks the agent as lost.

A job that runs longer than `maxVMLifetime` (25 h) loses its VM. The controller counts the 25 h
from the time that Orchard created the VM, so a restart of the controller does not extend it.

## The controller does not start

The controller checks its whole configuration when it starts, and prints every problem. The
[configuration reference](configuration.md) gives the rules of each key. Common causes:

* a key that the controller does not know, often a typing error;
* an `agent.sha256` without quotes, which YAML reads as a number;
* a token that is missing from the environment;
* `buildkite did not accept the agent token`: the token is not a cluster agent token.

## VMs remain after their jobs

`bk_agent_stack_orchard.vm.leftover` counts VMs whose job ended. It is more than 0 for a few seconds
after each job, and longer while Buildkite does not answer, because the controller deletes a VM
only when it knows that the job ended. If it stays above 0, read the log of the controller for a
delete that Orchard refused.

A VM with the name prefix of this controller and no job UUID at the end of its name is an
orphan, and the controller deletes it. A VM whose name ends in a job UUID belongs to the
controller that the name shows, so a controller never deletes it as an orphan, even when the
`controllerID` of that other controller starts with this one.

## Git checkout fails with "cannot overwrite multiple values"

The image has more than one `credential.helper` in its global git configuration, and the agent
cannot replace them. The startup script removes them before the agent starts. If you still see
this, the image sets them in another file, such as the system configuration of git.
