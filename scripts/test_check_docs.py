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


if __name__ == "__main__":
    unittest.main()
