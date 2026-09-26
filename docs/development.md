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

## The integration tests

The integration tests run against a real Orchard in synthetic mode, at the version that `mise.toml`
pins:

* The skeleton journey (T-060) runs the controller binary with the fake Stacks API server. One job
  goes from `scheduled` to a deleted VM, and at the end no VM of the controller remains (I-6).
* The Orchard client test (T-041) sends each call of the client of this project to the real API:
  create, a second create of the same name, list, events, and delete. A synthetic worker writes
  each line of the startup script as an event, so the test also proves the events of a VM.

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
