"""Tests of the publication step of scripts/release-finish.sh.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'

The tests source the script, which defines publish_release and runs nothing else. A stub gh, first
on PATH, records its arguments.
"""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("release-finish.sh")


def publish(tag: str) -> list[str]:
    """Run publish_release for a tag, and return the arguments that gh received."""
    with tempfile.TemporaryDirectory() as tmp:
        record = pathlib.Path(tmp, "args")
        stub = pathlib.Path(tmp, "gh")
        stub.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$RECORD"\n')
        stub.chmod(0o755)
        env = {**os.environ, "PATH": f"{tmp}:{os.environ['PATH']}", "RECORD": str(record)}
        subprocess.run(
            ["bash", "-c", f'source "{SCRIPT}"; publish_release repos/o/r 42 {tag}'],
            env=env, check=True, capture_output=True, text=True)
        return record.read_text().split("\n")[:-1]


def pairs(args: list[str]) -> list[tuple[str, str]]:
    return [(args[i], args[i + 1]) for i in range(len(args) - 1) if args[i] in ("-F", "-f")]


class PublishTest(unittest.TestCase):
    def test_a_0_x_tag_is_a_pre_release_and_not_the_latest(self) -> None:
        args = publish("v0.1.0")
        self.assertIn(("-F", "draft=false"), pairs(args))
        self.assertIn(("-F", "prerelease=true"), pairs(args))
        # make_latest is a string enum of the API, so it goes through -f, not as a JSON boolean.
        self.assertIn(("-f", "make_latest=false"), pairs(args))
        self.assertNotIn(("-F", "make_latest=false"), pairs(args))

    def test_a_1_0_tag_is_not_a_pre_release(self) -> None:
        args = publish("v1.0.0")
        self.assertIn(("-F", "prerelease=false"), pairs(args))
        self.assertFalse(any("make_latest" in a for a in args), args)

    def test_the_patch_goes_to_the_release_of_the_id(self) -> None:
        args = publish("v0.2.0")
        self.assertEqual(args[:3], ["api", "--method", "PATCH"])
        self.assertIn("repos/o/r/releases/42", args)


if __name__ == "__main__":
    unittest.main()
