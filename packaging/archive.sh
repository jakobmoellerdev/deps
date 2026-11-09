#!/usr/bin/env bash
# Writes the release archives from the release binaries $BIN/deps_<os>_<arch>[.exe] (default
# dist/bin, task release:build) with hack/archive. Usage: archive.sh <tag>
#
#   deps_<linux|darwin>_<arch>.tar.xz  ->  deps, LICENSE, completions/{deps.bash,_deps,deps.fish}
#   deps_windows_<arch>.zip            ->  deps.exe, LICENSE, completions/deps.ps1
#
# The archives go to $DIST (default dist) and are reproducible: every mtime is SOURCE_DATE_EPOCH
# (default: the commit time of the tag, else of HEAD), owners are root, modes are fixed, entries
# are sorted, and Go compresses them, not the host's xz or zip, whose output differs by version.
set -euo pipefail

tag=${1:?usage: archive.sh <tag>}
DIST=${DIST:-dist}
export BIN=${BIN:-$DIST/bin}
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=packaging/common.sh
source "$here/common.sh"

export_source_date_epoch "$tag"
check_binaries

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
write_completions "$tmp/completions"
(cd "$here/.." && go build -o "$tmp/archive" ./hack/archive)
"$tmp/archive" -bin "$BIN" -completions "$tmp/completions" -license "$here/../LICENSE" -out "$DIST"
