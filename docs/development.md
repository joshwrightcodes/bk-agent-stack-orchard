# Development

This guide tells a developer how to run the gates and the integration tests.

## Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT",
"RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in BCP 14 [RFC 2119] [RFC 8174] when, and only when, they appear in
all capitals, as shown here.

## Prerequisites

* A Mac with Apple silicon (macOS on arm64). The integration tests and the real-VM tests need it,
  because Orchard places a macOS VM only on an arm64 worker with the Tart runtime. The gates `lint`,
  `test`, and `check-docs` run on any host.
* [`mise`](https://mise.jdx.dev/). It installs every other tool, Go and Orchard included.
* Docker. Two tasks need it: `check-c4` (it renders the C4 model) and `snapshot` (it builds the
  image). `mise run check` runs `check-c4`, so it also needs Docker. `smoke` needs it too, for kind.
* Tart. Install Tart from the `openai/tools` tap as the section "Install Tart" says, or let the
  Ansible role of `deploy/hosts` install it on a Mac host. Only the real-VM tests (`tart-test`,
  `failures-test`, `restart-test`, `softnet-test`) need Tart. The other tasks do not.

## Tools

`mise` pins every tool that a developer and CI share, Orchard included.

```sh
mise install     # install the pinned tool versions
mise run check   # the gates: documents, commits, dependencies, lint, tests, and a snapshot image
```

The gates are also tasks of their own: `check-docs`, `check-commits`, `check-deps`, `lint`,
`lint-format`, `test`, `check-generate`, and `snapshot`. `mise tasks` lists them. `test` runs
`go test -race`, so the race detector runs on each local and CI run.

`mise run check` and `mise run ci` are different gates:

* `check` is the gate before a pull request. It runs `check-c4`, so it needs Docker.
* `ci` runs the checks that the required CI jobs run, so a pass predicts a pass in CI. It adds
  `test-scripts`, `vuln`, `check-hook`, `check-hosts`, `check-generate`, and `integration`, which
  `check` does not run. `check-generate` reads the GitHub API, so the command is
  `GITHUB_TOKEN=$(gh auth token) mise run ci`. The token MUST stay in the environment. It MUST NOT
  go into a file.
* `ci` runs the Docker jobs (`check-c4`, `orchard-image`, `smoke`, and `snapshot`) through
  `ci-docker`. When Docker is absent, `ci-docker` prints that it skips them and exits 0. Those
  jobs are then "CI only".
* The title gate of a pull request is CI only, because it needs the pull request title.

If `check-deps` fails because NOTICE does not name a module, a dependency update added a module
that the binary links. Fix it in this order:

1. Run `mise run licenses`, or `go-licenses`, to see the new module and its licence. The texts are
   in `build/licenses`.
2. Add the module to the list of its licence in `NOTICE`, in the existing format: one line,
   `  * <module path>`, in alphabetical order.
3. Copy `NOTICE` to `deploy/helm/bk-agent-stack-orchard/NOTICE`, because the chart carries a copy
   that equals the file of the root.
4. Run `mise run check-deps`.

## The package map

Each component of the target design (section 4) is one package with one responsibility, so that
the pure functions stay testable without a VM. The implementation plan (section 3) gives the rule
for the interfaces.

```
cmd/bk-agent-stack-orchard/     process entry point, flags, signal handling, the real clients
internal/app/                   the wiring: the parts, both APIs, and the one clock
internal/secret/                a value that no log, metric, or VM specification may carry
internal/config/                configuration types, loading, and validation
internal/profile/               profiles, the match map, and the VM size
internal/job/                   the job identifier: a UUID that no other identifier can replace
internal/match/                 Matcher: job tags to one profile (pure)
internal/admission/             Admission: candidates, capacity, order (pure)
internal/reaper/                Reaper: the decision table (pure) and its executor
internal/vmspec/                the Orchard VM specification builder and the VM name
internal/buildkite/             the Stacks interface, its client, and its errors
internal/orchard/               the Orchard interface, this project's REST client (ADR-0010)
internal/inventory/             worker and VM inventory with freshness
internal/jobstates/             batched job states with observation times
internal/launcher/              reserve, issue tokens, create VMs, notify
internal/progress/              the steps of each VM: job page lines, span events, phase durations
internal/stack/                 stack registration, the queue poll, and the paused value
internal/controller/            the tick loop, which a test can run one tick at a time
internal/telemetry/             OTel meter, tracer, and the slog bridge
internal/health/                the gRPC health service: liveness and readiness
test/fakestacks/                the fake Stacks API server
test/component/                 T-020: in-memory fakes of both APIs and random event sequences
test/integration/               tests against orchard dev --synthetic, and the load test (tag load)
test/smoke/                     the kind smoke deployment: the fake Stacks server as a program, and the
                                Orchard and collector manifests
deploy/helm/                    the controller chart
deploy/orchard/                 the Orchard StatefulSet and its bootstrap job
deploy/hosts/                   Ansible for the Mac hosts
images/                         the builds of the job VM images (agent and pilot: tart and the guest agent)
tools/orchardgen/               the generator of the Orchard wire types (ADR-0015)
scripts/                        the gate scripts, and the smoke and release scripts
```

## Run the controller locally

This quick start runs the controller against a synthetic Orchard and the fake Stacks API. No VM
starts, and no Buildkite account is necessary. A person checked these commands on 2026-11-01, on
macOS on arm64, with Orchard 0.58.0.

The fake of `test/smoke/fakestacks` serves one stack and no jobs. The controller therefore
registers, becomes ready, and polls. To see a job go from `scheduled` to a deleted VM, run
`mise run integration`. Its fake gets the job from the test code.

1. Keep port 6120 free, and set a work directory:

   ```sh
   work=$(mktemp -d)
   ```

2. Build the controller and the fake Stacks API:

   ```sh
   mise x -- go build -o "$work/controller" ./cmd/bk-agent-stack-orchard
   mise x -- go build -o "$work/fakestacks" ./test/smoke/fakestacks
   ```

3. Write a minimal configuration. A profile names the tags that it matches, the image, and the
   size of the VM:

   ```sh
   cat > "$work/config.yaml" <<'EOF'
   controllerID: local
   healthAddress: 127.0.0.1:8080
   buildkite:
     queue: macos-local
     endpoint: http://127.0.0.1:8090
   orchard:
     url: http://127.0.0.1:6120
     serviceAccount: bk-agent-stack-orchard
   profiles:
     - name: xcode-27
       match: {xcode: "27"}
       image: ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0
       cpu: 8
       memoryMiB: 28672
       diskSizeGB: 150
   EOF
   ```

4. Start Orchard in synthetic mode with one worker. It writes a context into
   `$HOME/.orchard/orchard.yml`, so give it a home of its own. Wait until the worker reports:

   ```sh
   mkdir "$work/home"
   resources=org.cirruslabs.tart-vms=2,org.cirruslabs.logical-cores=16
   resources=$resources,org.cirruslabs.memory-mib=65536
   HOME="$work/home" "$(mise which orchard)" dev --synthetic --workers 1 \
     --data-dir "$work/home/data" --resources "$resources" > "$work/orchard.log" 2>&1 &
   sleep 5; curl -s http://127.0.0.1:6120/v1/workers
   ```

5. Start the fake Stacks API. `STACK_KEY` is the `controllerID` of the configuration:

   ```sh
   STACK_KEY=local LISTEN=127.0.0.1:8090 "$work/fakestacks" &
   ```

6. Start the controller. The two tokens are dummy values, because both fakes accept any token. The
   exporters are off, so the log goes to the standard output, and the command writes it to a
   file:

   ```sh
   BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN=bkct_local \
   BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN=local \
   OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none \
     "$work/controller" -config "$work/config.yaml" > "$work/controller.log" 2>&1 &
   ```

   After a few seconds, `cat "$work/controller.log"` shows a warning that the image has no digest,
   then "the controller registered its stack and starts its loop", and then "the controller is
   ready". The controller serves its gRPC probes on `127.0.0.1:8080`.

7. Stop each process with `kill %3 %2 %1`, which stops the controller first. Check that nothing
   holds port 6120 (`lsof -i :6120`). Delete the work directory.

## Change checklist: a new profile field

A profile field reaches the VM specification that the controller sends to Orchard. Do these steps
in order, because the documents decide the behaviour (CONTRIBUTING, section "Before code"):

1. Change `docs/product/requirements.md` when a pipeline author or an operator can see the new
   behaviour. Write an ADR when the change replaces an approach.
2. Change the profile and configuration sections of `docs/architecture/target-design.md`: the
   field, its default, its validation, and its place in the VM specification (sections 5 and 6.4).
3. Write the failing test first. Add the field to `internal/profile/profile.go` with its
   validation, and test the validation in `internal/profile`.
   A field that names an environment variable goes into `validateVMVariables`, so that it accepts
   only a name that starts with `VM_`. The controller pod holds other secrets.
4. Map the field to the specification in `internal/vmspec/vmspec.go`, and test it in
   `internal/vmspec/vmspec_test.go`.
5. Add the field to the specification test (T-064, `test/integration/specification_test.go`), so
   that Orchard returns it unchanged.
6. When the field goes into the Helm values, change `deploy/helm/bk-agent-stack-orchard` and its
   values schema, and run `mise run check-deploy`.
7. When the specification needs a field that the generated Orchard types lack, add the field to
   the allow list of its type in `tools/orchardgen/main.go`, run `mise run generate`, and commit
   the result (ADR-0015).
8. Run `mise run test`, `mise x -- golangci-lint run ./...`, `mise run lint-format`, and
   `mise run check-docs`.

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

`scripts/smoke.sh` deploys the chart and the Orchard base of `deploy/orchard` on a kind cluster.
The cluster also has the fake Stacks API and an OpenTelemetry collector. The script deploys them as
an operator does:

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
events. The events are:

* Jobs arrive and are cancelled.
* Agents take jobs.
* VMs fail.
* Workers stop and return.
* The queue pauses.
* Time passes.
* The controller restarts.

After each event the suite checks the invariants I-1, I-2, I-5, and I-8. At the end of each
sequence it checks I-6.

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

The load test (T-080) runs the controller at the target scale. It uses `orchard dev` with 50
synthetic workers of two slots each. It sends 500 jobs in the pattern of the demand model of the
system design baseline, section 4.2. The demand model runs fifteen times faster than a workday, so
the test takes about 25 minutes. The test is also the agents. It takes each job whose VM runs, and
ends the job after its duration.

```sh
mise run load
```

The test reports four values in its log and in the summary of the `load` workflow. They are the tick
duration, the busiest second of each Stacks API endpoint, the most VMs at one time, and the wait for
a VM. It fails on a broken invariant (I-1, I-2, I-6) or on a second above the limits of the Stacks
API. It judges the tick only when `LOAD_TICK_BUDGET` parses as a duration. The repository variable
is `34ms`, two times the hosted p95 of 17 ms (open item O-5, closed on 2026-11-02).

`LOAD_JOBS` runs fewer jobs, for example `LOAD_JOBS=60`, while a developer works on the test. The
test writes a progress line each minute. On a failure it shows the end of the controller log.

Keep the host awake for the whole run. A Mac that sleeps pauses the run, and the synthetic workers
go offline. The result is then not a measurement. Run the test with `caffeinate -dims` and the lid
open.

The `load` workflow runs it each night, on demand, and on a pull request that changes it, on the
same runner as the integration tests, a hosted macOS runner. No branch protection requires it. When
a scheduled or manual run fails, the workflow opens an issue titled "Nightly load test failed", or
comments on the issue that is open.

## The fuzz tests

Each fuzz test states a property that must hold for any input, at a place where input comes from
outside the controller:

| Fuzz test | Input | Property |
|---|---|---|
| `FuzzTagsGiveOnlyLiteralRules` | The agent rules of a job, which pipeline authors write | An accepted rule is a literal `key=value` that a rule names and that has no comma, newline, or quote, and a match is an equal match map |
| `FuzzATagReachesTheEnvironmentAndNeverTheScript` | A tag of a job, which pipeline authors write | The tag reaches `BUILDKITE_AGENT_TAGS` in the VM environment and never the startup script |
| `FuzzOwnedJobClaimsOnlyTheMachinesOfThisController` | A machine name in Orchard, which any tool can set | The controller claims only the exact name that it gives to a valid job |
| `FuzzOwnedJobTellsControllersApart` | Two controller identifiers | A controller never claims a machine of another controller |
| `FuzzParseIDAcceptsOnlyTheBuildkiteShape` | A job identifier from Buildkite | Only the lowercase UUID shape passes, unchanged |
| `FuzzLoadGivesAnErrorOrAValidConfiguration` | The configuration file | Load gives an error or a valid configuration, and never panics |

`go test ./...` runs the seed inputs of each one. `mise run fuzz` fuzzes each target for
`FUZZTIME` (default `1m`). The `fuzz` workflow does this each night. When a target fails, go
writes the input to `testdata/fuzz/` in its package, and the workflow keeps that directory as an
artifact. Add the input to the seeds of the test, and correct the code, in one pull request.

A failed night opens an issue titled "Nightly fuzz run failed", or comments on the issue that is
open. The `fuzz` workflow also runs the long property run of the component suite each night, with
`-rapid.checks=20000 -rapid.steps=200`. It takes about 9 minutes on a development Mac (2026-11-02).
When it fails, the workflow keeps `test/component/testdata/rapid/` as an artifact. Replay a fail
file as the component suite section describes.

## The integration tests

The integration tests run against a real Orchard in synthetic mode, at the version that `mise.toml`
pins:

* The skeleton journey (T-060) runs the controller binary with the fake Stacks API server. One job
  goes from `scheduled` to a deleted VM. The job page shows each step. At the end, no VM of the
  controller remains (I-6).
* The admission journeys run the controller binary in the same way. In T-061, three jobs wait for
  a worker with two slots: the third job stays `scheduled` until a slot is free. In T-062, a job
  that no profile matches stays `scheduled` over 2 ticks, with one notification on its job page,
  and no VM starts.
* The restart journey (T-021) kills the controller with SIGKILL and starts it again, at each crash
  window of the target design, section 7.4. Each job keeps at most one VM. The VM of a job that
  ended while no controller ran goes. The first pass extends every reservation.
* The readiness journey (T-063) revokes the agent token while the controller runs. The gRPC
  `readiness` service then stops serving, the log names the token, and `liveness` serves. Each
  controller of the integration tests serves its probes on a free port of its own.
* The interruption journeys cover two cases. When the queue is paused (T-022), no VM starts, and
  the controller does not reserve or finish a job. The VMs in progress continue. When a job is
  cancelled while its VM boots (T-023), the controller deletes the VM and does not take the job
  again.
* The Orchard client test (T-041) sends each call of the client of this project to the real API.
  The calls are create, a second create of the same name, list, events, and delete. A synthetic
  worker writes each line of the startup script as an event. The test therefore also proves the
  events of a VM.
* The specification test (T-064) sends the VM specification of each startup transport, with every
  optional field of a profile set. It then reads the stored VM back. Orchard ignores a field that
  it does not know. The test therefore checks that every field of the specification comes back with
  the same value.

To probe a controller by hand, use a gRPC health client, for example
[`grpc-health-probe`](https://github.com/grpc-ecosystem/grpc-health-probe):

```sh
grpc-health-probe -addr=localhost:8080 -service=readiness
```

```sh
mise run integration
```

Run the tests on macOS on arm64. Orchard places a VM only on a worker of the same architecture and
runtime. A macOS VM is arm64 and runs on Tart. A synthetic worker takes its architecture from the
host and its runtime from the host OS: Tart on macOS, and Vetu on Linux. On any other host each
test stops at once and gives this reason.

These conditions also apply:

* Keep port 6120 free, because Orchard fixes the port of `orchard dev`. A test stops with a
  message when something already answers there. The tests therefore run one after the other.
* Each test gives `orchard dev` a home directory of its own, because `orchard dev` writes a context
  into `$HOME/.orchard/orchard.yml`. The Orchard configuration of the developer does not change.
* Without `mise`, `ORCHARD_BIN` can name an Orchard binary. Use the version that `mise.toml` pins.
* The macOS binary of Orchard 0.56.0 and later fails its own signature check
  ([openai/orchard#456](https://github.com/openai/orchard/issues/456), checked 2026-10-31). macOS 26
  kills the binary at launch, before it writes any output. Each test then waits for workers that
  never report. The rest of its signature is intact. On each `mise install`, the `postinstall` of
  the Orchard entry in `mise.toml` does these steps:
  1. It keeps the release binary as `orchard.release`.
  2. It verifies the binary against a correct Developer ID requirement for the team of the
     release. If this check fails, the install fails.
  3. Only then does it make `orchard`, an ad hoc signed copy of the verified file.

  mise checks the SHA-256 of the archive first. An install that an earlier version of this hook
  signed in place fails the check. In this case, run `mise install --force github:openai/orchard`.

## The CI runner of the integration tests

CI runs the integration tests on macOS on arm64 for the same reason. The repository is public, so
the job runs on a hosted macOS runner (`macos-latest`), which costs nothing. The workflows fix the
runner. A self-hosted runner MUST NOT serve a public repository, because a pull request from a
fork can run code on it. The sections below are for a developer who runs the Tart tests on a Mac.

### Install Tart

Tart, Softnet, and Orchard come from the `openai/tools` Homebrew tap
([openai/homebrew-tools](https://github.com/openai/homebrew-tools#tart-related-projects)). The
`cirruslabs/cli` tap is no longer where they are published. Homebrew 7 loads a formula from a tap
that is not official only after the tap is trusted:

```sh
brew trust openai/tools
brew install openai/tools/tart openai/tools/softnet
```

Homebrew installs `softnet` without the setuid bit. The first time that a VM uses Softnet, Tart
asks for a sudo password on a terminal to set the bit. Set the bit once after each install or
upgrade of Softnet, because an upgrade replaces the file:

```sh
softnet_bin=$(realpath "$(brew --prefix)/bin/softnet")
sudo chown root "$softnet_bin" && sudo chmod u+s "$softnet_bin"
```

The Tart test below needs Tart and Softnet. Install Tart at version
2.41.1. The pin is `orchard_worker_tart_version` in
`deploy/hosts/roles/orchard_worker/defaults/main.yml` (checked 2026-11-01), and the Mac hosts use
the same version. The minimum version is 2.39.0 (target design, section 9.2). Install the release
archive of the pinned version, and check it against the SHA-256 in the same file
(`orchard_worker_tart_sha256`). A Homebrew formula can replace the archive when it gives the same
version. Then check the version:

```sh
tart --version
```

Orchard comes from `mise install` (the version of `mise.toml`), so it needs no formula here.

### The Tart test

`mise run tart-test` runs one job on a real Tart VM of this Mac: the controller, `orchard dev` with
a real worker, and the agent image. It is the evidence that the Tart guest agent runs the startup
script (test strategy, open item O-2). Build the image with `mise run agent-image`, and name it:

```sh
BKASO_TART_IMAGE=bkaso-agent mise run tart-test
```

The VM takes one of the two VM slots of the Mac. CI does not run this test, because a VM cannot
run a macOS VM.

`mise run failures-test` runs three jobs that cannot start on real VMs. The jobs have a profile
image digest that does not exist, an agent that exits, and an agent that runs and acquires nothing.
The test checks what each job page says, and that no VM remains. It takes about five minutes.

`mise run restart-test` crashes the controller while the agent of a job runs in a real VM. The VM
must continue. The next process must take it over without a second VM, and it must delete the VM
when the job ends.

`mise run softnet-test` probes the network from a VM of the controller specification. The VM must
reach the Internet and DNS. It must not reach the LAN, another VM, or a service of the Mac itself.
Softnet alone lets a VM reach the Mac through the gateway of its network, and the hosts close this
path with pf (`deploy/hosts`). `deploy/hosts/test/pf.sh` loads the same rules on the Mac from the
template of the role. It needs `ansible` (from `mise install`) and sudo. The control of the test
permits the router of the LAN, so the rules permit it too. While the rules are loaded, the Mac
accepts connections only on the interface of its default route and on loopback. From any other
interface, AirDrop and VPN tunnels included, it accepts only DNS and DHCP. The rules go when the
Mac restarts, or with `pf.sh unload`:

```sh
deploy/hosts/test/pf.sh load "$(route -n get default | awk '/gateway:/ {print $2}')/32"
BKASO_TART_IMAGE=bkaso-agent mise run softnet-test
deploy/hosts/test/pf.sh unload
```

`mise run hosts-test` needs the opposite: its test VM reaches an Orchard controller on the Mac,
which the rules block, so remove them before it.
