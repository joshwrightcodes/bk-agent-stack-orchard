#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Opens an issue for a failed scheduled run, or comments on the issue that is already open.
#
# Usage: scripts/report-failure.sh <title> <what failed>
#
# One title names one workflow, so a second failure adds a comment and does not open a second
# issue. The caller supplies GH_TOKEN with `issues: write`, and RUN_URL, the link to the run.
set -euo pipefail

title="${1:?usage: report-failure.sh <title> <what failed>}"
what="${2:?usage: report-failure.sh <title> <what failed>}"
run_url="${RUN_URL:?RUN_URL is not set}"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
{
	echo "$what"
	echo
	echo "Run: $run_url"
} >"$body"

existing="$(gh issue list --state open --search "$title in:title" \
	--json number,title --jq "map(select(.title == \"$title\")) | .[0].number // empty")"

if [ -n "$existing" ]; then
	gh issue comment "$existing" --body-file "$body"
else
	gh issue create --title "$title" --body-file "$body"
fi
