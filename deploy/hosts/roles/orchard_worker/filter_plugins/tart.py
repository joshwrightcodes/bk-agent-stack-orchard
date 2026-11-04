# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""A filter for the images in the cache of Tart that the host no longer needs.

"tart list --source oci" names each cached image as <repository>@<digest> or <repository>:<tag>.
An image pulled by digest is a directory, and an image pulled by tag is a link to the directory of
its digest. "tart delete" removes the entry, and then removes each digest that nothing links to and
that was not pulled by digest (VMStorageOCI.gc, Tart 2.41.1). The filter therefore never removes a
link or a digest that a kept image can depend on.
"""

from __future__ import annotations


def _split(name):
    """Give the repository of an image, and whether a tag, not a digest, names it."""
    if "@" in name:
        return name.split("@", 1)[0], False
    slash = name.rfind("/")
    colon = name.rfind(":")
    if colon > slash:
        return name[:colon], True
    return name, True


def _normal(name):
    """Give the name that Tart lists for an image: a name with no tag or digest is :latest."""
    repository, tagged = _split(name)
    if tagged and repository == name:
        return f"{name}:latest"
    return name


def tart_prunable(cached, keep):
    """Give the cached images to remove: each one that is not in keep, with two exceptions.

    - A tag of a repository that has a kept image stays, because it can link to a kept digest.
    - A digest of a repository that has a kept tag stays, because the tag can link to it.
    """
    kept = {_normal(str(name)) for name in keep or []}
    kept_repositories = {_split(name)[0] for name in kept}
    tagged_repositories = {_split(name)[0] for name in kept if _split(name)[1]}
    prunable = set()
    for name in (str(entry) for entry in cached or []):
        if name in kept:
            continue
        repository, tagged = _split(name)
        if tagged and repository in kept_repositories:
            continue
        if not tagged and repository in tagged_repositories:
            continue
        prunable.add(name)
    return sorted(prunable)


class FilterModule:
    """The filters of the orchard_worker role for the cache of Tart."""

    def filters(self):
        return {"tart_prunable": tart_prunable}
