#!/usr/bin/env bash
# Opens a pull request that sets Formula/deps.rb (the repository is its own Homebrew tap) to a
# release's formula (packaging/homebrew/formula.sh). Usage: pr.sh <tag> <formula.rb>
#
# The branch release/homebrew-<tag> starts at the base branch; the pull request body links the
# Actions run that opened it. Run it from a clone of the repository.
#
# Environment:
#   PR_TOKEN           Gitea/Forgejo token that pushes the branch and opens the pull request;
#                      a token of a user, so the pull request's CI runs (events of the Actions
#                      job token start no workflows)
#   GITHUB_SERVER_URL  default https://git.jakob-moeller.cloud
#   GITHUB_API_URL     default $GITHUB_SERVER_URL/api/v1
#   GITHUB_REPOSITORY  default jakobmoellerdev/deps
#   PR_BASE            base branch, default main
#   PR_AUTHOR          commit author, default deps-bot <deps-bot@jakob-moeller.cloud>
#   GITHUB_RUN_ID      set in Actions: the body links $GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/<id>
#
# Pre-release tags (v1.2.3-rc.1) are skipped, and so is a tag that is not newer than the base
# branch's formula, so re-running an old release job never downgrades it. Re-running for the
# same tag force-pushes the same branch and keeps its open pull request.
set -euo pipefail

tag=${1:?usage: pr.sh <tag> <formula.rb>}
formula=${2:?usage: pr.sh <tag> <formula.rb>}
: "${PR_TOKEN:?PR_TOKEN is required}"
server=${GITHUB_SERVER_URL:-https://git.jakob-moeller.cloud}
server=${server%/}
api=${GITHUB_API_URL:-$server/api/v1}
api=${api%/}
repo=${GITHUB_REPOSITORY:-jakobmoellerdev/deps}
base=${PR_BASE:-main}
author=${PR_AUTHOR:-deps-bot <deps-bot@jakob-moeller.cloud>}
version=${tag#v}
branch=release/homebrew-$tag

if [[ $version == *-* ]]; then
	echo "::notice::$tag is a pre-release; Formula/deps.rb stays as it is"
	exit 0
fi

tmp=$(mktemp -d)
trap 'git worktree remove --force "$tmp/wt" 2>/dev/null; rm -rf "$tmp"' EXIT
# The token goes through a curl config file, so it never appears in the process list.
printf 'header = "Authorization: token %s"\n' "$PR_TOKEN" >"$tmp/auth"

# request <method> <path> [curl args...] prints the HTTP status; the body goes to $tmp/body.
request() {
	local method=$1 path=$2
	shift 2
	curl -sS -K "$tmp/auth" -X "$method" -o "$tmp/body" -w '%{http_code}' "$@" "$api$path"
}

status=$(request GET /user)
[[ $status == 200 ]] || { echo "pr.sh: GET /user: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
user=$(jq -r .login "$tmp/body")

# Git authenticates with the token too: an empty value drops the job token's header that
# actions/checkout stores for the server, then the token's own Basic header follows. Both come
# through the environment, never the command line or .git/config.
export GIT_TERMINAL_PROMPT=0 GIT_CONFIG_COUNT=2
export GIT_CONFIG_KEY_0=http.$server/.extraheader GIT_CONFIG_VALUE_0=
export GIT_CONFIG_KEY_1=http.$server/.extraheader
GIT_CONFIG_VALUE_1="Authorization: Basic $(printf '%s:%s' "$user" "$PR_TOKEN" | base64 -w0)"
export GIT_CONFIG_VALUE_1
remote=$server/$repo.git

git fetch -q "$remote" "refs/heads/$base"
current=$({ git show FETCH_HEAD:Formula/deps.rb 2>/dev/null || true; } | sed -n 's/^  version "\(.*\)"$/\1/p')
if [[ -n $current && $(printf '%s\n%s\n' "$current" "$version" | sort -V | tail -n1) == "$current" ]]; then
	echo "::notice::Formula/deps.rb on $base already has deps $current, not older than $tag; skipped"
	exit 0
fi

git worktree add -q --detach "$tmp/wt" FETCH_HEAD
mkdir -p "$tmp/wt/Formula"
cp "$formula" "$tmp/wt/Formula/deps.rb"
git -C "$tmp/wt" add Formula/deps.rb
git -C "$tmp/wt" -c user.name="${author% <*}" -c user.email="$(sed 's/.*<\(.*\)>.*/\1/' <<<"$author")" \
	commit -q -m "chore(homebrew): deps $version"
git -C "$tmp/wt" push -q --force "$remote" "HEAD:refs/heads/$branch"
echo "pushed $branch"

status=$(request GET "/repos/$repo/pulls?state=open&limit=50")
[[ $status == 200 ]] || { echo "pr.sh: list pull requests: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
open=$(jq -r --arg b "$branch" '[.[] | select(.head.ref == $b)][0].html_url // empty' "$tmp/body")
if [[ -n $open ]]; then
	echo "pull request already open: $open"
	exit 0
fi

body="Points the Homebrew formula (\`Formula/deps.rb\`) at the $tag release binaries for macOS and Linux."
[[ -n ${GITHUB_RUN_ID:-} ]] && body+=$'\n\n'"Opened by the release run $server/$repo/actions/runs/$GITHUB_RUN_ID."
jq -n --arg head "$branch" --arg base "$base" --arg title "chore(homebrew): deps $version" --arg body "$body" \
	'{head: $head, base: $base, title: $title, body: $body}' >"$tmp/pr.json"
status=$(request POST "/repos/$repo/pulls" -H 'Content-Type: application/json' --data-binary "@$tmp/pr.json")
[[ $status == 201 ]] || { echo "pr.sh: create pull request: HTTP $status: $(cat "$tmp/body")" >&2 && exit 1; }
echo "opened $(jq -r .html_url "$tmp/body")"
