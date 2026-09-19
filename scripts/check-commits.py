#!/usr/bin/env python3
"""Check that commit messages follow Conventional Commits 1.0.0.

CONTRIBUTING.md states the rules. This script is the gate for them (test strategy, T-123). It
reads the commit range and changes nothing.

Usage:
    scripts/check-commits.py [<base-ref>]

The default base reference is origin/main. A pull request checks its own commits, so CI gives the
merge base of the target branch.
"""

from __future__ import annotations

import re
import subprocess
import sys

TYPES = ["feat", "fix", "docs", "test", "refactor", "perf", "build", "ci", "chore", "revert"]

SUBJECT = re.compile(
    r"^(?P<type>" + "|".join(TYPES) + r")"
    r"(?:\((?P<scope>[a-z0-9][a-z0-9-]*)\))?"
    r"(?P<breaking>!)?"
    r": (?P<description>.+)$"
)

MAX_SUBJECT = 72
MAX_BODY = 72
SEPARATOR = "\x1e"


def commits(base: str) -> list[tuple[str, str]]:
    revs = subprocess.run(["git", "rev-list", f"{base}..HEAD"],
                          capture_output=True, text=True, check=True).stdout.split()
    out = []
    for rev in revs:
        message = subprocess.run(["git", "log", "-1", "--format=%B", rev],
                                 capture_output=True, text=True, check=True).stdout
        out.append((rev[:8], message.rstrip("\n")))
    return out


def check(rev: str, message: str) -> list[str]:
    problems = []
    lines = message.split("\n")
    subject = lines[0]

    match = SUBJECT.match(subject)
    if not match:
        problems.append(
            f"subject does not follow Conventional Commits: {subject!r}. "
            f"Use <type>[(scope)][!]: <description>, with a type of {', '.join(TYPES)}"
        )
    else:
        description = match.group("description")
        if len(subject) > MAX_SUBJECT:
            problems.append(f"subject is {len(subject)} characters, the limit is {MAX_SUBJECT}")
        if description[0].isupper():
            problems.append("description starts with a capital letter")
        if description.endswith("."):
            problems.append("description ends with a full stop")

    if len(lines) > 1 and lines[1].strip():
        problems.append("the line after the subject must be empty")

    for number, line in enumerate(lines[1:], 2):
        if len(line) > MAX_BODY and "http" not in line and not line.startswith("    "):
            problems.append(f"body line {number} is {len(line)} characters, the limit is {MAX_BODY}")

    if "BREAKING CHANGE" in message and match and not match.group("breaking"):
        problems.append("a BREAKING CHANGE footer needs '!' before the colon in the subject")

    return [f"{rev}: {p}" for p in problems]


def main() -> int:
    base = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    try:
        found = commits(base)
    except subprocess.CalledProcessError:
        print(f"check-commits: cannot read the range {base}..HEAD", file=sys.stderr)
        return 2

    if not found:
        print(f"check-commits: no commit in {base}..HEAD")
        return 0

    problems = [p for rev, message in found for p in check(rev, message)]
    if problems:
        print(f"check-commits: {len(problems)} problem(s) in {len(found)} commit(s)\n",
              file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1

    print(f"check-commits: {len(found)} commit(s) follow Conventional Commits")
    return 0


if __name__ == "__main__":
    sys.exit(main())
