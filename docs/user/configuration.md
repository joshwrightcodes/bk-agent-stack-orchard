# Configuration reference

<!-- A test writes this page from the code (internal/config/reference_test.go). Do not edit it:
change the comments of the configuration types, or the template in internal/config/testdata,
and run mise run docs-reference. -->

The controller reads one YAML file, and its secrets from environment variables. It reads both when
it starts. To change either, restart the controller.

The file never holds a secret. The controller refuses a key that it does not know, so a secret that
somebody adds to the file stops the controller instead of reaching a log or a VM.

## A minimal file

```yaml
controllerID: macos-prod
buildkite:
  queue: macos
orchard:
  url: https://orchard.example.com:6120
  serviceAccount: bk-agent-stack-orchard
profiles:
  - name: default
    match: {}
    image: ghcr.io/cirruslabs/macos-golden-gate-base@sha256:<digest>
    cpu: 4
    memoryMiB: 8192
    diskSizeGB: 50
```

With the Helm chart, the file is the value `config`, and the chart checks it against its schema
first.

## Keys

A duration is a number and a unit, such as `5s`, `2m`, or `25h`. A key with no default and no
"Required" can be left out.

### `controllerID`

The name of this controller. It is part of the name of each VM, so it is lower-case letters, digits,
and dashes, 23 characters or fewer. Required.

Type: string.

### `buildkite`

The connection to Buildkite.

| Key | Type | Default | Description |
|---|---|---|---|
| `buildkite.queue` | string |  | The self-hosted Buildkite queue that this controller serves. Other agents and stacks may serve the same queue: the controller counts only the workers that its profiles select, and it leaves each job that no profile carries in the queue for them. Required. |
| `buildkite.endpoint` | string | `https://agent.buildkite.com/v3` | The URL of the agent API of Buildkite. It must use https, except on a loopback address. |
| `buildkite.plainHTTPEndpoint` | boolean | `false` | Allows an endpoint without TLS that is not a loopback address, such as a fake API in a test cluster. The agent token then travels unencrypted, and the controller warns at start. |
| `buildkite.pollInterval` | duration | `1s` | How often the controller reads the scheduled jobs of the queue. It must be shorter than stateFreshness. |
| `buildkite.jobStatesInterval` | duration | `5s` | How often the controller reads the state of the jobs that have a VM. It must be shorter than stateFreshness. |
| `buildkite.reservationExpiry` | duration | `5m` | How long Buildkite holds a job for this controller after it reserves the job. From 1 s to 1 h, in whole seconds. |
| `buildkite.reservationRenewBefore` | duration | `2m` | How long before a reservation expires the controller renews it, while the VM of the job starts. It must be shorter than reservationExpiry. |
| `buildkite.jobTokenLifetime` | duration | `30m` | The longest that the token that the controller gives each VM stays valid. The token opens one job only. Each token stays valid for the unplaced, boot, and acquire timeouts of the profile of its job and one minute more, up to this value. From 1 s to 30 min, in whole seconds, and at least the sum of the unplaced, boot, and acquire timeouts of each profile and one minute. |

### `orchard`

The connection to Orchard.

| Key | Type | Default | Description |
|---|---|---|---|
| `orchard.url` | string |  | The URL of the Orchard controller. It must use https, except on a loopback address. Required. |
| `orchard.serviceAccount` | string |  | The Orchard service account of this controller. It needs the compute:read and compute:write roles. Required. |
| `orchard.inventoryInterval` | duration | `1s` | How often the controller reads the workers and VMs of Orchard. It must be shorter than stateFreshness. |
| `orchard.workerOfflineTimeout` | duration | `3m` | How long a worker can be silent before the controller counts it as offline. Set it to the worker offline timeout of your Orchard controller. |

### `maxInFlight`

The most VMs that this controller runs at one time. 0 means the free VM slots of the workers that
can run the profiles.

Type: integer. Default: `0`.

### `stateFreshness`

How old the data of Buildkite and Orchard can be before the controller stops starting VMs and
reports that it is not ready.

Type: duration. Default: `1m`.

### `healthAddress`

The address of the gRPC health service, with the services liveness and readiness. The service has no
authentication, so the default is a loopback address. On Kubernetes, set an address that the kubelet
reaches, such as :8080, as the Helm chart does.

Type: string. Default: `127.0.0.1:8080`.

### `defaults`

The values that a profile takes when it sets none of its own.

| Key | Type | Default | Description |
|---|---|---|---|
| `defaults.timeouts` | object |  | The timeouts of each profile that sets none of its own. |
| `defaults.timeouts.unplaced` | duration | `2m` | How long a VM can wait for Orchard to give it a worker. After this, the VM is deleted, and the job goes back to the queue to start again. |
| `defaults.timeouts.boot` | duration | `15m` | How long the image pull, the clone, and the start of the VM can take. After this, the job fails and the VM is deleted. |
| `defaults.timeouts.acquire` | duration | `5m` | How long the boot of macOS and the start of the agent can take, until the agent takes the job. After this, the job fails with the last lines of the startup script. |
| `defaults.timeouts.maxVMLifetime` | duration | `25h` | The longest that any VM lives, whatever the state of its job. A job that runs longer loses its VM. |
| `defaults.agent` | object |  | Where the agent of each VM comes from, for each profile that does not say. |
| `defaults.agent.source` | string | `download` | download: the VM downloads the archive at url, checks it against sha256, and runs its agent. image: the VM runs the agent of the image, which must be v4. |
| `defaults.agent.url` | string | `https://github.com/buildkite/agent/releases/download/v4.9.0/buildkite-agent-darwin-arm64-4.9.0.tar.gz` | The https URL of a buildkite-agent release archive for macOS on arm64. Set it with sha256. |
| `defaults.agent.sha256` | string | `faaf0084edf08a92a877253620604fbd6ce496d2851a7ae5126dd19f27429e76` | The SHA-256 of the archive at url: 64 lower-case hexadecimal characters, in quotes. Set it with url. |

### `profiles`

The types of job VM. A job runs on the profile whose match map equals its tags. At least one profile
is required.

| Key | Type | Default | Description |
|---|---|---|---|
| `profiles[].name` | string |  | The name of the profile: lower-case letters, digits, and dashes. Each profile has its own name. The agent of each VM has the tag profile=NAME. Required. |
| `profiles[].match` | map of strings |  | The tags that a job must have, besides its queue, to run on this profile. A job runs on the profile whose match map equals its tags exactly, and {} matches a job with no tag other than the queue. Two profiles cannot have the same map. A job that matches no profile is left for another runner of the queue. |
| `profiles[].image` | string |  | The Tart image of the VMs, in an OCI registry. It must run the Tart guest agent. Use a digest (@sha256:...), or the controller warns that the image can change. Required. |
| `profiles[].cpu` | integer |  | The CPU cores of each VM. Required. |
| `profiles[].memoryMiB` | integer |  | The memory of each VM, in MiB. Required. |
| `profiles[].diskSizeGB` | integer |  | The disk of each VM, in GB. It must be at least the disk of the image. Required. |
| `profiles[].workerLabels` | map of strings |  | The labels that a worker must have to run the VMs of this profile. |
| `profiles[].softnetAllow` | list of strings |  | IPv4 ranges in private networks that the VMs can reach, such as a mirror or an artifact store. Each one is a range with a prefix length from /16 to /32. In 10.0.0.0/8, 172.16.0.0/12, and 192.168.0.0 to 192.168.99.255, where the VM networks are, the prefix length is from /29 to /32. Every pipeline on the queue can reach them, so the controller warns at start. |
| `profiles[].startupTransport` | string | `tart-guest-agent` | How the Orchard worker runs the startup script of a VM: tart-guest-agent, or ssh with vmCredentials. |
| `profiles[].vmCredentials` | object |  | The credentials of the image, for startupTransport ssh only. |
| `profiles[].vmCredentials.usernameEnv` | string |  | The environment variable of the controller that holds the user name of the image. It must start with VM_. Required with vmCredentials. |
| `profiles[].vmCredentials.passwordEnv` | string |  | The environment variable of the controller that holds the password of the image. It must start with VM_. Required with vmCredentials. |
| `profiles[].timeouts` | object |  | The timeouts of this profile. A timeout that is unset or 0 takes the value of defaults.timeouts. |
| `profiles[].timeouts.unplaced` | duration | `defaults.timeouts.unplaced` | How long a VM can wait for Orchard to give it a worker. After this, the VM is deleted, and the job goes back to the queue to start again. |
| `profiles[].timeouts.boot` | duration | `defaults.timeouts.boot` | How long the image pull, the clone, and the start of the VM can take. After this, the job fails and the VM is deleted. |
| `profiles[].timeouts.acquire` | duration | `defaults.timeouts.acquire` | How long the boot of macOS and the start of the agent can take, until the agent takes the job. After this, the job fails with the last lines of the startup script. |
| `profiles[].timeouts.maxVMLifetime` | duration | `defaults.timeouts.maxVMLifetime` | The longest that any VM lives, whatever the state of its job. A job that runs longer loses its VM. |
| `profiles[].otel` | object |  | The telemetry of the agent in each VM. |
| `profiles[].otel.enabled` | boolean | `false` | Turns on the OpenTelemetry tracing of the agent in each VM. The spans of the agent then join the trace of the job. |
| `profiles[].otel.endpoint` | string |  | The OTLP endpoint that the agent sends to. It must use https, except on a loopback address. Empty means the default of the agent. |
| `profiles[].otel.headersEnv` | string |  | The environment variable of the controller that holds the OTLP headers of the agent, such as a token of the collector. It must start with VM_. |
| `profiles[].agent` | object |  | Where the agent of each VM comes from. A value that is unset takes the value of defaults.agent. |
| `profiles[].agent.source` | string | `defaults.agent.source` | download: the VM downloads the archive at url, checks it against sha256, and runs its agent. image: the VM runs the agent of the image, which must be v4. |
| `profiles[].agent.url` | string | `defaults.agent.url` | The https URL of a buildkite-agent release archive for macOS on arm64. Set it with sha256. |
| `profiles[].agent.sha256` | string | `defaults.agent.sha256` | The SHA-256 of the archive at url: 64 lower-case hexadecimal characters, in quotes. Set it with url. |

## Environment variables

| Variable | Contents |
|---|---|
| `BK_AGENT_STACK_ORCHARD_CONFIG` | The path of the file. The default is `/etc/bk-agent-stack-orchard/config.yaml`. The `-config` flag takes precedence. |
| `BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN` | The cluster agent token of the queue. Required. |
| `BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN` | The token of the Orchard service account. Required. |
| `BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT` | The PEM of the certificate of Orchard, or of its certificate authority, when it is private. The calls to Orchard then trust this PEM only, and not the authorities of the system. |
| `VM_*` | The variables that a profile names, such as `vmCredentials.passwordEnv` and `otel.headersEnv`. Only a variable that starts with `VM_` can go into a VM. |
| `OTEL_*` | The standard OpenTelemetry settings of the telemetry of the controller. |

## When the controller refuses the file

The controller checks the whole file when it starts, and reports every problem, not only the
first. It does not start until each one is fixed. Besides the limits in the tables, it refuses:

* two profiles with the same name or the same `match` map;
* a `match` key `queue`, `profile`, or `agent-stack`, which the controller sets itself;
* a profile whose `unplaced`, `boot`, and `acquire` timeouts and one minute add up to more
  than `buildkite.jobTokenLifetime`;
* `vmCredentials` that do not agree with `startupTransport`;
* half of an `agent` pair: `url` without `sha256`, or the reverse.

It warns, and starts, when an image has no digest, or a profile has `softnetAllow` entries.
