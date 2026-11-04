#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
"""Report an upstream release that the documents must answer.

Several accepted facts in this project carry a version and a date: the minimum Orchard version,
the Tart version, the Buildkite agent line, the Stacks API client, and the macOS images that the
job VM images build on. This script compares those facts with the current upstream state and
writes a report.

It reports only. A version change can need a decision, so it changes no document.

The expected values come from the documents themselves, not from a copy in this script. When a
document changes its pinned version, this check follows it. When a document no longer states a
version in the expected shape, this check fails, because the report would otherwise be silent.
For the same reason, a dependency whose upstream state this check could not read (an outage, a
rate limit, a refused request, or a tag with no version) is "could not check", never "no".

Usage:
    scripts/check-upstream.py [--output <file>]

Exit status:
    0  every dependency was checked, whatever it found (moved or not)
    2  a document did not give an expected value, which is a defect in this script or in the
       document
    3  the upstream state of at least one dependency could not be read; the table, --output,
       and the step outputs are still written for the dependencies that were checked

In GitHub Actions it writes the step outputs moved=true|false (a checked dependency moved) and
unchecked=true|false (exit status 3).
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable
from typing import NamedTuple

ROOT = pathlib.Path(__file__).resolve().parent.parent
DESIGN = ROOT / "docs/architecture/target-design.md"
BASELINE = ROOT / "docs/architecture/system-design-baseline.md"
# The Tart that the Mac hosts install. An accepted ADR never changes, so it cannot be the source.
HOSTS = ROOT / "deploy/hosts/roles/orchard_worker/defaults/main.yml"
IMAGE_BUILD = ROOT / "images/build.sh"

TIMEOUT = 20
UNCHECKED_EXIT = 3

# ghcr.io answers a HEAD of a manifest only for a media type that the request accepts. The tag
# that the images follow can be an index or a single manifest, in the OCI or the Docker format.
MANIFEST_TYPES = ", ".join(
    (
        "application/vnd.oci.image.index.v1+json",
        "application/vnd.oci.image.manifest.v1+json",
        "application/vnd.docker.distribution.manifest.list.v2+json",
        "application/vnd.docker.distribution.manifest.v2+json",
    )
)


class LookupFailed(Exception):
    """The upstream state of a dependency could not be read."""


class Row(NamedTuple):
    name: str
    expected: str
    current: str
    moved: bool
    problem: str = ""  # Why the check could not read the upstream state; empty when it could.

    @property
    def status(self) -> str:
        if self.problem:
            return "could not check"
        return "yes" if self.moved else "no"


def sends_token(url: str) -> bool:
    """Only the GitHub API gets the token: the scheme and the host must match exactly.

    A test of the text alone would also give the token to a URL that only names the API, such as
    a host whose name starts with it, or a query that contains it.
    """
    parts = urllib.parse.urlsplit(url)
    return parts.scheme == "https" and parts.hostname == "api.github.com"


def get_json(url: str, headers: dict[str, str] | None = None):
    request = urllib.request.Request(url, headers=headers or {})
    token = os.environ.get("GITHUB_TOKEN")
    if token and sends_token(url):
        request.add_header("Authorization", f"Bearer {token}")
    request.add_header("User-Agent", "bk-agent-stack-orchard-upstream-watch")
    with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
        return json.load(response)


def latest_release(repo: str) -> str:
    """The newest release tag of a GitHub repository, or the newest tag when it has no release."""
    try:
        return get_json(f"https://api.github.com/repos/{repo}/releases/latest")["tag_name"]
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    tags = get_json(f"https://api.github.com/repos/{repo}/tags?per_page=1")
    if not tags:
        raise LookupFailed(f"{repo} has no release and no tag")
    return tags[0]["name"]


def ghcr_digest(image: str, tag: str) -> str:
    """The digest of a tag of a public image in the GitHub container registry."""
    token_url = f"https://ghcr.io/token?scope=repository:{image}:pull&service=ghcr.io"
    token = get_json(token_url)["token"]
    request = urllib.request.Request(
        f"https://ghcr.io/v2/{image}/manifests/{tag}",
        method="HEAD",
        headers={"Authorization": f"Bearer {token}", "Accept": MANIFEST_TYPES},
    )
    with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
        digest = response.headers.get("Docker-Content-Digest")
    if not digest:
        raise LookupFailed(f"ghcr.io gave no digest for {image}:{tag}")
    return digest


def lookup(fetch: Callable[..., str], *args: str) -> tuple[str, str]:
    """The upstream value and an empty problem, or "unknown" and why the lookup failed.

    Each kind of fault gives a row, not a traceback, so one failed dependency does not hide the
    others.
    """
    try:
        return fetch(*args), ""
    except urllib.error.HTTPError as error:
        # HTTPError keeps the URL in filename on every Python version, and in url only on some.
        return "unknown", f"HTTP {error.code} from {error.filename}"
    except urllib.error.URLError as error:
        return "unknown", f"cannot reach the server: {error.reason}"
    except (TimeoutError, OSError) as error:
        return "unknown", f"cannot reach the server: {error}"
    except (KeyError, IndexError, TypeError, ValueError) as error:
        return "unknown", f"an answer of an unexpected shape: {error!r}"
    except LookupFailed as error:
        return "unknown", str(error)


def stated(path: pathlib.Path, pattern: str, what: str) -> str:
    match = re.search(pattern, path.read_text(encoding="utf-8"))
    if not match:
        print(f"check-upstream: {path.relative_to(ROOT)} no longer states {what}", file=sys.stderr)
        raise SystemExit(2)
    return match.group(1)


def normalise(version: str) -> tuple[int, ...]:
    return tuple(int(part) for part in re.findall(r"\d+", version))


def version_row(name: str, expected: str, current: str, problem: str) -> Row:
    """Compare an upstream version with the version that a document states."""
    if not problem and not normalise(current):
        problem = f"the upstream tag {current!r} holds no version"
    moved = not problem and normalise(current) > normalise(expected)
    return Row(name, expected, current, moved, problem)


def digest_row(name: str, pinned: str, current: str, problem: str) -> Row:
    """Compare an upstream digest with the digest that a document pins."""
    return Row(name, pinned[:19], current[:19], not problem and current != pinned, problem)


def collect() -> list[Row]:
    orchard_min = stated(
        DESIGN, r"minimum Orchard version MUST be ([0-9.]+)", "a minimum Orchard version"
    )
    tart_version = stated(HOSTS, r'(?m)^orchard_worker_tart_version: "([0-9.]+)"', "a Tart version")
    agent_version = stated(BASELINE, r"v(\d+\.\d+\.\d+) current", "a Buildkite agent version")
    stacks_version = stated(
        DESIGN, r"`github.com/buildkite/stacksapi` v([0-9.]+)", "a stacksapi version"
    )

    rows: list[Row] = []
    for name, repo, expected in (
        ("Orchard", "openai/orchard", orchard_min),
        ("Tart", "openai/tart", tart_version),
        ("Buildkite agent", "buildkite/agent", agent_version),
        ("stacksapi", "buildkite/stacksapi", stacks_version),
    ):
        rows.append(version_row(name, expected, *lookup(latest_release, repo)))

    # The tools that images/build.sh adds to the images, pinned by version.
    for name, repo, variable in (
        ("Tart guest agent", "openai/tart-guest-agent", "GUEST_AGENT_VERSION"),
        ("Homebrew", "Homebrew/brew", "HOMEBREW_VERSION"),
    ):
        expected = stated(IMAGE_BUILD, rf'(?m)^{variable}="([0-9.]+)"', f"the {name} version")
        rows.append(version_row(name, expected, *lookup(latest_release, repo)))

    # The base of the job VM images, pinned by digest in images/build.sh. A new digest of the tag
    # that it follows is a new macOS release to build and test against.
    image, tag = "cirruslabs/macos-golden-gate-vanilla", "latest"
    pinned = stated(
        IMAGE_BUILD,
        rf'(?m)^BASE="ghcr.io/{re.escape(image)}@(sha256:[0-9a-f]{{64}})"',
        "the base of the images",
    )
    rows.append(
        digest_row(f"{image.split('/')[1]}:{tag}", pinned, *lookup(ghcr_digest, image, tag))
    )
    return rows


def table(rows: list[Row]) -> str:
    lines = ["| Dependency | The documents state | Upstream now | Moved |", "|---|---|---|---|"]
    lines += [f"| {row.name} | {row.expected} | {row.current} | {row.status} |" for row in rows]
    return "\n".join(lines)


def report(rows: list[Row], output: pathlib.Path | None = None) -> int:
    """Print the table and the summary, write the outputs, and return the exit status."""
    moved = [row for row in rows if row.moved]
    unchecked = [row for row in rows if row.problem]
    text = table(rows)

    print(text)
    print()
    if moved:
        print(f"check-upstream: {len(moved)} dependency change(s) need an answer in the documents")
    if unchecked:
        print(f"check-upstream: could not check {len(unchecked)} dependency(ies):", file=sys.stderr)
        for row in unchecked:
            print(f"  {row.name}: {row.problem}", file=sys.stderr)
    if not moved and not unchecked:
        print("check-upstream: the documents agree with upstream")

    if output:
        output.write_text(text + "\n", encoding="utf-8")

    step_output = os.environ.get("GITHUB_OUTPUT")
    if step_output:
        with open(step_output, "a", encoding="utf-8") as handle:
            handle.write(f"moved={'true' if moved else 'false'}\n")
            handle.write(f"unchecked={'true' if unchecked else 'false'}\n")

    return UNCHECKED_EXIT if unchecked else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="check-upstream")
    parser.add_argument("--output", type=pathlib.Path)
    arguments = parser.parse_args(argv)
    return report(collect(), arguments.output)


if __name__ == "__main__":
    sys.exit(main())
