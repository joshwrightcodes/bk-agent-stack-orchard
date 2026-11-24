---
status: proposed
date: 2026-11-24
decision-makers: Josh Wright
---

# ADR-0021: Mount operator files read-only in job VMs

## Summary

Jobs on the fleet need files that are the same for every job of a profile, such as an SSH
configuration, the host keys of GitHub, CA certificates, and binaries that Buildkite plugins run.
Today such a file needs a new image, or a secret that each pipeline fetches.
[ADR-0004](0004-treat-all-builds-as-untrusted.md) forbids host directory mounts. The decision is
that a profile MAY name host directories, and that each job VM of the profile mounts them
read-only. Both the controller and Orchard enforce the read-only rule. This ADR does not decide
how the Git checkout authenticates. This ADR replaces the host directory rule of
[ADR-0004](0004-treat-all-builds-as-untrusted.md), and
[ADR-0004](0004-treat-all-builds-as-untrusted.md) keeps its status. The cost is a read-only file
path from each VM to its host, and files that every job of the profile can read.

## Context

[ADR-0004](0004-treat-all-builds-as-untrusted.md) forbids host directory mounts because one
malicious build can corrupt a shared cache, and all later builds then use the corrupted content.
That risk needs a write. A job that can only read a directory cannot change what the next job
reads.

The operator of a fleet has files that every job of a profile needs and no job changes: an SSH
configuration, `known_hosts`, CA certificates, and tools that plugins run, which each job would
otherwise download from the internet. The image can carry them, but then each change of a file
needs a new build of each image ([ADR-0020](0020-leave-the-job-vm-images-to-the-operator.md)). A
Buildkite secret can carry a small file, but each pipeline then has to fetch it. Neither suits a
fleet (issue #256, 2026-11-24).

Orchard supports host directories through `hostDirs` on the VM resource. Each entry has a
`name`, a `path`, and a read-only flag `ro`, and the worker passes it to `tart run` as
`--dir=<name>:<path>[:ro]`. The Orchard controller refuses every `hostDirs` entry unless it starts
with `--insecure-allow-host-dirs`. Each path MUST also match an entry of `hostDirPolicies` in the
cluster settings, and an entry with `ro` refuses a mount that is not read-only. Only an account with
`admin:write` can change the cluster settings, and a change replaces the whole object. The flag
applies to the whole Orchard controller, so every account with `compute:write` can then create a
VM with any directory that the policies allow. Orchard does not check that the directory exists on
the worker that it picks, and a Tart `--dir` path MAY also be an `http` or `https` archive
(Orchard 0.58.0: `pkg/resource/v1/host_dir.go`, `pkg/resource/v1/host_dir_policy.go`,
`internal/controller/api_vms.go`, `internal/controller/api_cluster_settings.go`,
`internal/worker/vmmanager/tart/tart.go`, read on 2026-11-24).

The behaviour of a mount in a macOS guest is `UNKNOWN`: the mount point, when the guest mounts it
relative to the start of the startup script, and the owner and modes of its files. The owner is the
maintainers, and the trigger is a spike on the Mac of an engineer before this ADR is accepted
(owner answer, 2026-11-24). The design uses `/Volumes/My Shared Files/<name>`, the automatic mount
point that the Tart documentation names for macOS 13 and later.

[ADR-0017](0017-download-the-agent-when-the-vm-starts.md) did not mount the agent from the host,
because the mount was a new path from the VM to the host. This ADR accepts a read-only path for
operator files. The decision of [ADR-0017](0017-download-the-agent-when-the-vm-starts.md) does not
change: the VM still downloads its agent, or runs the agent of its image.

## Goals and non-goals

Goals:

* **Fleet files are configuration.** A change of a file needs no new image and no change of a
  pipeline.
* **Containment.** A job cannot change a file that a later job reads
  ([PR-030](../product/requirements.md#pr-030)).
* **Two locks on the read-only rule.** A defect of the controller alone cannot make a mount
  writable.
* **Auditability.** The configuration shows which profile mounts which directory.

Non-goals:

* Host directory caches, or any directory that a job writes. They stay out of the project.
* Secret injection for each job. Every job of a profile reads the same files.
* The distribution of the files to the Mac hosts. The operator provides each directory on each host
  of its profile (owner answer, 2026-11-24).
* A mount of the agent. [ADR-0017](0017-download-the-agent-when-the-vm-starts.md) still applies.
* Credentials for the Git checkout. A mount can hold them, but whether a VM may receive them, and
  how the VM uses them, is a separate decision. Until that decision, a mount holds no credential
  ([SE-02](../architecture/system-design-baseline.md#se-02)).

## Options considered

**No host directories (the current state).** The image or a Buildkite secret carries each file.
No new path from the VM to the host exists. However, a change of a file needs a new image, or a
change of each pipeline, and a plugin that needs a binary downloads it in each job.

**Read-only host directories, which only the controller enforces.** A profile names directories,
and the controller sets `ro` on each. Orchard runs with `--insecure-allow-host-dirs` and a policy
that allows writable mounts. However, a defect of the controller, or another account with
`compute:write`, can then create a writable mount.

**Read-only host directories, which the controller and Orchard enforce.** As the previous option,
and each entry of `hostDirPolicies` has `ro`, so Orchard refuses a mount that is not read-only.
The policies also limit the paths. However, every account with `compute:write` can mount any path
that the policies allow, and Orchard runs with a flag that its authors call insecure.

| Goal | No host directories | Read-only, the controller only | Read-only, the controller and Orchard |
|---|---|---|---|
| Fleet files are configuration | No | Yes | Yes |
| Containment | Yes | Yes, while the controller has no defect | Yes |
| Two locks on the read-only rule | — | No | Yes |
| Auditability | Yes | Yes | Yes |

"—" shows that the goal does not apply to the option.

## Decision

Mount read-only host directories that the profile names, and have both the controller and Orchard
enforce the read-only rule.

* A profile MAY have `hostDirs`, a list of entries with a `name` and a `path`.
* The controller MUST set `ro: true` on each `hostDirs` entry of a VM specification. The
  configuration MUST NOT have a field that makes a mount writable.
* The controller MUST refuse at start a `name` that does not match `[a-z0-9-]+`, a duplicate
  `name` in one profile, and a `path` that is not an absolute, clean local path. It MUST refuse a
  `path` with `..` or `:`, the path `/`, and a URL.
* The Orchard controller MUST run with `--insecure-allow-host-dirs` only on a fleet that has a
  profile with `hostDirs`. Each entry of `hostDirPolicies` MUST have `ro`.
* Before the agent starts, the startup script MUST wait for each mount, for a time that the
  configuration sets for each profile, with a short default. If a mount does not appear in that
  time, the script MUST stop with a message that names the mount.
* The controller MUST write a warning at start that names each profile with `hostDirs` and its
  paths, and says that every job of the queue that selects the profile can read them.
* The operator MUST NOT put a file in a mounted directory that a build of the queue must not read.
  The operator MUST NOT put a credential in a mounted directory, unless a later decision permits
  that credential.

This decision amends [ADR-0004](0004-treat-all-builds-as-untrusted.md) by partial replacement, as
[ADR-0020](0020-leave-the-job-vm-images-to-the-operator.md) does for
[ADR-0012](0012-build-the-job-vm-images-from-a-vanilla-macos-image.md). It replaces these parts of
[ADR-0004](0004-treat-all-builds-as-untrusted.md):

* The decision bullet "It MUST NOT have host directory mounts" becomes "It MUST NOT have a
  writable host directory mount. It MAY have the read-only host directory mounts that its profile
  names."
* The confirmation "each generated VM specification has no host directories" becomes "each
  generated VM specification has only the host directories of its profile, each with `ro`".

The other rules of [ADR-0004](0004-treat-all-builds-as-untrusted.md) stay, so
[ADR-0004](0004-treat-all-builds-as-untrusted.md) keeps its status.

## Consequences

Good:

* An operator changes a fleet file on the hosts. No image build and no pipeline change is
  necessary.
* A plugin can run a binary from a mount, and the job does not download it.
* No job can change a file that another job reads, because both the controller and Orchard refuse
  a writable mount.

Bad:

* Each VM has a read-only file path to its host. A defect of the file sharing of Tart or of the
  Virtualization framework of macOS reaches the host through it.
* Every job of the queue that selects the profile can read and copy each mounted file, as it can a
  file of the image.
* A change of a file on a host reaches the VMs that run. A job can read a file that the operator is
  writing, unless the operator replaces each file in one rename.
* Orchard runs with `--insecure-allow-host-dirs`. Every account with `compute:write` can mount any
  path that the policies allow, and Orchard 0.58.0 has no narrower control.
* If a host lacks a directory, each VM of the profile on that host fails, and its job fails.

Review trigger:

* Review this decision if Orchard adds a host directory control for each account.
* Review it if a guest escape through the file sharing of Tart or of the Virtualization framework
  is published.
* Review it if the spike shows that a guest cannot use a file of a mount as the files of the image,
  for example an SSH configuration that `ssh` refuses for its modes.

## Confirmation

* Unit tests confirm that each generated VM specification has only the `hostDirs` of its profile,
  and that each has `ro: true`.
* Configuration tests confirm the refusal of each invalid `name` and `path`, and the warning.
* Script tests confirm that the agent does not start when a mount is missing, and that the message
  names the mount.
* A test of the bootstrap Job confirms that each `hostDirPolicies` entry has `ro`, and that the Job
  keeps the other cluster settings.
* A test on a real Tart VM confirms that a job reads a mounted file, that a write to the mount
  fails, and that a missing directory fails the job before an agent starts.
