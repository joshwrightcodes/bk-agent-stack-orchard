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
[SE-02](../architecture/system-design-baseline.md#se-02), [PR-030](../product/requirements.md#pr-030), and a goal and a consequence of [ADR-0004](0004-treat-all-builds-as-untrusted.md), which allow only
a job acquisition token in a VM. The cost is a long-lived checkout credential that every pipeline
on the queue can read and copy, and that every Orchard account with `compute:write` can reach.

## Context

[ADR-0004](0004-treat-all-builds-as-untrusted.md) has the goal that a build gets no credential with
a longer life or a larger scope than its job ([SE-02](../architecture/system-design-baseline.md#se-02)), and its decision forbids a Buildkite or an
orchestrator credential in a VM, except the credential of its own job. A Git checkout credential
is neither, but the goal, [SE-02](../architecture/system-design-baseline.md#se-02), and [PR-030](../product/requirements.md#pr-030) say that the VM receives only a job acquisition
token. A key in the image breaks that rule already, with no record of it.

The pipeline author selects the profile with the agent tags, and the controller does not check
who the author is (target design §5 and §9.1). A credential of a profile therefore belongs to every
pipeline on the queue, including a build of a pull request whose pipeline file comes from the
branch.

Orchard ties a host directory to a path policy, not to an account, a profile, or a worker. Every
account with `compute:write` can create a VM that mounts a path that a policy allows, and read
what its startup script writes in the events of the VM with `compute:read` (Orchard 0.58.0,
`internal/controller/api_vms.go`, read on 2026-11-24). In this deployment that is the controller,
each `worker-<host>` account, and `operator` (target design §9.2). Several controllers on one
Orchard share the policies, so the controller of one queue can mount the directory of another.

Buildkite can issue the agent a token for each job that reads the contents of a GitHub repository
and expires after one hour. The queue turns it on, and it needs the GitHub App of Buildkite with
full access and agent v3.63.1 or later. The agent then rewrites the SSH URLs of GitHub.com to
HTTPS. GitHub Enterprise Server needs an HTTPS repository URL, and the tokens do not cover other
Git hosts ([Self-hosted agent code access](https://buildkite.com/docs/agent/self-hosted/code-access),
read on 2026-11-24). The agent asks for a token for each URL that git asks about
(`credential.useHttpPath`), so a job can get a token for any repository that Buildkite permits, not
only its own. On this fleet, a job checked out with these tokens once the startup script removed
the extra Git credential helpers of the image ([ADR-0017](0017-download-the-agent-when-the-vm-starts.md), evidence of 2026-11-03).

`buildkite-agent` v4.10.2 does these things (tag v4.10.2, read on 2026-11-24):

* It sets its own helper with `git config --global credential.helper` when the tokens are on for
  the job (`internal/job/executor.go`, `configureRepositoryProviderGitCredentials`). With one value
  already set, the command replaces it. With two, it fails. On a host that the tokens do not cover,
  its helper answers a credential that fails (`clicommand/git_credentials_helper.go`).
* With `ssh-keyscan` on, which is the default, it adds `-o StrictHostKeyChecking=accept-new` to
  `GIT_SSH_COMMAND` (`internal/job/ssh_host_key_checking.go`). The option on the command line wins
  over a `StrictHostKeyChecking yes` of `~/.ssh/config`, so `ssh` trusts a host that is not in
  `known_hosts` the first time. `BUILDKITE_NO_SSH_KEYSCAN=true` turns this off.
* It reads `BUILDKITE_GIT_SSH_KEY` from its environment, writes the key to a new file with mode
  `0600` for the default checkout, and removes the file after the checkout
  (`internal/job/checkout_ssh.go`, `prepareGitSSHKey`). The variable stays in the environment of
  the job, so every step can read it.

Git asks each helper in its list in order, the helpers of the system configuration first, and
calls `store` on each after a fetch that works. An empty value of `credential.helper` clears the
list up to that point (git-config, `credential.helper`). A helper of the system, such as
`osxkeychain`, would otherwise answer first, and keep the credential in the keychain of the VM.

The Buildkite stack for Kubernetes solves the same need in two ways (`buildkite/agent-stack-k8s`
v0.53.1, read on 2026-11-24):

* `gitCredentialsSecret` mounts a `.git-credentials` file read-only into the checkout container,
  and sets a helper that only answers `get` from it. The `store` helper writes back to its file,
  and a read-only file refuses the write (`internal/controller/scheduler/scheduler.go`).
* An environment variable such as `SSH_PRIVATE_ED25519_KEY`, `SSH_KNOWN_HOSTS`, or `SSH_CONFIG`, or
  its `_PATH` form, reaches `ssh-env-config.sh` of `buildkite/docker-ssh-env-config`. The script
  copies each file into `~/.ssh`, with mode `0700` for the directory and `0600` for each file. It
  does not replace a file that exists.

The stack for Kubernetes gives the credentials to the checkout container only. A job VM of this
project runs every step of a job as one user, with passwordless `sudo`, so it has no such limit.

`ssh` refuses a private key that a user other than its owner can read. The owner and modes of the
files of a mount in a macOS guest are `UNKNOWN` ([ADR-0021](0021-mount-operator-files-read-only-in-job-vms.md)).

## Goals and non-goals

Goals:

* **Any Git host.** A profile can check out from a host that the tokens of Buildkite do not cover.
* **No secret in the VM specification.** The controller, the specification of a VM, and the store
  of Orchard hold no checkout credential.
* **A key changes without a new image.** The operator replaces a file on the hosts.
* **`ssh` accepts the files.** The result does not depend on how a mount reports its modes.
* **`known_hosts` holds.** `ssh` connects only to a host whose key the operator listed.
* **The smallest exposure.** A credential reaches as little as possible, for as short a time as
  possible ([SE-02](../architecture/system-design-baseline.md#se-02)).

Non-goals:

* A credential for each pipeline or for each job. Every job of the profile gets the same files.
* A choice of credential by the pipeline. The profile decides, as it does for `softnetAllow`.
* Secrets for steps other than the checkout. Jobs get those through standard Buildkite mechanisms.
* Isolation between queues that share one Orchard. The owner accepts the risk (2026-11-24).

## Options considered

**The tokens of Buildkite only.** Short-lived, read-only, and no file on the host. However, they
cover GitHub only, they need the GitHub App with full access, and a key for another Git host still
has to be in the image.

**A key in the image (the current state).** No new mechanism. However, a change of the key needs a
new image, and the image carries a credential that no document records.

**The files of the mount, used in place.** A profile sets `GIT_SSH_COMMAND` to read the SSH
configuration and the key of the mount. No copy is necessary. However, `ssh` refuses the key if
the mount reports modes that are too open, and the modes are `UNKNOWN`.

**The files of the mount, copied into `~/.ssh` at the start.** The startup script copies the SSH
files with the modes that `ssh` needs, and a read-only helper reads `.git-credentials` of the
mount. The VM is deleted after its job, so the copy lasts no longer than the job. However, every
step of the job can read the copy, and every Orchard account with `compute:write` can reach the
directory through a VM of its own.

**The key in `BUILDKITE_GIT_SSH_KEY`.** The agent writes the key to a file for the checkout only.
However, the variable stays in the environment of every step, and the key is then also in the
environment of the controller, in each VM specification, and in the store of Orchard, which every
account with `compute:read` reads.

| Goal | Tokens only | Image | Mount in place | Mount, copied | `BUILDKITE_GIT_SSH_KEY` |
|---|---|---|---|---|---|
| Any Git host | No | Yes | Yes | Yes | Yes |
| No secret in the VM specification | Yes | Yes | Yes | Yes | No |
| A key changes without a new image | Yes | No | Yes | Yes | Yes |
| `ssh` accepts the files | Yes | Yes | `UNKNOWN` | Yes | Yes |
| `known_hosts` holds | — | With `no-ssh-keyscan` | With `no-ssh-keyscan` | Yes, the script turns off the keyscan | With `no-ssh-keyscan` |
| The smallest exposure | Yes | No | No | No | No |

"—" shows that the goal does not apply to the option.

## Decision

Copy the SSH files of a named mount into `~/.ssh`, and read `.git-credentials` of a named mount
through a read-only helper. Prefer the tokens of Buildkite for GitHub.com.

* A profile MAY set `checkout.sshFrom` to the `name` of one of its `hostDirs`. Before the agent
  starts, the startup script MUST copy the files `config`, `known_hosts`, `id_ed25519`, `id_ecdsa`,
  and `id_rsa` of that mount, each one that is a regular file and not a symlink, into `~/.ssh`. It
  MUST copy no other file. The directory MUST have mode `0700`. Before each copy the script MUST
  remove the file of the same name, and it MUST write each file with mode `0600`, so that no file
  is ever more open and no copy writes through a symlink of the image. A copied file replaces the
  file of the image, which the stack for Kubernetes does not do. The script MUST write the names of
  the files that it copied to the event log, and never their contents. If the mount holds none of
  the files, or a copy fails, the script MUST stop before the agent starts, with a message that
  names the mount.
* With `checkout.sshFrom`, the script MUST export `BUILDKITE_NO_SSH_KEYSCAN=true`. The agent then
  adds `-o StrictHostKeyChecking=yes` to `GIT_SSH_COMMAND` instead of `accept-new`
  (`internal/job/executor.go`, `configureSSHKeyChecking`, v4.10.2, read on 2026-11-26). An option
  on the command line wins over `~/.ssh/config`, so a copied `config` cannot turn the check off.
  The mount MUST hold `known_hosts`, and the script MUST stop before the agent starts when it does
  not, so that `ssh` trusts only the hosts that the operator listed.
* A profile MAY set `checkout.gitCredentialsFrom` to the `name` of one of its `hostDirs`. Before
  the agent starts, the startup script MUST set the global `credential.useHttpPath` to `true`, and
  MUST add two values to the global `credential.helper`: an empty value, which clears the helpers
  of the system, and then this helper, with the name of the mount in place of `<name>`:

  ```text
  !f() { test "$1" = get || exit 0; exec git credential-store --file '/Volumes/My Shared Files/<name>/.git-credentials' get; }; f
  ```

  With `useHttpPath`, git sends the path of the repository. An entry of `.git-credentials` with a
  path then answers for that repository only, and an entry without a path answers for every
  repository of its host. Without it, git sends no path, and an entry with a path never answers
  (git-credentials, `credential.useHttpPath`). The helper answers `get` only, so git never writes
  to the read-only file or to the keychain. The
  name is shell text in this helper, which is safe only because a name matches `^[a-z0-9-]+$`. If
  the file does not exist, the script MUST stop before the agent starts, with a message that names
  the mount. With two or more stored values, the agent fails to set its own helper, so a queue that
  turns on the tokens of Buildkite by mistake fails its jobs, and does not lose the helper silently.
* The controller MUST refuse at start a `checkout` field that names no `hostDirs` entry of the
  same profile. A `checkout` field is set only when its value is not empty.
* The operator MUST NOT turn on the tokens of Buildkite for a queue whose profiles set
  `checkout.gitCredentialsFrom`. One controller serves one queue, so a fleet that needs both runs a
  second controller and queue. A helper for one host (`credential.<url>.helper`) might let both
  share a queue; a test on a real VM decides it.
* For a repository on GitHub.com, the operator SHOULD use the tokens of Buildkite rather than a
  file of a mount.
* A credential of a mount MUST be read-only, and SHOULD reach only the repositories of the queue: a
  deploy key, or the key of an account that reads only those repositories.
* The controller MUST write a warning at start that names each profile with a `checkout` field,
  and says that every pipeline on the queue, whoever wrote it, can read and copy the credentials.
* The image SHOULD register the values of the credentials with `buildkite-agent redactor add` in a
  hook, so that a job log does not show them by mistake. This does not stop a job that reads the
  file and changes what it prints, so the startup script and the agent never write a credential,
  and a job can.

The owner accepts that every Orchard account with `compute:write`, including another controller of
the same Orchard, can reach the credentials of a mount (2026-11-24). `SECURITY.md` lists this risk.

This decision amends [SE-02](../architecture/system-design-baseline.md#se-02) and [PR-030](../product/requirements.md#pr-030): the only Buildkite credential of a VM is the job
acquisition token of its job, and a profile MAY give the VM a read-only checkout credential from a
mount. It also amends [ADR-0004](0004-treat-all-builds-as-untrusted.md) by partial replacement, as [ADR-0021](0021-mount-operator-files-read-only-in-job-vms.md) does:

* The goal "No long-lived credentials. A build gets no credential with a longer life or a larger
  scope than its job" gains "except a read-only checkout credential of a mount that its profile
  names".
* The consequence "No credential in a VM has a longer life or a larger scope than its job" gains
  the same exception.

The decision of [ADR-0004](0004-treat-all-builds-as-untrusted.md) does not change, because it
forbids only Buildkite and orchestrator credentials, so
[ADR-0004](0004-treat-all-builds-as-untrusted.md) keeps its status.

## Consequences

Good:

* A profile can check out from any Git host, with no key in the image.
* The operator replaces a key on the hosts, and the next job uses it.
* The controller, the VM specification, and the store of Orchard hold no checkout credential.
* `ssh` gets its files with the modes that it needs, whatever the mount reports, and keeps to the
  `known_hosts` of the operator.

Bad:

* Every step of every pipeline on the queue, whoever wrote it, can read and copy the credential. A
  malicious build keeps a copy after its job, so a leak lasts until the operator replaces the key.
* Every Orchard account with `compute:write`, and every other controller of the same Orchard, can
  reach the credential through a VM of its own. The owner accepts this risk.
* The credential lives longer than the job, which [SE-02](../architecture/system-design-baseline.md#se-02) wanted to avoid.
* `checkout.gitCredentialsFrom` and the tokens of Buildkite exclude each other on one queue, so a
  fleet that needs both runs two controllers.
* During a rotation, the hosts change one at a time, so the Git host accepts the old key and the new
  key until the last host has the new one.

Review trigger:

* Review this decision if the tokens of Buildkite cover the Git hosts of the fleet.
* Review it if Orchard ties a host directory to an account.

## Confirmation

* Configuration tests confirm the refusal of a `checkout` field that names no `hostDirs` entry of
  its profile, and the warning.
* Script tests, in `sh` and `zsh`, confirm that the script copies only the named regular files,
  with modes `0700` and `0600`, removes a symlink at the destination first, writes their names and
  not their contents, exports `BUILDKITE_NO_SSH_KEYSCAN=true`, and stops before the agent starts
  when the mount has no `known_hosts` or `.git-credentials` is missing.
* Script tests confirm that the stored helper list has the empty value before the helper of the
  mount, that no helper before the empty value answers, that `credential.useHttpPath` is `true`,
  that an entry with a path answers for its repository only and an entry without a path for each
  repository of its host, and that the helper ignores `store` and `erase`.
* A test on a real Tart VM confirms a checkout of a private repository over SSH, and one over HTTPS
  with `.git-credentials`, with the files of a mount.
