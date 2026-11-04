# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
"""Tests of scripts/check-upstream.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import pathlib
import tempfile
import unittest
import urllib.error
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "check_upstream", pathlib.Path(__file__).with_name("check-upstream.py")
)
check_upstream = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_upstream)


class TokenTest(unittest.TestCase):
    def test_the_github_api_gets_the_token(self) -> None:
        self.assertTrue(check_upstream.sends_token("https://api.github.com/repos/a/b/tags"))

    def test_another_host_that_names_the_api_does_not(self) -> None:
        for url in (
            "https://evil.example/?api.github.com",
            "https://api.github.com.evil.example/repos",
            "https://evil.example/api.github.com/repos",
            "https://user@evil.example/#api.github.com",
        ):
            with self.subTest(url=url):
                self.assertFalse(check_upstream.sends_token(url))

    def test_the_registry_does_not(self) -> None:
        self.assertFalse(check_upstream.sends_token("https://ghcr.io/v2/a/b/tags/list"))

    def test_plain_http_does_not(self) -> None:
        self.assertFalse(check_upstream.sends_token("http://api.github.com/repos/a/b"))


def http_error(code: int) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("https://api.github.com/x", code, "error", {}, None)


class LookupTest(unittest.TestCase):
    def test_a_value_has_no_problem(self) -> None:
        self.assertEqual(check_upstream.lookup(lambda: "v1.2.3"), ("v1.2.3", ""))

    def test_each_kind_of_fault_is_a_problem_and_not_a_traceback(self) -> None:
        faults = {
            "HTTP 403": http_error(403),
            "cannot reach": urllib.error.URLError("no DNS"),
            "cannot reach the server: timed out": TimeoutError("timed out"),
            "unexpected shape": KeyError("tag_name"),
            "no release": check_upstream.LookupFailed("a/b has no release and no tag"),
        }
        for words, fault in faults.items():
            with self.subTest(words=words):

                def fetch(fault=fault):
                    raise fault

                current, problem = check_upstream.lookup(fetch)
                self.assertEqual(current, "unknown")
                self.assertIn(words, problem)


class RowTest(unittest.TestCase):
    def test_a_newer_version_moved(self) -> None:
        row = check_upstream.version_row("Tart", "2.41.1", "2.42.0", "")
        self.assertEqual((row.moved, row.status), (True, "yes"))

    def test_the_same_version_did_not(self) -> None:
        row = check_upstream.version_row("Tart", "2.41.1", "v2.41.1", "")
        self.assertEqual((row.moved, row.status), (False, "no"))

    def test_a_failed_lookup_is_not_a_no(self) -> None:
        row = check_upstream.version_row("Tart", "2.41.1", "unknown", "HTTP 403")
        self.assertEqual((row.moved, row.status), (False, "could not check"))

    def test_a_tag_with_no_version_is_not_a_no(self) -> None:
        row = check_upstream.version_row("Tart", "2.41.1", "nightly", "")
        self.assertEqual(row.status, "could not check")
        self.assertIn("nightly", row.problem)

    def test_a_digest_moves_only_when_it_was_read(self) -> None:
        pinned = "sha256:" + "a" * 64
        self.assertEqual(check_upstream.digest_row("base", pinned, pinned, "").status, "no")
        self.assertEqual(
            check_upstream.digest_row("base", pinned, "sha256:" + "b" * 64, "").status, "yes"
        )
        self.assertEqual(
            check_upstream.digest_row("base", pinned, "unknown", "HTTP 406").status,
            "could not check",
        )


class ReportTest(unittest.TestCase):
    def run_report(self, rows: list) -> tuple[int, str, str, str]:
        with tempfile.TemporaryDirectory() as directory:
            step_output = pathlib.Path(directory, "output")
            out, err = io.StringIO(), io.StringIO()
            with (
                mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(step_output)}),
                contextlib.redirect_stdout(out),
                contextlib.redirect_stderr(err),
            ):
                code = check_upstream.report(rows)
            return code, out.getvalue(), err.getvalue(), step_output.read_text(encoding="utf-8")

    def test_the_documents_agree_only_when_every_dependency_was_checked(self) -> None:
        code, out, _, outputs = self.run_report([check_upstream.Row("Tart", "1", "1", False)])
        self.assertEqual(code, 0)
        self.assertIn("the documents agree", out)
        self.assertEqual(outputs, "moved=false\nunchecked=false\n")

    def test_a_failed_lookup_is_loud_and_exits_3(self) -> None:
        rows = [
            check_upstream.Row("Tart", "1", "1", False),
            check_upstream.Row("Orchard", "1", "unknown", False, "HTTP 403 from x"),
        ]
        code, out, err, outputs = self.run_report(rows)
        self.assertEqual(code, check_upstream.UNCHECKED_EXIT)
        self.assertNotIn("the documents agree", out)
        self.assertIn("| Orchard | 1 | unknown | could not check |", out)
        self.assertIn("Orchard: HTTP 403 from x", err)
        self.assertEqual(outputs, "moved=false\nunchecked=true\n")

    def test_a_move_is_still_reported_beside_a_failed_lookup(self) -> None:
        rows = [
            check_upstream.Row("Tart", "1", "2", True),
            check_upstream.Row("Orchard", "1", "unknown", False, "timed out"),
        ]
        code, out, _, outputs = self.run_report(rows)
        self.assertEqual(code, check_upstream.UNCHECKED_EXIT)
        self.assertIn("1 dependency change(s)", out)
        self.assertEqual(outputs, "moved=true\nunchecked=true\n")

    def test_main_reads_the_documents_and_reports_an_outage(self) -> None:
        def down(*_args):
            raise urllib.error.URLError("no network")

        with (
            mock.patch.object(check_upstream, "get_json", down),
            mock.patch.object(check_upstream.urllib.request, "urlopen", down),
            mock.patch.dict(os.environ, {}, clear=False),
            contextlib.redirect_stdout(io.StringIO()) as out,
            contextlib.redirect_stderr(io.StringIO()),
        ):
            os.environ.pop("GITHUB_OUTPUT", None)
            code = check_upstream.main([])
        self.assertEqual(code, check_upstream.UNCHECKED_EXIT)
        self.assertEqual(out.getvalue().count("could not check"), 7)


if __name__ == "__main__":
    unittest.main()
