# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests of scripts/check-docs.py.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "check_docs", pathlib.Path(__file__).with_name("check-docs.py"))
check_docs = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_docs)


class CheckDocsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name).resolve()
        (self.root / "docs/adrs").mkdir(parents=True)
        for number in range(1, 15):
            (self.root / f"docs/adrs/{number:04d}-x.md").write_text("# x\n")
        self.saved = (check_docs.ROOT, check_docs.ADR_DIR, check_docs.GENERATED)
        check_docs.ROOT = self.root
        check_docs.ADR_DIR = self.root / "docs/adrs"
        check_docs.GENERATED = set()
        check_docs.errors.clear()
        self.addCleanup(self.restore)

    def restore(self) -> None:
        check_docs.ROOT, check_docs.ADR_DIR, check_docs.GENERATED = self.saved
        check_docs.errors.clear()

    def write(self, name: str, text: str) -> None:
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def test_a_file_in_images_is_checked(self) -> None:
        self.write("images/README.md", "x" * 101 + "\n")
        check_docs.check_width()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("images/README.md:1", check_docs.errors[0])

    def test_a_url_line_skips_only_the_url(self) -> None:
        url = "https://example.com/" + "a" * 150
        self.write("README.md", f"See [the page]({url}) for details.\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)
        self.write("README.md", f"{'word ' * 21}see {url} for details.\n")
        check_docs.check_width()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def test_a_link_target_with_parentheses_leaves_no_stray_bracket(self) -> None:
        target = "https://example.com/Foo_(bar)_" + "a" * 150
        self.write("README.md", f"{'word ' * 15}[the page]({target}) end.\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)

    def test_an_adr_range_must_match_the_files(self) -> None:
        self.write("README.md", "The set runs from ADR-0001 to ADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("ADR-0014", check_docs.errors[0])

    def test_a_full_adr_range_and_a_subset_range_pass(self) -> None:
        self.write("README.md",
                   "The set runs from ADR-0001 to ADR-0014.\n"
                   "ADR-0002 to ADR-0008 are accepted.\n")
        check_docs.check_adr_ranges()
        self.assertEqual([], check_docs.errors)

    def test_a_wrapped_or_backticked_adr_range_is_checked(self) -> None:
        self.write("README.md", "The set runs from ADR-0001 to\nADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)
        self.assertIn("README.md:1", check_docs.errors[0])
        check_docs.errors.clear()
        self.write("README.md", "The set runs from `ADR-0001` to `ADR-0009`.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def test_an_adr_file_and_a_dated_history_line_are_exempt_from_the_range_rule(self) -> None:
        self.write("docs/adrs/0014-y.md", "The set runs from ADR-0001 to ADR-0009.\n")
        self.write("README.md",
                   "On 2026-09-19 the header said ADR-0001 to ADR-0009. It is fixed now.\n")
        check_docs.check_adr_ranges()
        self.assertEqual([], check_docs.errors)
        self.write("README.md", "On 2026-09-19 the header was fixed. It says ADR-0001 to ADR-0009.\n")
        check_docs.check_adr_ranges()
        self.assertEqual(1, len(check_docs.errors), check_docs.errors)

    def test_a_fenced_block_inside_a_list_item_is_code_for_the_width_check(self) -> None:
        self.write("README.md", "1. Run:\n\n   ```sh\n   " + "x" * 120 + "\n   ```\n")
        check_docs.check_width()
        self.assertEqual([], check_docs.errors)


if __name__ == "__main__":
    unittest.main()
