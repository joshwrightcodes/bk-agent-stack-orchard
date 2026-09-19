#!/usr/bin/env python3
"""Check the dependency rules of this repository.

Two of these rules are licence rules, not preferences. The project publishes an image under the
AGPL-3.0, and Orchard carries a licence with a field-of-use restriction, so no Orchard code may
enter the binary (ADR-0010).

Checks:

  1. go.mod names no Orchard module, and has no replace directive for one;
  2. no file of the Orchard repository is in the tree;
  3. go.mod declares the Go version that the documents state;
  4. github.com/buildkite/stacksapi, when go.mod names it, is v1.2.0 or later.
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
GO_MOD = ROOT / "go.mod"
DESIGN = ROOT / "docs/architecture/target-design.md"

ORCHARD = ("github.com/cirruslabs/orchard", "github.com/openai/orchard")
MINIMUM_STACKSAPI = (1, 2, 0)

errors: list[str] = []


def stated_go_version() -> str:
    match = re.search(r"^\* \*\*Go version:\*\* ([0-9.]+)\.$", DESIGN.read_text(), re.M)
    if not match:
        print("check-deps: the target design no longer states a Go version", file=sys.stderr)
        raise SystemExit(2)
    return match.group(1)


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
