#!/usr/bin/env bash
# End-to-end test of `deps run` against a real GitHub repository (task e2e:github).
#
# Needs: gh (logged in to github.com with the repo and workflow scopes), git, go, jq, and the
# deps binary ($DEPS_BIN, default bin/deps). The test repository ($DEPS_E2E_REPO, default
# <gh user>/deps-e2e) is created if missing and force-reset before and after the scenario:
# main = the fixed commit of testdata/repo, no open PRs, no branches but main, no open issues.
# Every step asserts its outcome through the GitHub API and the script fails on the first
# mismatch. See docs/ci.md "GitHub end-to-end test".
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
fixture=$here/testdata/repo
bin=$(realpath "${DEPS_BIN:-$root/bin/deps}")
merge_timeout=${DEPS_E2E_MERGE_TIMEOUT:-300}

for tool in gh git go jq; do
  command -v "$tool" >/dev/null || { echo "e2e: $tool not found" >&2; exit 1; }
done
[ -x "$bin" ] || { echo "e2e: $bin not found; run task build" >&2; exit 1; }

user=$(gh api user --jq .login)
repo=${DEPS_E2E_REPO:-$user/deps-e2e}
token=$(gh auth token --hostname github.com)

# deps must only see the token of this test and no CI environment.
unset GITHUB_TOKEN GITEA_TOKEN FORGEJO_TOKEN GITHUB_ACTIONS GITEA_ACTIONS FORGEJO_ACTIONS DEPS_GIT_AUTHOR \
  DEPS_DEFAULT_ACTIONS_URL GITHUB_STEP_SUMMARY GITEA_STEP_SUMMARY
export DEPS_TOKEN=$token GIT_TERMINAL_PROMPT=0

work=$(mktemp -d "${TMPDIR:-/tmp}/deps-e2e.XXXXXX")
trap 'rm -rf "$work"' EXIT

# Branches and titles deps creates for the fixture.
b_node=deps/actions-setup-node
b_python=deps/actions-setup-python
b_cobra=deps/github.com-spf13-cobra
reset_prefix="[e2e reset] "
dashboard_title="Dependency Dashboard"

declare -a timings=()
step_start=0
log() { printf '\n\033[1m== %s\033[0m\n' "$*" >&2; }
fail() { printf '\033[31me2e FAIL: %s\033[0m\n' "$*" >&2; exit 1; }
ok() { printf '  ok  %s\n' "$*" >&2; }
step() { step_start=$SECONDS; log "$*"; }
done_step() { timings+=("$(printf '%-48s %4ss' "$1" $((SECONDS - step_start)))"); }

# git with the gh credential helper for github.com, independent of the user's git config.
ghgit() {
  git -c credential.https://github.com.helper= \
    -c "credential.https://github.com.helper=!gh auth git-credential" "$@"
}

# poll <timeout seconds> <description> <command...> retries the command every 5s.
poll() {
  local timeout=$1 what=$2 start=$SECONDS
  shift 2
  until "$@"; do
    [ $((SECONDS - start)) -lt "$timeout" ] || fail "timed out after ${timeout}s waiting for $what"
    sleep 5
  done
}

# --- repository setup and reset ---

fixture_commit() {
  # A fixed author, committer and date make the fixture commit (and its SHA) reproducible.
  local dir=$work/fixture
  rm -rf "$dir" && cp -a "$fixture" "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" add -A
  GIT_AUTHOR_DATE=2026-01-01T00:00:00Z GIT_COMMITTER_DATE=2026-01-01T00:00:00Z \
    git -C "$dir" -c user.name=deps-e2e -c user.email=deps-e2e@noreply.invalid -c commit.gpgsign=false \
    commit -q -m "deps e2e fixture"
  echo "$dir"
}

ensure_repo() {
  if ! gh repo view "$repo" --json name >/dev/null 2>&1; then
    gh repo create "$repo" --public --description "Test repository of the deps GitHub end-to-end test; force-reset by every run" >/dev/null
    ok "created $repo"
  fi
}

# settings <allow_auto_merge> configures the repository and waits until GitHub reports it: a
# fresh repository answers with the old values for a few seconds.
settings() {
  gh api -X PATCH "repos/$repo" -F has_issues=true -F has_pull_requests=true -F has_wiki=false \
    -F has_projects=false -F allow_auto_merge="$1" -F delete_branch_on_merge=true \
    -F allow_merge_commit=true -F allow_squash_merge=true -F allow_rebase_merge=true >/dev/null
  poll 60 "allow_auto_merge=$1 on $repo" settings_are "$1"
}

settings_are() {
  [ "$(gh api "repos/$repo" --jq '[.allow_auto_merge, .has_issues, .delete_branch_on_merge] | map(tostring) | join(",")')" = "$1,true,true" ]
}

protect() {
  # Required check "check"; admins may bypass so the test can force-reset and push to main.
  gh api -X PUT "repos/$repo/branches/main/protection" --input - >/dev/null <<'EOF'
{"required_status_checks": {"strict": false, "checks": [{"context": "check"}]},
 "enforce_admins": false, "required_pull_request_reviews": null, "restrictions": null,
 "allow_force_pushes": true, "allow_deletions": false}
EOF
}

reset_repo() {
  log "reset $repo"
  ensure_repo
  settings true
  # Close open PRs and retitle closed unmerged deps PRs, dropping their bodies: an unmerged
  # closed PR with the same title would make the next run skip that update ("closed PR for
  # this version"), one of a major branch with its versions marker that major.
  local n state title
  gh api --paginate "repos/$repo/pulls?state=all&per_page=100" \
    --jq ".[] | select(.merged_at == null and (.title | startswith(\"$reset_prefix\") | not)) | [.number, .state, .title] | @tsv" |
    while IFS=$'\t' read -r n state title; do
      gh api -X PATCH "repos/$repo/pulls/$n" -f title="$reset_prefix$title" -f body="$reset_prefix" -f state=closed >/dev/null
      if [ "$state" = open ]; then ok "closed PR #$n"; fi
    done
  gh api --paginate "repos/$repo/issues?state=open&per_page=100" --jq '.[] | select(.pull_request == null) | .number' |
    while read -r n; do
      gh api -X PATCH "repos/$repo/issues/$n" -f state=closed -f state_reason=not_planned >/dev/null
      ok "closed issue #$n"
    done
  local dir
  dir=$(fixture_commit)
  ghgit -C "$dir" push -q --force "https://github.com/$repo.git" HEAD:refs/heads/main
  gh api --paginate "repos/$repo/branches?per_page=100" --jq '.[].name | select(. != "main")' |
    while read -r b; do
      gh api -X DELETE "repos/$repo/git/refs/heads/$b" >/dev/null
      ok "deleted branch $b"
    done
  gh api -X PATCH "repos/$repo" -f default_branch=main >/dev/null
  protect
  ok "main = $(git -C "$dir" rev-parse --short HEAD) (fixture)"
}

# --- deps runs and API helpers ---

runs=0
# deps_run runs `deps run` on a fresh clone of main; the log is $last.
deps_run() {
  runs=$((runs + 1))
  local clone=$work/clone-$runs
  last=$work/run-$runs.log
  ghgit clone -q --depth=1 "https://github.com/$repo.git" "$clone"
  git -C "$clone" config credential.https://github.com.helper ''
  git -C "$clone" config --add credential.https://github.com.helper '!gh auth git-credential'
  local start=$SECONDS rc=0
  # The debug log stays in $DEPS_E2E_LOGS (if set) for diagnosis; $last is the report only.
  "$bin" run --show-skipped --log-level debug --dir "$clone" "$@" >"$last.debug" 2>&1 || rc=$?
  grep -v '^time=' "$last.debug" >"$last" || true
  if [ -n "${DEPS_E2E_LOGS:-}" ]; then mkdir -p "$DEPS_E2E_LOGS" && cp "$last.debug" "$DEPS_E2E_LOGS/run-$runs.log"; fi
  sed 's/^/  | /' "$last" >&2
  printf '  deps run #%d: exit %d in %ss\n' "$runs" "$rc" $((SECONDS - start)) >&2
  [ "$rc" -eq 0 ] || fail "deps run #$runs exited $rc"
  grep -q ', 0 errors' "$last" || fail "deps run #$runs reported errors"
}

# status <branch> prints the status column of the branch's "pr" line in the last run.
status() {
  awk -v b="  $1  " 'index($0, "pr ") == 1 && (i = index($0, b)) { s = substr($0, 4, i - 4); sub(/^ +/, "", s); print s }' "$last"
}

expect_status() {
  local got
  got=$(status "$1")
  [ "$got" = "$2" ] || fail "run #$runs: $1: status \"$got\", want \"$2\""
  ok "$1: $2"
}

expect_dashboard_status() {
  grep -q "^dashboard $1  $dashboard_title" "$last" || fail "run #$runs: dashboard status is not $1: $(grep '^dashboard' "$last")"
  ok "dashboard: $1"
}

# pr_json <branch> [state] prints the newest PR of the branch.
pr_json() {
  gh api "repos/$repo/pulls?head=${repo%%/*}:$1&state=${2:-open}&per_page=1" --jq '.[0] // empty'
}

# branch_sha prints the tip of a branch, nothing if it does not exist.
branch_sha() {
  ghgit ls-remote "https://github.com/$repo.git" "refs/heads/$1" | cut -f1
}

# dashboard_json prints dashboard issue $dash_n; issue lists lag behind a fresh issue, so the
# number comes from the run's "dashboard" line.
dashboard_json() {
  gh api "repos/$repo/issues/$dash_n"
}

# expect_pr <branch> <title regex> asserts an open PR with the deps body; prints its JSON.
expect_pr() {
  local pr
  pr=$(pr_json "$1" "${3:-open}")
  [ -n "$pr" ] || fail "no ${3:-open} PR for $1"
  local title body
  title=$(jq -r .title <<<"$pr")
  body=$(jq -r .body <<<"$pr")
  [[ $title =~ $2 ]] || fail "$1: title \"$title\" does not match $2"
  grep -qF '| File | From | To | Type | Age |' <<<"$body" || fail "$1: body has no update table"
  grep -qF '### Release notes' <<<"$body" || fail "$1: body has no release notes section"
  grep -qE '^<!-- deps:fingerprint=[0-9a-f]{16} -->$' <<<"$body" || fail "$1: body has no fingerprint"
  [ "$(jq -r .base.ref <<<"$pr")" = main ] || fail "$1: base is not main"
  ok "$1: PR #$(jq -r .number <<<"$pr") \"$title\""
  echo "$pr"
}

# file_on <ref> <path> prints a file of the test repository at ref.
file_on() {
  gh api "repos/$repo/contents/$2?ref=$1" -H 'Accept: application/vnd.github.raw'
}

merged() { [ "$(gh api "repos/$repo/pulls/$1" --jq .merged)" = true ]; }

# check_green <sha> succeeds once the "check" run of sha completed successfully.
check_green() {
  local c
  c=$(gh api "repos/$repo/commits/$1/check-runs?check_name=check" --jq '[.check_runs[] | .status + "/" + (.conclusion // "")] | first // ""')
  case $c in
    completed/success) return 0 ;;
    completed/*) fail "check of $1 concluded $c" ;;
  esac
  return 1
}

# push_main <message> <command> commits what command changes in a clone of main and pushes it.
push_main() {
  local msg=$1 dir=$work/edit-$RANDOM
  shift
  ghgit clone -q --depth=1 "https://github.com/$repo.git" "$dir"
  (cd "$dir" && "$@")
  git -C "$dir" -c user.name=deps-e2e -c user.email=deps-e2e@noreply.invalid -c commit.gpgsign=false commit -qam "$msg"
  ghgit -C "$dir" push -q origin HEAD:main
  ok "pushed to main: $msg"
}

# --- scenario ---

case ${1:-} in
  "") ;;
  reset) reset_repo && exit 0 ;;
  *) echo "usage: $0 [reset]" >&2 && exit 2 ;;
esac

total_start=$SECONDS
step "setup and reset"
reset_repo
done_step "setup and reset"

step "1. first run: PRs, dashboard, auto-merge"
deps_run
expect_status "$b_node" created
expect_status "$b_python" created
expect_status "$b_cobra" "created, automerge"
expect_dashboard_status created
expect_pr "$b_node" '^chore\(deps\): update actions/setup-node to v7\.[0-9]+\.[0-9]+$' >/dev/null
expect_pr "$b_python" '^chore\(deps\): update actions/setup-python to v7\.[0-9]+\.[0-9]+$' >/dev/null
file_on "$b_python" .github/workflows/build.yml | grep -qE 'uses: actions/setup-python@[0-9a-f]{40} # v7\.[0-9]+\.[0-9]+$' ||
  fail "$b_python: build.yml is not digest-pinned with a version comment"
ok "$b_python: digest-pinned with version comment"
# state=all: GitHub may already have merged it if the check was fast.
cobra=$(expect_pr "$b_cobra" '^fix\(deps\): update github\.com/spf13/cobra to v1\.10\.[0-9]+$' all)
cobra_n=$(jq -r .number <<<"$cobra")
[ "$(jq -r '.auto_merge.merge_method // ""' <<<"$cobra")" != "" ] || [ "$(jq -r .merged_at <<<"$cobra")" != null ] ||
  fail "$b_cobra: auto-merge not enabled on #$cobra_n"
ok "$b_cobra: auto-merge enabled ($(jq -r '.auto_merge.merge_method // "already merged"' <<<"$cobra"))"
file_on "$(jq -r .head.sha <<<"$cobra")" go.sum | grep -qE '^github.com/spf13/cobra v1\.10\.[0-9]+ h1:' ||
  fail "$b_cobra: go.sum not tidied"
ok "$b_cobra: go.sum updated by go mod tidy"
dash_n=$(awk '$1 == "dashboard" { n = split($NF, p, "/"); print p[n] }' "$last")
[[ $dash_n =~ ^[0-9]+$ ]] || fail "no dashboard issue URL in run #$runs"
dash=$(dashboard_json)
[ "$(jq -r '.state + "/" + .title' <<<"$dash")" = "open/$dashboard_title" ] || fail "dashboard #$dash_n is not an open \"$dashboard_title\" issue"
dash_body=$(jq -r .body <<<"$dash")
for b in "$b_node" "$b_python" "$b_cobra"; do
  grep -qF -- "- [ ] <!-- deps:rebase=$b -->" <<<"$dash_body" || fail "dashboard has no rebase checkbox for $b"
done
grep -qF '<!-- deps:dashboard -->' <<<"$dash_body" || fail "dashboard has no marker"
[ "$(gh api "repos/$repo/issues?state=open&per_page=100" --jq "[.[] | select(.pull_request == null)] | length")" -le 1 ] ||
  fail "more than one open issue (duplicate dashboard?)"
ok "dashboard #$dash_n with rebase checkboxes"
log "waiting up to ${merge_timeout}s for GitHub to auto-merge #$cobra_n"
poll "$merge_timeout" "auto-merge of #$cobra_n" merged "$cobra_n"
ok "#$cobra_n merged by GitHub auto-merge"
done_step "1. first run + auto-merge"

step "2. second run: up to date, dashboard unchanged"
deps_run # main moved by the merge: the dashboard drops the merged PR
expect_status "$b_node" up-to-date
expect_status "$b_python" up-to-date
[ -z "$(status "$b_cobra")" ] || fail "$b_cobra still handled after the merge"
before=$(dashboard_json | jq -r .updated_at)
sleep 2
deps_run
expect_status "$b_node" up-to-date
expect_status "$b_python" up-to-date
expect_dashboard_status up-to-date
[ "$(dashboard_json | jq -r .updated_at)" = "$before" ] || fail "dashboard #$dash_n was edited by an unchanged run"
ok "dashboard #$dash_n not edited (updated_at $before)"
open_issues=$(gh api "repos/$repo/issues?state=open&per_page=100" --jq '[.[] | select(.pull_request == null) | .number | tostring] | join(",")')
[ "$open_issues" = "$dash_n" ] || fail "open issues are $open_issues, want only dashboard #$dash_n"
ok "exactly one dashboard issue"
done_step "2. second run"

step "3. dashboard rebase checkbox"
node_sha=$(branch_sha "$b_node")
node_n=$(pr_json "$b_node" | jq -r .number)
ticked=$(dashboard_json | jq -r .body | sed "s|^- \[ \] <!-- deps:rebase=$b_node -->|- [x] <!-- deps:rebase=$b_node -->|")
grep -qF -- "- [x] <!-- deps:rebase=$b_node -->" <<<"$ticked" || fail "could not tick the rebase checkbox"
gh api -X PATCH "repos/$repo/issues/$dash_n" -f body="$ticked" >/dev/null
ok "ticked rebase for $b_node"
deps_run
expect_status "$b_node" rebased
expect_status "$b_python" up-to-date
expect_dashboard_status updated
new_sha=$(branch_sha "$b_node")
[ -n "$new_sha" ] && [ "$new_sha" != "$node_sha" ] || fail "$b_node not rebased: $node_sha -> $new_sha"
ok "$b_node rebased: ${node_sha:0:7} -> ${new_sha:0:7}"
[ "$(gh api "repos/$repo/commits/$new_sha" --jq '.parents[0].sha')" = "$(branch_sha main)" ] || fail "$b_node is not on top of main"
[ "$(pr_json "$b_node" | jq -r .number)" = "$node_n" ] || fail "$b_node: PR changed"
dashboard_json | jq -r .body | grep -qF -- "- [ ] <!-- deps:rebase=$b_node -->" || fail "rebase checkbox not unticked"
ok "checkbox unticked, PR #$node_n kept"
done_step "3. dashboard rebase"

step "4. obsolete PR closed"
python=$(pr_json "$b_python")
python_n=$(jq -r .number <<<"$python")
push_main "e2e: ignore actions/setup-python" sh -c 'printf "  - names: [actions/setup-python]\n    ignore: true\n" >>.deps.yaml'
deps_run
expect_status "$b_python" closed
expect_status "$b_node" up-to-date
[ "$(gh api "repos/$repo/pulls/$python_n" --jq .state)" = closed ] || fail "#$python_n not closed"
[ -z "$(branch_sha "$b_python")" ] || fail "$b_python not deleted"
ok "#$python_n closed and $b_python deleted"
done_step "4. obsolete PR"

step "5. direct merge when auto-merge is disallowed"
settings false
ok "allow_auto_merge disabled"
push_main "e2e: downgrade cobra" cp "$fixture/go.mod" "$fixture/go.sum" .
deps_run
expect_status "$b_cobra" "created, automerge waiting for checks"
cobra=$(expect_pr "$b_cobra" '^fix\(deps\): update github\.com/spf13/cobra to v1\.10\.[0-9]+$')
cobra_n=$(jq -r .number <<<"$cobra")
[ "$(jq -r .auto_merge <<<"$cobra")" = null ] || fail "#$cobra_n has platform auto-merge although it is disallowed"
cobra_sha=$(jq -r .head.sha <<<"$cobra")
log "waiting up to ${merge_timeout}s for the check of #$cobra_n"
poll "$merge_timeout" "check of #$cobra_n" check_green "$cobra_sha"
ok "check of ${cobra_sha:0:7} green"
start=$SECONDS
until merged "$cobra_n"; do
  [ $((SECONDS - start)) -lt "$merge_timeout" ] || fail "deps did not merge #$cobra_n within ${merge_timeout}s"
  deps_run
  case $(status "$b_cobra") in
    "up-to-date, merged") break ;;
    "up-to-date, automerge waiting for checks") sleep 5 ;; # GitHub still computes mergeability
    *) fail "$b_cobra: unexpected status \"$(status "$b_cobra")\"" ;;
  esac
done
merged "$cobra_n" || fail "#$cobra_n not merged"
merged_by=$(gh api "repos/$repo/pulls/$cobra_n" --jq .merged_by.login)
[ "$merged_by" = "$user" ] || fail "#$cobra_n merged by $merged_by, want $user (deps' token)"
ok "#$cobra_n merged directly by deps ($merged_by)"
dashboard_json | jq -r .body | grep -qF "deps:rebase=$b_cobra" && fail "dashboard #$dash_n still lists merged #$cobra_n as open"
ok "dashboard #$dash_n no longer lists #$cobra_n"
done_step "5. direct merge fallback"

step "reset"
reset_repo
done_step "final reset"

log "summary ($repo, $runs deps runs, $((SECONDS - total_start))s total)"
printf '  %s\n' "${timings[@]}" >&2
echo "e2e: PASS" >&2
