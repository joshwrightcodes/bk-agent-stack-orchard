#!/usr/bin/env python3
"""Check the dependency rules of this repository.

Two of these rules are licence rules, not preferences. The project publishes an image under the
AGPL-3.0, and Orchard carries a licence with a field-of-use restriction, so no Orchard code may
enter the binary (ADR-0010).

Checks:

  1. go.mod names no Orchard module, and has no replace directive for one;
  2. no file of the Orchard repository is in the tree;
  3. go.mod declares the Go version that the documents state;
  4. github.com/buildkite/stacksapi, when go.mod names it, is v1.2.0 or later;
  5. the Orchard of mise.toml, which the skeleton journey runs, is the minimum Orchard version
     that the design states, and every platform of it carries a SHA-256;
  6. the image of deploy/orchard/image takes the same version and the same SHA-256 of each Linux
     archive, so the fleet runs the Orchard that the tests run;
  7. the Mac hosts of deploy/hosts install the same version, with the same SHA-256 of the macOS
     archive, so the workers run the Orchard that the tests run.
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
GO_MOD = ROOT / "go.mod"
MISE = ROOT / "mise.toml"
DESIGN = ROOT / "docs/architecture/target-design.md"
ORCHARD_IMAGE = ROOT / "deploy/orchard/image/Dockerfile"
HOSTS = ROOT / "deploy/hosts/roles/orchard_worker/defaults/main.yml"

ORCHARD = ("github.com/cirruslabs/orchard", "github.com/openai/orchard")
MINIMUM_STACKSAPI = (1, 2, 0)

errors: list[str] = []


def stated_go_version() -> str:
    match = re.search(r"^\* \*\*Go version:\*\* ([0-9.]+)\.$", DESIGN.read_text(), re.M)
    if not match:
        print("check-deps: the target design no longer states a Go version", file=sys.stderr)
        raise SystemExit(2)
    return match.group(1)


def stated_orchard_version() -> str:
    match = re.search(r"minimum Orchard version MUST be ([0-9.]+)", DESIGN.read_text())
    if not match:
        print("check-deps: the target design no longer states a minimum Orchard version",
              file=sys.stderr)
        raise SystemExit(2)
    return match.group(1)


def check_orchard_tool() -> None:
    """The Orchard that the tests run is the Orchard that the design pins.

    A regular expression reads the one table that this rule needs, because the Python of a
    developer can predate the TOML parser of the standard library.
    """
    if not MISE.exists():
        return

    text = MISE.read_text(encoding="utf-8")

    table = re.search(r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"',
                      text, re.M)
    if not table:
        errors.append("mise.toml pins no Orchard, and the skeleton journey needs one")
        return

    expected = stated_orchard_version()
    if table.group(1) != expected:
        errors.append(
            f"mise.toml pins Orchard {table.group(1)}, the design states {expected}. The tests "
            f"must run the Orchard that the fleet runs"
        )

    platforms = re.findall(r'^([a-z0-9-]+)\s*=\s*\{([^}]*)\}', text.split(
        '[tools."github:openai/orchard".platforms]', 1)[-1], re.M)
    if not platforms:
        errors.append("mise.toml names no platform for Orchard, so no install is verified")

    for platform, options in platforms:
        if not re.search(r'checksum\s*=\s*"sha256:[0-9a-f]{64}"', options):
            errors.append(
                f"mise.toml installs Orchard on {platform} with no SHA-256, so a tag that moves "
                f"would change the binary without a failed install"
            )


def check_orchard_image() -> None:
    """Rule 6: the image of the Orchard controller agrees with the Orchard of mise.toml."""
    if not ORCHARD_IMAGE.exists() or not MISE.exists():
        return

    image = ORCHARD_IMAGE.read_text(encoding="utf-8")
    mise = MISE.read_text(encoding="utf-8")

    version = re.search(r'^ARG ORCHARD_VERSION=(\S+)$', image, re.M)
    pinned = re.search(r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"',
                       mise, re.M)
    if not version or not pinned or version.group(1) != pinned.group(1):
        errors.append(
            f"{ORCHARD_IMAGE.relative_to(ROOT)} builds Orchard "
            f"{version.group(1) if version else 'of no version'}, and mise.toml pins "
            f"{pinned.group(1) if pinned else 'none'}"
        )

    for arch, platform in (("AMD64", "linux-x64"), ("ARM64", "linux-arm64")):
        in_image = re.search(rf'^ARG ORCHARD_SHA256_{arch}=([0-9a-f]{{64}})$', image, re.M)
        in_mise = re.search(rf'^{platform}\s*=\s*\{{[^}}]*checksum\s*=\s*"sha256:([0-9a-f]{{64}})"',
                            mise, re.M)
        if not in_image or not in_mise or in_image.group(1) != in_mise.group(1):
            errors.append(
                f"{ORCHARD_IMAGE.relative_to(ROOT)} checks the {platform} archive against a "
                f"SHA-256 that is not the one of mise.toml"
            )


def check_orchard_hosts() -> None:
    """Rule 7: the Orchard of the Mac hosts agrees with the Orchard of mise.toml."""
    if not HOSTS.exists() or not MISE.exists():
        return

    hosts = HOSTS.read_text(encoding="utf-8")
    mise = MISE.read_text(encoding="utf-8")

    version = re.search(r'^orchard_worker_orchard_version: "([^"]+)"$', hosts, re.M)
    pinned = re.search(r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"',
                       mise, re.M)
    if not version or not pinned or version.group(1) != pinned.group(1):
        errors.append(
            f"{HOSTS.relative_to(ROOT)} installs Orchard "
            f"{version.group(1) if version else 'of no version'}, and mise.toml pins "
            f"{pinned.group(1) if pinned else 'none'}"
        )

    in_hosts = re.search(r'^orchard_worker_orchard_sha256: "([0-9a-f]{64})"$', hosts, re.M)
    in_mise = re.search(r'^macos-arm64\s*=\s*\{[^}]*checksum\s*=\s*"sha256:([0-9a-f]{64})"',
                        mise, re.M)
    if not in_hosts or not in_mise or in_hosts.group(1) != in_mise.group(1):
        errors.append(
            f"{HOSTS.relative_to(ROOT)} checks the macos-arm64 archive against a SHA-256 that is "
            f"not the one of mise.toml"
        )


def main() -> int:
    if not GO_MOD.exists():
        print("check-deps: no go.mod yet, nothing to check")
        return 0

    text = GO_MOD.read_text(encoding="utf-8")

    for module in ORCHARD:
        if module in text:
            errors.append(
                f"go.mod names {module}. The controller calls the Orchard REST API with its own "
                f"client, because the licences do not combine (ADR-0010)"
            )

    for line in text.split("\n"):
        if line.strip().startswith("replace "):
            errors.append(f"go.mod has a replace directive: {line.strip()!r}")

    expected_go = stated_go_version()
    declared = re.search(r"^go (\S+)$", text, re.M)
    if not declared:
        errors.append("go.mod declares no Go version")
    elif not declared.group(1).startswith(expected_go):
        errors.append(
            f"go.mod declares Go {declared.group(1)}, the documents state {expected_go}"
        )

    stacks = re.search(r"github\.com/buildkite/stacksapi v(\d+)\.(\d+)\.(\d+)", text)
    if stacks:
        found = tuple(int(part) for part in stacks.groups())
        if found < MINIMUM_STACKSAPI:
            wanted = ".".join(str(p) for p in MINIMUM_STACKSAPI)
            errors.append(
                f"stacksapi is v{'.'.join(stacks.groups())}, the design states v{wanted} or later"
            )

    for path in ROOT.rglob("*"):
        if ".git/" in str(path) or not path.is_file():
            continue
        if path.name in ("orchard.go", "openapi.yaml") and "docs" not in str(path):
            errors.append(f"{path.relative_to(ROOT)} looks like a file of the Orchard repository")

    check_orchard_tool()
    check_orchard_image()
    check_orchard_hosts()

    if errors:
        print(f"check-deps: {len(errors)} problem(s)\n", file=sys.stderr)
        for error in errors:
            print(f"  {error}", file=sys.stderr)
        return 1

    note = "" if stacks else " (nothing imports stacksapi yet)"
    print(f"check-deps: the dependency rules hold{note}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
