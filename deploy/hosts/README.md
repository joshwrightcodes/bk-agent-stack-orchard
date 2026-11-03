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
   launchd job loads the rules into the anchor `com.apple/400.bkaso-vm-isolation` and enables pf
   at each start of the host. For this reason, `/etc/pf.conf` stays as macOS ships it.
5. It runs `orchard worker run` from launchd, as root, and the worker drops to the worker user.
   This is the Orchard workaround for the Local Network permission of macOS.
6. It pulls the image of each profile that the host can take.

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

The role makes the bootstrap token of Orchard from the account, the token, and the CA. The worker
trusts only the certificate of the token. A host therefore needs no change of its trust settings.
A new certificate from the same CA needs no new token. launchd gives the token to the worker on its
standard input. The source is `/usr/local/etc/orchard-worker/bootstrap-token` (root, `0600`). For
this reason, the token is in no process listing.

The host pulls the image of each profile whose `workerLabels` the labels of the worker contain. It
pulls no other image, because a 1 TB host holds only a few Xcode images. A profile names
its image by digest. The image in the cache is therefore always the image of the profile. When the
registry needs credentials, give `orchard_worker_registry_username` and
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

The check of the names and the tokens before the playbook covers the hosts of the run only. A run
with `--limit mac-01.hosts.internal` does not compare that host with the other hosts of the
inventory, so two hosts that share a name or a token pass when the operator runs them one at a
time. The run of the whole fleet finds it. The operator therefore runs the whole fleet after a
change of a name or a token.

The worker writes its log to `/var/log/orchard-worker.log`, and launchd writes the output of a
failed start to `/var/log/orchard-worker.stdout.log`.

## Tests

`mise run check-hosts` lints the role, and checks the images that a host pulls for different
labels. It also checks that the role refuses a `softnetAllow` entry outside the private ranges,
and a wide entry inside the networks of the VMs. It renders the pf rules, checks their order, and
parses them with `pfctl -n` on a Mac. CI runs it.

`mise run hosts-test` runs the playbook against a Tart VM on an Apple silicon Mac. An Orchard
controller is on the Mac, and the certificate comes from a test CA. The test checks these points:

- The worker registers with its labels.
- The token is in no process listing.
- Softnet has the setuid bit.
- pf has the rules.
- A second run changes nothing.
- The worker and the rules return after a restart.

A macOS VM cannot run a second macOS VM. The test therefore does not check the hypervisor, and it
cannot start a VM of the pilot image. A real host does that.
