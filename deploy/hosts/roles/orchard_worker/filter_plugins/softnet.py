# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Filters for the addresses that Softnet can give the host on the network of a VM.

Softnet picks the network of each VM, a /29, from a few private ranges (the variable
orchard_worker_softnet_pools), and the host takes the first address of that network, the gateway
of the VM. These filters find where the softnetAllow entries of the profiles meet those addresses,
so that select.yml refuses a wide entry and the pf rules guard the rest.
"""

from __future__ import annotations

import ipaddress

# The prefix length of the network that Softnet gives each VM.
NETWORK_BITS = 29


def _ipv4(entries):
    """Give the IPv4 networks of the entries, and skip the IPv6 ones."""
    networks = []
    for entry in entries:
        network = ipaddress.ip_network(str(entry), strict=False)
        if network.version == 4:
            networks.append(network)
    return networks


def _overlaps(entry, pools):
    """Give the part of the entry that each pool shares with it."""
    for pool in _ipv4(pools):
        if entry.subnet_of(pool):
            yield entry
        elif pool.subnet_of(entry):
            yield pool


def softnet_wide_overlaps(allow, pools):
    """Give the entries that are wider than /29 and overlap a pool of Softnet.

    Such an entry holds the gateway of the network of some VM, which is an address of the host.
    """
    return [
        str(entry)
        for entry in _ipv4(allow)
        if entry.prefixlen < NETWORK_BITS and any(_overlaps(entry, pools))
    ]


def softnet_host_guard(allow, pools):
    """Give the addresses inside the entries that Softnet can give the host.

    For a /29 or narrower part, that is the first address of its /29, when the part holds it. A
    wider part holds the gateway of each /29 in it, so it goes in whole, which keeps the list short.
    """
    guard = []
    for entry in _ipv4(allow):
        for part in _overlaps(entry, pools):
            if part.prefixlen < NETWORK_BITS:
                guard.append(str(part))
                continue
            network = part.supernet(new_prefix=NETWORK_BITS)
            gateway = network.network_address + 1
            if gateway in part:
                guard.append(f"{gateway}/32")
    return sorted(set(guard), key=lambda text: ipaddress.ip_network(text))


class FilterModule:
    """The filters of the orchard_worker role."""

    def filters(self):
        return {
            "softnet_wide_overlaps": softnet_wide_overlaps,
            "softnet_host_guard": softnet_host_guard,
        }
