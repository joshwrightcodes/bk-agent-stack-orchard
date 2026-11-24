---
status: proposed
date: 2026-11-24
decision-makers: Josh Wright
---

# ADR-0022: Authenticate the checkout with files of a read-only mount

## Summary

Pipelines on the fleet check out private repositories over SSH, and today the key is in the image
or in a Buildkite secret of each pipeline (issue #256, 2026-11-24).
[ADR-0021](0021-mount-operator-files-read-only-in-job-vms.md) lets a profile mount host
directories read-only, and leaves checkout credentials to a separate decision. The decision is
that a profile MAY name a mount whose SSH files the startup script copies into `~/.ssh`, and a
mount whose `.git-credentials` file a read-only Git credential helper reads. For GitHub.com, the
checkout tokens that Buildkite issues for each job stay the recommended choice. This ADR amends
[SE-02](../architecture/system-design-baseline.md#se-02) and [PR-030](../product/requirements.md#pr-030), which allow only a job acquisition token in a VM. The cost is a
long-lived checkout credential that every job of the profile can read and copy.

## Context

[ADR-0004](0004-treat-all-builds-as-untrusted.md) has the goal that a build gets no credential with
a longer life or a larger scope than its job ([SE-02](../architecture/system-design-baseline.md#se-02)), and its decision forbids a Buildkite or an
orchestrator credential in a VM, except the credential of its own job. A Git checkout credential
is neither, but [SE-02](../architecture/system-design-baseline.md#se-02) and [PR-030](../product/requirements.md#pr-030) say that the VM receives only a job acquisition
token. A key in the image breaks that rule already, with no record of it.

Buildkite can issue the agent a token for each job that reads the contents of one GitHub
repository and expires after one hour. The queue turns it on, and it needs the GitHub App of
Buildkite with full access and agent v3.63.1 or later. The agent then rewrites the SSH URLs of
GitHub.com to HTTPS. GitHub Enterprise Server needs an HTTPS repository URL, and the tokens do not
cover other Git hosts
([Self-hosted agent code access](https://buildkite.com/docs/agent/self-hosted/code-access), read on
2026-11-24). On this fleet, a job checked out with these tokens once the startup script removed
the extra Git credential helpers of the image ([ADR-0017](0017-download-the-agent-when-the-vm-starts.md), evidence of 2026-11-03).

`buildkite-agent` v4.10.2 sets its own helper with `git config --global credential.helper` only
when these tokens are on for the job (`internal/job/executor.go`,
`configureRepositoryProviderGitCredentials`). With one value already set, that command replaces
the value. The agent also reads `BUILDKITE_GIT_SSH_KEY`: it writes the key to a new file with mode
`0600` for the default checkout only, and removes the file after the checkout
(`internal/job/checkout_ssh.go`, `prepareGitSSHKey`, tag v4.10.2, read on 2026-11-24).

The Buildkite stack for Kubernetes solves the same need in two ways (`buildkite/agent-stack-k8s`
v0.53.1, read on 2026-11-24):

* `gitCredentialsSecret` mounts a `.git-credentials` file read-only into the checkout container,
  and sets a helper that only answers `get` from it. The `store` helper writes back to its file,
  and a read-only file refuses the write (`internal/controller/scheduler/scheduler.go`).
* An environment variable such as `SSH_PRIVATE_ED25519_KEY`, `SSH_KNOWN_HOSTS`, or `SSH_CONFIG`, or
  its `_PATH` form, reaches `ssh-env-config.sh` of `buildkite/docker-ssh-env-config`. The script
  copies each file into `~/.ssh`, with mode `0700` for the directory and `0600` for each file.

The stack for Kubernetes gives the credentials to the checkout container only. A job VM of this
project runs every step of a job as one user, so it has no such limit.

`ssh` refuses a private key that a user other than its owner can read. The owner and modes of the
files of a mount in a macOS guest are `UNKNOWN` ([ADR-0021](0021-mount-operator-files-read-only-in-job-vms.md)).

## Goals and non-goals

Goals:

* **Any Git host.** A profile can check out from a host that the tokens of Buildkite do not cover.
* **No secret in the VM specification.** The controller, Orchard, and the specification of a VM
  hold no checkout credential.
* **A key changes without a new image.** The operator replaces a file on the hosts.
* **`ssh` accepts the files.** The result does not depend on how a mount reports its modes.
* **The smallest exposure.** A credential reaches as little as possible, for as short a time as
  possible ([SE-02](../architecture/system-design-baseline.md#se-02)).

Non-goals:

* A credential for each pipeline or for each job. Every job of the profile gets the same files.
* A choice of credential by the pipeline. The profile decides, as it does for `softnetAllow`.
* Secrets for steps other than the checkout. Jobs get those through standard Buildkite mechanisms.

## Options considered

**The tokens of Buildkite only.** Short-lived, one repository, read-only, and no file on the host.
However, they cover GitHub only, they need the GitHub App with full access, and a key for another
Git host still has to be in the image.

**A key in the image (the current state).** No new mechanism. However, a change of the key needs a
new image, and the image carries a credential that no document records.

**The files of the mount, used in place.** A profile sets `GIT_SSH_COMMAND` to read the SSH
configuration and the key of the mount. No copy is necessary. However, `ssh` refuses the key if
the mount reports modes that are too open, and the modes are `UNKNOWN`.

**The files of the mount, copied into `~/.ssh` at the start.** The startup script copies the SSH
files with the modes that `ssh` needs, as the stack for Kubernetes does, and a read-only helper
reads `.git-credentials` of the mount. The VM is deleted after its job, so the copy lasts no longer
than the job. However, the copy is readable by every step of the job, and not only by the
checkout.

**The key in `BUILDKITE_GIT_SSH_KEY`.** The agent writes the key for the checkout only, and removes
it after. This is the smallest exposure in the VM. However, the key is then in the environment of
the controller, in the VM specification, and in the store of Orchard, which every account with
`compute:read` can read.

| Goal | Tokens only | Image | Mount in place | Mount, copied | `BUILDKITE_GIT_SSH_KEY` |
|---|---|---|---|---|---|
| Any Git host | No | Yes | Yes | Yes | Yes |
| No secret in the VM specification | Yes | Yes | Yes | Yes | No |
| A key changes without a new image | Yes | No | Yes | Yes | Yes |
| `ssh` accepts the files | Yes | Yes | `UNKNOWN` | Yes | Yes |
| The smallest exposure | Yes | No | No | No | Partly: the checkout only, but in Orchard |

## Decision

Copy the SSH files of a named mount into `~/.ssh`, and read `.git-credentials` of a named mount
through a read-only helper. Prefer the tokens of Buildkite for GitHub.com.

* A profile MAY set `checkout.sshFrom` to the `name` of one of its `hostDirs`. Before the agent
  starts, the startup script MUST copy the files `config`, `known_hosts`, `id_ed25519`, `id_ecdsa`,
  and `id_rsa` of that mount, each one that exists, into `~/.ssh`, with mode `0700` for the
  directory and `0600` for each file. It MUST copy no other file. A copied file replaces the file
  of the image with the same name. The script MUST write the names of the files that it copied,
  and never their contents. If the mount holds none of the files, or a copy fails, the script
  MUST stop before the agent starts, with a message that names the mount.
* A profile MAY set `checkout.gitCredentialsFrom` to the `name` of one of its `hostDirs`. Before
  the agent starts, the startup script MUST set a global Git credential helper that answers only
  `get`, from the file `.git-credentials` of that mount. If the file does not exist, the script
  MUST stop before the agent starts, with a message that names the mount.
* The controller MUST refuse at start a `checkout` field that names no `hostDirs` entry of the
  same profile.
* The operator MUST NOT turn on the tokens of Buildkite for a queue whose profiles set
  `checkout.gitCredentialsFrom`, because the agent then replaces the helper. The controller cannot
  see the setting of the queue, so the documents state this rule.
* For a repository on GitHub.com, the operator SHOULD use the tokens of Buildkite rather than a
  file of a mount.
* A key of a mount SHOULD be a read-only deploy key, or the key of an account that reads only the
  repositories of the queue.
* The controller MUST write a warning at start that names each profile with a `checkout` field,
  and says that every job on the queue that selects the profile can read the credentials.

This decision amends [SE-02](../architecture/system-design-baseline.md#se-02) and [PR-030](../product/requirements.md#pr-030): the only Buildkite credential of a VM is the job
acquisition token of its job, and a profile MAY give the VM checkout credentials from a mount. It
changes no rule of [ADR-0004](0004-treat-all-builds-as-untrusted.md), whose decision forbids only Buildkite and orchestrator credentials.

## Consequences

Good:

* A profile can check out from any Git host, with no key in the image.
* The operator replaces a key on the hosts, and the next job uses it.
* No checkout credential passes through the controller or Orchard.
* `ssh` gets its files with the modes that it needs, whatever the mount reports.

Bad:

* Every step of every job that selects the profile can read and copy the credential. A malicious
  build keeps a copy after its job, so a leak lasts until the operator replaces the key.
* The credential lives longer than the job, which [SE-02](../architecture/system-design-baseline.md#se-02) wanted to avoid.
* `checkout.gitCredentialsFrom` and the tokens of Buildkite exclude each other on one queue, and
  only the documents enforce it.

Review trigger:

* Review this decision if the tokens of Buildkite cover the Git hosts of the fleet.
* Review it if the agent can read a key from a file for the checkout only, as it does for
  `BUILDKITE_GIT_SSH_KEY`.

## Confirmation

* Configuration tests confirm the refusal of a `checkout` field that names no `hostDirs` entry of
  its profile, and the warning.
* Script tests confirm that the script copies only the named files, with modes `0700` and `0600`,
  writes their names and not their contents, and stops before the agent starts when the mount holds
  none of them or `.git-credentials` is missing.
* Script tests confirm that the helper answers `get` from the file and ignores `store` and `erase`.
* A test on a real Tart VM confirms a checkout of a private repository over SSH with the files of
  a mount.
