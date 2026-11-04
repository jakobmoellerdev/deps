#!/usr/bin/env bash
# Builds dist/deps_<version>_<arch>.deb for amd64 and arm64 from the release binaries
# dist/deps_linux_<arch> (task release:build). Usage: build.sh <tag> (for example v0.3.0).
#
# The packages are reproducible: every mtime is SOURCE_DATE_EPOCH (default: the commit time
# of the tag, else of HEAD), owners are root:root, modes are fixed and dpkg-deb sorts the
# archive members.
set -euo pipefail

tag=${1:?usage: build.sh <tag>}
DIST=${DIST:-dist}
# shellcheck source=packaging/common.sh
source "$(dirname "$0")/../common.sh"

# Debian versions sort "~" before anything, so a v1.2.3-rc.1 tag becomes 1.2.3~rc.1 < 1.2.3.
version=${tag#v}
version=${version/-/\~}
if ! [[ $version =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
	echo "build.sh: $tag does not give a valid Debian version ($version)" >&2
	exit 1
fi

export_source_date_epoch "$tag"
check_binaries

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
umask 022

comp=$tmp/completions
write_completions "$comp"

for arch in amd64 arm64; do
	bin=$DIST/deps_linux_$arch
	root=$tmp/$arch
	install -D -m 0755 "$bin" "$root/usr/bin/deps"
	install -D -m 0644 "$comp/bash" "$root/usr/share/bash-completion/completions/deps"
	install -D -m 0644 "$comp/zsh" "$root/usr/share/zsh/vendor-completions/_deps"
	install -D -m 0644 "$comp/fish" "$root/usr/share/fish/vendor_completions.d/deps.fish"
	install -D -m 0644 "$(dirname "$0")/../../LICENSE" "$root/usr/share/doc/deps/copyright"
	mkdir -p "$root/DEBIAN"
	size=$(du -sk --apparent-size "$root/usr" | cut -f1)
	cat >"$root/DEBIAN/control" <<EOF
Package: deps
Version: $version
Architecture: $arch
Maintainer: Jakob Möller <contact@jakob-moeller.com>
Installed-Size: $size
Recommends: git
Section: devel
Priority: optional
Homepage: https://git.jakob-moeller.cloud/jakobmoellerdev/deps
Description: dependency updates for GitHub, Gitea and Forgejo repositories
 deps finds the dependencies of a repository (GitHub/Gitea/Forgejo Actions, Go
 modules, container images, npm packages and custom-annotated versions), looks
 up newer versions and prints them as a diff, writes them, or pushes one branch
 and opens one pull request per dependency. Files are edited byte-exactly and
 never reformatted.
EOF
	chmod 0644 "$root/DEBIAN/control"
	find "$root" -type d -exec chmod 0755 {} +
	find "$root" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
	dpkg-deb --root-owner-group -Zxz --build "$root" "$DIST/deps_${version}_${arch}.deb"
done
