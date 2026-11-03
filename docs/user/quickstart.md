# Quick start

This guide runs one real Buildkite job in a new Tart VM on your Mac, and deletes the VM when the job
ends. It uses your own Buildkite account and a real Orchard on your Mac. It takes about 15 minutes,
and most of that is the first download of the macOS image.

The quick start is for a trial. To run a fleet, read [Install the controller](install.md).

## Before you start

You need:

* **A Mac with Apple silicon**, with about 60 GB of free disk space. The Mac is the host of the
  VMs. Apple permits at most two macOS VMs on one Mac at a time.
* **Tart and Softnet.** Install them from Homebrew, then give Softnet the set-user-ID bit, so that
  a VM can start without a password prompt:

  ```sh
  brew trust openai/tools
  brew install openai/tools/tart openai/tools/softnet
  softnet_bin=$(realpath "$(brew --prefix)/bin/softnet")
  sudo chown root "$softnet_bin" && sudo chmod u+s "$softnet_bin"
  ```

  Set the bit again after each upgrade of Softnet.
* **[mise](https://mise.jdx.dev)**. It installs Orchard, cosign, and jq at the versions of this
  repository.
* **A Buildkite cluster.** In Buildkite:
  1. Open a cluster, or create one.
  2. Create a **self-hosted** queue, for example `macos-quickstart`. Use a queue that no other
     agent serves.
  3. Create a cluster agent token for the cluster.
  4. Create a pipeline in the cluster. You add its step later.

## Run it

```sh
git clone https://github.com/joshwrightcodes/bk-agent-stack-orchard
cd bk-agent-stack-orchard
mise install
export BUILDKITE_AGENT_TOKEN=...   # the cluster agent token, from your secret store
mise run quickstart -- --queue macos-quickstart
```

The command:

1. Checks the Mac, Tart, Softnet, and the token, and stops with a message if one is missing.
2. Pulls the image `ghcr.io/cirruslabs/macos-golden-gate-base` into the Tart cache. It pulls the
   image by its digest, which [Options](#options) shows, so the image cannot change under you.
3. Starts a local Orchard controller and one Orchard worker, which uses the Tart of your Mac.
4. Downloads the newest release of the controller for macOS. It checks that the release was
   built from a commit on the `main` branch of this repository. It checks the signature of the
   release, which must come from the release workflow of this repository for that commit, and the
   checksum of the archive. If the GitHub CLI (`gh`) is installed and logged in, it also checks the
   provenance attestation of the release, from the same workflow and commit. It stops if one of
   these checks fails.
5. Starts the controller, with one profile for the queue.
6. Prints a pipeline step, then the log of the controller.

## Run a job

Paste the step that the command prints into your pipeline, and run a build:

```yaml
steps:
  - label: ":apple: Hello from a Tart VM"
    command: sw_vers && hostname && echo "this VM is deleted when the job ends"
    agents: {queue: "macos-quickstart"}
```

In the log of the controller, you see the job get a VM, and the VM go when the job ends:

```text
11:32:55  the controller is ready
11:35:01  the controller asked for a machine  (job=01a19ecc-9730-4bfe-9433-5f5b466de535)
11:35:40  the reaper acted on a machine  (job=01a19ecc-9730-4bfe-9433-5f5b466de535, reason=job_terminal)
```

In Buildkite, the agent of the job has the name of the VM as its host name. The VM downloads
`buildkite-agent` when it starts, so the image needs no agent of its own.

## Stop it

Press Ctrl-C. The command deletes each VM that it started, stops the controller and Orchard, and
deletes its work directory. The image stays in the Tart cache. To delete it:

```sh
tart delete ghcr.io/cirruslabs/macos-golden-gate-base@sha256:14f4fc32a778763ccae1a4d543ea0f177720c570dd7f859eda10b9e0020c7d05
```

## Options

| Option | Default | What it does |
|---|---|---|
| `--queue NAME` | (required) | The self-hosted queue of the token |
| `--image REF` | `ghcr.io/cirruslabs/macos-golden-gate-base@sha256:14f4fc32a778763ccae1a4d543ea0f177720c570dd7f859eda10b9e0020c7d05` | The Tart image of the VMs. It must run the Tart guest agent. The default is one fixed version of the upstream image, pinned by its digest |
| `--cpu N` | `4` | The CPU cores of each VM |
| `--memory MIB` | `8192` | The memory of each VM, in MiB |
| `--disk GB` | `50` | The disk of each VM, in GB. It must be at least the disk of the image |
| `--match KEY=VALUE` | none | A tag that a job must have besides the queue. Repeat it for more tags |
| `--orchard-url URL` | a local Orchard | An Orchard that you run. Put its service account token in `BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN`, and a private CA in `BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT` |
| `--service-account NAME` | `bk-agent-stack-orchard` | The service account of `--orchard-url` |
| `--version vX.Y.Z` | the newest published release | The release of the controller |
| `--from-source` | off | Build the controller from your clone instead of a release. It needs Go, which `mise install` gives |

## When something goes wrong

| What you see | What to do |
|---|---|
| `has published no release yet` | No release of the controller exists yet. Use `--from-source`. |
| `the signature of checksums.txt … is not one of the release workflow` | The download is not a release of this project. Do not use it. Report it as [SECURITY.md](../../SECURITY.md) says. |
| `has no valid provenance attestation` | The provenance of the download is not from the release workflow of this project. Do not use it. Report it as [SECURITY.md](../../SECURITY.md) says. |
| `something already answers on port 6120` | Another Orchard runs on this Mac. Stop it, or give its address with `--orchard-url`. |
| `Softnet needs the set-user-ID bit` | Run the two commands that the message shows. |
| The controller stops with `buildkite did not accept the agent token` | The token is not a cluster agent token. Create one for the cluster of the queue. |
| The job stays `scheduled`, and the log says `the controller skips a job that no profile carries` | The tags of the job must be the queue and the `--match` tags, and no others. |
| The job fails with `the image has "buildkite-agent version 3…"` | The profile uses the agent of the image. The quick start downloads the agent, so this happens only with your own configuration. |

## Next steps

* [How it works](how-it-works.md): the life of a job, profiles, and capacity.
* [Install the controller](install.md) for a fleet: your Orchard, your Mac hosts, and your images.
* [Troubleshooting](troubleshooting.md) when a job waits or fails.
* [How the agent gets into each VM](install.md#the-agent-in-each-vm), and how to use a mirror.
