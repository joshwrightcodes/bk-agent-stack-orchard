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
mise run check-deploy
```

CI validates against the schemas of Kubernetes 1.37. A values schema refuses a release without a
`controllerID`, a queue, an Orchard URL, or the name of the Secret of the tokens. The chart creates
no Secret.

## The smoke deployment

`scripts/smoke.sh` deploys the chart and the Orchard base of `deploy/orchard` on a kind cluster,
with the fake Stacks API and an OpenTelemetry collector, as an operator deploys them:

* ko builds the image of the controller and of the fake, and `deploy/orchard/image` the image of
  Orchard; kind loads all three.
* An authority that the test makes signs the certificate of Orchard, and the controller trusts it
  through `BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT`.
* The bootstrap Job of Orchard sets the service accounts, and the controller uses its token.
* The NetworkPolicy of the chart stays on, and kind enforces it.

It passes when the controller becomes ready through its gRPC readiness probe, and the collector
receives its metrics and its spans. On a failure it shows the pods, the events, and the logs.

```sh
mise run smoke
KEEP=1 mise run smoke   # keep the cluster to look at it
```

CI runs it on each pull request. It needs Docker.

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

## The load test

The load test (T-080) runs the controller at the target scale: `orchard dev` with 50 synthetic
workers of two slots each, and 500 jobs that arrive in the pattern of the demand model of the
system design baseline, section 4.2. The demand model runs fifteen times faster than a workday, so
the test takes about 25 minutes. The test is also the agents: it takes each job whose VM runs, and
ends it after its duration.

```sh
mise run load
```

It reports the tick duration, the busiest second of each Stacks API endpoint, the most VMs at one
time, and the wait for a VM, in its log and in the summary of the `load` workflow. It fails on a
broken invariant (I-1, I-2, I-6) or on a second above the limits of the Stacks API. It judges the
tick only when `LOAD_TICK_BUDGET` is set (open item O-5).

`LOAD_JOBS` runs fewer jobs, for example `LOAD_JOBS=60`, while you work on the test. The test
writes a progress line each minute, and on a failure it shows the end of the controller log.

The host MUST stay awake for the whole run. A Mac that sleeps pauses the run, and the synthetic
workers go offline, so the result is not a measurement. Run it with `caffeinate -dims` and the lid
open.

The `load` workflow runs it each night, on demand, and on a pull request that changes it, on the
same runner as the integration tests. No branch protection requires it. While the runner is a VM
on a laptop, a night on which the laptop sleeps gives no result.

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

Tart, Softnet, and Orchard come from the `openai/tools` Homebrew tap
([openai/homebrew-tools](https://github.com/openai/homebrew-tools#tart-related-projects)). The
`cirruslabs/cli` tap is no longer where they are published. Homebrew 7 loads a formula from a tap
that is not official only after the tap is trusted:

```sh
brew trust openai/tools
brew install openai/tools/tart openai/tools/softnet
```

Homebrew installs `softnet` without the setuid bit, and Tart asks for a sudo password on a
terminal to set it the first time that a VM uses Softnet. Set it once after each install or
upgrade of Softnet, because an upgrade replaces the file:

```sh
softnet_bin=$(realpath "$(brew --prefix)/bin/softnet")
sudo chown root "$softnet_bin" && sudo chmod u+s "$softnet_bin"
```

The runner needs only Tart. The Tart test below also needs Softnet, and it needs Tart 2.39.0 or
later (target design, section 9.2). On 2026-09-27 the tap still gives Tart 2.38.0
([openai/homebrew-tools#97](https://github.com/openai/homebrew-tools/pull/97) updates it). Until
then, install Tart from its release, and check the SHA-256 that the release publishes:

```sh
gh release download 2.39.0 --repo openai/tart --pattern tart.tar.gz \
  --pattern tart_2.39.0_checksums.txt
grep ' tart.tar.gz$' tart_2.39.0_checksums.txt | shasum -a 256 -c -
tar -xzf tart.tar.gz
mkdir -p ~/Applications && cp -R tart.app ~/Applications/
ln -sf ~/Applications/tart.app/Contents/MacOS/tart "$(brew --prefix)/bin/tart"
```

Orchard comes from `mise install` (the version of `mise.toml`), so it needs no formula here.

### The Tart test

`mise run tart-test` runs one job on a real Tart VM of this Mac: the controller, `orchard dev` with
a real worker, and the agent image. It is the evidence that the Tart guest agent runs the startup
script (test strategy, open item O-2). Build the image with `mise run agent-image`, and name it:

```sh
BKASO_TART_IMAGE=bkaso-agent mise run tart-test
```

The VM takes one of the two VM slots of the Mac, so the runner VM can run at the same time. CI does
not run this test, because a VM cannot run a macOS VM.

`mise run softnet-test` probes the network from a VM of the controller specification: it must reach
the Internet and DNS, and not the LAN, another VM, or a service of the Mac itself. Softnet alone
lets a VM reach the Mac through the gateway of the VM network, and the hosts close that with pf
(`deploy/hosts`). Load the same rules on the Mac before the test. They go when the Mac restarts, or
with `sudo pfctl -a com.apple/400.bkaso-vm-isolation -F all`:

```sh
sudo pfctl -E
sudo pfctl -a com.apple/400.bkaso-vm-isolation -f deploy/hosts/roles/orchard_worker/files/vm-isolation.pf
BKASO_TART_IMAGE=bkaso-agent mise run softnet-test
```

`mise run hosts-test` needs the opposite: its test VM reaches an Orchard controller on the Mac,
which the rules block, so remove them before it.

### Operate the runner

`scripts/journey-runner.sh` does each step. It needs `tart`, and `gh` signed in with an account that
administers the repository.

| Command | When | What it does |
|---|---|---|
| `create` | Once | Clones the macOS image (about 30 GiB), starts the VM, installs the runner after it checks the SHA-256, registers the runner with the label `bk-orchard-mac`, and sets `JOURNEY_RUNNER` |
| `start` | After a restart of the Mac, or after `stop` | Starts the VM without a window. The runner starts in the VM when the VM starts, and launchd starts it again whenever it stops |
| `stop` | When the Mac needs its resources | Stops the VM. The integration tests wait in the queue until the next `start` |
| `restart` | When GitHub shows the runner offline and the VM runs | Starts the runner again in the VM. A job that the runner holds fails |
| `status` | Any time | Shows the VM, the runner as GitHub sees it, and `JOURNEY_RUNNER` |
| `remove` | When the runner is no longer necessary | Removes the runner from GitHub, deletes `JOURNEY_RUNNER`, and deletes the VM |

```sh
scripts/journey-runner.sh create
scripts/journey-runner.sh status
```

A Mac that sleeps stops the clock of the VM. When the Mac wakes, the runner can renew its session
with the old time, GitHub refuses the token as expired, and the runner exits: the VM runs, and
GitHub shows the runner offline. The service of the runner has `KeepAlive`, so launchd starts the
runner again within 30 s, when the clock of the VM is right again. `create` and `start` set it, and
`restart` starts a runner at once.

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
