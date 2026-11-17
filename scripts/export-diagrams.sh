#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors
# Export each draw.io drawing in docs/diagrams to an SVG with a transparent background.
#
# Usage: scripts/export-diagrams.sh        (mise run diagrams)
#
# The draw.io desktop app does the export. The Brewfile installs it. Set DRAWIO to use another
# path to its command-line program.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"

drawio="${DRAWIO:-}"
if [ -z "$drawio" ]; then
	if command -v drawio >/dev/null 2>&1; then
		drawio="drawio"
	elif [ -x /Applications/draw.io.app/Contents/MacOS/draw.io ]; then
		drawio="/Applications/draw.io.app/Contents/MacOS/draw.io"
	else
		echo "export-diagrams: draw.io is not installed. Run brew bundle, or set DRAWIO." >&2
		exit 1
	fi
fi

# --theme light writes fixed colours. The default theme writes an SVG that follows the colour
# scheme of the operating system, which can differ from the theme of the page that shows it.
for source in "$root"/docs/diagrams/*.drawio; do
	"$drawio" --export --format svg --transparent --theme light --border 8 \
		--disable-update --output "${source%.drawio}.svg" "$source"
done
