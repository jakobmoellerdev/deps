# Running deps in CI

deps behaves the same locally and in CI. In CI it detects the platform from the runner environment and uses the job token, unless you pass another.

## Composite action

The repository root has a composite `action.yml`. It downloads a released static binary for the runner, verifies it against the release's `checksums.txt` with `sha256sum`, prints `deps version`, and runs it. Go is not needed to run the action. The same `action.yml` works unchanged on GitHub Actions and on Gitea/Forgejo Actions with default settings.

| Input | Default | Meaning |
|---|---|---|
| `token` | `${{ github.token }}` (the job token on GitHub, Gitea and Forgejo) | exported as `DEPS_TOKEN`; needs contents:write and pull-requests:write (issues:write for the dashboard) |
| `args` | `run` | arguments for `deps` |
| `version` | the action's ref if it is a release tag (`@v0.1.0`), else the latest release | release tag to download, e.g. `v0.1.0` |
| `repository` | `https://github.com/jakobmoellerdev/deps` on github.com runners, `https://git.jakob-moeller.cloud/jakobmoellerdev/deps` elsewhere | where releases are downloaded from: a Gitea/Forgejo repository URL (latest via `<host>/api/v1/repos/<repo>/releases/latest`) or a GitHub repository URL (latest via api.github.com; on github.com runners assets are fetched with `gh release download` and the job token, so private repositories work) |

Only linux/amd64 and linux/arm64 runners are supported; on any other OS or architecture the download step fails with `deps: unsupported runner <os>/<arch>; only linux/amd64 and linux/arm64 are released`. Pin a release tag (`@v0.1.0`) so runs are reproducible; `@main` downloads the latest release.

The default host for short `owner/repo@ref` actions is the platform default (`github.com`, or `data.forgejo.org` on Forgejo). Instances with a non-default `DEFAULT_ACTIONS_URL` should set [`actions.defaultHost`](configuration.md#actionsdefaulthost) or `DEPS_DEFAULT_ACTIONS_URL`.

## Gitea / Forgejo

```yaml
# .gitea/workflows/deps.yml (or .forgejo/workflows/deps.yml)
name: deps
on:
  schedule: [{cron: "0 5 * * *"}]
  workflow_dispatch:
permissions:
  contents: write
  pull-requests: write
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
        with:
          args: run -v --log-level info
```

Use the full URL in `uses:`. A short `jakobmoellerdev/deps@v0.1.0` is resolved against the instance's `DEFAULT_ACTIONS_URL`, which is github.com by default. Inside this repository, the dogfood workflow uses `uses: ./`.

If the job token cannot push, open PRs, or read private action/module repositories (`GITEA_TOKEN` cannot read other private repos by default), store a personal access token as a secret and pass it to both steps:

```yaml
      - uses: actions/checkout@v7.0.1
        with: {token: "${{ secrets.DEPS_TOKEN }}"}
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
        with: {token: "${{ secrets.DEPS_TOKEN }}"}
```

## GitHub

A GitHub workflow cannot `uses:` an action hosted on Gitea. There are two options.

**(a) Mirror the repository to GitHub** and use the action from the mirror. The defaults still download the binary from the Gitea releases; the `@v0.1.0` ref selects that release. Set `repository: https://github.com/<owner>/deps` only if the mirror also publishes the release assets.

```yaml
name: deps
on:
  schedule: [{cron: "0 5 * * *"}]
  workflow_dispatch:
permissions:
  contents: write
  pull-requests: write
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/create-github-app-token@v2
        id: app
        with:
          app-id: ${{ vars.DEPS_APP_ID }}
          private-key: ${{ secrets.DEPS_APP_KEY }}
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ steps.app.outputs.token }}
      - uses: <owner>/deps@v0.1.0
        with:
          token: ${{ steps.app.outputs.token }}
```

**(b) Without a mirror**, download and verify the binary in a `run` step instead of the action step:

```yaml
      - run: |
          base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.1.0
          cd "$RUNNER_TEMP" && curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt"
          grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 deps
      - run: '"$RUNNER_TEMP/deps" run'
        env:
          DEPS_TOKEN: ${{ steps.app.outputs.token }}
```

Use `deps_linux_arm64` on arm64 runners.

**Token caveat.** PRs and pushes made with `GITHUB_TOKEN` do not trigger workflows (`pull_request`, `push`), so CI never runs on bot PRs. Use a GitHub App installation token (as above) or a PAT. Pass it as `DEPS_TOKEN` (or `--token`) **and** as `actions/checkout`'s `token:`. deps pushes with the checkout's git credentials, so the branches it pushes then trigger CI.

**App identity.** If the commits should appear as the App, set `gitAuthor` (or `DEPS_GIT_AUTHOR`) to its noreply identity, e.g. `my-app[bot] <123+my-app[bot]@users.noreply.github.com>`. If the identity ever changes, list the old email in `gitIgnoredAuthors`; otherwise existing branches are reported as `skipped: branch modified by someone else`.

## Schedules

[`schedule`](configuration.md#schedule) entries must be cron triggers of the workflow. deps reads the triggering cron from the event payload (`GITHUB_EVENT_PATH`, field `schedule`; GitHub and Gitea 28.3 fill it) and creates PRs only for groups whose schedule lists it. `workflow_dispatch` and other non-cron triggers act on every group. For a daily run with weekly groups:

```yaml
on:
  schedule:
    - cron: "0 0 * * *"   # daily: deps without a schedule
    - cron: "0 0 * * 0"   # weekly: also groups with schedule: ["0 0 * * 0"]
  workflow_dispatch:
```

Both crons fire on Sunday at 00:00, so that day has two runs; the second finds the PRs of the first up to date. On runners that do not provide the payload, pass the cron explicitly, e.g. `args: run --trigger-schedule "0 0 * * 0"` in a workflow with only that cron (a `schedule` event without a readable cron matches every schedule, with a warning).

## Permissions

| Operation | Needs |
|---|---|
| push `<branchPrefix>*` branches, delete obsolete ones | contents: write |
| list, create, edit and close PRs | pull-requests: write |
| request auto-merge (`automerge`) | contents: write and pull-requests: write, and merge rights on the base branch. GitHub also needs "Allow auto-merge" in the repository settings; without it deps merges directly once checks are green, which reads commit statuses and check runs (statuses: read, checks: read) |
| find, create and edit the dashboard issue (`dependencyDashboard`) | issues: write |
| read private actions, modules, release notes | read access to those repositories (the same host's token, `DEPS_GITHUB_COM_TOKEN` for github.com, or the git credential helper) |

`deps run --dry-run` needs none of these and works anonymously.

## Tools on the runner

| Tool | Needed for |
|---|---|
| `git` | always for `run`: remote URL, `ls-remote`, commits via plumbing, push; the checkout's credentials are used |
| `go` | not needed to run deps; only for `go mod tidy` on go.mod updates, `go env` (falls back to the environment without it); `go list` for GOPRIVATE/direct modules |
| `npm`, `pnpm`, `yarn` | regenerating lockfiles. If the tool is not on `PATH`, deps runs it via `npx --yes <tool>[@<packageManager version>]`, so having `npm`/`npx` (e.g. from `actions/setup-node`) is enough. With none of them, the lockfile's manifests fail with `<tool> not found (needed to update <lock>)`. |
| Docker credentials | private registries: `~/.docker/config.json` or credential helpers (e.g. after `docker login`) |

`deps run` requires the checked-out `HEAD` to be the tip of `origin/<base>`, which a fresh `actions/checkout` of the default branch is. Shallow clones are fine.

## Platform detection

`run` detects the platform in this order:

1. **Environment** (no network):
   1. `FORGEJO_ACTIONS=true` → forgejo, from `FORGEJO_SERVER_URL`, `FORGEJO_API_URL` and `FORGEJO_REPOSITORY`, each falling back to the `GITHUB_*` name.
   2. `GITEA_ACTIONS=true` → gitea, from `GITHUB_SERVER_URL`, `GITHUB_API_URL` and `GITHUB_REPOSITORY`.
   3. `GITHUB_ACTIONS=true` → github (including GHES), from the same variables.
2. **The `origin` remote**, in the forms `https://host/owner/repo(.git)`, `git@host:owner/repo(.git)` and `ssh://git@host[:port]/owner/repo(.git)`:
   - `github.com` → github, API `https://api.github.com`.
   - Any other host is probed unauthenticated, with a 10s timeout each:
     1. `/api/forgejo/v1/version` → forgejo;
     2. `/api/v1/version` → gitea (both use API `https://host/api/v1`);
     3. `/api/v3/meta` → GHES (`https://host/api/v3`).

     If none matches, deps fails with `cannot detect platform for <host>; pass --platform and --api-url`.
3. **Overrides:** `--platform`, `--api-url` and `--repo` replace the detected values. If only the kind is given, the API URL is derived per kind.

`plan`, `apply` and `explain` only use step 1.

## Token sources

1. **Platform API and push:** `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`.
   - GitHub API requests send `Authorization: Bearer <t>`; Gitea and Forgejo requests send `Authorization: token <t>`.
2. **Lookups:**
   - The platform token is sent to the platform host as Basic `x-access-token:<t>`.
   - `DEPS_GITHUB_COM_TOKEN` is used for github.com when the platform is elsewhere, e.g. for actions and GitHub releases looked up from a Gitea job.
   - On a 401 without a static token, deps runs `git credential fill` once per host and retries.
3. **npm:** `.npmrc` `_authToken` entries. **docker:** the Docker credential chain.

## GitHub end-to-end test

`task e2e:github` builds `bin/deps` and runs `hack/e2e-github/e2e.sh`, which drives `deps run` against the real repository `<gh user>/deps-e2e` (override with `DEPS_E2E_REPO=owner/name`). It is not part of `task ci`: it needs network, a `gh` login for github.com with the `repo` and `workflow` scopes, and `git`, `go` and `jq`. A full run takes about 3 minutes, mostly waiting for the `check` workflow.

**Setup (idempotent, every run).** The repository is created if missing (public, so branch protection works on the free plan). Settings: issues on, `allow_auto_merge: true`, `delete_branch_on_merge: true`; branch protection on `main` requires the status check `check` (from `.github/workflows/check.yml`, `on: pull_request`), with `enforce_admins: false` and force pushes allowed so the test can reset and push to `main`.

**Reset (before and after the scenario).** `main` is force-pushed to a fixed commit of `hack/e2e-github/testdata/repo` (same SHA every time): a never-running workflow with `actions/setup-node@v7.0.0` and `actions/setup-python@v7.0.0`, a `go.mod` requiring `github.com/spf13/cobra v1.10.1`, and a `.deps.yaml` with `dependencyDashboard: true`, `pin: digest` for setup-python, `automerge: true` for patch updates, and `allow` bounds so new upstream releases do not change the update types. Open PRs are closed, and closed unmerged PRs are retitled `[e2e reset] …` so the next run does not skip them as "closed PR for this version". Branches other than `main` are deleted and open issues are closed. `hack/e2e-github/e2e.sh reset` runs only the reset.

**Scenario.** Each step runs `deps run -v` with `DEPS_TOKEN=$(gh auth token)` on a fresh clone of `main` and checks the result through the GitHub API. The script stops at the first mismatch.

1. **First run.** Three PRs are created on `deps/github-actions/actions-setup-node`, `deps/github-actions/actions-setup-python` and `deps/gomod/github.com-spf13-cobra`. The script checks the titles, the body (update table with ages, `### Release notes`, fingerprint), the digest pin with its `# v7.x.y` comment, and the tidied `go.sum`. It also checks that the dashboard issue exists with a rebase checkbox per PR, and that GitHub auto-merge is enabled on the cobra patch PR. GitHub must then merge that PR within 5 minutes (`DEPS_E2E_MERGE_TIMEOUT`).
2. **Second run.** The run after the merge reports the other PRs `up-to-date`. One more run must leave the dashboard unedited (same `updated_at`), and the dashboard must be the only open issue.
3. **Rebase checkbox.** Ticking the setup-node rebase box through the API makes the next run report `rebased`. The branch moves to a new commit on top of `main`, the PR number stays the same, and the box is unticked again.
4. **Obsolete PR.** A push to `main` adds a rule ignoring `actions/setup-python`. The next run reports `closed`: the PR is closed and its branch deleted.
5. **Direct merge.** With `allow_auto_merge` disabled, `main` is pushed back to cobra v1.10.1. The run creates the PR with `automerge waiting for checks`. Once the `check` run is green, later runs report `up-to-date, merged`. The PR must be merged by the token's user (deps), and the dashboard must no longer list it.

The summary prints the duration of each step and of each `deps run`. Set `DEPS_E2E_LOGS=<dir>` to keep each run's debug log (`--log-level debug`). GitHub's issue list lags a few seconds behind a new issue, so the script reads the dashboard by the number from the run's `dashboard` line. deps itself only lists issues at the start of a run, and scheduled runs are far enough apart.

## Releases (maintainers)

Every release is published on both forges: on Gitea by the `release` job below, and on the GitHub mirror by `.github/workflows/release.yml`, which runs when the push mirror delivers the tag (or via `workflow_dispatch` with a `tag` input to republish an existing tag). GitHub runners download from the mirror because the Gitea instance's edge blocks GitHub's datacenter IPs (HTTP 403). The mirror's workflow `.github/workflows/deps.yml` runs `deps plan` daily: pull requests and issues are disabled on the mirror and the push mirror prunes foreign branches, so PRs are only made on Gitea.

The Gitea push mirror needs a GitHub token with the `workflow` scope, otherwise GitHub rejects pushes that change `.github/workflows/*`.

Pushing a `v*` tag runs `.gitea/workflows/ci.yml`: the `test` job runs `task ci` once (Go from `actions/setup-go@v7.0.0` with `go-version-file: go.mod`, i.e. `toolchain go1.27.3`, and `cache: true`), then the `release` job (`needs: test`, `contents: write`):

1. `task release:build VERSION=<tag>` builds `dist/deps_linux_amd64` and `dist/deps_linux_arm64` with `CGO_ENABLED=0`, `-trimpath` and `-ldflags "-s -w -buildid= -X main.version=<tag>"`, and writes `dist/checksums.txt` (`sha256sum`).
2. Each binary is checked to be static (no `Requesting program interpreter` in `readelf -l`), because the action runs it on arbitrary runner images.
3. `task release:notes` runs [git-cliff](https://git-cliff.org) (pinned v2.14.2, installed and sha512-verified by `task changelog:install`; the checkout uses `fetch-depth: 0` so it sees all commits and tags) with `cliff.toml` and writes the notes of the tag, `git-cliff --latest --strip header`, to `dist/RELEASE_NOTES.md`.
4. `gt release create <tag> --verify-tag --notes-file dist/RELEASE_NOTES.md` (the Gitea CLI on the runner, authenticated with `GITEA_TOKEN` = the job token) publishes the release with the notes and the three assets.

git-cliff is installed from its GitHub release instead of through `orhun/git-cliff-action`: that action always sends `github.token` to api.github.com to find the release, and on Gitea that token is a Gitea token, so the request fails with 401.

**Release notes and CHANGELOG.md.** `cliff.toml` groups conventional commits (`feat`, `fix`, `perf`, `docs`, `ci`/`build`, `chore`/`refactor`/`style`/`test`, and `chore(deps)`/`fix(deps)` as Dependencies); other commit subjects are kept under "Changes"; merge commits are skipped. Use conventional commit subjects to get grouped notes. To update the changelog in the repository before a release:

```sh
task changelog -- --tag v0.2.0     # names the unreleased section v0.2.0
git add CHANGELOG.md && git commit -m "docs: changelog for v0.2.0"
git tag -a v0.2.0 -m "deps v0.2.0" && git push origin main v0.2.0
```

There is no release yet; the action's latest-release lookup fails until the first tag is pushed. Workflows that use the action at `@main` or without a release ref pick up a new release automatically; workflows pinned to `@vX.Y.Z` get it when deps (or Renovate) bumps their `uses:` ref.
