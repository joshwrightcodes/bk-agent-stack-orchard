# The Mac hosts

This directory makes each Mac host an Orchard worker (target design, section 11). One Ansible role,
`orchard_worker`, does these steps on each host:

1. It checks the host:
   - the Command Line Tools, which give Python to Ansible;
   - an Apple silicon Mac;
   - macOS 27.2 or later, because the job VMs run macOS 27.2 and a macOS guest needs a host of the
     same version or later;
   - the hypervisor;
   - the automatic login of the worker user.
2. It installs Orchard, Tart, and Softnet at pinned versions, from their release archives, checked
   against their SHA-256. Orchard is the version of `mise.toml`, and `scripts/check-deps.py` keeps
   the two in agreement. Softnet gets the setuid bit, because Tart starts it for each VM with root.
   Only root and the worker user can run it: its mode is `04750`, and an access control entry
   lets the worker user in. Root owns every file of the tools. The playbook checks each program
   that root runs, and each directory above it up to `/usr/local`. It stops when one is not owned
   by root and the group wheel, or when a group or other user can write it.
   The same tools are in the `openai/tools` Homebrew tap, which a developer Mac uses
   (`docs/development.md`). A host does not use the tap, for these reasons:
   - The tap has one version of each tool.
   - An upgrade of Softnet removes its setuid bit.
   - On 2026-09-27, the tap gave Tart 2.38.0, which the worker cannot use.
3. It disables the sleep of the system and the disks. It makes the host start again after a power
   failure and on a packet of the network (`pmset`). Orchard fails the VMs of a worker that does
   not report for 3 minutes. A host that sleeps therefore fails the jobs on it.
4. It loads pf rules that keep the VMs away from the host (`templates/vm-isolation.pf.j2`).
   Softnet lets a VM reach the gateway of its network on every port, and the gateway is the host.
   Softnet gives each VM its own network, a /29 from a few private ranges, with the first address
   for the host. pf on macOS cannot name these interfaces or their addresses. Because of this, the
   rules block traffic by destination. Traffic from an interface that is not an admin interface
   can reach the host only for DHCP and DNS. It can reach a private range only when a profile of
   the host permits that range (`softnetAllow`). An address that Softnet can give the host stays
   closed inside a permitted range too, as the comments of the template explain. By default, the
   admin interfaces (`orchard_worker_admin_interfaces`) are the interface of the default route. A
   launchd job loads the rules into the anchor `com.apple/400.bkaso-vm-isolation` and enables pf.
   It runs at each start of the host, when the network changes, and every 30 seconds, so the rules
   follow the addresses of the host, and pf stays enabled. For this reason, `/etc/pf.conf` stays
   as macOS ships it. The job writes to `/var/log/bkaso-vm-isolation.log` only when it enables pf,
   loads rules that were missing, or fails. Outside a start of the host, such a line means that
   the VMs could reach the host until then.
5. It runs `orchard worker run` from launchd, as root, and the worker drops to the worker user.
   This is the Orchard workaround for the Local Network permission of macOS. A change that
   restarts the worker drains the host first. The section [Changes that restart the
   worker](#changes-that-restart-the-worker) explains how.
6. It pulls the image of each profile that the host can take. It removes each cached image that no
   profile of the host names and no VM of the worker uses, so that old images do not fill the
   disk.

## Before the playbook

Do these steps by hand on each host, once. The playbook checks the last two.

1. Make the worker user, for example `admin`, in Setup Assistant.
2. Enable Remote Login for that user, and add the SSH key of the operator to
   `~/.ssh/authorized_keys`.
3. Install the Command Line Tools: `xcode-select --install`.
4. Enable the automatic login of the worker user: System Settings, Users and Groups,
   "Automatically log in as". Tart and the registry credentials need its login keychain. On
   macOS 27, `sysadminctl -autologin set` failed and a written `/etc/kcpassword` was ignored
   (2026-09-27), so this step is manual.

## The inventory

Copy `inventory.example.yml`, and give the values of the fleet:

| Variable | Value |
|---|---|
| `orchard_worker_controller_url` | The address of the internal load balancer of the Orchard controller |
| `orchard_worker_ca_cert` | `ca.crt` of the `orchard-tls` Secret, as PEM |
| `orchard_worker_name` | Required, with no default. The name of the worker, a DNS label such as `mac-01`. Each host has its own name, and the playbook stops when a name is missing or two hosts share one. The account of the host is `worker-<name>` |
| `orchard_worker_token` | The token of this host only: the key `worker-<name>` of the `orchard-worker-tokens` Secret. Keep one token for each host in Ansible Vault, for example `vault_orchard_worker_tokens[orchard_worker_name]`. The playbook stops when two hosts share a token |
| `orchard_worker_labels` | The labels of the worker, for example `{pool: general}` |
| `orchard_worker_otlp_endpoint` | The OTLP endpoint of the collector. Orchard exports its metrics to it |
| `orchard_worker_config_file` | The configuration file of the controller, or the values of its Helm release |
| `orchard_worker_drain_timeout` | Optional. The drain time limit, in seconds. The default is `5400` (90 minutes). See [Changes that restart the worker](#changes-that-restart-the-worker) |

The role makes the bootstrap token of Orchard from the account, the token, and the CA. The worker
trusts only the certificate of the token. A host therefore needs no change of its trust settings.
A new certificate from the same CA needs no new token. launchd gives the token to the worker on its
standard input. The source is `/usr/local/etc/orchard-worker/bootstrap-token` (root, `0600`). For
this reason, the token is in no process listing.

The host pulls the image of each profile whose `workerLabels` the labels of the worker contain. It
pulls no other image, because a 1 TB host holds only a few Xcode images. For the same reason, it
removes each cached image that no profile of the host names, unless the controller lists a VM of
the worker with that image. A profile names its image by digest. The image in the cache is
therefore always the image of the profile. When the registry needs credentials, give
`orchard_worker_registry_username` and
`orchard_worker_registry_password`. The role gives the password to `tart login` on its standard
input, and it logs out after the pull. The keychain of the host therefore keeps no credentials.

Each `softnetAllow` entry of the profiles of a host is an address or a range inside the private
ranges that the pf rules block, such as `10.20.30.40/32`. The playbook stops on a wider entry,
such as `0.0.0.0/0`. It also stops on an entry wider than /29 inside the ranges from which
Softnet picks the networks of the VMs (`orchard_worker_softnet_pools` in `vars/main.yml`), such as
`192.168.0.0/16`, because such an entry holds an address of the host. The controller refuses the
same entries. Each key of `orchard_worker_labels` has only lower case letters, digits, `_`,
`.`, and `-`. A label value has no comma, equals sign, or space.

## Run

Change one host first, and the fleet after it:

```sh
cd deploy/hosts
ansible-playbook -i inventory.yml site.yml --limit mac-01.hosts.internal
ansible-playbook -i inventory.yml site.yml
```

The play changes one host at a time. To change more hosts at a time, add
`-e orchard_worker_serial=<number or percentage>`, for example `-e orchard_worker_serial=10%`. The
hosts of one batch drain together, so their capacity is out together. When a host fails, the play
stops, so a change that breaks the worker does not reach the next host.

The check of the names and the tokens before the playbook covers the hosts of the run only. A run
with `--limit mac-01.hosts.internal` does not compare that host with the other hosts of the
inventory, so two hosts that share a name or a token pass when the operator runs them one at a
time. The run of the whole fleet finds it. The operator therefore runs the whole fleet after a
change of a name or a token.

The worker writes its log to `/var/log/orchard-worker.log`, and launchd writes the output of a
failed start to `/var/log/orchard-worker.stdout.log`.

## Changes that restart the worker

These changes restart the worker:

- a new version of Orchard, Tart, or Softnet;
- a new token of the host;
- new labels, or a new address of the controller.

When the worker stops, it stops and deletes every VM on the host, and the jobs in those VMs fail.
For this reason, the play drains the host before such a change:

1. It pauses the worker in Orchard. Orchard places no new VM on a paused worker, and the
   controller counts no room on it.
2. It waits until no VM of the worker is pending or running. A failed VM runs no job, so it does not
   hold the drain.
3. It makes the change, and restarts the worker.
4. It waits until the worker reports to the controller again. If the worker does not report within
   `orchard_worker_register_timeout` (300 seconds), the play stops, and the worker stays paused.
5. It resumes the worker. A worker that you paused before the run stays paused.

A run that restarts nothing drains nothing. This includes a run that only pulls images or changes
the pf rules.

The drain waits up to `orchard_worker_drain_timeout`, 90 minutes by default. That covers a VM that
Orchard placed immediately before the pause: the boot and acquire timeouts of its profile, 15 and 5
minutes by default, then a job of up to 60 minutes, and 10 minutes to spare. If your jobs take
longer, raise the limit. If a VM still runs at the limit, the play resumes the worker, changes
nothing that restarts it, and goes on with the next host. At the end, the play names each such
host and fails. Run the play for those hosts again later.

The play writes `/usr/local/etc/orchard-worker/drain` when it pauses the worker, and removes it when
it resumes the worker. If a run stops between the two, for example on a failed check, the worker
stays paused. Fix the cause and run the play for the host again: it restarts and resumes the worker.

The play calls the API of Orchard from the host, with the account and the token of the host, and
trusts only the CA of the inventory. The token goes only to the `uri` module, and `no_log` keeps it
and the answers of the controller out of the output. It is in no command line.

Other settings of the drain:

| Variable | Default | Effect |
|---|---|---|
| `orchard_worker_drain_poll` | `30` | How often the play counts the VMs of the worker during the drain, in seconds |
| `orchard_worker_drain_only` | `false` | `true` drains the host and leaves the worker paused, with no change that restarts it. The next run without it makes the change and resumes the worker |
| `orchard_worker_drain` | `true` | `false` changes the host without the API of Orchard: no drain, no wait for the worker to report, and no removal of cached images. A restart then deletes the VMs on the host. Use it only when the controller cannot answer |
| `orchard_worker_prune_images` | `true` | `false` keeps every cached image, for example while you change the image of a profile |

The runbook [Change the Mac hosts](../../docs/user/runbooks/mac-host-changes.md) gives the steps for
an upgrade, a rollback, a new image, and a new token. The runbook [Remove a Mac
host](../../docs/user/runbooks/mac-host-removal.md) takes a host out of service.

## Tests

`mise run check-hosts` lints the role, and checks the images that a host pulls for different
labels. It also checks that the role refuses a `softnetAllow` entry outside the private ranges,
and a wide entry inside the networks of the VMs. It renders the pf rules, checks their order, and
parses them with `pfctl -n` on a Mac. It checks which VMs hold a drain, and which cached images a
host removes. CI runs it.

`mise run hosts-test` runs the playbook against a Tart VM on an Apple silicon Mac. An Orchard
controller is on the Mac, and the certificate comes from a test CA. The test checks these points:

- The worker registers with its labels.
- The token is in no process listing.
- Softnet has the setuid bit.
- pf has the rules.
- A second run changes nothing.
- The host removes a cached image that no profile names, and keeps the image of its profile.
- The worker and the rules return after a restart.
- A change of the labels drains the host first. While a VM is pending on the worker, the play
  reaches the drain time limit, resumes the worker, and names the host. When no VM is left, the
  play makes the change, and resumes the worker after it reports.
- A drain-only run leaves the worker paused, and the next run makes the change and resumes it.

A macOS VM cannot run a second macOS VM. The test therefore does not check the hypervisor, and it
cannot start a VM of the pilot image. A real host does that.
