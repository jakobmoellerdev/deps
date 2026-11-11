#!/usr/bin/env bash
# Releases on a Forgejo/Gitea instance through its API, so the workflows need no forge CLI.
#
#   forge-release.sh create   <tag> <notes-file> <file>...   create the release of an existing tag and upload files
#   forge-release.sh upload   <tag> <file>...                upload files, replacing assets of the same name
#   forge-release.sh download <tag> <dir> <name>...          download named assets into <dir>
#
# Environment:
#   GITHUB_SERVER_URL   forge base URL (set by Actions)
#   GITHUB_REPOSITORY   <owner>/<repo> (set by Actions)
#   FORGEJO_TOKEN       API token; GITHUB_TOKEN is used when it is unset (both are the job token in Actions)
#
# create is idempotent: an existing release of the tag is reused, and upload replaces assets of the same name,
# so a re-run of the release job ends with the same assets.
set -euo pipefail

: "${GITHUB_SERVER_URL:?}" "${GITHUB_REPOSITORY:?}"
token=${FORGEJO_TOKEN:-${GITHUB_TOKEN:?no FORGEJO_TOKEN or GITHUB_TOKEN}}
api=${GITHUB_SERVER_URL%/}/api/v1/repos/$GITHUB_REPOSITORY

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The token goes through a curl config file, so it never appears in the process list.
printf 'header = "Authorization: token %s"\n' "$token" >"$tmp/auth"

# request <method> <url> [curl args...] prints the HTTP status; the body goes to ${out:-$tmp/body}.
request() {
	local method=$1 url=$2
	shift 2
	curl -sS -K "$tmp/auth" -X "$method" -o "${out:-$tmp/body}" -w '%{http_code}' "$@" "$url"
}

fail() {
	echo "forge-release.sh: $1" >&2
	exit 1
}

# release_id <tag> prints the id of the release of <tag>, or nothing.
release_id() {
	local status
	status=$(out=$tmp/release request GET "$api/releases/tags/$1")
	[[ $status == 200 ]] || return 0
	jq -r .id <"$tmp/release"
}

upload() { # <tag> <file>...
	local tag=$1 id status name asset
	shift
	id=$(release_id "$tag")
	[[ -n $id ]] || fail "no release for $tag"
	status=$(out=$tmp/release request GET "$api/releases/$id")
	[[ $status == 200 ]] || fail "GET release $id: HTTP $status: $(cat "$tmp/release")"
	for file in "$@"; do
		name=${file##*/}
		asset=$(jq -r --arg n "$name" '.assets[]? | select(.name == $n) | .id' <"$tmp/release" | head -1)
		if [[ -n $asset ]]; then
			status=$(request DELETE "$api/releases/$id/assets/$asset")
			[[ $status == 204 ]] || fail "DELETE asset $name: HTTP $status: $(cat "$tmp/body")"
		fi
		status=$(request POST "$api/releases/$id/assets?name=$name" -F "attachment=@$file")
		[[ $status == 201 ]] || fail "upload $name: HTTP $status: $(cat "$tmp/body")"
		echo "uploaded $name to $tag"
	done
}

case ${1:?usage: forge-release.sh create|upload|download ...} in
create)
	tag=${2:?usage: forge-release.sh create <tag> <notes-file> <file>...}
	notes=${3:?usage: forge-release.sh create <tag> <notes-file> <file>...}
	shift 3
	status=$(request GET "$api/tags/$tag")
	[[ $status == 200 ]] || fail "tag $tag does not exist: HTTP $status"
	if [[ -z $(release_id "$tag") ]]; then
		jq -n --arg t "$tag" --rawfile b "$notes" \
			'{tag_name: $t, name: $t, body: $b, draft: false, prerelease: false}' >"$tmp/create.json"
		status=$(request POST "$api/releases" -H 'Content-Type: application/json' -d "@$tmp/create.json")
		case $status in
		201) echo "created release $tag" ;;
		409) echo "release $tag already exists; reusing it" ;;
		*) fail "create release $tag: HTTP $status: $(cat "$tmp/body")" ;;
		esac
	else
		echo "release $tag already exists; reusing it"
	fi
	[[ $# -eq 0 ]] || upload "$tag" "$@"
	;;
upload)
	tag=${2:?usage: forge-release.sh upload <tag> <file>...}
	shift 2
	[[ $# -gt 0 ]] || fail "upload needs at least one file"
	upload "$tag" "$@"
	;;
download)
	tag=${2:?usage: forge-release.sh download <tag> <dir> <name>...}
	dir=${3:?usage: forge-release.sh download <tag> <dir> <name>...}
	shift 3
	id=$(release_id "$tag")
	[[ -n $id ]] || fail "no release for $tag"
	status=$(out=$tmp/release request GET "$api/releases/$id")
	[[ $status == 200 ]] || fail "GET release $id: HTTP $status: $(cat "$tmp/release")"
	mkdir -p "$dir"
	for name in "$@"; do
		url=$(jq -r --arg n "$name" '.assets[]? | select(.name == $n) | .browser_download_url' <"$tmp/release" | head -1)
		[[ -n $url ]] || fail "$tag has no asset $name"
		status=$(out=$dir/$name request GET "$url" -L)
		[[ $status == 200 ]] || fail "download $name: HTTP $status"
		echo "downloaded $name"
	done
	;;
*)
	fail "unknown command $1"
	;;
esac
