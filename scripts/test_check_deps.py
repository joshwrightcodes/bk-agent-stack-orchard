# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of the chart copy rule of scripts/check-deps.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_deps", pathlib.Path(__file__).with_name("check-deps.py"))
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


if __name__ == "__main__":
    unittest.main()
