#!/usr/bin/env python3
"""Report an upstream release that the documents must answer.

Several accepted facts in this project carry a version and a date: the minimum Orchard version,
the Tart version, the Buildkite agent line, the Stacks API client, and the macOS images that the
job VM images build on. This script compares those facts with the current upstream state and writes a report.

It reports only. A version change can need a decision, so it changes no document.

The expected values come from the documents themselves, not from a copy in this script. When a
document changes its pinned version, this check follows it. When a document no longer states a
version in the expected shape, this check fails, because the report would otherwise be silent.

Usage:
    scripts/check-upstream.py [--output <file>]

Exit status is 0 when the check ran, whatever it found. It is 2 when a document did not give an
expected value, which is a defect in this script or in the document.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
DESIGN = ROOT / "docs/architecture/target-design.md"
BASELINE = ROOT / "docs/architecture/system-design-baseline.md"
ADR_TART = ROOT / "docs/adrs/0005-run-and-schedule-vms-with-tart-and-orchard.md"
IMAGE_BUILD = ROOT / "images/build.sh"

TIMEOUT = 20


def get_json(url: str, headers: dict[str, str] | None = None):
    request = urllib.request.Request(url, headers=headers or {})
    token = os.environ.get("GITHUB_TOKEN")
    if token and "api.github.com" in url:
        request.add_header("Authorization", f"Bearer {token}")
    request.add_header("User-Agent", "bk-agent-stack-orchard-upstream-watch")
    with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
        return json.load(response)


def latest_release(repo: str) -> str | None:
    """The newest release tag of a GitHub repository, or the newest tag when it has no release."""
    try:
        return get_json(f"https://api.github.com/repos/{repo}/releases/latest")["tag_name"]
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    tags = get_json(f"https://api.github.com/repos/{repo}/tags?per_page=1")
    return tags[0]["name"] if tags else None


def ghcr_tags(image: str) -> list[str]:
    """The tags of a public image in the GitHub container registry."""
    token_url = f"https://ghcr.io/token?scope=repository:{image}:pull&service=ghcr.io"
    try:
        token = get_json(token_url)["token"]
        listing = get_json(f"https://ghcr.io/v2/{image}/tags/list?n=100",
                           {"Authorization": f"Bearer {token}"})
    except urllib.error.HTTPError:
        return []
    return listing.get("tags") or []


def stated(path: pathlib.Path, pattern: str, what: str) -> str:
    match = re.search(pattern, path.read_text(encoding="utf-8"))
    if not match:
        print(f"check-upstream: {path.relative_to(ROOT)} no longer states {what}", file=sys.stderr)
        raise SystemExit(2)
    return match.group(1)


def normalise(version: str) -> tuple[int, ...]:
    return tuple(int(part) for part in re.findall(r"\d+", version))


def ghcr_digest(image: str, tag: str) -> str | None:
    """The digest of a tag of a public image in the GitHub container registry, or None."""
    token_url = f"https://ghcr.io/token?scope=repository:{image}:pull&service=ghcr.io"
    try:
        token = get_json(token_url)["token"]
        request = urllib.request.Request(
            f"https://ghcr.io/v2/{image}/manifests/{tag}", method="HEAD",
            headers={"Authorization": f"Bearer {token}",
                     "Accept": "application/vnd.oci.image.manifest.v1+json"})
        with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
            return response.headers.get("Docker-Content-Digest")
    except urllib.error.HTTPError:
        return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path)
    arguments = parser.parse_args()

    orchard_min = stated(DESIGN, r"minimum Orchard version MUST be ([0-9.]+)",
                         "a minimum Orchard version")
    tart_version = stated(ADR_TART, r"Tart ([0-9.]+) \(", "a Tart version")
    agent_version = stated(BASELINE, r"v(\d+\.\d+\.\d+) current", "a Buildkite agent version")
    stacks_version = stated(DESIGN, r"`github.com/buildkite/stacksapi` v([0-9.]+)",
                            "a stacksapi version")

    rows: list[tuple[str, str, str, bool]] = []

    for name, repo, expected in (
        ("Orchard", "openai/orchard", orchard_min),
        ("Tart", "openai/tart", tart_version),
        ("Buildkite agent", "buildkite/agent", agent_version),
        ("stacksapi", "buildkite/stacksapi", stacks_version),
    ):
        current = latest_release(repo) or "unknown"
        moved = normalise(current) > normalise(expected)
        rows.append((name, expected, current, moved))

    # The tools that images/build.sh adds to the images, pinned by version.
    for name, repo, variable in (
        ("Tart guest agent", "openai/tart-guest-agent", "GUEST_AGENT_VERSION"),
        ("Homebrew", "Homebrew/brew", "HOMEBREW_VERSION"),
    ):
        expected = stated(IMAGE_BUILD, rf'(?m)^{variable}="([0-9.]+)"', f"the {name} version")
        current = latest_release(repo) or "unknown"
        rows.append((name, expected, current, normalise(current) > normalise(expected)))

    # The base of the job VM images, pinned by digest in images/build.sh. A new digest of the tag
    # that it follows is a new macOS release to build and test against.
    image, tag = "cirruslabs/macos-golden-gate-vanilla", "latest"
    pinned = stated(IMAGE_BUILD, rf'(?m)^BASE="ghcr.io/{re.escape(image)}@(sha256:[0-9a-f]{{64}})"',
                    "the base of the images")
    current = ghcr_digest(image, tag) or "unknown"
    rows.append((f"{image.split('/')[1]}:{tag}", pinned[:19], current[:19],
                 current not in ("unknown", pinned)))

    moved = [row for row in rows if row[3]]

    lines = ["| Dependency | The documents state | Upstream now | Moved |",
             "|---|---|---|---|"]
    for name, expected, current, has_moved in rows:
        lines.append(f"| {name} | {expected} | {current} | {'yes' if has_moved else 'no'} |")
    table = "\n".join(lines)

    print(table)
    print()
    if moved:
        print(f"check-upstream: {len(moved)} dependency change(s) need an answer in the documents")
    else:
        print("check-upstream: the documents agree with upstream")

    if arguments.output:
        arguments.output.write_text(table + "\n", encoding="utf-8")

    step_output = os.environ.get("GITHUB_OUTPUT")
    if step_output:
        with open(step_output, "a", encoding="utf-8") as handle:
            handle.write(f"moved={'true' if moved else 'false'}\n")

    return 0


if __name__ == "__main__":
    sys.exit(main())
