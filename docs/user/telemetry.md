# Telemetry

The controller sends metrics, traces, and logs with OpenTelemetry (OTLP) to a collector that you
run. It has no Prometheus endpoint. It also writes each log record to its standard output as JSON.

## Export

Set the standard OpenTelemetry variables in the environment of the controller:

| Variable | Example | What it does |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `https://collector.example.com:4318` | The collector |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` or `grpc` | The protocol of OTLP |
| `OTEL_EXPORTER_OTLP_HEADERS` | `authorization=Bearer …` | Headers, such as a token of the collector |
| `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` | `otlp` or `none` | Turns a signal on or off. The controller refuses any other value |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` | How often metrics go out, in milliseconds |

With the Helm chart, set the endpoint with the `otel` values, and the other variables with
`extraEnv`.

## Metrics

Each name starts with `bk_agent_stack_orchard.`, except the names of the OpenTelemetry conventions
for CI systems (`cicd.`). No metric has a job identifier or a VM name, so the number of series stays
small.

| Metric | Type, unit | Attributes | What it measures |
|---|---|---|---|
| `bk_agent_stack_orchard.job.waiting.count` | up-down counter, `{job}` | `bk_agent_stack_orchard.profile` | Matched jobs of the last read of the queue that wait for a VM |
| `bk_agent_stack_orchard.job.waiting.oldest.age` | gauge, `s` | `bk_agent_stack_orchard.profile` | How long the oldest waiting job has waited, or 0 when no job of the profile waits |
| `bk_agent_stack_orchard.admission.blocked` | gauge, `1` | `bk_agent_stack_orchard.admission.reason` | 1 for the reason that stopped admission at the last tick, and 0 for each other reason (below) |
| `bk_agent_stack_orchard.job.skipped` | counter, `{job}` | | Jobs that match no profile, and that the controller leaves for another runner |
| `bk_agent_stack_orchard.queue.truncated` | counter, `{read}` | | Reads of the queue that did not list it whole, so that jobs behind the head of the queue wait for a later read |
| `bk_agent_stack_orchard.job.failed` | counter, `{job}` | `error.type` | Jobs that the controller failed, such as after a timeout |
| `bk_agent_stack_orchard.slot.count` | up-down counter, `{slot}` | `bk_agent_stack_orchard.pool`, `bk_agent_stack_orchard.slot.state` (`total`, `used`, `free`) | VM slots of the Mac hosts |
| `bk_agent_stack_orchard.vm.count` | up-down counter, `{vm}` | `bk_agent_stack_orchard.profile`, `bk_agent_stack_orchard.vm.phase` (`unplaced`, `booting`, `running`, `failed`) | VMs of this controller, by phase |
| `cicd.worker.count` | up-down counter, `{worker}` | `cicd.worker.state` (`busy`, `offline`) | Job VMs, by the status that Orchard gives them: `busy` while the VM runs, and `offline` when it failed. A VM carries one job, so no VM is `available` |
| `bk_agent_stack_orchard.job.phase.duration` | histogram, `s` | `bk_agent_stack_orchard.profile`, `bk_agent_stack_orchard.job.phase` | How long each phase of a start took (below) |
| `bk_agent_stack_orchard.vm.deletions` | counter, `{vm}` | `bk_agent_stack_orchard.vm.delete.reason` | VMs that the controller deleted, by reason (below) |
| `cicd.system.errors` | counter, `{error}` | `cicd.system.component` (`buildkite`, `orchard`), `bk_agent_stack_orchard.operation` (below), `error.type` (`unauthorized`, `rate_limited`, `timeout`, `_OTHER`) | Calls to Buildkite or Orchard that failed |
| `bk_agent_stack_orchard.tick.duration` | histogram, `s` | `bk_agent_stack_orchard.tick.step` | How long each step of the loop took |
| `bk_agent_stack_orchard.vm.leftover` | gauge, `{vm}` | | VMs whose job ended, and that are not deleted yet |
| `bk_agent_stack_orchard.source.staleness` | gauge, `s` | `bk_agent_stack_orchard.source` (`scheduled_jobs`, `job_states`, `inventory`) | How old the last answer of each source is. A source that has not answered since the start is as old as the process |
| `bk_agent_stack_orchard.loop.heartbeat.age` | gauge, `s` | | Time since the loop last finished a tick. Before the loop starts, it is the age of the process |
| `bk_agent_stack_orchard.stack.registered` | gauge, `1` | | 1 while Buildkite holds the registration of the stack. 0 while the controller registers, which it tries again while Buildkite does not answer, and after it withdrew |

`bk_agent_stack_orchard.pool` is the `pool` label of the worker, or `none`. The HTTP clients also
give `http.client.request.duration`.

Each series exists from the start of the process, with 0 where nothing is counted. A pool is
known only from the workers, so its series start with the first read of Orchard. A series that
falls to zero reports 0, and does not stop. The ages are computed when the collector reads them,
so they keep rising while the loop is stuck. The liveness check fails after 15 minutes without a
finished tick, and the platform then restarts the process.

An alert that compares a value does not fire on a series that does not exist. For each alert on
the controller, also alert when its series is absent for the `service.instance.id` of the
controller, which covers a process that does not start or that crashes in a loop.

`cicd.system.errors` counts a call once, after its last try. The client tries a call again after
a failure in transport, a 5xx status, or 429, inside the time limit of the call, and those tries
show only in `http.client.request.duration` with `http.response.status_code`. `error.type` is
`unauthorized` for a credential that the system refused (401 from Buildkite, and 401 or 403 from
Orchard), `rate_limited` for 429, `timeout` for a call that reached its time limit, and `_OTHER`
for each other failure. An answer about one job, such as a job that ended or a VM that exists, is
not a failure of the system. The calls of `bk_agent_stack_orchard.operation`:

| Component | Operations |
|---|---|
| `buildkite` | `register`, `deregister`, `scheduled_jobs`, `reserve`, `tokens`, `states`, `finish`, `notify` |
| `orchard` | `create_vm`, `list_vms`, `delete_vm`, `list_workers`, `events` |

The two gauges of the waiting jobs read the last read of the queue at each tick, also while
admission is stopped. A read that did not list the whole queue (`queue.truncated`) does not count
the jobs behind its last page. While a source does not answer, the last read of the queue stays,
so the count stays and the age rises.

The reasons of `bk_agent_stack_orchard.admission.reason`. Admission starts no VM while one of them
holds:

| Reason | Why admission stopped |
|---|---|
| `queue_unread` | The controller has not read the queue since it started. |
| `queue_paused` | An operator paused the queue in Buildkite. |
| `scheduled_jobs_stale` | The last read of the queue is older than `stateFreshness`. |
| `inventory_unanswered` | Orchard has not answered since the start. |
| `inventory_stale` | The last answer of Orchard is older than `stateFreshness`. |
| `job_states_unanswered` | The job states have not answered since the start, so no reaper pass has seen the VMs of the last process. |
| `job_states_stale` | The last job states are older than `stateFreshness`. |

The phases of `bk_agent_stack_orchard.job.phase.duration`:

| Phase | From | To |
|---|---|---|
| `scheduled_to_reserved` | the job is scheduled | the controller reserves it |
| `reserved_to_placed` | the reservation | Orchard gives the VM a host |
| `placed_to_running` | the host | the VM runs |
| `running_to_accepted` | the VM runs | the agent takes the job |
| `finished_to_deleted` | the job ends | the VM is deleted |
| `total` | the job is scheduled | the agent takes the job |

The controller sees a change at each read, so each phase is accurate to about `jobStatesInterval`
(5 s).

The reasons of `bk_agent_stack_orchard.vm.delete.reason`:

| Reason | Why the VM went |
|---|---|
| `job_terminal` | The job ended. This is the normal reason. |
| `job_unknown` | Buildkite does not know the job. |
| `reservation_lapsed` | The reservation expired before the agent took the job, so the job is back in the queue. Or Buildkite refused to extend the reservation, so another stack can hold the job. The controller does not fail the job in either case. |
| `vm_failed` | Orchard reports the VM as failed. |
| `unplaced_timeout` | No host took the VM in time. The job goes back to the queue. |
| `boot_timeout` | The VM did not start in time. The job failed. |
| `acquire_timeout` | The agent did not take the job in time. The job failed. |
| `lifetime` | The VM reached `maxVMLifetime`. |
| `orphan` | The VM has the name prefix of this controller but no job in its name. |

## Traces

Each Buildkite job has one trace, and the trace ID is the job UUID without its dashes. To find the
trace of a job, search your backend for that ID. The trace has no root span, because no process
of the controller lives as long as a job. Each step of the loop is a short trace of its own, with
links to the job spans that it wrote.

| Span | Trace | What it covers | Attributes |
|---|---|---|---|
| `job.match` | job | The first time that this process sees the job in the queue. A job that no profile carries has the event `no profile`, with the reason in `bk_agent_stack_orchard.match.reason`. | `bk_agent_stack_orchard.job.matched`, `bk_agent_stack_orchard.profile`, `bk_agent_stack_orchard.job.skipped` |
| `job.admit` | job | Admission starts the job. | `bk_agent_stack_orchard.profile` |
| `stacks.reserve` | job | The reservation of the job, and each extension of it. | `bk_agent_stack_orchard.job.reserved`, and `bk_agent_stack_orchard.reservation.extension` on an extension |
| `stacks.issue_token` | job | The token call of the job. | `bk_agent_stack_orchard.job.token_issued` |
| `orchard.vm.create` | job | The create of the VM. | |
| `orchard.vm.change` | job | A step of the VM, as the event `placed` or `running`. | The events carry `bk_agent_stack_orchard.worker.name` and `bk_agent_stack_orchard.vm.name` |
| `stacks.notify_skipped` | job | The skip notice of a job that no profile carries. | `bk_agent_stack_orchard.notification.refused` when Buildkite refused the line |
| `reaper.decide` | job | One action of the reaper on the VM of the job. | `bk_agent_stack_orchard.vm.phase`, `bk_agent_stack_orchard.job.state`, `bk_agent_stack_orchard.reaper.decision`, `bk_agent_stack_orchard.reaper.reason`, `bk_agent_stack_orchard.reaper.row` |
| `stacks.finish_job` | job | The finish of a job that the controller fails, under `reaper.decide`. | |
| `orchard.vm.events` | job | The read of the startup-script lines for a finish, under `reaper.decide`. | |
| `orchard.vm.delete` | job | The delete of the VM, under `reaper.decide`. | |
| `tick.poll`, `tick.inventory`, `tick.job_states`, `tick.reap`, `tick.admit` | its own | One step of the loop. It links to each job span of the step. | |

One call to Buildkite can reserve, or get the tokens of, several jobs. Each job then has its own
`stacks.reserve` or `stacks.issue_token` span with the answer for that job, and the HTTP span of
the call is under the step of the loop. The HTTP clients add the spans of `otelhttp`. A span whose
work failed has the status Error and an `exception` event.

## Logs

Each record goes to the collector and to standard output, in the same shape. When they are known, a
record has the job (`cicd.pipeline.task.run.id`), the pipeline (`cicd.pipeline.name`), the build
(`cicd.pipeline.run.id`), the VM (`bk_agent_stack_orchard.vm.name`), the profile, and the host
(`bk_agent_stack_orchard.worker.name`), and the trace and span when one is active. Each time the
controller acts on a VM, it writes one record with the reason.

A record about admission has the reason in `bk_agent_stack_orchard.admission.reason`. A record
that says more in a sentence has it in `bk_agent_stack_orchard.detail`.

## The agent in each VM

When a profile sets `otel.enabled: true`, the agent in each VM sends its own traces to
`otel.endpoint`, in the trace of the job, so the steps of the build appear after the steps of the
VM. The VM cannot reach private networks, so use a collector on the internet, with authentication
and a rate limit. See the `otel` keys in the [configuration reference](configuration.md).

## Alerts

The controller does not ship rules for a backend, and it sets no thresholds. Choose the threshold
and the window of each alert from your own objectives, such as how long a job can wait in your
fleet. These conditions are a good start:

| Alert | Signal and direction | What it means | What to do |
|---|---|---|---|
| Jobs wait too long | `bk_agent_stack_orchard.job.waiting.oldest.age` rises and stays high. | Jobs wait longer than your wait objective. The fleet is full, or no host carries the profile. | [A job stays scheduled](troubleshooting.md#a-job-stays-scheduled) |
| The controller is not ready | `bk_agent_stack_orchard.source.staleness` of a source rises and stays high. | The controller has no fresh answer from Buildkite or Orchard, so it starts no VM. | [A job stays scheduled](troubleshooting.md#a-job-stays-scheduled), the row about readiness |
| Admission is stopped | `bk_agent_stack_orchard.admission.blocked` stays at 1 for a reason. | The controller starts no VM, and the reason says why. `queue_paused` is a choice of an operator. | [A job stays scheduled](troubleshooting.md#a-job-stays-scheduled) |
| The controller fails jobs | `bk_agent_stack_orchard.job.failed` keeps going up. | VMs fail, or time out before an agent takes the job. | [A job fails before it runs](troubleshooting.md#a-job-fails-before-it-runs) |
| Timeouts repeat | `bk_agent_stack_orchard.vm.deletions` with a reason that ends in `_timeout` keeps going up. | VMs do not reach a host, boot, or start an agent in time. | [A job fails before it runs](troubleshooting.md#a-job-fails-before-it-runs) |
| VMs remain | `bk_agent_stack_orchard.vm.leftover` rises and stays high. Ignore it while Buildkite is down. | The controller does not delete the VMs of ended jobs. | [VMs remain after their jobs](troubleshooting.md#vms-remain-after-their-jobs) |
| Orphans exist | `bk_agent_stack_orchard.vm.deletions` with the reason `orphan` goes up. | A VM with the name prefix of this controller has no job. Two controllers can have the same `controllerID`. | [VMs remain after their jobs](troubleshooting.md#vms-remain-after-their-jobs) |
| The fleet is full | `bk_agent_stack_orchard.slot.count` with the state `free` stays at 0. | Every slot is in use, so new jobs wait. | [A job stays scheduled](troubleshooting.md#a-job-stays-scheduled) |
| The queue is longer than one read | `bk_agent_stack_orchard.queue.truncated` keeps going up. | Jobs that never leave the queue fill its head, so jobs behind them wait longer. | [A job stays scheduled](troubleshooting.md#a-job-stays-scheduled) |
| Unknown worker | The Orchard metric `org.cirruslabs.orchard.controller.worker_resource` shows a `worker` value that is not in your list of hosts. | A worker registered with a name that no host of your fleet has, for example with a stolen host token. Orchard does not tie a worker name to an account, so this is the only sign of such a worker. It does not show a known name that another host uses. The project has not yet seen this series in a collector, so check that your collector receives it. | [Revoke one host](runbooks/orchard-worker-accounts.md#revoke-one-host) |
