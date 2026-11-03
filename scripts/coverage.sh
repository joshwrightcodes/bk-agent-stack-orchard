#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later

# coverage.sh runs the Go tests with the race detector and a coverage profile, and fails when the
# total statement coverage is below the threshold below. It writes these files:
#
#   build/coverage.out   the coverage profile, without the excluded files
#   build/coverage.html  the profile as an HTML report (go tool cover -html)
#   build/coverage.md    the coverage of each package and the total, as a Markdown table
#
# The coverage of a package counts only the statements that the tests of that package run. The
# total leaves out these files, because no binary of the controller links them or a generator
# writes them:
#
#   *_gen.go             generated code (mise run generate)
#   test/                the test suites and the fakes that they run against
#   internal/repocheck/  a package that only the tests import
set -eu

# The lowest total coverage, in percent, that passes. Raise it when the total rises.
threshold=85

module="$(go list -m)"
mkdir -p build

# go test writes the profile of every package into one file. -race sets the atomic mode, and
# -covermode states it.
go test -race -covermode=atomic -coverprofile=build/coverage-all.out ./...

grep -v \
	-e '_gen\.go:' \
	-e "^$module/test/" \
	-e "^$module/internal/repocheck/" \
	build/coverage-all.out >build/coverage.out
rm build/coverage-all.out

go tool cover -func=build/coverage.out >build/coverage.txt
cat build/coverage.txt
total="$(awk '$1 == "total:" { sub(/%$/, "", $NF); print $NF }' build/coverage.txt)"
rm build/coverage.txt
if [ -z "$total" ]; then
	echo "coverage: go tool cover printed no total" >&2
	exit 1
fi
go tool cover -html=build/coverage.out -o build/coverage.html

# Each line of a profile after the first is "file:start,end statements count". The package of a
# block is the directory of its file, relative to the module.
{
	printf '## Go test coverage\n\n'
	printf '| Package | Statements | Coverage |\n'
	printf '| --- | ---: | ---: |\n'
	awk -v prefix="$module/" '
		NR > 1 {
			file = substr($1, 1, index($1, ":") - 1)
			if (index(file, prefix) == 1) file = substr(file, length(prefix) + 1)
			package = file
			sub(/\/[^\/]*$/, "", package)
			statements[package] += $2
			if ($3 > 0) covered[package] += $2
		}
		END {
			for (package in statements) {
				percent = statements[package] ? 100 * covered[package] / statements[package] : 100
				printf "| %s | %d | %.1f%% |\n", package, statements[package], percent
			}
		}
	' build/coverage.out | sort
	printf '| **Total** | | **%s%%** |\n\n' "$total"
	printf 'The threshold is %s%% (scripts/coverage.sh).\n' "$threshold"
} >build/coverage.md

if awk -v total="$total" -v threshold="$threshold" 'BEGIN { exit !(total + 0 < threshold + 0) }'; then
	echo "coverage: the total, ${total}%, is below the threshold of ${threshold}%" >&2
	exit 1
fi
echo "coverage: the total is ${total}%, and the threshold is ${threshold}%"
