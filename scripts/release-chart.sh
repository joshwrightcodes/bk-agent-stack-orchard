#!/usr/bin/env bash
# Package the Helm chart into build/chart.
#
# The chart and the controller have one version. release-please writes it into Chart.yaml in the
# release pull request, and the release workflow checks that it is the version of the tag.
# GoReleaser adds the package to the checksums and to the release, so the signature of the
# checksums also covers the chart.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

rm -rf build/chart
mkdir -p build/chart
helm package deploy/helm/bk-agent-stack-orchard --destination build/chart
