# SPDX-License-Identifier: AGPL-3.0-or-later
"""Filters for the VMs that the Orchard controller lists.

The play reads the VMs of its own worker before a change that restarts the worker
(tasks/drain.yml).
"""

from __future__ import annotations

# The status of a VM that runs nothing: Orchard sets it when a VM cannot reach "running", or when
# its worker goes offline. VM.TerminalState of Orchard 0.58.0 gives true for this status only.
FAILED = "failed"


def _on_worker(vms, worker):
    """Give the VMs that Orchard placed on the worker."""
    return [vm for vm in vms or [] if vm.get("worker") == worker]


def orchard_active_vms(vms, worker):
    """Give the names of the VMs of the worker that are pending or running.

    These are the VMs that a restart of the worker would delete while they start or run a job, the
    same VMs that "orchard pause worker --wait" waits for.
    """
    return sorted(
        vm.get("name", "") for vm in _on_worker(vms, worker) if vm.get("status") != FAILED
    )


class FilterModule:
    """The filters of the orchard_worker role for the VMs of Orchard."""

    def filters(self):
        return {
            "orchard_active_vms": orchard_active_vms,
        }
