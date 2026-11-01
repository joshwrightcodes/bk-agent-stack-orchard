# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of scripts/check-upstream.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_upstream", pathlib.Path(__file__).with_name("check-upstream.py"))
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


if __name__ == "__main__":
    unittest.main()
