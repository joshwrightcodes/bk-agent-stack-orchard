#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Check that commit messages follow Conventional Commits 1.0.0.

CONTRIBUTING.md states the rules. This script is the gate for them. It reads the commit range
and changes nothing.

Usage:
    scripts/check-commits.py [<base-ref>]
    scripts/check-commits.py --title "<pull request title>"

The default base reference is origin/main. A pull request checks its own commits, so CI gives the
merge base of the target branch. Each commit of a person MUST carry a Signed-off-by trailer with
the email of the author (the Developer Certificate of Origin). A GitHub App bot is exempt. The
title mode applies the subject rules to a pull request title, with a lower limit, because GitHub
adds " (#NNN)" to the subject of the squash commit.
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

# release-please hides a commit by its type, not by its scope, so a fix with one of these scopes
# would reach the release notes. The scope names the workflows.
CI_SCOPES = ("ci",)

MAX_SUBJECT = 72
MAX_BODY = 72
MAX_TITLE = 65
BOT_EMAIL_SUFFIX = "[bot]@users.noreply.github.com"
SIGNOFF = re.compile(r"^Signed-off-by: .+? <([^<>\s]+)>\s*$", re.MULTILINE | re.IGNORECASE)
FIELD = "\x1f"
SEPARATOR = "\x1e"


def commits(base: str) -> list[tuple[str, str, str, str]]:
    # --no-merges: a pull request event checks out refs/pull/N/merge, so the range contains a
    # merge commit that GitHub wrote. The rules apply to the commits that a person wrote.
    revs = subprocess.run(["git", "rev-list", "--no-merges", f"{base}..HEAD"],
                          capture_output=True, text=True, check=True).stdout.split()
    out = []
    for rev in revs:
        # One call: the author name, the author email, and the message, split on the unit
        # separator, which a name or an email cannot hold. The message is last and can hold
        # anything else.
        raw = subprocess.run(["git", "log", "-1", f"--format=%an{FIELD}%ae{FIELD}%B", rev],
                             capture_output=True, text=True, check=True).stdout
        author, email, message = raw.split(FIELD, 2)
        out.append((rev[:8], message.rstrip("\n"), author, email.strip()))
    return out


BOTS = ("dependabot[bot]", "renovate[bot]", "github-actions[bot]")


def check_signoff(rev: str, message: str, email: str) -> list[str]:
    """Return the problems with the sign-off of one commit. A bot of a GitHub App is exempt."""
    if email.lower().endswith(BOT_EMAIL_SUFFIX):
        return []
    # Only the trailer block counts: the last paragraph. A line in the body or in a quote does not.
    trailers = message.rstrip("\n").split("\n\n")[-1] if "\n\n" in message else ""
    emails = [found.lower() for found in SIGNOFF.findall(trailers)]
    if not emails:
        return [f"{rev}: no Signed-off-by trailer. Commit with 'git commit -s'"]
    if email.lower() not in emails:
        return [f"{rev}: the Signed-off-by email does not match the author email {email}"]
    return []


def check_title(title: str) -> list[str]:
    """Return the problems with a pull request title."""
    return check("title", title, subject_limit=MAX_TITLE)


def check(rev: str, message: str, author: str = "", subject_limit: int = 0) -> list[str]:
    problems = []
    lines = message.split("\n")
    subject = lines[0]
    # A bot writes the module path into the subject, and a long path passes the limit. The type
    # and the shape still apply, because its configuration sets them.
    subject_limit = subject_limit or (200 if author in BOTS else MAX_SUBJECT)

    match = SUBJECT.match(subject)
    if not match:
        problems.append(
            f"subject does not follow Conventional Commits: {subject!r}. "
            f"Use <type>[(scope)][!]: <description>, with a type of {', '.join(TYPES)}"
        )
    else:
        description = match.group("description")
        if match.group("type") == "fix" and match.group("scope") in CI_SCOPES:
            problems.append(
                f"fix({match.group('scope')}) would show in the release notes. "
                "Use ci: for a change of a workflow"
            )
        if len(subject) > subject_limit:
            problems.append(f"subject is {len(subject)} characters, the limit is {subject_limit}")
        # Dependabot writes "Bump …" with a capital letter, and its configuration sets only the
        # type and the scope, so the case of the description is a rule for people. Dependabot
        # still opens the security updates that GitHub gives it.
        if description[0].isupper() and author not in BOTS:
            problems.append("description starts with a capital letter")
        if description.endswith("."):
            problems.append("description ends with a full stop")

    if len(lines) > 1 and lines[1].strip():
        problems.append("the line after the subject must be empty")

    for number, line in enumerate(lines[1:], 2):
        if author in BOTS:
            break
        if len(line) > MAX_BODY and "http" not in line and not line.startswith("    "):
            problems.append(f"body line {number} is {len(line)} characters, the limit is {MAX_BODY}")

    if "BREAKING CHANGE" in message and match and not match.group("breaking"):
        problems.append("a BREAKING CHANGE footer needs '!' before the colon in the subject")

    return [f"{rev}: {p}" for p in problems]


def main() -> int:
    if len(sys.argv) == 3 and sys.argv[1] == "--title":
        problems = check_title(sys.argv[2])
        if problems:
            for problem in problems:
                print(f"  {problem}", file=sys.stderr)
            return 1
        print("check-commits: the title follows Conventional Commits")
        return 0

    base = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    try:
        found = commits(base)
    except subprocess.CalledProcessError:
        print(f"check-commits: cannot read the range {base}..HEAD", file=sys.stderr)
        return 2

    if not found:
        print(f"check-commits: no commit in {base}..HEAD")
        return 0

    problems = [p for rev, message, author, email in found
                for p in check(rev, message, author) + check_signoff(rev, message, email)]
    if problems:
        print(f"check-commits: {len(problems)} problem(s) in {len(found)} commit(s)\n",
              file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1

    print(f"check-commits: {len(found)} commit(s) follow Conventional Commits and carry a sign-off")
    return 0


if __name__ == "__main__":
    sys.exit(main())
