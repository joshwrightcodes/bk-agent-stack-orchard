# Runbook: change the Mac hosts

This runbook changes the Mac hosts while jobs run on them. It covers an upgrade of Orchard, Tart,
or Softnet and its rollback, a new image for a profile, a new token for a host, and new labels. The
playbook is in [`deploy/hosts`](../../../deploy/hosts/README.md). To take a host out of service,
use the runbook [Remove a Mac host](mac-host-removal.md).

## How the playbook protects running jobs

The playbook changes one host at a time. When a change restarts the Orchard worker of a host, the
playbook drains the host first:

1. It pauses the worker, so that Orchard places no new VM on it.
2. It waits until no VM of the worker is pending or running.
3. It makes the change, and waits until the worker reports again.
4. It resumes the worker.

The worker deletes every VM on the host when it stops, so without the drain the jobs on the host
fail.

These changes restart the worker: a new version of Orchard, Tart, or Softnet, a new token, new
labels, and a new address of the controller. A run that only pulls images or changes the pf rules
restarts nothing, and drains nothing.

The drain waits up to `orchard_worker_drain_timeout`, 90 minutes by default. If a VM still runs at
that limit, the playbook resumes the worker, changes nothing that restarts it, and goes on with the
next host. At the end, it names each such host and fails:

```text
The play did not change these hosts, because their drain did not finish: mac-03: 1 VM(s) still
pending or running at the drain time limit of 5400 s. Run the play for them again later.
```

Run the playbook again later with `--limit` for those hosts. A run with more time is also possible:
add `-e orchard_worker_drain_timeout=<seconds>`.

The [README of the hosts](../../../deploy/hosts/README.md#changes-that-restart-the-worker) describes
each setting of the drain.

## Before each change

1. Run the playbook for one host first, with `--limit <host>`. Check that the worker is online and
   takes jobs. Then run it for the fleet.
2. Plan the time. Each host drains for about the length of the jobs that run on it when the
   playbook pauses it. To drain more hosts at a time, add `-e orchard_worker_serial=<number or
   percentage>`. The capacity of those hosts is out together.
3. Do not run two playbooks against the same host at the same time.

## Upgrade Orchard, Tart, or Softnet

The playbook installs each tool at the version and the SHA-256 of the role defaults
(`deploy/hosts/roles/orchard_worker/defaults/main.yml`). A release of this project sets these pins.
To use other versions, set the variables in the inventory, for example in `group_vars/workers.yml`:

| Tool | Variables |
|---|---|
| Orchard | `orchard_worker_orchard_version`, `orchard_worker_orchard_sha256` |
| Tart | `orchard_worker_tart_version`, `orchard_worker_tart_sha256` |
| Softnet | `orchard_worker_softnet_version`, `orchard_worker_softnet_sha256` |

The SHA-256 is that of the release archive for Apple silicon, as the release publishes it.

1. Read the release notes of the new version. Look for a change that needs the Orchard controller
   and the workers in a set order, or a change of the minimum macOS version.
2. For Orchard: the Orchard controller and the workers run the same version. In Orchard 0.58.0, the
   worker keeps code for an older controller, so upgrade the hosts first and the Orchard controller
   after them, unless the release notes say otherwise. Keep the time between the two short. The
   Orchard controller is in [`deploy/orchard`](../../../deploy/orchard/README.md).
3. Set the new version and its SHA-256. Run the playbook for one host:

   ```sh
   cd deploy/hosts
   ansible-playbook -i inventory.yml site.yml --limit mac-01.hosts.internal
   ```

   The playbook downloads the archive, checks its SHA-256, and unpacks it into a directory of its
   version under `/usr/local/opt/bkaso`. It then drains the host, switches the command to the new
   version, and restarts the worker. It checks the versions of `orchard` and `tart`, and that the
   worker reports to the controller.
4. Check that the worker is online, and that a job runs on the host.
5. Run the playbook for the fleet.

If the new worker does not report within 300 seconds, the playbook stops on that host, and the
worker stays paused. No other host changes. Read `/var/log/orchard-worker.log` on the host, then
fix the cause or roll back.

## Roll back an upgrade

The directory of each version stays under `/usr/local/opt/bkaso`, so a rollback needs no download.

1. Set the previous version and its SHA-256 again.
2. Run the playbook for the hosts that have the new version: first the host that failed, if one
   did, then the others. The playbook drains each host, switches the command back, restarts the
   worker, and resumes it.
3. For Orchard, roll back the Orchard controller before the hosts, which is the reverse order of
   the upgrade.

## Change the image of a profile

The hosts pull the images of the profiles from the configuration of the controller
(`orchard_worker_config_file`). A host pulls an image before the controller uses it, so that no job
waits for a pull of a large image and fails on its boot timeout. The playbook also removes each
cached image that no profile of the host names. The order of the steps therefore matters.

To add a profile, or to give a profile a new image:

1. Change the profile in the configuration file that the playbook reads. Do not apply it to the
   controller yet.
2. Run the playbook for the fleet with `-e orchard_worker_prune_images=false`. Each host pulls the
   new image and keeps the old one, which the controller still uses.
3. Check that the playbook changed every host. To see the images of a host, run
   `tart list --source oci` on it as the worker user.
4. Apply the configuration to the controller.
5. Run the playbook again, without `orchard_worker_prune_images=false`. Each host removes the old
   image. A host keeps an image while the controller lists a VM of the worker with it.

To remove a profile, apply the configuration to the controller first, then run the playbook. The
hosts then remove its image.

The same order applies to `softnetAllow`, because the playbook writes the pf rules of a host from
the profiles. To permit more addresses, run the playbook first, then apply the configuration. To
permit fewer, apply the configuration first, then run the playbook.

These runs restart no worker, so they drain no host.

## Change the token of a host

A new token stops the old one in Orchard at once, and the worker of the host then cannot reach the
controller. Drain the host before the token changes:

1. With the current token still in the inventory vault, drain the host and leave it paused:

   ```sh
   ansible-playbook -i inventory.yml site.yml --limit mac-01.hosts.internal \
     -e orchard_worker_drain_only=true
   ```

   The playbook names the host as drained and paused. It changes nothing that restarts the worker.
2. Follow steps 1 and 2 of "Rotate the token of one host" in the runbook
   [Orchard worker accounts](orchard-worker-accounts.md). The worker runs no VM, so no job fails
   when its token stops.
3. Run the playbook for the host. It writes the new token, restarts the worker, waits for it to
   report, and resumes it.

## Change the labels or the address of the controller

Set the new value in the inventory, and run the playbook. It drains each host before it restarts
the worker. The playbook calls the API of Orchard at the address in the inventory, so the new
address must reach the controller before the run.

## Run without the drain

If the controller cannot answer, the drain cannot run, and the playbook stops before it changes
anything that restarts the worker. To change the hosts anyway, add `-e orchard_worker_drain=false`.
The playbook then restarts the worker without a drain, which deletes the VMs on the host, and it
removes no cached image.
