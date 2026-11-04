#!/usr/bin/env bash
# Uploads .deb, .pkg.tar.zst (Arch Linux) and .apk (Alpine) files to the Gitea/Forgejo package
# registries of one owner. Usage: publish.sh <file>...
#
# Environment:
#   PACKAGES_URL    https://<host>/api/packages/<owner>
#   PACKAGES_USER   user for HTTP Basic auth (the owner of the token)
#   PACKAGES_TOKEN  token with the write:package scope
#   DEB_DISTRIBUTION, DEB_COMPONENT   default stable and main
#   ARCH_REPOSITORY                   default jakobmoellerdev (the pacman.conf section name)
#   ALPINE_BRANCH, ALPINE_REPOSITORY  default stable and main
#
# Re-runs are idempotent: when the registry already has the file (409), an identical file is
# skipped and a different one is deleted and uploaded again.
set -euo pipefail

: "${PACKAGES_URL:?}" "${PACKAGES_USER:?}" "${PACKAGES_TOKEN:?}"
registry=${PACKAGES_URL%/}
deb=$registry/debian/pool/${DEB_DISTRIBUTION:-stable}/${DEB_COMPONENT:-main}
arch_repo=$registry/arch/${ARCH_REPOSITORY:-jakobmoellerdev}
alpine_repo=$registry/alpine/${ALPINE_BRANCH:-stable}/${ALPINE_REPOSITORY:-main}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The credentials go through a curl config file, so they never appear in the process list.
printf 'user = "%s:%s"\n' "$PACKAGES_USER" "$PACKAGES_TOKEN" >"$tmp/auth"

# request <method> <url> [curl args...] prints the HTTP status; the body goes to ${out:-$tmp/body}.
request() {
	local method=$1 url=$2
	shift 2
	curl -sS -K "$tmp/auth" -X "$method" -o "${out:-$tmp/body}" -w '%{http_code}' "$@" "$url"
}

# field <key> reads "key = value" lines (.PKGINFO of Arch and Alpine packages) from stdin.
field() {
	awk -v k="$1" -F ' = ' '$1 == k { print $2; exit }'
}

for file in "$@"; do
	# Each format sets id, upload (PUT target), existing (GET URL of the published file) and
	# delete (DELETE URL that removes it).
	case $file in
	*.deb)
		name=$(dpkg-deb --field "$file" Package)
		version=$(dpkg-deb --field "$file" Version)
		arch=$(dpkg-deb --field "$file" Architecture)
		id="${name}_${version}_${arch}"
		upload=$deb/upload
		existing=$deb/$id.deb
		delete=$deb/$name/$version/$arch
		;;
	*.pkg.tar.zst)
		tar --zstd -xOf "$file" .PKGINFO >"$tmp/pkginfo"
		name=$(field pkgname <"$tmp/pkginfo")
		version=$(field pkgver <"$tmp/pkginfo")
		arch=$(field arch <"$tmp/pkginfo")
		id="$name-$version-$arch"
		upload=$arch_repo
		existing=$arch_repo/$arch/$id.pkg.tar.zst
		delete=$arch_repo/$name/$version/$arch
		;;
	*.apk)
		tar --warning=no-unknown-keyword -xzOf "$file" .PKGINFO >"$tmp/pkginfo"
		name=$(field pkgname <"$tmp/pkginfo")
		version=$(field pkgver <"$tmp/pkginfo")
		arch=$(field arch <"$tmp/pkginfo")
		id="$name-$version-$arch"
		upload=$alpine_repo
		existing=$alpine_repo/$arch/$name-$version.apk
		delete=$existing
		;;
	*)
		echo "publish.sh: $file: unknown package format" >&2
		exit 1
		;;
	esac
	status=$(request PUT "$upload" --upload-file "$file")
	if [[ $status == 409 ]]; then
		status=$(out=$tmp/existing request GET "$existing")
		if [[ $status == 200 ]] && cmp -s "$file" "$tmp/existing"; then
			echo "::notice::$id is already published with the same content; skipped"
			continue
		fi
		echo "::warning::$id is already published with different content; replacing it"
		status=$(request DELETE "$delete")
		[[ $status == 204 ]] || { echo "DELETE $id: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
		status=$(request PUT "$upload" --upload-file "$file")
	fi
	[[ $status == 201 ]] || { echo "upload $id: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
	echo "published $id to $upload"
done
