#!/usr/bin/env bash
# Builds the Debian, RPM, Arch Linux or Alpine packages for both architectures from the release
# binaries $BIN/deps_linux_<goarch> (default dist/bin, task release:build) with nfpm and
# packaging/nfpm/nfpm.yaml:
#
#   build.sh deb <tag>        ->  dist/deps_<version>_<arch>.deb
#   build.sh rpm <tag>        ->  dist/deps-<version>-1.<arch>.rpm
#   build.sh archlinux <tag>  ->  dist/deps-<pkgver>-1-<arch>.pkg.tar.zst
#   build.sh apk <tag>        ->  dist/deps-<pkgver>-r0-<arch>.apk
#
# NFPM is the nfpm binary (default: nfpm on PATH). The packages are reproducible: every mtime
# is SOURCE_DATE_EPOCH (default: the commit time of the tag, else of HEAD), owners are
# root:root and modes are fixed.
set -euo pipefail

format=${1:?usage: build.sh <deb|rpm|archlinux|apk> <tag>}
tag=${2:?usage: build.sh <deb|rpm|archlinux|apk> <tag>}
export DIST=${DIST:-dist}
export BIN=${BIN:-$DIST/bin}
nfpm=${NFPM:-nfpm}
here=$(cd "$(dirname "$0")" && pwd)
export LICENSE_FILE=${LICENSE_FILE:-$(cd "$here/../.." && pwd)/LICENSE}
# shellcheck source=packaging/common.sh
source "$here/../common.sh"

version=${tag#v}
core=${version%%-*}
pre=
[[ $version == *-* ]] && pre=${version#*-}
invalid() {
	echo "build.sh: $tag does not give a valid $1 version" >&2
	exit 1
}
[[ $core =~ ^[0-9]+(\.[0-9]+)*$ ]] || invalid "$format"

case $format in
deb)
	# Debian versions sort "~" before anything, so a v1.2.3-rc.1 tag becomes 1.2.3~rc.1 < 1.2.3.
	[[ -z $pre || $pre =~ ^[A-Za-z0-9.+~-]+$ ]] || invalid Debian
	pkgver=$core${pre:+~$pre}
	release=
	file() { echo "deps_${pkgver}_$1.deb"; }
	;;
rpm)
	# rpm sorts "~" before anything too, so a v1.2.3-rc.1 tag becomes 1.2.3~rc.1 < 1.2.3; "-" is
	# reserved for the release.
	[[ -z $pre || $pre =~ ^[A-Za-z0-9.+~]+$ ]] || invalid RPM
	pkgver=$core${pre:+~$pre}
	release=1
	file() { echo "deps-$pkgver-1.$1.rpm"; }
	;;
archlinux)
	# pacman's vercmp sorts letters that follow a number before the number's end, so a
	# v1.2.3-rc.1 tag becomes 1.2.3rc.1 < 1.2.3. A pre-release must start with a letter, and
	# "-" is reserved for pkgrel.
	[[ -z $pre || $pre =~ ^[A-Za-z][A-Za-z0-9.]*$ ]] || invalid "Arch Linux"
	pkgver=$core$pre
	release=1
	file() { echo "deps-$pkgver-1-$1.pkg.tar.zst"; }
	;;
apk)
	# Alpine versions only allow the pre-release suffixes _alpha, _beta, _pre and _rc, which
	# sort before the release, so a v1.2.3-rc.1 tag becomes 1.2.3_rc1 < 1.2.3.
	if [[ -z $pre ]]; then
		pkgver=$core
	elif [[ $pre =~ ^(alpha|beta|pre|rc)\.?([0-9]*)$ ]]; then
		pkgver=${core}_${BASH_REMATCH[1]}${BASH_REMATCH[2]}
	else
		invalid Alpine
	fi
	release=0
	file() { echo "deps-$pkgver-r0-$1.apk"; }
	;;
*)
	echo "build.sh: unknown format $format (want deb, rpm, archlinux or apk)" >&2
	exit 1
	;;
esac

export_source_date_epoch "$tag"
check_binaries

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
umask 022
write_completions "$tmp/completions"

for goarch in amd64 arm64; do
	case $format:$goarch in
	deb:*) arch=$goarch ;;
	*:amd64) arch=x86_64 ;;
	*:arm64) arch=aarch64 ;;
	esac
	GOARCH=$goarch PKG_VERSION=$pkgver PKG_RELEASE=$release COMPLETIONS=$tmp/completions \
		"$nfpm" package --config "$here/nfpm.yaml" --packager "$format" --target "$DIST/$(file "$arch")"
done
