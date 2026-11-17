#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests for scripts/link-ids.py. Run: python3 -m unittest discover -s scripts -p 'test_*.py'"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

SPEC = importlib.util.spec_from_file_location(
    "link_ids", pathlib.Path(__file__).with_name("link-ids.py")
)
link_ids = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(link_ids)


class RewrapTest(unittest.TestCase):
    def test_a_requirement_title_keeps_its_anchor_on_the_line_above(self) -> None:
        title = "**PR-001: " + " ".join(["word"] * 30) + "**"
        lines = ["", '<a id="pr-001"></a>', title, ""]
        link_ids.rewrap(lines, 2)
        self.assertEqual('<a id="pr-001"></a>', lines[1])
        self.assertTrue(lines[2].startswith("**PR-001: "), lines[2])
        self.assertTrue(all(link_ids.width(line) <= 100 for line in lines), lines)

    def test_a_paragraph_above_an_anchor_does_not_take_it(self) -> None:
        text = " ".join(["word"] * 30)
        lines = ["", text, '<a id="pr-001"></a>', "**PR-001: Title**", ""]
        link_ids.rewrap(lines, 1)
        self.assertIn('<a id="pr-001"></a>', lines)
        self.assertEqual("**PR-001: Title**", lines[lines.index('<a id="pr-001"></a>') + 1])


if __name__ == "__main__":
    unittest.main()
