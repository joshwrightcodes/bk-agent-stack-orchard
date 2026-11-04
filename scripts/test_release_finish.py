# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
"""Tests of scripts/release-finish.sh.

Run with: python3 -m unittest discover -s scripts -p 'test_*.py'

PublishTest sources the script, which defines publish_release and runs nothing else. A stub gh,
first on PATH, records its arguments. FinishTest runs the whole script against a stub gh that
serves a draft release, and changes its assets or its notes at a chosen moment.
"""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys
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
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
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


# A stub gh for the whole script. It serves one draft release with id 42 from state.json. The
# state has two versions of the assets and the notes: the first until the event that "switch"
# names, then the second. "relist" is the second list of the assets, which the script makes just
# before the publish. "patch" is the publish itself. --jq goes through the jq of the system.
GH_STUB = r"""
import json, pathlib, re, subprocess, sys

state_file = pathlib.Path(sys.argv[0]).with_name("state.json")
state = json.loads(state_file.read_text())
args = sys.argv[1:]
with open(state["record"], "a") as record:
    record.write(json.dumps(args) + "\n")
version = state["version"]
out = None
if "--method" in args and args[args.index("--method") + 1] == "PATCH":
    state["published"] = True
    if state["switch"] == "patch":
        state["version"] = 1
    out = {}
elif "-H" in args:
    asset = int(args[-1].rsplit("/", 1)[1])
    entry = next(a for a in state["assets"][version] if a["id"] == asset)
    sys.stdout.write(entry["content"])
else:
    path = args[1]
    if path.endswith("/assets"):
        state["lists"] += 1
        if state["switch"] == "relist" and state["lists"] == 2:
            state["version"] = version = 1
        out = [{"id": a["id"], "name": a["name"]} for a in state["assets"][version]]
    else:
        release = {"id": 42, "tag_name": "v0.1.0", "draft": not state["published"],
                   **state["notes"][version]}
        out = [release] if path.endswith("/releases") else release
state_file.write_text(json.dumps(state))
if out is not None:
    text = json.dumps(out)
    if "--jq" in args:
        text = subprocess.run(["jq", "-r", args[args.index("--jq") + 1]], input=text,
                              capture_output=True, text=True, check=True).stdout
        sys.stdout.write(text)
    else:
        print(text)
"""

FILES = {
    "dist/x_linux_amd64.tar.gz": "archive",
    "dist/x_linux_amd64.tar.gz.sbom.json": "sbom",
    "dist/bk-agent-stack-orchard_0.1.0.openvex.json": "vex",
    "dist/checksums.txt": "sums",
    "dist/checksums.txt.sigstore.json": "bundle",
    "dist/checksums.txt.intoto.jsonl": "provenance",
    "build/chart/bk-agent-stack-orchard-0.1.0.tgz": "chart",
    "build/chart/bk-agent-stack-orchard-0.1.0.tgz.sigstore.json": "chart bundle",
}

NOTES = {"name": "v0.1.0", "body": "## Features\n\n* a feature"}


def draft_assets(
    changes: dict[str, str] | None = None,
    first_id: int = 1,
) -> list[dict[str, object]]:
    contents = {pathlib.PurePath(p).name: c for p, c in FILES.items()}
    contents.update(changes or {})
    return [
        {"id": first_id + i, "name": n, "content": contents[n]}
        for i, n in enumerate(sorted(contents))
    ]


class FinishTest(unittest.TestCase):
    def finish(
        self,
        assets: list[list[dict[str, object]]],
        notes: list[dict[str, str]] | None = None,
        switch: str = "none",
    ) -> tuple[subprocess.CompletedProcess[str], list[list[str]]]:
        """Run the script against the stub, and return the result and the calls of gh."""
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp, "repo")
            root.mkdir()
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            for path, content in FILES.items():
                pathlib.Path(root, path).parent.mkdir(parents=True, exist_ok=True)
                pathlib.Path(root, path).write_text(content)
            bin_dir = pathlib.Path(tmp, "bin")
            bin_dir.mkdir()
            stub = bin_dir / "gh"
            stub.write_text(f"#!{sys.executable}\n{GH_STUB}")
            stub.chmod(0o755)
            record = pathlib.Path(tmp, "record")
            record.write_text("")
            (bin_dir / "state.json").write_text(
                json.dumps(
                    {
                        "record": str(record),
                        "version": 0,
                        "lists": 0,
                        "published": False,
                        "switch": switch,
                        "assets": assets,
                        "notes": notes or [NOTES, NOTES],
                    }
                )
            )
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "GITHUB_REPOSITORY": "o/r",
            }
            result = subprocess.run(
                ["bash", str(SCRIPT), "v0.1.0"], cwd=root, env=env, capture_output=True, text=True
            )
            calls = [json.loads(line) for line in record.read_text().splitlines()]
            return result, calls

    @staticmethod
    def published(calls: list[list[str]]) -> bool:
        return any("PATCH" in call for call in calls)

    def test_matching_assets_publish_the_draft(self) -> None:
        result, calls = self.finish([draft_assets(), draft_assets()])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.published(calls))
        # The assets are downloaded once before the publish and once after it.
        downloads = [c for c in calls if "-H" in c]
        self.assertEqual(len(downloads), 2 * len(FILES))

    def test_a_changed_asset_keeps_the_draft(self) -> None:
        changed = draft_assets({"x_linux_amd64.tar.gz": "other"})
        result, calls = self.finish([changed, changed])
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.published(calls))

    def test_an_extra_asset_keeps_the_draft(self) -> None:
        extra = draft_assets({"extra.tar.gz": "extra"})
        result, calls = self.finish([extra, extra])
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.published(calls))

    def test_a_draft_without_the_provenance_keeps_the_draft(self) -> None:
        # The bundle of the provenance attestation is an asset of each release, so that a reader
        # can check the provenance without the GitHub API.
        missing = [a for a in draft_assets() if a["name"] != "checksums.txt.intoto.jsonl"]
        result, calls = self.finish([missing, missing])
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.published(calls))

    def test_a_draft_without_the_vex_document_keeps_the_draft(self) -> None:
        missing = [a for a in draft_assets() if not str(a["name"]).endswith(".openvex.json")]
        result, calls = self.finish([missing, missing])
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.published(calls))

    def test_an_asset_replaced_during_the_check_keeps_the_draft(self) -> None:
        # The same content under new IDs: a replacement that happened while the script downloaded.
        result, calls = self.finish([draft_assets(), draft_assets(first_id=100)], switch="relist")
        self.assertEqual(result.returncode, 1)
        self.assertIn("changed during the check", result.stderr)
        self.assertFalse(self.published(calls))

    def test_an_asset_changed_just_before_the_publish_fails_the_run(self) -> None:
        changed = draft_assets({"checksums.txt": "other"}, first_id=100)
        result, calls = self.finish([draft_assets(), changed], switch="patch")
        self.assertTrue(self.published(calls))
        self.assertEqual(result.returncode, 1)
        self.assertIn("did not verify", result.stderr)

    def test_notes_changed_just_before_the_publish_fail_the_run(self) -> None:
        other = {**NOTES, "body": "changed"}
        result, calls = self.finish(
            [draft_assets(), draft_assets()], notes=[NOTES, other], switch="patch"
        )
        self.assertTrue(self.published(calls))
        self.assertEqual(result.returncode, 1)
        self.assertIn("notes of v0.1.0 changed", result.stderr)


if __name__ == "__main__":
    unittest.main()
