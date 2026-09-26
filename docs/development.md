# Development

This guide tells a developer how to run the gates and the integration tests, and how to operate the
self-hosted runner that the integration tests use in CI while the repository is private.

## Tools

`mise` pins every tool that a developer and CI share, Orchard included.

```sh
mise install     # install the pinned tool versions
mise run check   # the gates: documents, commits, dependencies, lint, tests, and a snapshot image
```

The gates are also tasks of their own: `check-docs`, `check-commits`, `check-deps`, `lint`,
`lint-format`, `test`, `check-generate`, and `snapshot`. `mise tasks` lists them.

## The Helm chart

`deploy/helm/bk-agent-stack-orchard` deploys the controller (target design, section 11). CI lints
it, renders it with `deploy/helm/bk-agent-stack-orchard/ci/values.yaml`, and validates the result
against the Kubernetes schemas:

```sh
mise run check-chart
```

A values schema refuses a release without a `controllerID`, a queue, an Orchard URL, or the name
of the Secret of the tokens. The chart creates no Secret.

## The component suite

The component suite (T-020) runs the controller that `internal/app` builds against in-memory fakes
of both APIs, on a clock that the suite moves. `pgregory.net/rapid` draws random sequences of
events: jobs arrive and are cancelled, agents take jobs, VMs fail, workers stop and return, the
queue pauses, time passes, and the controller restarts. After each event the suite checks the
invariants I-1, I-2, I-5, and I-8, and at the end of each sequence it checks I-6.

Beside the random sequences, the suite runs the outages of the target design, section 7.5, as
scenarios on the same fakes (T-024): Buildkite not available, Orchard not available, and a worker
that stops reporting. Each scenario also checks the request budget of the Stacks API in each
second of the run. The client tests prove that the controller waits for `Retry-After` (T-040).

`go test ./...` runs it, on any host, in a few seconds. When an invariant breaks, rapid shrinks the
sequence to a short one, prints it, and writes a fail file under `test/component/testdata/rapid/`.
The failure message gives the command that replays it:

```sh
go test ./test/component/ -run TestRandomEventsHoldTheInvariants -rapid.failfile=<the file>
```

A wider search, for example after a change to the Reaper or to admission:

```sh
go test ./test/component/ -rapid.checks=10000 -rapid.steps=200
```

## The integration tests

The integration tests run against a real Orchard in synthetic mode, at the version that `mise.toml`
pins:

* The skeleton journey (T-060) runs the controller binary with the fake Stacks API server. One job
  goes from `scheduled` to a deleted VM, the job page shows each step, and at the end no VM of the
  controller remains (I-6).
* The admission journeys run the controller binary in the same way. In T-061, three jobs wait for
  a worker with two slots: the third job stays `scheduled` until a slot is free. In T-062, a job
  that no profile matches ends with exit status -1 and a message that names the profiles, and no
  VM starts.
* The restart journey (T-021) kills the controller with SIGKILL and starts it again, at each crash
  window of the target design, section 7.4. Each job keeps at most one VM, the VM of a job that
  ended while no controller ran goes, and the first pass extends every reservation.
* The readiness journey (T-063) revokes the agent token while the controller runs. The gRPC
  `readiness` service then stops serving, the log names the token, and `liveness` serves. Each
  controller of the integration tests serves its probes on a free port of its own.

To probe a controller by hand, use a gRPC health client, for example
[`grpc-health-probe`](https://github.com/grpc-ecosystem/grpc-health-probe):

```sh
grpc-health-probe -addr=localhost:8080 -service=readiness
```
* The interruption journeys cover a paused queue (T-022), in which no VM starts, no job is reserved
  or finished, and the VMs in progress continue, and a cancellation while a VM boots (T-023), in
  which the controller deletes the VM and does not take the job again.
* The Orchard client test (T-041) sends each call of the client of this project to the real API:
  create, a second create of the same name, list, events, and delete. A synthetic worker writes
  each line of the startup script as an event, so the test also proves the events of a VM.
* The specification test (T-064) sends the VM specification of each startup transport, with every
  optional field of a profile set, and reads the stored VM back. Orchard ignores a field that it
  does not know, so every field that the specification sent MUST come back with the same value.

```sh
mise run integration
```

The tests MUST run on macOS on arm64. Orchard places a VM only on a worker of the same
architecture and runtime. A macOS VM is arm64 and runs on Tart. A synthetic worker takes its
architecture from the host and its runtime from the host OS: Tart on macOS, and Vetu on Linux. On
any other host each test stops at once and gives this reason.

These conditions also apply:

* Port 6120 MUST be free, because Orchard fixes the port of `orchard dev`. A test stops with a
  message when something already answers there. The tests therefore run one after the other.
* Each test gives `orchard dev` a home directory of its own, because `orchard dev` writes a context
  into `$HOME/.orchard/orchard.yml`. The Orchard configuration of the developer does not change.
* Without `mise`, `ORCHARD_BIN` can name an Orchard binary. It MUST be the version that `mise.toml`
  pins.

## The CI runner of the integration tests

CI runs the integration tests on macOS on arm64 for the same reason. A hosted macOS runner costs
ten times the minutes of a Linux runner in a private repository, so the job runs on a self-hosted
runner while the repository is private. The runner is inside a Tart macOS VM on an Apple silicon
Mac, so the jobs do not reach the files of the Mac.

The repository variable `JOURNEY_RUNNER` selects the runner. When it names the label
`bk-orchard-mac`, the job runs on the VM. When it is empty, the job runs on a hosted macOS runner.

### Install Tart

Install Tart from its release, and check the SHA-256 that the release publishes. The formula of
the `cirruslabs/cli` Homebrew tap fails with the current Homebrew (2026-09-26).

```sh
gh release download 2.38.0 --repo openai/tart --pattern tart.tar.gz \
  --pattern tart_2.38.0_checksums.txt
grep ' tart.tar.gz$' tart_2.38.0_checksums.txt | shasum -a 256 -c -
tar -xzf tart.tar.gz
mkdir -p ~/Applications && cp -R tart.app ~/Applications/
ln -sf ~/Applications/tart.app/Contents/MacOS/tart /opt/homebrew/bin/tart
```

### Operate the runner

`scripts/journey-runner.sh` does each step. It needs `tart`, and `gh` signed in with an account that
administers the repository.

| Command | When | What it does |
|---|---|---|
| `create` | Once | Clones the macOS image (about 30 GiB), starts the VM, installs the runner after it checks the SHA-256, registers the runner with the label `bk-orchard-mac`, and sets `JOURNEY_RUNNER` |
| `start` | After a restart of the Mac, or after `stop` | Starts the VM without a window. The runner starts in the VM when the VM starts |
| `stop` | When the Mac needs its resources | Stops the VM. The integration tests wait in the queue until the next `start` |
| `status` | Any time | Shows the VM, the runner as GitHub sees it, and `JOURNEY_RUNNER` |
| `remove` | When the runner is no longer necessary | Removes the runner from GitHub, deletes `JOURNEY_RUNNER`, and deletes the VM |

```sh
scripts/journey-runner.sh create
scripts/journey-runner.sh status
```

The runner takes a job only while the VM runs and the Mac is awake. A job that finds no runner waits
in the queue, and GitHub cancels it after 24 hours. While jobs are expected, `caffeinate -i` keeps
the Mac awake.

### Before the repository is public

A self-hosted runner MUST NOT serve a public repository. A pull request from a fork can run code on
a self-hosted runner, and that code runs on the Mac of the owner. Before the repository is public,
run:

```sh
scripts/journey-runner.sh remove
```

This deletes `JOURNEY_RUNNER`, so the integration tests run on a hosted macOS runner, which costs
nothing in a public repository.
