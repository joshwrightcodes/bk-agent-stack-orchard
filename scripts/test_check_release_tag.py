"""Tests of scripts/check-release-tag.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_release_tag", pathlib.Path(__file__).with_name("check-release-tag.py"))
check_release_tag = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_release_tag)


class CheckTest(unittest.TestCase):
    def test_a_release_gives_its_version(self) -> None:
        self.assertEqual(check_release_tag.check("v0.3.1"), ("0.3.1", []))

    def test_a_release_candidate_gives_its_version(self) -> None:
        self.assertEqual(check_release_tag.check("v0.4.0-rc.1"), ("0.4.0-rc.1", []))

    def test_the_first_release_is_accepted(self) -> None:
        self.assertEqual(check_release_tag.check("v0.1.0"), ("0.1.0", []))

    def test_a_major_version_of_1_needs_the_maintainer(self) -> None:
        _, problems = check_release_tag.check("v1.0.0")
        self.assertTrue(any("major version" in p for p in problems), problems)

    def test_a_tag_without_the_v_fails(self) -> None:
        _, problems = check_release_tag.check("0.3.1")
        self.assertTrue(any("vMAJOR.MINOR.PATCH" in p for p in problems), problems)

    def test_a_tag_with_two_parts_fails(self) -> None:
        _, problems = check_release_tag.check("v0.3")
        self.assertTrue(problems)

    def test_a_leading_zero_fails(self) -> None:
        _, problems = check_release_tag.check("v0.03.1")
        self.assertTrue(problems)

    def test_a_suffix_other_than_a_release_candidate_fails(self) -> None:
        _, problems = check_release_tag.check("v0.3.1-beta")
        self.assertTrue(problems)


if __name__ == "__main__":
    unittest.main()
