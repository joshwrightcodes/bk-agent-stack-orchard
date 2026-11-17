# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests of scripts/bump-upstream.py.

Each test runs the script on a copy of the files that it changes, taken from this checkout, so a
document that no longer has the text that the script changes fails here first.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import pathlib
import re
import shutil
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("bump_upstream", HERE / "bump-upstream.py")
bump_upstream = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bump_upstream)

FILES = [
    "images/build.sh",
    "images/README.md",
    "internal/profile/agent.go",
    "docs/user/configuration.md",
    "docs/architecture/target-design.md",
    "docs/architecture/system-design-baseline.md",
    "docs/product/requirements.md",
]
NEW_SHA = "1" * 64


class BumpTest(unittest.TestCase):
    def setUp(self) -> None:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = pathlib.Path(directory.name)
        for name in FILES:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(bump_upstream.ROOT / name, target)
        for name, value in (("ROOT", self.root), ("IMAGE_BUILD", self.root / "images/build.sh")):
            patcher = mock.patch.object(bump_upstream, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.sources = {"download": NEW_SHA, "github": NEW_SHA, "file": NEW_SHA}
        for name, key in (
            ("sha256_of", "download"),
            ("recorded_sha256", "github"),
            ("published_sha256", "file"),
        ):
            patcher = mock.patch.object(
                bump_upstream, name, lambda *_args, _key=key: self.sources[_key]
            )
            patcher.start()
            self.addCleanup(patcher.stop)

    def read(self, name: str) -> str:
        return (self.root / name).read_text(encoding="utf-8")

    def snapshot(self) -> dict[str, str]:
        return {name: self.read(name) for name in FILES}

    def run_main(self, *argv: str) -> tuple[int, str, str]:
        output = self.root / "output"
        out, err = io.StringIO(), io.StringIO()
        with (
            mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(output)}),
            contextlib.redirect_stdout(out),
            contextlib.redirect_stderr(err),
        ):
            code = bump_upstream.main([*argv, "--today", "2099-01-02"])
        text = output.read_text(encoding="utf-8") if output.exists() else ""
        return code, out.getvalue() + err.getvalue(), text

    def pinned(self, variable: str) -> str:
        return bump_upstream.pinned(variable)[0]

    def test_the_agent_moves_in_every_copy_of_its_pin(self) -> None:
        old = self.pinned("AGENT")
        major = old.split(".")[0]
        new = f"{major}.999.1"
        code, log, outputs = self.run_main(
            "buildkite-agent", "--version", new, "--released", "2099-01-01"
        )
        self.assertEqual(code, 0, log)
        self.assertIn(f"title=fix(deps): update the Buildkite agent to {new}\n", outputs)
        self.assertIn("branch=upstream/buildkite-agent\n", outputs)

        url = f"download/v{new}/buildkite-agent-darwin-arm64-{new}.tar.gz"
        self.assertIn(f'AGENT_VERSION="{new}"', self.read("images/build.sh"))
        self.assertIn(f'AGENT_SHA256="{NEW_SHA}"', self.read("images/build.sh"))
        for name in ("internal/profile/agent.go", "docs/user/configuration.md"):
            self.assertIn(url, self.read(name))
            self.assertIn(NEW_SHA, self.read(name))
        self.assertIn(
            f"v{new} current (2099-01-01, checked 2099-01-02)",
            self.read("docs/architecture/system-design-baseline.md"),
        )
        self.assertIn(
            f"v{new} is current (2099-01-01, checked 2099-01-02)",
            self.read("docs/product/requirements.md"),
        )
        design = self.read("docs/architecture/target-design.md")
        self.assertIn(f"; v{new} on 2099-01-01, checked 2099-01-02)", design)
        self.assertEqual(design.count(url), 2)
        for name in FILES:
            self.assertNotIn(f"buildkite-agent-darwin-arm64-{old}.tar.gz", self.read(name), name)

        body = pathlib.Path(re.search(r"(?m)^body=(.+)$", outputs).group(1))
        self.addCleanup(body.unlink)
        text = body.read_text(encoding="utf-8")
        self.assertIn("## AI tools\n\nnone\n", text)
        self.assertIn(NEW_SHA, text)

    def test_the_newest_release_already_pinned_changes_nothing(self) -> None:
        before = self.snapshot()
        old = self.pinned("HOMEBREW")
        code, log, outputs = self.run_main("homebrew", "--version", old, "--released", "2099-01-01")
        self.assertEqual(code, 0, log)
        self.assertEqual(outputs, "changed=false\n")
        self.assertEqual(self.snapshot(), before)

    def test_a_new_major_line_of_the_agent_is_left_to_a_person(self) -> None:
        before = self.snapshot()
        major = int(self.pinned("AGENT").split(".")[0]) + 1
        code, log, outputs = self.run_main(
            "buildkite-agent", "--version", f"{major}.0.0", "--released", "2099-01-01"
        )
        self.assertEqual(code, 0, log)
        self.assertIn("new major line", log)
        self.assertEqual(outputs, "changed=false\n")
        self.assertEqual(self.snapshot(), before)

    def test_a_sha256_that_disagrees_changes_nothing_and_exits_4(self) -> None:
        before = self.snapshot()
        self.sources["github"] = "2" * 64
        code, log, outputs = self.run_main(
            "homebrew", "--version", "999.0.0", "--released", "2099-01-01"
        )
        self.assertEqual(code, 4, log)
        self.assertIn("GitHub gives", log)
        self.assertEqual(outputs, "")
        self.assertEqual(self.snapshot(), before)

    def test_a_download_that_nothing_confirms_exits_4(self) -> None:
        self.sources["github"] = ""
        code, log, _ = self.run_main("homebrew", "--version", "999.0.0", "--released", "2099-01-01")
        self.assertEqual(code, 4, log)
        self.assertIn("nothing but the download", log)

    def test_a_document_that_lost_its_text_changes_nothing_and_exits_2(self) -> None:
        name = "docs/product/requirements.md"
        (self.root / name).write_text(
            self.read(name).replace("is current", "is the newest"), encoding="utf-8"
        )
        before = self.snapshot()
        major = self.pinned("AGENT").split(".")[0]
        code, log, outputs = self.run_main(
            "buildkite-agent", "--version", f"{major}.999.0", "--released", "2099-01-01"
        )
        self.assertEqual(code, 2, log)
        self.assertIn("docs/product/requirements.md", log)
        self.assertEqual(outputs, "")
        self.assertEqual(self.snapshot(), before)

    def test_each_image_tool_moves_its_pin_and_its_row(self) -> None:
        for key, variable, row in (
            ("homebrew", "HOMEBREW", "| Homebrew {} |"),
            ("tart-guest-agent", "GUEST_AGENT", "| The Tart guest agent {} |"),
        ):
            with self.subTest(dependency=key):
                code, log, _ = self.run_main(
                    key, "--version", "999.0.0", "--released", "2099-01-01"
                )
                self.assertEqual(code, 0, log)
                self.assertIn(f'{variable}_VERSION="999.0.0"', self.read("images/build.sh"))
                self.assertIn(f'{variable}_SHA256="{NEW_SHA}"', self.read("images/build.sh"))
                self.assertIn(row.format("999.0.0"), self.read("images/README.md"))


class ChecksumFileTest(unittest.TestCase):
    def test_the_line_of_the_archive_gives_its_sha256(self) -> None:
        text = (
            f"{'a' * 64}  buildkite-agent-darwin-amd64-1.0.0.tar.gz\n"
            f"{'B' * 64}  buildkite-agent-darwin-arm64-1.0.0.tar.gz\n"
        )

        class Answer(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *_):
                return False

        with mock.patch.object(
            bump_upstream.urllib.request, "urlopen", lambda *_a, **_k: Answer(text.encode())
        ):
            value = bump_upstream.published_sha256(
                "https://example.invalid/SUMS",
                "https://example.invalid/buildkite-agent-darwin-arm64-1.0.0.tar.gz",
            )
        self.assertEqual(value, "b" * 64)


if __name__ == "__main__":
    unittest.main()
