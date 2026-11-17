#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Move a pin of images/build.sh to the newest upstream release, with its SHA-256.

Renovate finds a new release of these tools but cannot compute the SHA-256 of a release archive,
so the weekly upstream job runs this script and opens a draft pull request with its change. A
person then tests the change on a Mac, because a hosted runner cannot start a macOS VM.

The script changes the pin in images/build.sh, every copy of the old pin in the files that repeat
it, and the version and the dates of the documents that state the current release. It does not
change a sentence that records what was read or tested at an older version: a person checks that
again, and the pull request says so.

A new major version of the Buildkite agent is a new release line, which the target design names,
so the script leaves it to the upstream issue and changes nothing.

Usage:
    scripts/bump-upstream.py <dependency> [--version V --released YYYY-MM-DD] [--today YYYY-MM-DD]

    <dependency> is buildkite-agent, homebrew, or tart-guest-agent. --version and --released skip
    the lookup of the newest release, for a test or a manual run.

Exit status:
    0  the pin moved, or there was nothing to do
    2  a file no longer has the text that the script changes, which is a defect in this script or
       in the file
    3  the newest release or its archive could not be read
    4  the SHA-256 of the archive differs from the one that the release publishes

In GitHub Actions it writes the step outputs changed=true|false, and with a change also version,
previous, branch, title, and body (a file that holds the body of the pull request).
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import importlib.util
import os
import pathlib
import re
import sys
import tempfile
import urllib.error
import urllib.request
from collections.abc import Callable
from typing import NamedTuple

ROOT = pathlib.Path(__file__).resolve().parent.parent
IMAGE_BUILD = ROOT / "images/build.sh"
TIMEOUT = 120
CHUNK = 1 << 20

# The lookups and the token rule of the upstream watch, so that both scripts read upstream alike.
_SPEC = importlib.util.spec_from_file_location(
    "check_upstream", pathlib.Path(__file__).with_name("check-upstream.py")
)
check_upstream = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(check_upstream)

DATE = r"\d{4}-\d{2}-\d{2}"


class Failed(Exception):
    """The script cannot make the change. The exit status says why."""

    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


class Release(NamedTuple):
    version: str
    released: str  # YYYY-MM-DD


class Edit(NamedTuple):
    """A change of one file: each pattern MUST match, so that a changed document fails loudly."""

    path: str
    pattern: str  # A regular expression.
    replacement: str
    count: int = 0  # The number of matches that MUST exist; 0 means one or more.


class Dependency(NamedTuple):
    name: str  # The name in the title of the pull request.
    repo: str
    variable: str  # The version variable of images/build.sh; its SHA-256 is <prefix>_SHA256.
    tag: Callable[[str], str]  # The release tag of a version.
    archive: Callable[[str], str]  # The URL of the release archive that the image installs.
    commit_type: str
    published_sha256: Callable[[str], str] | None  # The URL of a checksum file, if any.
    edits: Callable[[str, str, str, str, Release, str], list[Edit]]
    notes: list[str]  # What a person checks before the pull request leaves draft.


def agent_edits(old: str, new: str, old_sha: str, new_sha: str, release: Release, today: str):
    old_url = f"download/v{old}/buildkite-agent-darwin-arm64-{old}.tar.gz"
    new_url = f"download/v{new}/buildkite-agent-darwin-arm64-{new}.tar.gz"
    edits = [
        Edit("internal/profile/agent.go", re.escape(old_url), new_url, 1),
        Edit("internal/profile/agent.go", re.escape(old_sha), new_sha, 1),
        Edit("docs/user/configuration.md", re.escape(old_url), new_url, 1),
        Edit("docs/user/configuration.md", re.escape(old_sha), new_sha, 1),
        Edit("docs/architecture/target-design.md", re.escape(old_url), new_url, 2),
        Edit("docs/architecture/target-design.md", re.escape(old_sha), new_sha, 2),
        Edit(
            "docs/architecture/target-design.md",
            rf"\(checked {DATE},\nthe same pin as `images/build.sh`\)",
            f"(checked {today},\nthe same pin as `images/build.sh`)",
            1,
        ),
        Edit(
            "docs/architecture/target-design.md",
            rf"; v\d+\.\d+\.\d+ on {DATE}, checked {DATE}\)",
            f"; v{new} on {release.released}, checked {today})",
            1,
        ),
        Edit(
            "docs/product/requirements.md",
            rf"v\d+\.\d+\.\d+ is current \({DATE}, checked {DATE}\)",
            f"v{new} is current ({release.released}, checked {today})",
            1,
        ),
        # scripts/check-upstream.py reads the version of the agent from this sentence.
        Edit(
            "docs/architecture/system-design-baseline.md",
            rf"v\d+\.\d+\.\d+ current \({DATE}, checked {DATE}\)",
            f"v{new} current ({release.released}, checked {today})",
            1,
        ),
        Edit(
            "images/README.md", re.escape(f"`buildkite-agent` {old} "), f"`buildkite-agent` {new} "
        ),
    ]
    return edits


def homebrew_edits(old: str, new: str, old_sha: str, new_sha: str, release: Release, today: str):
    return [Edit("images/README.md", re.escape(f"| Homebrew {old} |"), f"| Homebrew {new} |", 1)]


def guest_agent_edits(old: str, new: str, old_sha: str, new_sha: str, release: Release, today: str):
    return [
        Edit(
            "images/README.md",
            re.escape(f"| The Tart guest agent {old} |"),
            f"| The Tart guest agent {new} |",
            1,
        )
    ]


DEPENDENCIES = {
    "buildkite-agent": Dependency(
        name="the Buildkite agent",
        repo="buildkite/agent",
        variable="AGENT",
        tag=lambda v: f"v{v}",
        archive=lambda v: (
            "https://github.com/buildkite/agent/releases/download/"
            f"v{v}/buildkite-agent-darwin-arm64-{v}.tar.gz"
        ),
        # A new default agent changes what every VM runs, so it is a fix and a patch release.
        commit_type="fix(deps)",
        published_sha256=lambda v: (
            f"https://github.com/buildkite/agent/releases/download/v{v}/buildkite-agent-{v}.SHA256SUMS"
        ),
        edits=agent_edits,
        notes=[
            "Read the release notes of each release since the old pin, for a security fix, a "
            "flag of `buildkite-agent start` that changed, or a change of the checkout.",
            "At the new tag, check that `clicommand/agent_start.go` still reads the environment "
            "variables that the startup script sets, and that `internal/job/checkout_ssh.go` "
            "still sets the credential helper with `git config --global`. Then update the "
            "sentences that name the version that was read: the target design (sections 5 and 8), "
            "`internal/vmspec/vmspec.go`, and `docs/user/install.md`.",
            "Run the real-VM checks of `docs/testing-on-real-vms.md` with `agent.source: "
            "download`, and with a rebuilt image and `agent.source: image`.",
        ],
    ),
    "homebrew": Dependency(
        name="Homebrew",
        repo="Homebrew/brew",
        variable="HOMEBREW",
        tag=lambda v: v,
        archive=lambda v: f"https://github.com/Homebrew/brew/releases/download/{v}/Homebrew.pkg",
        commit_type="build(images)",
        published_sha256=None,
        edits=homebrew_edits,
        notes=[
            "Read the release notes since the old pin.",
            "Build the pilot image on a Mac (`mise run pilot-image`), and run a job on it.",
        ],
    ),
    "tart-guest-agent": Dependency(
        name="the Tart guest agent",
        repo="openai/tart-guest-agent",
        variable="GUEST_AGENT",
        tag=lambda v: f"v{v}",
        archive=lambda v: (
            "https://github.com/openai/tart-guest-agent/releases/download/"
            f"v{v}/tart-guest-agent-darwin-all.tar.gz"
        ),
        commit_type="build(images)",
        published_sha256=None,
        edits=guest_agent_edits,
        notes=[
            "Read the release notes since the old pin. The startup-script transport needs the "
            "guest agent (ADR-0009).",
            "Build both images on a Mac, and run the real-VM checks of "
            "`docs/testing-on-real-vms.md`.",
        ],
    ),
}


def version_of(tag: str) -> str:
    match = re.fullmatch(r"v?(\d+\.\d+\.\d+)", tag)
    if not match:
        raise Failed(3, f"the release tag {tag!r} is not a version")
    return match.group(1)


def newest_release(repo: str) -> Release:
    try:
        answer = check_upstream.get_json(f"https://api.github.com/repos/{repo}/releases/latest")
        return Release(version_of(answer["tag_name"]), answer["published_at"][:10])
    except (urllib.error.URLError, OSError, KeyError, TypeError) as error:
        raise Failed(3, f"cannot read the newest release of {repo}: {error}") from error


def recorded_sha256(repo: str, tag: str, name: str) -> str:
    """The SHA-256 that GitHub records for an asset of a release, or "" when it records none."""
    try:
        answer = check_upstream.get_json(f"https://api.github.com/repos/{repo}/releases/tags/{tag}")
        assets = {asset["name"]: asset.get("digest") or "" for asset in answer["assets"]}
    except (urllib.error.URLError, OSError, KeyError, TypeError) as error:
        raise Failed(3, f"cannot read the release {tag} of {repo}: {error}") from error
    if name not in assets:
        raise Failed(3, f"the release {tag} of {repo} has no asset {name}")
    digest = assets[name]
    return digest.removeprefix("sha256:").lower() if digest.startswith("sha256:") else ""


def sha256_of(url: str) -> str:
    """The SHA-256 of a download. Only the GitHub API gets the token, so a download gets none."""
    digest = hashlib.sha256()
    request = urllib.request.Request(url, headers={"User-Agent": "bk-agent-stack-orchard-bump"})
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
            while chunk := response.read(CHUNK):
                digest.update(chunk)
    except (urllib.error.URLError, OSError) as error:
        raise Failed(3, f"cannot download {url}: {error}") from error
    return digest.hexdigest()


def published_sha256(url: str, archive: str) -> str:
    """The SHA-256 that a checksum file of the release gives for one archive."""
    request = urllib.request.Request(url, headers={"User-Agent": "bk-agent-stack-orchard-bump"})
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
            text = response.read().decode("utf-8")
    except (urllib.error.URLError, OSError, UnicodeDecodeError) as error:
        raise Failed(3, f"cannot read {url}: {error}") from error
    name = archive.rsplit("/", 1)[1]
    for line in text.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1].lstrip("*").lstrip("./") == name:
            return parts[0].lower()
    raise Failed(3, f"{url} gives no SHA-256 for {name}")


def pinned(variable: str) -> tuple[str, str]:
    text = IMAGE_BUILD.read_text(encoding="utf-8")
    version = re.search(rf'(?m)^{variable}_VERSION="(\d+\.\d+\.\d+)"$', text)
    sha = re.search(rf'(?m)^{variable}_SHA256="([0-9a-f]{{64}})"$', text)
    if not version or not sha:
        raise Failed(2, f"images/build.sh no longer pins {variable}_VERSION and {variable}_SHA256")
    return version.group(1), sha.group(1)


def apply(edits: list[Edit]) -> None:
    """Make every edit, or none: each file is written only when all patterns matched."""
    changed: dict[str, str] = {}
    for edit in edits:
        text = changed.get(edit.path)
        if text is None:
            text = (ROOT / edit.path).read_text(encoding="utf-8")
        found = len(re.findall(edit.pattern, text))
        if found == 0 or (edit.count and found != edit.count):
            want = edit.count or "one or more"
            raise Failed(2, f"{edit.path}: {found} match(es) of {edit.pattern!r}, want {want}")
        # A function as the replacement keeps a backslash in the new text literal.
        changed[edit.path] = re.sub(edit.pattern, lambda _, new=edit.replacement: new, text)
    for path, text in changed.items():
        (ROOT / path).write_text(text, encoding="utf-8")


def normalise(version: str) -> tuple[int, ...]:
    return tuple(int(part) for part in version.split("."))


def body(
    dependency: Dependency, old: str, new: str, sha: str, release: Release, sources: list[str]
) -> str:
    tags = f"https://github.com/{dependency.repo}/releases/tag/{dependency.tag(new)}"
    compare = f"https://github.com/{dependency.repo}/compare/{dependency.tag(old)}...{dependency.tag(new)}"
    checks = "\n".join(f"- [ ] {note}" for note in dependency.notes)
    agree = " and ".join(sources)
    return f"""## What changes

The weekly upstream job moves {dependency.name} from {old} to {new} (released {release.released}).
The SHA-256 of `{dependency.archive(new).rsplit("/", 1)[1]}` is `{sha}`. The download of the
archive gave it, and {agree} gave the same.

Release: {tags}
Changes: {compare}

## Why

Renovate cannot compute the SHA-256 of a release archive, so `scripts/bump-upstream.py` makes
this change. A hosted runner cannot start a macOS VM, so a person tests it before it leaves draft:

{checks}

## AI tools

none

## Traceability

* Work unit: none
* Requirements: none
* Evidence: CI on this pull request, and the checks above.
"""


def bump(key: str, release: Release | None, today: str) -> dict[str, str]:
    dependency = DEPENDENCIES[key]
    old, old_sha = pinned(dependency.variable)
    release = release or newest_release(dependency.repo)
    new = release.version

    if normalise(new) <= normalise(old):
        print(f"bump-upstream: {dependency.name} {old} is the newest release")
        return {"changed": "false"}
    if key == "buildkite-agent" and normalise(new)[0] != normalise(old)[0]:
        print(
            f"bump-upstream: {dependency.name} {new} starts a new major line; the upstream issue "
            "reports it, and a person decides"
        )
        return {"changed": "false"}

    # The download gives the SHA-256. GitHub records one for each release asset, and some
    # releases publish a checksum file too. Every source that exists MUST agree, and at least one
    # besides the download MUST exist.
    archive = dependency.archive(new)
    name = archive.rsplit("/", 1)[1]
    sha = sha256_of(archive)
    sources = {"GitHub": recorded_sha256(dependency.repo, dependency.tag(new), name)}
    if dependency.published_sha256:
        sources["the checksum file"] = published_sha256(dependency.published_sha256(new), archive)
    sources = {source: value for source, value in sources.items() if value}
    if not sources:
        raise Failed(4, f"nothing but the download gives a SHA-256 for {archive}")
    for source, value in sources.items():
        if value != sha:
            raise Failed(4, f"{archive} has SHA-256 {sha}, and {source} gives {value}")

    edits = [
        Edit(
            "images/build.sh",
            rf'(?m)^{dependency.variable}_VERSION="{re.escape(old)}"$',
            f'{dependency.variable}_VERSION="{new}"',
            1,
        ),
        Edit(
            "images/build.sh",
            rf'(?m)^{dependency.variable}_SHA256="{old_sha}"$',
            f'{dependency.variable}_SHA256="{sha}"',
            1,
        ),
    ]
    edits += dependency.edits(old, new, old_sha, sha, release, today)
    apply(edits)

    title = f"{dependency.commit_type}: update {dependency.name} to {new}"
    print(f"bump-upstream: {title}")
    return {
        "changed": "true",
        "version": new,
        "previous": old,
        "branch": f"upstream/{key}",
        "title": title,
        "text": body(dependency, old, new, sha, release, list(sources)),
    }


def write_outputs(outputs: dict[str, str]) -> None:
    step_output = os.environ.get("GITHUB_OUTPUT")
    text = outputs.pop("text", None)
    if text is not None:
        handle, path = tempfile.mkstemp(prefix="bump-upstream-", suffix=".md")
        with os.fdopen(handle, "w", encoding="utf-8") as body_file:
            body_file.write(text)
        outputs["body"] = path
    if step_output:
        with open(step_output, "a", encoding="utf-8") as handle:
            for key, value in outputs.items():
                handle.write(f"{key}={value}\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="bump-upstream")
    parser.add_argument("dependency", choices=sorted(DEPENDENCIES))
    parser.add_argument("--version")
    parser.add_argument("--released")
    parser.add_argument("--today", default=datetime.date.today().isoformat())
    arguments = parser.parse_args(argv)
    if bool(arguments.version) != bool(arguments.released):
        parser.error("--version and --released go together")

    release = Release(arguments.version, arguments.released) if arguments.version else None
    try:
        outputs = bump(arguments.dependency, release, arguments.today)
    except Failed as failure:
        print(f"bump-upstream: {failure}", file=sys.stderr)
        return failure.status
    write_outputs(outputs)
    return 0


if __name__ == "__main__":
    sys.exit(main())
