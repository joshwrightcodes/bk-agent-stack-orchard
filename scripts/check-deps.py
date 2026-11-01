#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
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
     archive, so the workers run the Orchard that the tests run;
  8. the Mac hosts install at least the minimum Tart version that the design states;
  9. NOTICE names each module that the controller binary links, the Go standard library, and the
     NOTICE text of gRPC (Apache-2.0, section 4(d));
 10. the Helm chart carries copies of LICENSE and NOTICE that equal the files of the root, so
     that helm package includes them;
 11. every tracked Go file carries the SPDX licence line of the project before its package
     clause. A generator writes the line into a generated file.
"""

from __future__ import annotations

import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
GO_MOD = ROOT / "go.mod"
MISE = ROOT / "mise.toml"
DESIGN = ROOT / "docs/architecture/target-design.md"
ORCHARD_IMAGE = ROOT / "deploy/orchard/image/Dockerfile"
NOTICE = ROOT / "NOTICE"
CHART = ROOT / "deploy/helm/bk-agent-stack-orchard"
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


def check_tart_hosts() -> None:
    """Rule 8: the Tart of the Mac hosts is at least the minimum of the design."""
    if not HOSTS.exists():
        return

    minimum = re.search(r"^\*\*Tart version\.\*\* The minimum Tart version on a worker MUST be "
                        r"([0-9.]+) ", DESIGN.read_text(encoding="utf-8"), re.M)
    installed = re.search(r'^orchard_worker_tart_version: "([0-9.]+)"$',
                          HOSTS.read_text(encoding="utf-8"), re.M)
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


def parse_modules(output: str, own: str) -> list[str]:
    """The sorted, unique module paths of the go list output, without this module."""
    return sorted({line.strip() for line in output.splitlines() if line.strip()} - {own})


def linked_modules() -> list[str] | None:
    """The modules that the controller binary links, or None when go cannot list them."""
    own = re.search(r"^module (\S+)$", GO_MOD.read_text(encoding="utf-8"), re.M)
    try:
        result = subprocess.run(
            ["go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}",
             "./cmd/bk-agent-stack-orchard"],
            cwd=ROOT, capture_output=True, text=True, check=True)
    except (OSError, subprocess.CalledProcessError) as error:
        errors.append(f"go list cannot name the modules of the binary: {error}")
        return None
    return parse_modules(result.stdout, own.group(1) if own else "")


def notice_errors(modules: list[str], notice: str) -> list[str]:
    """Rule 9: NOTICE names each linked module, the Go library, and the gRPC NOTICE text."""
    found: list[str] = []
    for module in modules:
        # The path MUST stand alone: a longer path (go.opentelemetry.io/otel/sdk) does not name
        # a shorter module (go.opentelemetry.io/otel), and the other way round.
        pattern = rf"(?<![\w./-]){re.escape(module)}(?![\w-]|[/.]\w)"
        if not re.search(pattern, notice):
            found.append(f"NOTICE does not name {module}, which the controller binary links")
    if "Go standard library" not in notice:
        found.append("NOTICE does not name the Go standard library, which the binary links")
    if "google.golang.org/grpc" in modules and "Copyright 2014 gRPC authors" not in notice:
        found.append("NOTICE lacks the NOTICE text of gRPC, which Apache-2.0 section 4(d) needs")
    return found


def copy_errors(name: str, original: str, copy: str | None) -> list[str]:
    """Rule 10: a chart file equals the file of the root."""
    if copy is None:
        return [f"deploy/helm/bk-agent-stack-orchard/{name} is missing, so helm package omits "
                f"the licence notice"]
    if copy != original:
        return [f"deploy/helm/bk-agent-stack-orchard/{name} differs from {name} of the root. "
                f"Copy the root file"]
    return []


def check_licence_notices() -> None:
    if NOTICE.exists():
        modules = linked_modules()
        if modules is not None:
            errors.extend(notice_errors(modules, NOTICE.read_text(encoding="utf-8")))
    if CHART.exists():
        for name in ("LICENSE", "NOTICE"):
            copy = CHART / name
            errors.extend(copy_errors(
                name, (ROOT / name).read_text(encoding="utf-8"),
                copy.read_text(encoding="utf-8") if copy.exists() else None))


# The text is in two parts, so that the reuse tool does not read it as a header of this file.
SPDX_LINE = "// SPDX-License-" + "Identifier: AGPL-3.0-or-later"


def spdx_errors(name: str, text: str) -> list[str]:
    """Rule 11: the SPDX line stands above the package clause of a Go file."""
    for line in text.splitlines():
        if line.startswith("package "):
            break
        if line == SPDX_LINE:
            return []
    return [f"{name} lacks the line {SPDX_LINE!r} before its package clause"]


def check_spdx_headers() -> None:
    try:
        listed = subprocess.run(["git", "ls-files", "*.go"], cwd=ROOT, capture_output=True,
                                text=True, check=True).stdout.splitlines()
    except (OSError, subprocess.CalledProcessError) as error:
        errors.append(f"git cannot list the Go files: {error}")
        return
    for name in listed:
        if (ROOT / name).is_file():
            errors.extend(spdx_errors(name, (ROOT / name).read_text(encoding="utf-8")))


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
    check_tart_hosts()
    check_licence_notices()
    check_spdx_headers()

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
