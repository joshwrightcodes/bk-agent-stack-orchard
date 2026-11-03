# Testing on real VMs

The tests in this guide start real Tart VMs on your Mac, through a real Orchard worker. Run them
when you change the startup script, the VM specification, the images, or the playbook of the Mac
hosts. Each VM takes one of the two VM slots that Apple permits on a Mac.

Set up your Mac as the [development guide](development.md) says first.

## Why CI cannot run these tests

CI runs the integration tests on macOS on arm64, because a synthetic worker takes its runtime
from the host OS. The repository is public, so the job runs on a hosted macOS runner
(`macos-latest`), which costs nothing. The workflows fix the runner. Never let a self-hosted
runner serve a public repository, because a pull request from a fork can run code on it.

A hosted runner cannot start a macOS VM inside its own VM, so the tests below run only on your
Mac.

## Install Tart

Tart and Softnet come from the `openai/tools` Homebrew tap
([openai/homebrew-tools](https://github.com/openai/homebrew-tools#tart-related-projects)), and the
[`Brewfile`](../Brewfile) installs them. The `cirruslabs/cli` tap is no longer where they are
published. Homebrew 7 loads a formula from a tap that is not official only after you trust the
tap, so run `brew trust openai/tools` before `brew bundle`.

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

## The Tart tests

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
template of the role. It needs `ansible` and `jq` (from `mise install`), and sudo. The control of
the test permits the router of the LAN, so the rules permit it too.

Softnet gives each VM a network from a few private ranges, and the gateway of that network is the
Mac. With `--pools`, the rules also permit those ranges, as a profile with a wide `softnetAllow`
would. The gateway of each VM of the test is then inside `softnetAllow`, and the test checks that
the VM still cannot reach a service of the Mac through it. The test reads the copy of the rules
that `pf.sh` keeps, and it fails when the rules do not permit the gateway.

While the rules are loaded, the Mac accepts connections only on the interface of its default route
and on loopback. From any other interface, AirDrop and VPN tunnels included, it accepts only DNS and
DHCP. The rules go when the Mac restarts, or with `pf.sh unload`:

```sh
deploy/hosts/test/pf.sh load --pools "$(route -n get default | awk '/gateway:/ {print $2}')/32"
BKASO_TART_IMAGE=bkaso-agent mise run softnet-test
deploy/hosts/test/pf.sh unload
```

`mise run hosts-test` needs the opposite: its test VM reaches an Orchard controller on the Mac,
which the rules block, so remove them before it. Besides the playbook, it disables pf and removes
the rules in its VM, and checks that the launchd job of the hosts restores them within a minute. It
also checks that only the worker user can run Softnet.

The test also checks the drain of a host before a change that restarts the worker. Its VM cannot
start a job VM, so the test suspends the worker process and gives Orchard a VM, which stays pending
on the worker, as the VM of a starting job does. A change of the labels then runs into the drain
time limit: the playbook must resume the worker, change nothing, and fail with the name of the
host. When the test removes the VM, the same change must pause the worker, restart it with the new
labels, and resume it. Last, a drain-only run must leave the worker paused, and the next run must
resume it. Run the test from the repository root:

```sh
deploy/hosts/test/pf.sh unload   # only when the rules of the hosts are loaded on this Mac
mise run hosts-test
```

Run it after a change of the role, the playbook, or the pinned versions of Orchard, Tart, or
Softnet. CI cannot run it, because it needs a Tart VM.
