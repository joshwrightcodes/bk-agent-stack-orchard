#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Check the version pins and the licence copies of this repository.

Checks:

  1. the Orchard of mise.toml, which the skeleton journey runs, is the minimum Orchard version
     that the design states, and every platform of it carries a SHA-256;
  2. the image of deploy/orchard/image takes the same version and the same SHA-256 of each Linux
     archive, so the fleet runs the Orchard that the tests run;
  3. the Mac hosts of deploy/hosts install the same version, with the same SHA-256 of the macOS
     archive, so the workers run the Orchard that the tests run;
  4. the Mac hosts install at least the minimum Tart version that the design states;
  5. the Helm chart carries copies of LICENSE and NOTICE that equal the files of the root, so
     that helm package includes them;
  6. the GoReleaser action of the release workflow runs the GoReleaser version of mise.toml, so
     that the release builds with the GoReleaser that the local release task runs.
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
MISE = ROOT / "mise.toml"
DESIGN = ROOT / "docs/architecture/target-design.md"
ORCHARD_IMAGE = ROOT / "deploy/orchard/image/Dockerfile"
CHART = ROOT / "deploy/helm/bk-agent-stack-orchard"
HOSTS = ROOT / "deploy/hosts/roles/orchard_worker/defaults/main.yml"
RELEASE = ROOT / ".github/workflows/release.yml"

errors: list[str] = []


def stated_orchard_version() -> str:
    match = re.search(r"minimum Orchard version MUST be ([0-9.]+)", DESIGN.read_text())
    if not match:
        print(
            "check-deps: the target design no longer states a minimum Orchard version",
            file=sys.stderr,
        )
        raise SystemExit(2)
    return match.group(1)


def check_orchard_tool() -> None:
    """Rule 1: the Orchard that the tests run is the Orchard that the design pins.

    A regular expression reads the one table that this rule needs, because the Python of a
    developer can predate the TOML parser of the standard library.
    """
    if not MISE.exists():
        return

    text = MISE.read_text(encoding="utf-8")

    table = re.search(
        r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"', text, re.M
    )
    if not table:
        errors.append("mise.toml pins no Orchard, and the skeleton journey needs one")
        return

    expected = stated_orchard_version()
    if table.group(1) != expected:
        errors.append(
            f"mise.toml pins Orchard {table.group(1)}, the design states {expected}. The tests "
            f"must run the Orchard that the fleet runs"
        )

    platforms = re.findall(
        r"^([a-z0-9-]+)\s*=\s*\{([^}]*)\}",
        text.split('[tools."github:openai/orchard".platforms]', 1)[-1],
        re.M,
    )
    if not platforms:
        errors.append("mise.toml names no platform for Orchard, so no install is verified")

    for platform, options in platforms:
        if not re.search(r'checksum\s*=\s*"sha256:[0-9a-f]{64}"', options):
            errors.append(
                f"mise.toml installs Orchard on {platform} with no SHA-256, so a tag that moves "
                f"would change the binary without a failed install"
            )


def check_orchard_image() -> None:
    """Rule 2: the image of the Orchard controller agrees with the Orchard of mise.toml."""
    if not ORCHARD_IMAGE.exists() or not MISE.exists():
        return

    image = ORCHARD_IMAGE.read_text(encoding="utf-8")
    mise = MISE.read_text(encoding="utf-8")

    version = re.search(r"^ARG ORCHARD_VERSION=(\S+)$", image, re.M)
    pinned = re.search(
        r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"', mise, re.M
    )
    if not version or not pinned or version.group(1) != pinned.group(1):
        errors.append(
            f"{ORCHARD_IMAGE.relative_to(ROOT)} builds Orchard "
            f"{version.group(1) if version else 'of no version'}, and mise.toml pins "
            f"{pinned.group(1) if pinned else 'none'}"
        )

    for arch, platform in (("AMD64", "linux-x64"), ("ARM64", "linux-arm64")):
        in_image = re.search(rf"^ARG ORCHARD_SHA256_{arch}=([0-9a-f]{{64}})$", image, re.M)
        in_mise = re.search(
            rf'^{platform}\s*=\s*\{{[^}}]*checksum\s*=\s*"sha256:([0-9a-f]{{64}})"', mise, re.M
        )
        if not in_image or not in_mise or in_image.group(1) != in_mise.group(1):
            errors.append(
                f"{ORCHARD_IMAGE.relative_to(ROOT)} checks the {platform} archive against a "
                f"SHA-256 that is not the one of mise.toml"
            )


def check_orchard_hosts() -> None:
    """Rule 3: the Orchard of the Mac hosts agrees with the Orchard of mise.toml."""
    if not HOSTS.exists() or not MISE.exists():
        return

    hosts = HOSTS.read_text(encoding="utf-8")
    mise = MISE.read_text(encoding="utf-8")

    version = re.search(r'^orchard_worker_orchard_version: "([^"]+)"$', hosts, re.M)
    pinned = re.search(
        r'^\[tools\."github:openai/orchard"\]\s*\nversion\s*=\s*"([^"]+)"', mise, re.M
    )
    if not version or not pinned or version.group(1) != pinned.group(1):
        errors.append(
            f"{HOSTS.relative_to(ROOT)} installs Orchard "
            f"{version.group(1) if version else 'of no version'}, and mise.toml pins "
            f"{pinned.group(1) if pinned else 'none'}"
        )

    in_hosts = re.search(r'^orchard_worker_orchard_sha256: "([0-9a-f]{64})"$', hosts, re.M)
    in_mise = re.search(
        r'^macos-arm64\s*=\s*\{[^}]*checksum\s*=\s*"sha256:([0-9a-f]{64})"', mise, re.M
    )
    if not in_hosts or not in_mise or in_hosts.group(1) != in_mise.group(1):
        errors.append(
            f"{HOSTS.relative_to(ROOT)} checks the macos-arm64 archive against a SHA-256 that is "
            f"not the one of mise.toml"
        )


def check_tart_hosts() -> None:
    """Rule 4: the Tart of the Mac hosts is at least the minimum of the design."""
    if not HOSTS.exists():
        return

    minimum = re.search(
        r"^\*\*Tart version\.\*\* The minimum Tart version on a worker MUST be "
        r"([0-9.]+) ",
        DESIGN.read_text(encoding="utf-8"),
        re.M,
    )
    installed = re.search(
        r'^orchard_worker_tart_version: "([0-9.]+)"$', HOSTS.read_text(encoding="utf-8"), re.M
    )
    if not minimum or not installed:
        errors.append("the design states no minimum Tart version, or deploy/hosts pins none")
        return

    def version(text: str) -> tuple[int, ...]:
        return tuple(int(part) for part in text.split("."))

    if version(installed.group(1)) < version(minimum.group(1)):
        errors.append(
            f"{HOSTS.relative_to(ROOT)} installs Tart {installed.group(1)}, and the design needs "
            f"{minimum.group(1)} or later"
        )


def copy_errors(name: str, original: str, copy: str | None) -> list[str]:
    """Rule 5: a chart file equals the file of the root."""
    if copy is None:
        return [
            f"deploy/helm/bk-agent-stack-orchard/{name} is missing, so helm package omits "
            f"the licence notice"
        ]
    if copy != original:
        return [
            f"deploy/helm/bk-agent-stack-orchard/{name} differs from {name} of the root. "
            f"Copy the root file"
        ]
    return []


def check_chart_copies() -> None:
    if CHART.exists():
        for name in ("LICENSE", "NOTICE"):
            copy = CHART / name
            errors.extend(
                copy_errors(
                    name,
                    (ROOT / name).read_text(encoding="utf-8"),
                    copy.read_text(encoding="utf-8") if copy.exists() else None,
                )
            )


def goreleaser_errors(mise: str, workflow: str) -> list[str]:
    """Rule 6: the GoReleaser action runs the GoReleaser version of mise.toml."""
    pinned = re.search(r'^goreleaser = "([^"]+)"', mise, re.MULTILINE)
    if not pinned:
        return ["mise.toml pins no goreleaser version"]
    action = re.search(
        r"uses: goreleaser/goreleaser-action@[0-9a-f]{40}[^\n]*\n\s+with:\n"
        r"(?:\s+[a-z-]+:[^\n]*\n)*?\s+version: v?([0-9][^\s]*)",
        workflow,
    )
    if not action:
        return [
            ".github/workflows/release.yml has no GoReleaser action, pinned by its commit, "
            "with a version"
        ]
    if action.group(1) != pinned.group(1):
        return [
            f".github/workflows/release.yml runs GoReleaser {action.group(1)}, and mise.toml "
            f"pins {pinned.group(1)}. Set the version of the action to v{pinned.group(1)}"
        ]
    return []


def check_goreleaser() -> None:
    errors.extend(
        goreleaser_errors(MISE.read_text(encoding="utf-8"), RELEASE.read_text(encoding="utf-8"))
    )


def main() -> int:
    check_orchard_tool()
    check_orchard_image()
    check_orchard_hosts()
    check_tart_hosts()
    check_chart_copies()
    check_goreleaser()

    if errors:
        print(f"check-deps: {len(errors)} problem(s)\n", file=sys.stderr)
        for error in errors:
            print(f"  {error}", file=sys.stderr)
        return 1

    print("check-deps: the version pins and the licence copies agree")
    return 0


if __name__ == "__main__":
    sys.exit(main())
