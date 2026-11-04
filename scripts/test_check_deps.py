# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests of the chart copy rule and the GoReleaser rule of scripts/check-deps.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_deps", pathlib.Path(__file__).with_name("check-deps.py")
)
check_deps = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_deps)


class ChartCopyTest(unittest.TestCase):
    def test_a_chart_copy_that_differs_fails(self) -> None:
        errors = check_deps.copy_errors("NOTICE", "a\n", "b\n")
        self.assertEqual(len(errors), 1)

    def test_a_missing_chart_copy_fails(self) -> None:
        self.assertEqual(len(check_deps.copy_errors("LICENSE", "a\n", None)), 1)

    def test_an_equal_copy_passes(self) -> None:
        self.assertEqual(check_deps.copy_errors("LICENSE", "a\n", "a\n"), [])


MISE = 'go = "1.27.3"\ngoreleaser = "2.18.2"\n'

WORKFLOW = """
      - name: build the archives, the SBOMs, the checksums, and the image
        uses: goreleaser/goreleaser-action@a4f02ab5355a589de60d917a659ce617ed19b3de # v7.3.0
        with:
          version: v{version}
          args: release --clean
"""


class GoReleaserVersionTest(unittest.TestCase):
    def test_the_same_version_passes(self) -> None:
        workflow = WORKFLOW.format(version="2.18.2")
        self.assertEqual(check_deps.goreleaser_errors(MISE, workflow), [])

    def test_a_different_version_fails(self) -> None:
        workflow = WORKFLOW.format(version="2.17.0")
        self.assertEqual(len(check_deps.goreleaser_errors(MISE, workflow)), 1)

    def test_a_workflow_without_the_action_fails(self) -> None:
        self.assertEqual(len(check_deps.goreleaser_errors(MISE, "run: goreleaser release")), 1)

    def test_a_mise_without_goreleaser_fails(self) -> None:
        workflow = WORKFLOW.format(version="2.18.2")
        self.assertEqual(len(check_deps.goreleaser_errors('go = "1.27.3"\n', workflow)), 1)


if __name__ == "__main__":
    unittest.main()
