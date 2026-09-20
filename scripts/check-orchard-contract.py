#!/usr/bin/env python3
"""Compare the Orchard fields that this client depends on with the fields upstream has.

The controller talks to the Orchard REST API with a client of this project, because the licences
of the two projects do not combine. Nothing regenerates that client, so something must report
when the contract moves. This script is that report.

How it decides what upstream has:

  * The JSON tags of the Orchard resource types are the contract, because they are what the
    server marshals. The published OpenAPI description is neither complete nor current: at
    version 0.57.0 it omits `randomSerial`, `createdAt`, `last_seen`, and `scheduling_paused`,
    and it names the status message `status_message` in one place and nothing in another.
  * The script reads those tags from the upstream source at the pinned version, and compares
    them with the list below. It writes no upstream file into this repository.

What it does not do: it does not check behaviour. The integration tests against a real Orchard
with synthetic workers do that.

Usage:
    scripts/check-orchard-contract.py [--version 0.57.0]
"""

from __future__ import annotations

import argparse
import base64
import json
import re
import sys
import urllib.error
import urllib.request
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
DESIGN = ROOT / "docs/architecture/target-design.md"

# The fields that internal/orchard reads or writes, by the type that carries them upstream.
# A change here needs a change in the client, and the other way round.
EXPECTED = {
    "pkg/resource/v1/v1.go": {
        "VM": {
            "name", "image", "cpu", "memory", "diskSize", "os", "imagePullPolicy",
            "headless", "randomSerial", "netSoftnet", "netSoftnetAllow", "restart_policy",
            "username", "password", "labels", "resources", "hostDirs",
            "status", "status_message", "worker", "createdAt", "scheduled_at", "started_at",
            "startup_script",
        },
        "VMScript": {"transport", "script_content", "env"},
        "Event": {"kind", "timestamp", "payload"},
    },
    "pkg/resource/v1/worker.go": {
        "Worker": {"name", "last_seen", "scheduling_paused", "resources", "labels"},
    },
}

TAG = re.compile(r"`json:\"([^\",]+)")
TYPE = re.compile(r"^type (\w+) struct")
EMBEDDED = re.compile(r"^\t([A-Z]\w*)$")


def pinned_version() -> str:
    """The minimum Orchard version that the design states."""
    match = re.search(r"minimum Orchard version MUST be ([0-9.]+)", DESIGN.read_text())
    if not match:
        print("check-orchard-contract: the design no longer states a minimum version",
              file=sys.stderr)
        raise SystemExit(2)

    return match.group(1)


def upstream_source(path: str, version: str) -> str:
    url = f"https://api.github.com/repos/openai/orchard/contents/{path}?ref={version}"
    request = urllib.request.Request(url, headers={"User-Agent": "bk-agent-stack-orchard"})
    with urllib.request.urlopen(request, timeout=20) as response:
        return base64.b64decode(json.load(response)["content"]).decode("utf-8")


def tags_by_type(source: str) -> tuple[dict[str, set[str]], dict[str, set[str]]]:
    """The JSON tags of each structure, and the structures that each one embeds.

    A Go structure that embeds another carries the fields of that other structure on the wire, so
    the check follows an embedded type rather than reporting its fields as missing.
    """
    tags: dict[str, set[str]] = {}
    embeds: dict[str, set[str]] = {}
    current = None

    for line in source.split("\n"):
        if match := TYPE.match(line):
            current = match.group(1)
            tags[current] = set()
            embeds[current] = set()
        elif current and (match := TAG.search(line)):
            tags[current].add(match.group(1))
        elif current and (match := EMBEDDED.match(line)):
            embeds[current].add(match.group(1))

    return tags, embeds


def resolved(name: str, tags: dict[str, set[str]], embeds: dict[str, set[str]],
             seen: set[str] | None = None) -> set[str]:
    """Every tag that a structure carries, including the tags of what it embeds."""
    seen = seen or set()
    if name in seen or name not in tags:
        return set()

    seen.add(name)
    fields = set(tags[name])
    for embedded in embeds.get(name, set()):
        fields |= resolved(embedded, tags, embeds, seen)

    return fields


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", default=None)
    arguments = parser.parse_args()

    version = arguments.version or pinned_version()
    problems: list[str] = []
    checked = 0

    # One table for every file, because a structure can embed one that another file declares.
    tags: dict[str, set[str]] = {}
    embeds: dict[str, set[str]] = {}

    for path in EXPECTED:
        try:
            source = upstream_source(path, version)
        except urllib.error.HTTPError as error:
            print(f"check-orchard-contract: cannot read {path} at {version}: {error}",
                  file=sys.stderr)

            return 2

        file_tags, file_embeds = tags_by_type(source)
        tags.update(file_tags)
        embeds.update(file_embeds)

    for path, types in EXPECTED.items():
        for name, fields in types.items():
            if name not in tags:
                problems.append(f"{path}: the type {name} is gone")

                continue

            upstream = resolved(name, tags, embeds)
            for field in sorted(fields):
                checked += 1
                if field not in upstream:
                    problems.append(
                        f"{path}: {name} no longer has the field {field!r}, "
                        f"which internal/orchard uses")

    if problems:
        print(f"check-orchard-contract: the contract moved at Orchard {version}\n", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        print("\n  Update internal/orchard and this list together, and run the integration "
              "tests against that version.", file=sys.stderr)

        return 1

    print(f"check-orchard-contract: {checked} fields still agree with Orchard {version}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
