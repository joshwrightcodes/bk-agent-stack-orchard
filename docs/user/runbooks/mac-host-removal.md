# Runbook: remove a Mac host

This runbook takes a Mac host out of service: it drains the host, removes what the playbook put on
it, removes it from the inventory, and revokes its Orchard account. It also covers new hardware
under the name of an old host. To change a host that stays in service, use the runbook [Change the
Mac hosts](mac-host-changes.md).

If the host is lost or stolen, do not drain it. Revoke its account at once, as "Revoke one host" in
the runbook [Orchard worker accounts](orchard-worker-accounts.md) describes.

## Remove a host

1. Drain the host, and leave its worker paused:

   ```sh
   cd deploy/hosts
   ansible-playbook -i inventory.yml site.yml --limit mac-03.hosts.internal \
     -e orchard_worker_drain_only=true
   ```

   The playbook pauses the worker, so Orchard places no new VM on it. It waits until no VM of the
   worker is pending or running, up to `orchard_worker_drain_timeout`, and then names the host as
   drained and paused. If the time limit comes first, the playbook resumes the worker and names the
   host as not changed. Run the step again later.
2. On the host, stop the worker and the job that loads the pf rules. The worker runs no VM, so its
   stop ends no job:

   ```sh
   sudo launchctl bootout system/io.github.joshwrightcodes.orchard-worker
   sudo launchctl bootout system/io.github.joshwrightcodes.bkaso-vm-isolation
   ```

3. As the worker user, remove the cached images of Tart. Do this before the next step removes Tart:

   ```sh
   tart list --source oci --quiet | xargs tart delete
   ```

4. Remove the files that the playbook wrote, and the pf rules of the job VMs:

   ```sh
   sudo rm -f /Library/LaunchDaemons/io.github.joshwrightcodes.orchard-worker.plist \
     /Library/LaunchDaemons/io.github.joshwrightcodes.bkaso-vm-isolation.plist
   sudo /sbin/pfctl -a com.apple/400.bkaso-vm-isolation -F all
   sudo rm -f /etc/pf.anchors/io.github.joshwrightcodes.bkaso-vm-isolation
   sudo rm -rf /usr/local/etc/orchard-worker /usr/local/opt/bkaso
   sudo rm -f /usr/local/bin/orchard /usr/local/bin/tart /usr/local/bin/softnet
   sudo rm -f /var/log/orchard-worker.log /var/log/orchard-worker.stdout.log \
     /var/log/bkaso-vm-isolation.log
   ```

   `/usr/local/etc/orchard-worker` holds the token of the host, so its removal takes the token off
   the host. The power settings of the playbook stay. Change them with `pmset` if the host gets
   another use.
5. Remove the host from the inventory, and its token from the inventory vault.
6. Revoke the account of the host: follow "Revoke one host" in the runbook [Orchard worker
   accounts](orchard-worker-accounts.md). It removes the key of the host from the Secret, deletes
   the account and the worker record, and removes the host from the alert for unknown workers.

## Replace the hardware of a host

To give new hardware the name of an old host, the old worker record must go first. Orchard 0.58.0
refuses the registration of a worker whose name a record from another machine holds, with HTTP 409
and the message "this worker is managed from a different machine ID".

1. Remove the old host with steps 1 to 4 above, if you can still reach it.
2. Delete the old worker record. The runbook [Orchard worker accounts](orchard-worker-accounts.md),
   section "Access to the API and the Job", gives the command.
3. If the old hardware leaves your control with its disk, give the host a new token: "Rotate the
   token of one host" in the same runbook, steps 1 and 2.
4. Prepare the new hardware as "Before the playbook" in the [README of the
   hosts](../../../deploy/hosts/README.md#before-the-playbook) says. Then run the playbook for the
   host. It waits until the new worker reports to the controller.
