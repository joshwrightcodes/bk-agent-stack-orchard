#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Check that commit messages follow Conventional Commits 1.0.0.

CONTRIBUTING.md states the rules. This script is the gate for them. It reads the commit range
and changes nothing.

Usage:
    scripts/check-commits.py [<base-ref>]
    scripts/check-commits.py --title "<pull request title>"
    PR_BODY="<description>" scripts/check-commits.py --disclosure <base-ref>

The default base reference is origin/main. A pull request checks its own commits, so CI gives the
merge base of the target branch. Each commit of a person MUST carry a Signed-off-by trailer with
the email of the author (the Developer Certificate of Origin). A GitHub App bot is exempt. The
title mode applies the subject rules to a pull request title, with a lower limit, because GitHub
adds " (#NNN)" to the subject of the squash commit.

AI_POLICY.md states the rules for AI tools. The disclosure mode checks that the AI tools section
of a pull request description is complete, and that it does not say "none" when a commit names a
tool in Co-authored-by. No check can find a tool that nobody declared.
"""

from __future__ import annotations

import argparse
import os
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
# Conventional Commits defines the footer token as BREAKING CHANGE or BREAKING-CHANGE, followed by
# ": ". The phrase in the body text is not the footer.
BREAKING_FOOTER = re.compile(r"^BREAKING[ -]CHANGE: ", re.MULTILINE)
COAUTHOR = re.compile(
    r"^Co-authored-by:[ \t]*(.*?)[ \t]*(?:<([^<>\s]*)>)?[ \t]*$", re.MULTILINE | re.IGNORECASE
)
# An email or a whole name that a tool writes. A short name that a person can also have, such as
# Claude or Devin, counts only with the email of a tool.
TOOL_EMAIL = re.compile(
    r"^(noreply@anthropic\.com|(\d+\+)?copilot@users\.noreply\.github\.com|cursoragent@cursor\.com"
    r"|(noreply|codex)@openai\.com|noreply@chatgpt\.com|aider@aider\.chat)$",
    re.IGNORECASE,
)
TOOL_NAME = re.compile(
    r"^(claude( \d[\d.]*)? (code|opus|sonnet|haiku|fable)\b.*|(github )?copilot|cursor agent"
    r"|(openai )?codex|chatgpt|gemini (cli|code assist)|aider( \(.*\))?|windsurf"
    r"|[a-z0-9-]*(claude|copilot|cursor|codex|gemini|jules|devin)[a-z0-9-]*\[bot\])$",
    re.IGNORECASE,
)
# The automated accounts that AI_POLICY.md exempts from the disclosure. Another bot can drive an AI
# tool, so a bot account is not exempt because it is a bot.
EXEMPT_ACCOUNTS = ("renovate[bot]", "dependabot[bot]", "bk-agent-stack-orchard-release[bot]")
AI_SECTION = "AI tools"
ANY_HEADING = re.compile(r"^ {0,3}#{1,6}\s")
HTML_COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
FIELD = "\x1f"
SEPARATOR = "\x1e"


def commits(base: str) -> list[tuple[str, str, str, str]]:
    # --no-merges: a pull request event checks out refs/pull/N/merge, so the range contains a
    # merge commit that GitHub wrote. The rules apply to the commits that a person wrote.
    # --end-of-options: a base that starts with "-" is a revision, never an option of git.
    revs = subprocess.run(
        ["git", "rev-list", "--no-merges", "--end-of-options", f"{base}..HEAD"],
        capture_output=True,
        text=True,
        check=True,
    ).stdout.split()
    out = []
    for rev in revs:
        # One call: the author name, the author email, and the message, split on the unit
        # separator, which a name or an email cannot hold. The message is last and can hold
        # anything else.
        raw = subprocess.run(
            ["git", "log", "-1", f"--format=%an{FIELD}%ae{FIELD}%B", rev],
            capture_output=True,
            text=True,
            check=True,
        ).stdout
        author, email, message = raw.split(FIELD, 2)
        out.append((rev[:8], message.rstrip("\n"), author, email.strip()))
    return out


BOTS = ("dependabot[bot]", "renovate[bot]", "github-actions[bot]")


def trailer_block(message: str) -> str:
    """The trailers of a message: its last paragraph, after the subject. A line in the body or in
    a quote is not a trailer."""
    return message.rstrip("\n").split("\n\n")[-1] if "\n\n" in message else ""


def check_signoff(rev: str, message: str, email: str) -> list[str]:
    """Return the problems with the sign-off of one commit. A bot of a GitHub App is exempt."""
    if email.lower().endswith(BOT_EMAIL_SUFFIX):
        return []
    emails = [found.lower() for found in SIGNOFF.findall(trailer_block(message))]
    if not emails:
        return [f"{rev}: no Signed-off-by trailer. Commit with 'git commit -s'"]
    if email.lower() not in emails:
        return [f"{rev}: the Signed-off-by email does not match the author email {email}"]
    return []


def has_tool_coauthor(message: str) -> bool:
    """Return whether the trailers of a commit name an AI tool as a co-author."""
    return any(
        TOOL_EMAIL.match(email) or TOOL_NAME.match(name.strip('"'))
        for name, email in COAUTHOR.findall(trailer_block(message))
    )


def ai_section(body: str) -> str | None:
    """Return the text of the AI tools section of a description, or None if it has no section."""
    lines = HTML_COMMENT.sub("", body).split("\n")
    heading = f"## {AI_SECTION}"
    if heading not in [line.strip() for line in lines]:
        return None
    start = [line.strip() for line in lines].index(heading) + 1
    found = []
    for line in lines[start:]:
        if ANY_HEADING.match(line):
            break
        found.append(line)
    return "\n".join(found).strip()


def check_disclosure(body: str, assisted: list[str]) -> list[str]:
    """Return the problems with the disclosure of a pull request. `assisted` lists the commits
    that name an AI tool in Co-authored-by."""
    section = ai_section(body)
    if section is None:
        return [f"the description has no '## {AI_SECTION}' section. AI_POLICY.md states the rules"]
    if not section:
        return [
            f"the '## {AI_SECTION}' section is empty. Name each tool and say what it did, "
            "or write 'none'"
        ]
    if section.strip(" \"'`*_.").lower() == "none" and assisted:
        return [
            f"the '## {AI_SECTION}' section says none, but these commits name an AI tool in "
            f"Co-authored-by: {', '.join(assisted)}"
        ]
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
            problems.append(
                f"body line {number} is {len(line)} characters, the limit is {MAX_BODY}"
            )

    if BREAKING_FOOTER.search(trailer_block(message)) and match and not match.group("breaking"):
        problems.append("a BREAKING CHANGE footer needs '!' before the colon in the subject")

    return [f"{rev}: {p}" for p in problems]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="check-commits", description="Check commit messages against Conventional Commits."
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument(
        "base",
        nargs="?",
        default="origin/main",
        help="check the commits in <base>..HEAD (default: origin/main)",
    )
    mode.add_argument("--title", help="check a pull request title instead of commits")
    mode.add_argument(
        "--disclosure",
        metavar="BASE",
        help="check the description in PR_BODY against the commits in <BASE>..HEAD",
    )
    # argparse exits 2 on a usage error, such as --title with no value.
    arguments = parser.parse_args(argv)

    if arguments.title is not None:
        problems = check_title(arguments.title)
        if problems:
            for problem in problems:
                print(f"  {problem}", file=sys.stderr)
            return 1
        print("check-commits: the title follows Conventional Commits")
        return 0

    if arguments.disclosure is not None:
        body = os.environ.get("PR_BODY")
        if body is None:
            print("check-commits: PR_BODY is not set", file=sys.stderr)
            return 2
        if os.environ.get("PR_AUTHOR") in EXEMPT_ACCOUNTS:
            print("check-commits: AI_POLICY.md exempts this account from the disclosure")
            return 0
    base = arguments.disclosure if arguments.disclosure is not None else arguments.base
    try:
        found = commits(base)
    except subprocess.CalledProcessError as error:
        print(f"check-commits: cannot read the range {base}..HEAD", file=sys.stderr)
        if error.stderr:
            print(f"  {error.stderr.strip()}", file=sys.stderr)
        return 2

    if arguments.disclosure is not None:
        assisted = [rev for rev, message, _, _ in found if has_tool_coauthor(message)]
        problems = check_disclosure(body, assisted)
        if problems:
            for problem in problems:
                print(f"  {problem}", file=sys.stderr)
            return 1
        print("check-commits: the pull request discloses its AI tools")
        return 0

    if not found:
        print(f"check-commits: no commit in {base}..HEAD")
        return 0

    problems = [
        p
        for rev, message, author, email in found
        for p in check(rev, message, author) + check_signoff(rev, message, email)
    ]
    if problems:
        print(
            f"check-commits: {len(problems)} problem(s) in {len(found)} commit(s)\n",
            file=sys.stderr,
        )
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1

    print(f"check-commits: {len(found)} commit(s) follow Conventional Commits and carry a sign-off")
    return 0


if __name__ == "__main__":
    sys.exit(main())
