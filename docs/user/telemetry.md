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
| `bk_agent_stack_orchard.job.waiting.count` | up-down counter, `{job}` | `bk_agent_stack_orchard.profile` | Matched jobs that wait for a VM |
| `bk_agent_stack_orchard.job.waiting.oldest.age` | gauge, `s` | `bk_agent_stack_orchard.profile` | How long the oldest waiting job has waited |
| `bk_agent_stack_orchard.job.skipped` | counter, `{job}` | | Jobs that match no profile, and that the controller leaves for another runner |
| `bk_agent_stack_orchard.queue.truncated` | counter, `{read}` | | Reads of the queue that did not list it whole, so that jobs behind the head of the queue wait for a later read |
| `bk_agent_stack_orchard.job.failed` | counter, `{job}` | `error.type` | Jobs that the controller failed, such as after a timeout |
| `bk_agent_stack_orchard.slot.count` | up-down counter, `{slot}` | `bk_agent_stack_orchard.pool`, `bk_agent_stack_orchard.slot.state` (`total`, `used`, `free`) | VM slots of the Mac hosts |
| `bk_agent_stack_orchard.vm.count` | up-down counter, `{vm}` | `bk_agent_stack_orchard.profile`, `bk_agent_stack_orchard.vm.phase` (`unplaced`, `booting`, `running`, `failed`) | VMs of this controller, by phase |
| `cicd.worker.count` | up-down counter, `{worker}` | `cicd.worker.state` (`available`, `busy`, `offline`) | Agents in VMs, by state |
| `bk_agent_stack_orchard.job.phase.duration` | histogram, `s` | `bk_agent_stack_orchard.profile`, `bk_agent_stack_orchard.job.phase` | How long each phase of a start took (below) |
| `bk_agent_stack_orchard.vm.deletions` | counter, `{vm}` | `bk_agent_stack_orchard.vm.delete.reason` | VMs that the controller deleted, by reason (below) |
| `cicd.system.errors` | counter, `{error}` | `cicd.system.component` (`buildkite`, `orchard`), `error.type` (`unauthorized`, `rate_limited`, `timeout`, `_OTHER`) | Calls to Buildkite or Orchard that failed |
| `bk_agent_stack_orchard.tick.duration` | histogram, `s` | `bk_agent_stack_orchard.tick.step` | How long each step of the loop took |
| `bk_agent_stack_orchard.vm.leftover` | gauge, `{vm}` | | VMs whose job ended, and that are not deleted yet |
| `bk_agent_stack_orchard.source.staleness` | gauge, `s` | `bk_agent_stack_orchard.source` (`scheduled_jobs`, `job_states`, `inventory`) | How old the last answer of each source is |

`bk_agent_stack_orchard.pool` is the `pool` label of the worker, or `none`. The HTTP clients also
give `http.client.request.duration`.

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
| `reservation_lapsed` | The reservation expired before the agent took the job, so the job is back in the queue. |
| `vm_failed` | Orchard reports the VM as failed. |
| `unplaced_timeout` | No host took the VM in time. The job goes back to the queue. |
| `boot_timeout` | The VM did not start in time. The job failed. |
| `acquire_timeout` | The agent did not take the job in time. The job failed. |
| `lifetime` | The VM reached `maxVMLifetime`. |
| `orphan` | The VM has the name prefix of this controller but no job in its name. |

## Traces

Each Buildkite job has one trace, and the trace ID is the job UUID without its dashes. To find the
trace of a job, search your backend for that ID. The trace holds the spans of the controller for the
job: `job.match`, `job.admit`, `stacks.reserve`, `stacks.issue_token`, `orchard.vm.create`,
`reaper.decide`, `stacks.finish_job`, and `orchard.vm.delete`, with the steps of the VM as events.
`job.match` has the attributes `bk_agent_stack_orchard.job.matched`,
`bk_agent_stack_orchard.profile`, and, for a job that no profile carries,
`bk_agent_stack_orchard.job.skipped`. `reaper.decide` has `bk_agent_stack_orchard.vm.phase`,
`bk_agent_stack_orchard.job.state`, `bk_agent_stack_orchard.reaper.decision`,
`bk_agent_stack_orchard.reaper.reason`, and `bk_agent_stack_orchard.reaper.row`.
Each step of the loop (`tick.poll`, `tick.inventory`, `tick.job_states`, `tick.reap`, `tick.admit`)
is a short trace of its own, with links to the jobs that it affected.

## Logs

Each record goes to the collector and to standard output, in the same shape. When they are known, a
record has the job (`cicd.pipeline.task.run.id`), the pipeline (`cicd.pipeline.name`), the build
(`cicd.pipeline.run.id`), the VM (`bk_agent_stack_orchard.vm.name`), the profile, and the host
(`bk_agent_stack_orchard.worker.name`), and the trace and span when one is active. Each time the
controller acts on a VM, it writes one record with the reason.

## The agent in each VM

When a profile sets `otel.enabled: true`, the agent in each VM sends its own traces to
`otel.endpoint`, in the trace of the job, so the steps of the build appear after the steps of the
VM. The VM cannot reach private networks, so use a collector on the internet, with authentication
and a rate limit. See the `otel` keys in the [configuration reference](configuration.md).

## Alerts

The controller does not ship rules for a backend. These conditions are a good start:

| Alert | Condition |
|---|---|
| Jobs wait too long | `bk_agent_stack_orchard.job.waiting.oldest.age` is more than 15 min. |
| The controller is not ready | `bk_agent_stack_orchard.source.staleness` of a source is more than 2 min. |
| The controller fails jobs | `bk_agent_stack_orchard.job.failed` goes up for 15 min. |
| Timeouts repeat | `bk_agent_stack_orchard.vm.deletions` with a reason that ends in `_timeout` goes up. |
| VMs remain | `bk_agent_stack_orchard.vm.leftover` is more than 0 in two exports in a row, at the default interval of 60 s. Ignore it while Buildkite is down. |
| Orphans exist | `bk_agent_stack_orchard.vm.deletions` with the reason `orphan` is more than 0. |
| The fleet is full | `bk_agent_stack_orchard.slot.count` with the state `free` is 0 for 30 min. |
| The queue is longer than one read | `bk_agent_stack_orchard.queue.truncated` goes up for 10 min. See [Troubleshooting](troubleshooting.md#a-job-stays-scheduled). |
