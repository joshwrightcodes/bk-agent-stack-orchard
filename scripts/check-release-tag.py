#!/usr/bin/env python3
"""Check that a release tag follows the version rules of CONTRIBUTING.md.

The release workflow runs this before it builds anything. A tag that fails publishes nothing.

Usage:
    scripts/check-release-tag.py <tag>

It prints the version without the "v", which the workflow gives to the chart. It changes nothing.
"""

from __future__ import annotations

import re
import sys

TAG = re.compile(
    r"^v(?P<major>0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)"
    r"(?:-rc\.(?:0|[1-9]\d*))?$"
)


def check(tag: str) -> tuple[str, list[str]]:
    match = TAG.match(tag)
    if not match:
        return "", [f"{tag!r} is not vMAJOR.MINOR.PATCH, with an optional -rc.N suffix"]
    # The major version stays 0 until the maintainer decides that the project is stable. That
    # decision removes this rule in the same pull request.
    if match.group("major") != "0":
        return "", [f"{tag!r} has the major version {match.group('major')}, and the major version "
                    "stays 0 until the maintainer decides (CONTRIBUTING.md)"]
    return tag[1:], []


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-release-tag.py <tag>", file=sys.stderr)
        return 2
    version, problems = check(sys.argv[1])
    for problem in problems:
        print(f"check-release-tag: {problem}", file=sys.stderr)
    if problems:
        return 1
    print(version)
    return 0


if __name__ == "__main__":
    sys.exit(main())
