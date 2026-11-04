#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 Josh Wright
# Opens an issue for a failed scheduled run, or comments on the issue that is already open.
#
# One title names one workflow, so a second failure adds a comment and does not open a second
# issue. Anyone can open an issue, so the script comments only on an open issue with exactly this
# title that the workflow itself opened (REPORT_AUTHOR). An issue of another author with the same
# title cannot take the reports.
#
# Usage: scripts/report-failure.sh [--label <label>] <title> <what failed>
#
#   --label <label>   also match only issues with this label, and give it to a new issue
#
# Environment:
#   GH_TOKEN        a token with issues: write
#   RUN_URL         the link to the run
#   REPORT_AUTHOR   the author of the issues of the workflow, as gh names it
#                   (default: app/github-actions, the author of GITHUB_TOKEN)
#
# Exit status: 0 success, 1 failure, 2 usage error.
set -euo pipefail

readonly PROG="report-failure"

die() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}
usage() {
	sed -n 's/^# Usage: /usage: /p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

label=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--label)
		[[ $# -ge 2 && -n "$2" ]] || usage
		label="$2"
		shift 2
		;;
	-h | --help) usage ;;
	--)
		shift
		break
		;;
	-*) usage ;;
	*) break ;;
	esac
done
[[ $# -eq 2 && -n "$1" && -n "$2" ]] || usage
title="$1"
what="$2"
run_url="${RUN_URL:-}"
[[ -n "$run_url" ]] || die "RUN_URL is not set"
author="${REPORT_AUTHOR:-app/github-actions}"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
{
	printf '%s\n' "$what"
	echo
	printf 'Run: %s\n' "$run_url"
} >"$body"

list=(gh issue list --state open --author "$author" --search "$title in:title" --json "number,title,author")
create=(gh issue create --title "$title" --body-file "$body")
if [[ -n "$label" ]]; then
	list+=(--label "$label")
	create+=(--label "$label")
fi

# The search is fuzzy, and --author is a filter of the search, so the exact title and the author
# are checked again here.
existing="$("${list[@]}" |
	jq -r --arg t "$title" --arg a "$author" \
		'map(select(.title == $t and .author.login == $a)) | .[0].number // empty')"

if [[ -n "$existing" ]]; then
	gh issue comment "$existing" --body-file "$body"
else
	"${create[@]}"
fi
