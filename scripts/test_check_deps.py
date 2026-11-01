"""Tests of the licence rules of scripts/check-deps.py.

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

OWN = "github.com/example/own"
NOTICE = """
  * github.com/goccy/go-yaml - MIT
  * go.opentelemetry.io/otel - Apache License 2.0
The Go standard library - BSD-3-Clause
"""


class ParseModulesTest(unittest.TestCase):
    def test_the_list_is_unique_and_leaves_out_this_module(self) -> None:
        output = f"{OWN}\n\ngithub.com/goccy/go-yaml\n{OWN}\ngithub.com/goccy/go-yaml\n"
        self.assertEqual(check_deps.parse_modules(output, OWN), ["github.com/goccy/go-yaml"])


class NoticeTest(unittest.TestCase):
    def test_notice_names_every_linked_module(self) -> None:
        modules = ["github.com/goccy/go-yaml", "go.opentelemetry.io/otel",
                   "github.com/missing/module"]
        errors = check_deps.notice_errors(modules, NOTICE)
        self.assertEqual(len(errors), 1)
        self.assertIn("github.com/missing/module", errors[0])

    def test_a_longer_path_does_not_name_a_shorter_module(self) -> None:
        notice = NOTICE + "  * go.opentelemetry.io/otel/sdk - Apache License 2.0\n"
        errors = check_deps.notice_errors(["go.opentelemetry.io/otel/trace"], notice)
        self.assertEqual(len(errors), 1)

    def test_a_shorter_path_does_not_name_a_longer_module(self) -> None:
        notice = "go.opentelemetry.io/otel/sdk\nThe Go standard library\n"
        errors = check_deps.notice_errors(["go.opentelemetry.io/otel"], notice)
        self.assertEqual(len(errors), 1)

    def test_a_module_before_a_full_stop_counts(self) -> None:
        errors = check_deps.notice_errors(["github.com/goccy/go-yaml"], NOTICE + "x/go-yaml.\n"
                                          + "see github.com/goccy/go-yaml.\n")
        self.assertEqual(errors, [])

    def test_notice_names_the_go_standard_library(self) -> None:
        errors = check_deps.notice_errors([], "no library here\n")
        self.assertEqual(len(errors), 1)
        self.assertIn("standard library", errors[0])

    def test_notice_carries_the_grpc_notice_text(self) -> None:
        errors = check_deps.notice_errors(["google.golang.org/grpc"],
                                          NOTICE + "google.golang.org/grpc\n")
        self.assertTrue(any("NOTICE text of gRPC" in error for error in errors))
        errors = check_deps.notice_errors(["google.golang.org/grpc"],
                                          NOTICE + "google.golang.org/grpc\nCopyright 2014 gRPC "
                                          "authors.\n")
        self.assertEqual(errors, [])


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
