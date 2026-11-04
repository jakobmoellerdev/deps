#!/usr/bin/env bash
# Uploads .deb files to a Gitea/Forgejo Debian package registry.
# Usage: publish.sh <file.deb>...
#
# Environment:
#   DEB_REGISTRY  registry URL, https://<host>/api/packages/<owner>/debian
#   DEB_USER      user for HTTP Basic auth (the owner of the token)
#   DEB_TOKEN     token with the write:package scope
#   DEB_DISTRIBUTION, DEB_COMPONENT  default stable and main
#
# Re-runs are idempotent: when the registry already has the file (409), an identical file is
# skipped and a different one is deleted and uploaded again.
set -euo pipefail

: "${DEB_REGISTRY:?}" "${DEB_USER:?}" "${DEB_TOKEN:?}"
dist=${DEB_DISTRIBUTION:-stable}
comp=${DEB_COMPONENT:-main}
pool=${DEB_REGISTRY%/}/pool/$dist/$comp

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The credentials go through a curl config file, so they never appear in the process list.
printf 'user = "%s:%s"\n' "$DEB_USER" "$DEB_TOKEN" >"$tmp/auth"

# request <method> <url> [curl args...] prints the HTTP status; the body goes to ${out:-$tmp/body}.
request() {
	local method=$1 url=$2
	shift 2
	curl -sS -K "$tmp/auth" -X "$method" -o "${out:-$tmp/body}" -w '%{http_code}' "$@" "$url"
}

upload() {
	request PUT "$pool/upload" --upload-file "$1"
}

for deb in "$@"; do
	name=$(dpkg-deb --field "$deb" Package)
	version=$(dpkg-deb --field "$deb" Version)
	arch=$(dpkg-deb --field "$deb" Architecture)
	id="${name}_${version}_${arch}"
	status=$(upload "$deb")
	if [[ $status == 409 ]]; then
		status=$(out=$tmp/existing.deb request GET "$pool/${id}.deb")
		if [[ $status == 200 ]] && cmp -s "$deb" "$tmp/existing.deb"; then
			echo "::notice::$id is already published with the same content; skipped"
			continue
		fi
		echo "::warning::$id is already published with different content; replacing it"
		status=$(request DELETE "$pool/$name/$version/$arch")
		[[ $status == 204 ]] || { echo "DELETE $id: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
		status=$(upload "$deb")
	fi
	[[ $status == 201 ]] || { echo "upload $id: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
	echo "published $id to $pool"
done
