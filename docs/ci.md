# Running deps in CI

deps behaves the same locally and in CI. In CI it detects the forge from the job environment, reads the cron that triggered the run, and uses the job token unless you pass another. This guide covers GitHub Actions, Gitea Actions, Forgejo Actions and GitLab CI. Every input, flag and config key is listed in the generated references: [action inputs](reference/action.md), [CLI flags and environment](reference/cli.md), [configuration keys](reference/configuration.md).

## Getting started

1. **Add the workflow.** Copy the example for your forge: [GitHub](#github), [Gitea](#gitea), [Forgejo](#forgejo) or [GitLab](#gitlab-ci). Each one is a job with a checkout and deps. The first run opens one PR (merge request on GitLab) per outdated dependency or group.
2. **Choose the token.** On Gitea and Forgejo the job token works, but CI doesn't run on PRs it opens. On GitHub use an App token or a PAT, because `GITHUB_TOKEN` can't push changes to workflow files. On GitLab use a project or personal access token: the CI job token can't open merge requests. Details: [Tokens and permissions](#tokens-and-permissions).
3. **Set the schedule.** The workflow's `on.schedule` cron decides when deps runs. The examples also run [on push to the default branch](#run-on-push-to-the-default-branch), so open PRs are rebased right after a merge. To open some PRs only on certain crons, see [Schedules](#schedules).
4. **Optional: add a `.deps.yaml`.** Without one, every direct dependency is updated. Rules, groups and limits are in the [configuration reference](reference/configuration.md). To check the config on pull requests, see [Preview on pull requests](#preview-on-pull-requests).

Then, as needed: [automerge](#automerge), the [dependency dashboard](#dependency-dashboard), [signed commits](#signed-commits), [self-hosted runners](#self-hosted-runners-and-actions-hosts), and [troubleshooting](#troubleshooting).

## Contents

- [Getting started](#getting-started)
- [The composite action](#the-composite-action)
- [GitHub](#github)
- [Gitea](#gitea)
- [Forgejo](#forgejo)
- [GitLab CI](#gitlab-ci)
- [Tokens and permissions](#tokens-and-permissions)
- [Signed commits](#signed-commits)
  - [Platform commits (GitHub)](#platform-commits-github)
  - [Signing with a key](#signing-with-a-key)
- [Self-hosted runners and actions hosts](#self-hosted-runners-and-actions-hosts)
- [Self-managed GitLab](#self-managed-gitlab)
- [Several base branches](#several-base-branches)
- [Shared config and many repositories](#shared-config-and-many-repositories)
  - [Shared organisation config](#shared-organisation-config)
  - [One job for many repositories](#one-job-for-many-repositories)
- [Schedules](#schedules)
  - [Time windows](#time-windows)
- [Run on push to the default branch](#run-on-push-to-the-default-branch)
  - [Run when the dashboard is ticked](#run-when-the-dashboard-is-ticked)
  - [Merge right after CI](#merge-right-after-ci)
- [Automerge](#automerge)
- [Draft PRs and PRs after CI](#draft-prs-and-prs-after-ci)
- [Dependency dashboard](#dependency-dashboard)
- [Labels, reviewers and assignees](#labels-reviewers-and-assignees)
- [Post-upgrade tasks](#post-upgrade-tasks)
- [Rebasing or pausing a PR](#rebasing-or-pausing-a-pr)
- [Overlapping runs](#overlapping-runs)
- [Step summary](#step-summary)
- [Step outputs and report](#step-outputs-and-report)
- [Dry runs and config checks](#dry-runs-and-config-checks)
  - [Preview on pull requests](#preview-on-pull-requests)
- [Dependency graph (GitHub)](#dependency-graph-github)
- [Caching Go modules](#caching-go-modules)
- [Platform detection](#platform-detection)
- [Troubleshooting](#troubleshooting)
- [GitHub end-to-end test](#github-end-to-end-test)
- [Releases (maintainers)](#releases-maintainers)
  - [Attestations](#attestations)

## The composite action

The repository root has a composite [`action.yml`](../action.yml). The same file works unchanged on GitHub, Gitea and Forgejo. It runs these steps:

1. **Resolve.** It picks the release (see `version`) and the binary for the runner (linux/amd64 or linux/arm64).
2. **Preinstalled.** If `deps` is on PATH and `deps version` prints the resolved version, the action uses it and skips the cache and download steps (log: `deps: using preinstalled deps <version> (<path>)`). That binary is trusted as is, not checked against `checksums.txt`; set `use-preinstalled: false` to always take the cache/download path. It is not a cache, so `cache: false` does not disable it.
3. **Reuse or download.** Otherwise, with `cache: true` it reuses a binary of that version from the tool cache or the Actions cache (see **Binary cache** below). Otherwise it downloads it with `checksums.txt`, verifies it with `sha256sum`, and saves it to the Actions cache. It prints `deps version`.
4. **Run.** It runs `deps <args>` with `DEPS_TOKEN` set to the `token` input, and `DEPS_DRY_RUN`, `DEPS_LOG_LEVEL`, `DEPS_CONFIG`, `DEPS_SHOW_SKIPPED` and `DEPS_MAX_NEW_PRS` set to the `dry-run`, `log-level`, `config`, `show-skipped` and `max-new-prs` inputs (or, for an empty input, the job env's variable). Every deps flag reads `DEPS_<FLAG>` when not given, so other flags can be set in the job `env:`; a flag in `args` wins.

Go is not needed to run it; only go.mod updates need `go` for `go mod tidy`.

The inputs (`token`, `signing-key`, `allow-tasks`, `dry-run`, `log-level`, `config`, `show-skipped`, `max-new-prs`, `args`, `version`, `repository`, `use-preinstalled`, `cache`), the outputs (`version`, `cache-hit`), where the binary is downloaded from, and the supported runners (linux/amd64 and linux/arm64 only) are described in the generated [action reference](reference/action.md). This section covers how the steps behave.

**Binary cache.** Once the version is known, the action looks for that binary in two places before downloading:

1. **The runner's tool cache**, `$RUNNER_TOOL_CACHE/deps/<version>/<arch>`. Self-hosted runners that keep it between jobs reuse the binary without any network request (only the latest-release lookup remains, unless `version` or a release-tag ref is set). The log shows `deps: cache hit, using <version> from the tool cache`.
2. **The forge's Actions cache**, key `deps-<version>-linux-<arch>`, restored into that directory with `actions/cache/restore`. GitHub serves it from its cache service, Gitea and Forgejo from the runner's cache server (act_runner `cache:` settings). The log shows `Cache restored from key: deps-<version>-linux-<arch>` and `deps: cache hit, using <version> restored from the Actions cache`.

On a miss the binary is downloaded and verified, then saved under that key (`Cache saved with key: …`). Each cached binary has its checksum line from the release's `checksums.txt` stored next to it (`deps.sha256`), and it runs only if `sha256sum -c` passes; a mismatch logs a warning and downloads again. A new release is a new key, so nothing has to be evicted by hand. If the runner has no cache server, the restore and save steps only log a warning and the action downloads as before.

**Version pinning.** Pin a release tag (`@v0.1.0`) so runs are reproducible; `@main` downloads the latest release.

- deps updates its own `uses:` line like any other action.
- With [`pin: digest`](reference/configuration.md#pin), deps rewrites that line to `@<sha> # v0.1.0`. `github.action_ref` is then a SHA, and the action downloads the *latest* release. Set `version:` if the binary must match the pin exactly; deps keeps a `version:` equal to the current version in sync with the ref (`@<sha> # v0.5.0` plus `version: v0.5.0` both move to the new release).

## GitHub

### Workflow with a GitHub App token

```yaml
# .github/workflows/deps.yml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  push:
    branches: [main] # your default branch; see "Run on push to the default branch"
  workflow_dispatch:
concurrency:
  group: deps
  cancel-in-progress: false
permissions:
  contents: read
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/create-github-app-token@v3.3.0
        id: app
        with:
          client-id: ${{ vars.DEPS_APP_CLIENT_ID }}
          private-key: ${{ secrets.DEPS_APP_PRIVATE_KEY }}
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ steps.app.outputs.token }}
      - uses: jakobmoellerdev/deps@v0.5.0
        with:
          token: ${{ steps.app.outputs.token }}
          show-skipped: true
          log-level: info
```

Pass the token to **both** steps:

- **The action's `token` input** is the API token: deps lists, creates, edits and closes PRs with it. With an App token it also creates the commits and moves the branches through the API, so GitHub signs them ([platform commits](#platform-commits-github)).
- **`actions/checkout`** stores it as the git credential. deps lists and deletes branches with the checkout's credentials, and pushes with them when it commits with git.

**Why not `GITHUB_TOKEN`:**

1. **Workflow files.** `GITHUB_TOKEN` cannot change `.github/workflows/*`. A git push is rejected with `refusing to allow a GitHub App to create or update workflow … without workflows permission`; a platform commit (the default with `GITHUB_TOKEN`, which is an App installation token) fails earlier with `POST …/git/trees: 403 Forbidden: Resource not accessible by integration`. Action updates are exactly such commits.
2. **CI on the PRs.** `pull_request` runs for PRs that `GITHUB_TOKEN` opens or pushes to wait for approval: someone with write access has to approve them on the PR page, every time deps pushes ([GitHub's docs](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)). Other events, such as `push`, start no runs at all. So CI runs on deps PRs only by hand, and [automerge](#automerge) waits until someone approves.

`permissions: actions: write` does not help. It covers the Actions API (cancel or re-run runs, dispatch workflows, artifacts, caches), not git pushes. The `workflows` permission that the push error asks for exists only for GitHub Apps and PATs; the [`permissions` key](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions) has no `workflows` entry. Verified on 2026-11-04: a job with `contents: write, actions: write, pull-requests: write` was refused when pushing a change to `.github/workflows/check.yml`.

**Only `GITHUB_TOKEN`:** if you can't create an App or PAT, exclude workflow-file updates so the pushes succeed, and accept that CI on the PRs needs an approval each time (so automerge isn't automatic):

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
rules:
  - managers: [github-actions]
    ignore: true
```

Workflow permissions: `contents: write`, `pull-requests: write` (and `issues: write` for the dashboard). The commits are [platform commits](#platform-commits-github) by `github-actions[bot]`, verified by GitHub.

**Creating the App:**

1. *Settings → Developer settings → GitHub Apps → New GitHub App*. No webhook is needed.
2. Repository permissions:
   - *Contents*: read and write;
   - *Pull requests*: read and write;
   - *Workflows*: read and write;
   - *Issues*: read and write, for the [dashboard](reference/configuration.md#dependencydashboard);
   - *Checks* and *Commit statuses*: read, for the automerge fallback.
3. Install the App on the repositories deps should manage.
4. Store the App's client ID as the variable `DEPS_APP_CLIENT_ID` and a private key as the secret `DEPS_APP_PRIVATE_KEY`.

**A PAT instead.** A classic PAT needs the `repo` and `workflow` scopes. A fine-grained PAT needs *Contents*, *Pull requests* and *Workflows* set to write, plus *Issues* for the dashboard. Store it as a secret and pass it where the example uses `steps.app.outputs.token`. PRs then appear as opened by the PAT's user.

**The job token only.** It works when no workflow files change and CI on deps PRs isn't needed, e.g. when you only update go.mod with a separate CI trigger. Drop the App step and both `token:` inputs, and grant:

```yaml
permissions:
  contents: write
  pull-requests: write
  issues: write # only for dependencyDashboard
```

**Where the action comes from.** `uses: jakobmoellerdev/deps@…` resolves to [github.com/jakobmoellerdev/deps](https://github.com/jakobmoellerdev/deps), the public push mirror of the Gitea repository, so any workflow can use it. The defaults download the binary from the mirror's releases.

### Commit identity of the App

With an App token, deps creates its commits through the GitHub API ([`platformCommit: auto`](reference/configuration.md#platformcommit)): GitHub sets the author to the App's bot account, `<slug>[bot] <<id>+<slug>[bot]@users.noreply.github.com>`, and signs the commit. Nothing to configure; `gitAuthor` and `DEPS_GIT_AUTHOR` are ignored for these commits.

With `platformCommit: disabled` (or a signing key), deps commits with git as `deps[bot] <deps-bot@noreply.invalid>`. To have those commits appear as the App, set [`gitAuthor`](reference/configuration.md#gitauthor) in `.deps.yaml`, or set `DEPS_GIT_AUTHOR` in the job, to the App's noreply identity. Add this step after the token step; job-level environment variables reach the action's steps:

```yaml
      - name: deps commit identity
        env:
          GH_TOKEN: ${{ steps.app.outputs.token }}
          SLUG: ${{ steps.app.outputs.app-slug }}
        run: |
          id=$(gh api "/users/${SLUG}%5Bbot%5D" --jq .id)
          echo "DEPS_GIT_AUTHOR=${SLUG}[bot] <${id}+${SLUG}[bot]@users.noreply.github.com>" >> "$GITHUB_ENV"
```

deps updates only branches whose last commit has its own author email. If the identity changes, list the old email in [`gitIgnoredAuthors`](reference/configuration.md#gitignoredauthors). Otherwise existing branches are reported as `skipped: branch modified by someone else`. Switching to platform commits needs nothing: the configured git author still counts as deps' own. Switching back to git needs the App's noreply email in `gitIgnoredAuthors`.

### Without the action

If the action repository isn't reachable, install the binary with `go install` through the Go module proxy, then run it:

```yaml
      - uses: actions/setup-go@v7.0.0
        with:
          go-version: stable
      - run: go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@v0.5.0
      - run: deps run --show-skipped --log-level info
        env:
          DEPS_TOKEN: ${{ steps.app.outputs.token }}
```

Don't download from `git.jakob-moeller.cloud` on GitHub-hosted runners: its edge blocks GitHub's datacenter IPs (HTTP 403). The module proxy serves the module, and the mirror's releases serve the binaries.

### GitHub Enterprise Server

deps detects GHES from `GITHUB_ACTIONS=true` with the runner's `GITHUB_SERVER_URL` and `GITHUB_API_URL`.

- **Getting the action.** Mirror `jakobmoellerdev/deps` into the instance, or use [Without the action](#without-the-action). If the instance can't reach github.com, point `repository:` at a mirror that publishes the release assets.
- **Default actions host.** Short `owner/repo@ref` actions are looked up on github.com. If your workflows reference actions on the GHES instance instead, set [`actions.defaultHost`](reference/configuration.md#actionsdefaulthost).

## Gitea

```yaml
# .gitea/workflows/deps.yml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  push:
    branches: [main] # your default branch; see "Run on push to the default branch"
  workflow_dispatch:
concurrency:
  group: deps
  cancel-in-progress: false
permissions:
  contents: write
  pull-requests: write
  issues: write # only for dependencyDashboard
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.5.0
        with:
          show-skipped: true
          log-level: info
```

**`uses:` needs the full URL.** A short `jakobmoellerdev/deps@v0.5.0` is resolved against the instance's `DEFAULT_ACTIONS_URL`, which is github.com by default. This repository's own workflow (`.gitea/workflows/deps.yml`) is this example plus the bot token, signing key and identity, pinned to the latest release; deps opens the PR that bumps that pin after each release. It also has a `dry-run` job for pull requests ([Preview on pull requests](#preview-on-pull-requests)).

**The job token (`GITEA_TOKEN`).** Its permissions come from:

1. the job's `permissions:`;
2. the workflow's `permissions:`;
3. the owner's or repository's default (*Settings → Actions → General*).

The result is clamped to the configured maximum. With `contents: write` and `pull-requests: write` it can push deps branches and open PRs. Two limits apply:

- **No CI on deps PRs.** PRs and pushes made with the job token don't start new workflow runs. Here, deps PR #1, opened by `gitea-actions`, got no `pull_request` run.
- **Other private repositories.** It can read only its own repository and public ones. Private action or module repositories must allow it under *Settings → Actions → General → Cross-Repository Access* (read-only).

When CI must run on deps PRs (for [automerge](#automerge)), or a private repository can't grant cross-repository access, create a personal access token. It needs the scopes `write:repository`, and `write:issue` for the dashboard. Store it as the secret `DEPS_TOKEN` and pass it to both steps:

```yaml
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ secrets.DEPS_TOKEN }}
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.5.0
        with:
          token: ${{ secrets.DEPS_TOKEN }}
```

For a separate bot account, create the token on that account and give it write access to the repository. Set [`gitAuthor`](reference/configuration.md#gitauthor) to its name and email.

**Lookups on github.com.** Actions, GitHub releases and release notes on github.com are fetched anonymously from a Gitea job. Anonymous GitHub API requests share a low rate limit per IP. Set `DEPS_GITHUB_COM_TOKEN` to a github.com token, read-only and without scopes, to raise it and to read private github.com repositories:

```yaml
jobs:
  deps:
    runs-on: ubuntu-latest
    env:
      DEPS_GITHUB_COM_TOKEN: ${{ secrets.DEPS_GITHUB_COM_TOKEN }}
```

## Forgejo

```yaml
# .forgejo/workflows/deps.yml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  push:
    branches: [main] # your default branch; see "Run on push to the default branch"
  workflow_dispatch:
concurrency:
  group: deps
  cancel-in-progress: false
jobs:
  deps:
    runs-on: docker # a label of your runner
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.5.0
```

- **Detection.** deps detects Forgejo from `FORGEJO_ACTIONS=true` and reads `FORGEJO_SERVER_URL`, `FORGEJO_API_URL` and `FORGEJO_REPOSITORY`, falling back to the `GITHUB_*` names. API calls use the Gitea-compatible `/api/v1`.
- **The automatic token.** It is `FORGEJO_TOKEN`, also available as `GITHUB_TOKEN` and `github.token`, so the action's default `token` uses it. It can write to its repository, except in workflows on fork PRs, and read the repositories associated with the workflow. The Forgejo workflow reference has no `permissions:` key, so the example has no `permissions:` block.
- **CI and automerge.** As on Gitea, use a PAT secret for both steps when CI must run on deps PRs.
- **Default actions host.** Short `owner/repo@ref` actions resolve against `data.forgejo.org`, the default `DEFAULT_ACTIONS_URL` of Forgejo, and deps looks them up there too. `actions/checkout@v7.0.1` exists there. If your instance uses another `DEFAULT_ACTIONS_URL`, see [Self-hosted runners and actions hosts](#self-hosted-runners-and-actions-hosts).
- **Runner image.** The download step needs `bash`, `curl`, `git` and `sha256sum`. Debian-based images such as `node:<version>-bookworm`, a common image for Forgejo's `docker` label, have them; Alpine images need `apk add bash curl git`.

## GitLab CI

GitLab has no actions, so the job downloads the release binary and runs it:

```yaml
# .gitlab-ci.yml
deps:
  image: golang:1.27 # has git, curl and sha256sum; go is only needed for go.mod updates
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" || $CI_PIPELINE_SOURCE == "web"
    - if: $CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH # see "Run on push to the default branch"
  resource_group: deps # one run at a time, like a concurrency group
  variables:
    DEPS_VERSION: v0.5.0
  script:
    - |
      base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/$DEPS_VERSION
      (cd /tmp && curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt" &&
        grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 /usr/local/bin/deps)
    # The job token can't push: push with the deps token instead.
    - git remote set-url origin "${CI_SERVER_PROTOCOL}://oauth2:${DEPS_TOKEN}@${CI_SERVER_FQDN}/${CI_PROJECT_PATH}.git"
    - deps run --show-skipped --log-level info
```

Add a pipeline schedule (*Build → Pipeline schedules*) on the default branch, e.g. `0 5 * * *`.

- **Detection.** deps detects GitLab from `GITLAB_CI=true` and reads `CI_SERVER_URL`, `CI_API_V4_URL` and `CI_PROJECT_PATH`; nested groups (`group/subgroup/project`) work. Outside CI, the remote host `gitlab.com`, or an instance answering `/api/v4/version`, is GitLab ([Platform detection](#platform-detection)).
- **The token.** The CI job token (`CI_JOB_TOKEN`) can't create merge requests or issues, so create a project access token (*Settings → Access tokens*) or a personal access token of a bot user with the scopes `api` and `write_repository` and at least the Developer role. Store it as a masked CI/CD variable `DEPS_TOKEN` (`GITLAB_TOKEN` works too). Mark it protected only if the default branch is protected, or the job won't see it. Merge requests and pushes made with it start pipelines as usual.
- **Pushing.** deps pushes with the checkout's git credentials, and GitLab checks out with the job token, which may not push. The `git remote set-url` line makes git push with `DEPS_TOKEN`. The token then sits in the job's `.git/config`; see the security note of [post-upgrade tasks](#post-upgrade-tasks).
- **Merging.** For [automerge](#automerge) the token's role must be allowed to merge into the target branch (Maintainer on a default protected branch); for [branch automerge](#branch-automerge) it must be allowed to push to it.
- **Schedules.** GitLab doesn't tell the job which cron started it. If your `.deps.yaml` has [`schedule`](#schedules) entries, give each pipeline schedule the variable `DEPS_TRIGGER_SCHEDULE` with its cron (`0 5 * * 1`). Without it, a scheduled run acts on every group.
- **Lookups.** Lookups on the instance's host (git tags, [gitlabci](#self-managed-gitlab) includes) send the token as Basic `oauth2:<token>`; includes resolve against `CI_SERVER_URL`.
- **Not on GitLab:** the composite action, the step summary and step outputs, `::` workflow commands, [`deps preview`](#preview-on-pull-requests) check runs, [platform commits](#platform-commits-github) and the [dependency graph](#dependency-graph-github) are GitHub or Actions features. To check the config in merge request pipelines, run `deps plan`.

## Tokens and permissions

| | GitHub | Gitea | Forgejo |
|---|---|---|---|
| default token | `GITHUB_TOKEN`: can't change workflow files, and CI on its PRs waits for approval | `GITEA_TOKEN`: its PRs don't run CI | the automatic token: its PRs don't run CI |
| recommended token | a GitHub App token or PAT, given to `actions/checkout` and the action | the job token, or a PAT secret when CI must run on deps PRs | as on Gitea |
| push `<branchPrefix>*` branches, delete obsolete ones | `contents: write`; *Workflows* (App) or `workflow` (PAT) when workflow files change | `contents: write` | automatic token |
| list, create, edit and close PRs | `pull-requests: write` | `pull-requests: write` | automatic token |
| request auto-merge (`automerge`) | merge rights on the base branch; *Allow auto-merge* in the settings; `statuses: read` and `checks: read` for the direct-merge fallback | merge rights on the base branch | merge rights on the base branch |
| branch automerge (`automergeType: branch`) | `contents: write` and push rights on the base branch (bypass of its protection or rulesets); `statuses: read` and `checks: read` | push rights on the base branch (branch protection *Enable push*) | as on Gitea |
| open PRs after their checks (`prCreation`) | `statuses: read` and `checks: read` | read access (commit statuses) | as on Gitea |
| find, create and edit the dashboard issue (`dependencyDashboard`) | `issues: write` | `issues: write` | automatic token |
| read private actions, modules and release notes | read access to those repositories | Cross-Repository Access or a PAT | a PAT |
| CI runs on deps PRs | App token or PAT (with `GITHUB_TOKEN`: after approval) | PAT | PAT |

`deps run --dry-run` and `deps plan` only read. `--dry-run` works anonymously.

**Token sources:**

1. **Platform API.** deps takes `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, `GITLAB_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`.
   - GitHub and GitLab requests send `Authorization: Bearer <t>`.
   - Gitea and Forgejo requests send `Authorization: token <t>`.
2. **Git push.** deps uses the checkout's git credentials (`actions/checkout` stores its `token:`), not the API token. With [platform commits](#platform-commits-github), commits and branch updates use the API token, and git only lists and deletes branches.
3. **Lookups:**
   - The platform token is sent to the platform host as Basic `x-access-token:<t>` (`oauth2:<t>` on GitLab).
   - `DEPS_GITHUB_COM_TOKEN` is used for github.com when the platform is elsewhere.
   - On a 401 without a static token, deps runs `git credential fill` once per host and retries. deps never prompts (`GIT_TERMINAL_PROMPT=0`).
4. **npm, PyPI and Docker.** npm uses the `.npmrc` `_authToken` entries; PyPI indexes use the index URL's userinfo (`https://user:token@host/simple` in the manifest or `PIP_INDEX_URL`); docker uses the Docker credential chain (`~/.docker/config.json` or credential helpers, e.g. after `docker login`).
   - **Docker Hub.** Anonymous pulls are limited per IP address (`ratelimit-limit: 100;w=3600` at the time of writing), and hosted runners share IPs. deps resolves digests with uncounted HEADs, but each image whose release time is needed costs counted manifest GETs (two for a multi-platform image), so a repository with many Docker Hub images can exhaust the quota in one run. Lookups then fail at once with `rate limited by registry-1.docker.io` and a `docker login` hint. Log in before deps, e.g. with `docker/login-action` and a Docker Hub access token:

     ```yaml
     - uses: docker/login-action@v4.7.0
       with:
         username: ${{ vars.DOCKERHUB_USERNAME }}
         password: ${{ secrets.DOCKERHUB_TOKEN }}
     - uses: jakobmoellerdev/deps@v0.5.0
     ```
5. **Go modules.** `go` itself, through `GOPRIVATE`, `GONOPROXY`, `GOPROXY` and git credentials. Private modules need `GOPRIVATE` set in the job, and git credentials for their host. The checkout's credentials cover only the platform host; for other hosts use, e.g., `git config --global url."https://x-access-token:${TOKEN}@host/".insteadOf "https://host/"`.

## Signed commits

### Platform commits (GitHub)

On GitHub, deps can create its commits through the API instead of git ([`platformCommit`](reference/configuration.md#platformcommit), as Renovate's option of that name). GitHub signs commits it creates for a **GitHub App installation token**, so they show as verified, authored by the App's bot account and committed by GitHub. No key is involved.

- **When.** `auto` (the default) uses platform commits on GitHub when the token is an App installation token (it starts with `ghs_`: tokens from `actions/create-github-app-token`, and `GITHUB_TOKEN`) and no signing key is set. `enabled` uses them with any GitHub token; `disabled` never. `DEPS_PLATFORM_COMMIT` overrides the config.
- **PATs.** The API accepts commits made with a PAT, but GitHub leaves them unsigned (verified on 2026-11-04 with a classic PAT: `verification.reason: unsigned`), so `enabled` with a PAT only changes the author to the PAT's user. deps warns about it.
- **Signing key.** A key means local signing: `auto` then commits with git and signs; `enabled` together with a key is a usage error.
- **Author.** GitHub sets the author to the token's account; `gitAuthor` is ignored. deps treats a branch tip as its own when GitHub links its author to that account, and also when the tip has the configured git author (branches deps pushed with git before).
- **How.** deps uploads each changed file as a blob, creates a tree on the parent's tree and a commit, then moves the branch with the GraphQL `updateRefs` mutation, which compares the tip with the one listed at the start of the run and sets it in one step (`--force-with-lease`). If the branch moved meanwhile, the update is refused with `stale info` and the branch stays. Blob uploads are base64, so binary files and executable modes are kept. A failed step leaves the branch as it was; the objects created before it are unreferenced.
- **Gitea and Forgejo** are not supported. Their file API (`POST /repos/{owner}/{repo}/contents`, Gitea 28.3) commits on top of the tip of `branch` and can force-push the result to `new_branch` (`force_push`), but it has no expected old tip, so a push by someone else in between would be overwritten, and its file operations carry no file mode. Whether those commits are signed depends on the instance's `[repository.signing]` settings. `enabled` there is a usage error; `auto` uses git.

Example run with `GITHUB_TOKEN` (2026-11-04): the commit of the created PR had author `github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>`, committer `GitHub <noreply@github.com>` and `verification: {verified: true, reason: valid}`; the rebase after the base moved again produced a verified commit on the new base.

### Signing with a key

deps builds its git commits with `git commit-tree`, which signs only when asked to. Pass a private key and deps signs every commit it pushes. Branch protection that requires signed commits then accepts deps' PRs.

```yaml
    env:
      DEPS_GIT_AUTHOR: deps-bot <deps-bot@example.com> # the key owner's verified email
    steps:
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ secrets.DEPS_BOT_TOKEN }}
      - uses: jakobmoellerdev/deps@vX.Y.Z # signing needs a release newer than v0.1.0
        with:
          token: ${{ secrets.DEPS_BOT_TOKEN }}
          signing-key: ${{ secrets.DEPS_SIGNING_KEY }}
```

- **The key.** The `signing-key` input (`DEPS_SIGNING_KEY`, `--signing-key`) is the private key itself or the path of a file holding it. deps detects the format from the armor header:
  - `-----BEGIN OPENSSH PRIVATE KEY-----` signs with SSH (`ssh-keygen` on the runner);
  - `-----BEGIN PGP PRIVATE KEY BLOCK-----` signs with OpenPGP (`gpg` on the runner). deps imports the key into a temporary `GNUPGHOME`.

  The key must have no passphrase. Generate a dedicated one, e.g. `gpg --batch --passphrase '' --quick-gen-key 'deps-bot <deps-bot@example.com>' ed25519 sign never` or `ssh-keygen -t ed25519 -N '' -f deps-bot`. deps removes its temporary copies when it exits and logs only the fingerprint: `commit signing format=openpgp key=<fingerprint>`.
- **The identity.** A forge marks a signature as verified only if the commit's committer belongs to the key's owner. deps uses the same identity for author and committer: set [`gitAuthor`](reference/configuration.md#gitauthor) or `DEPS_GIT_AUTHOR` to the bot account's name and verified email. If you change the identity, list the old email in [`gitIgnoredAuthors`](reference/configuration.md#gitignoredauthors) so that deps keeps updating its existing branches.
- **Without a key**, deps behaves as before: commits are unsigned unless the checkout's git configuration sets `commit.gpgSign` (and `user.signingKey`), which `git commit-tree` honors.

**Gitea and Forgejo.**

- The key must be registered and verified on the committer's account, and the committer email must be a verified email of that user. For a GPG key, open *Settings → SSH / GPG Keys*, or `POST /user/gpg_keys` with `armored_public_key` plus `armored_signature`, a detached signature of the token from `GET /user/gpg_key_token`. An SSH key is verified on the same settings page with `ssh-keygen -Y sign -n gitea` over the shown token.
- A dedicated user (e.g. `deps-bot`) with write access to the repository and a token with `write:repository` (and `write:issue` for the dashboard) keeps the bot's identity, key and token apart from people. That token goes to both `actions/checkout` and the action, so that CI also runs on the PRs.
- **One bot for many repositories.** Store the bot's token and key once as Actions secrets of the owning user or organization instead of per repository: `PUT /api/v1/user/actions/secrets/{name}` (`gt secret set <name> --account`), or `PUT /api/v1/orgs/{org}/actions/secrets/{name}`. Every repository of that owner can then use `${{ secrets.DEPS_BOT_TOKEN }}` and `${{ secrets.DEPS_SIGNING_KEY }}`; each one still needs the bot as a collaborator with write access (also for `actions/checkout` of a private repository). Delete repository secrets of the same names, so that only one value is in use. Gitea cannot list or read back account secrets, so record the token name and key fingerprint in the secret descriptions.
- Branch protection: *Require signed commits*.

**GitHub.** Add the public key as a *signing key* (GPG or SSH) to the account whose verified email is the committer, usually a machine user. A GitHub App has no signing keys, so commits under its `[bot]` identity can't be signed by deps; use [platform commits](#platform-commits-github) for those. Branch protection: *Require signed commits*.

**Merging the PRs.** Only a fast-forward keeps deps' signed commit as it is. Every other merge style writes new commits, and those are signed only if the forge holds its own key:

- **Gitea/Forgejo:** merge, rebase and squash commits are signed only with `[repository.signing]` configured in `app.ini` (`SIGNING_KEY`, `MERGES`). Otherwise *Require signed commits* blocks those styles. Allow *Fast-forward only* (repository settings → *Pull Requests*).
- **GitHub:** merge and squash commits made on the website are signed by GitHub's own key and show as verified.

To keep deps' branches fast-forwardable when other changes land first, set [`rebaseWhen: behind-base-branch`](reference/configuration.md#rebasewhen).

## Self-hosted runners and actions hosts

**Short `owner/repo@ref` actions.** Forges resolve them against `DEFAULT_ACTIONS_URL`: github.com on GitHub and Gitea, data.forgejo.org on Forgejo. deps has to look the action up on the same host. It picks the host in this order:

1. [`actions.defaultHost`](reference/configuration.md#actionsdefaulthost) in `.deps.yaml`;
2. `DEPS_DEFAULT_ACTIONS_URL`, a URL or a bare host;
3. the platform default: `data.forgejo.org` on Forgejo, `github.com` otherwise.

With `--log-level info`, deps logs the chosen host as `actions default host host=<h> source=<s>`. For an instance whose `DEFAULT_ACTIONS_URL` is, e.g., `https://gitea.com`, add this to the config:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
actions:
  defaultHost: gitea.com
```

or set it in the job:

```yaml
    env:
      DEPS_DEFAULT_ACTIONS_URL: https://gitea.com
```

Actions with a full URL (`uses: https://host/owner/repo@ref`) are looked up on that host.

**Tools on the runner:**

| Tool | Needed for |
|---|---|
| `bash`, `curl`, `sha256sum` | the action's download step (`gh` instead of `curl` on github.com runners when it is installed) |
| `git` | always for `run`: remote URL, `ls-remote`, commits via plumbing, push; also for git-based Cargo registry indexes (an index URL without `sparse+`) |
| `go` | only for `go mod tidy` after go.mod updates, and `go list` for `GOPRIVATE`/direct modules. `go env` falls back to the environment without it. `GOTOOLCHAIN=auto` fetches a newer toolchain when go.mod requires one. |
| `npm`, `pnpm`, `yarn` | regenerating lockfiles; see the note below the table |
| `cargo` | updating `Cargo.lock` (requirement changes and lockfile-only updates), e.g. from `dtolnay/rust-toolchain`; without it those manifests fail with `cargo not found (needed to update Cargo.lock)` |
| `composer` 2.7+ (PHP with Composer, e.g. `shivammathur/setup-php`) | updating `composer.lock` (constraint changes and lockfile-only updates) with `composer update --no-install`; without it those composer.json files fail with `composer not found (needed to update composer.lock)`. Platform requirements are ignored (except a `config.platform.php`), so any PHP version works, unless [`composerIgnorePlatformReqs`](reference/configuration.md#composerignoreplatformreqs) lists which to ignore |
| `bundle` (Ruby with Bundler, e.g. `ruby/setup-ruby`) | updating `Gemfile.lock` (requirement changes and lockfile-only updates) with `bundle lock`; without it those Gemfiles fail with `bundle not found (needed to update Gemfile.lock)`. Bundler records its own version under `BUNDLED WITH`, so use the version the lock names |
| `julia` (e.g. `julia-actions/setup-julia`) | updating `Manifest.toml` (compat changes and manifest-only updates) with Pkg; without it those projects fail with `julia not found (needed to update Manifest.toml)`. Pkg writes the running `julia_version` into the manifest, so use the release the manifest names. Projects without a manifest (registered packages) need no Julia |
| `dart` (e.g. `dart-lang/setup-dart`), or `flutter` for Flutter packages (e.g. `subosito/flutter-action`) | updating `pubspec.lock` (constraint changes, git tags and lockfile-only updates) with `dart pub upgrade <name>`, `dart pub get` after SDK constraint changes; Flutter packages use the Dart SDK bundled with `flutter` (`FLUTTER_ROOT` set). The SDK must satisfy the pubspecs' SDK constraints. Without it those pubspecs fail with `dart not found (needed to update pubspec.lock)` or `flutter not found (…)` |
| `bazel` or `bazelisk` (e.g. `bazel-contrib/setup-bazel`) | updating `MODULE.bazel.lock` with `bazel mod deps --lockfile_mode=update` after `MODULE.bazel` or `.bazelversion` updates, when the workspace has a lock; without it those files fail with `bazel not found (needed to update MODULE.bazel.lock)`. Bazelisk picks the Bazel version of `.bazelversion` (and is preferred for `.bazelversion` updates) |
| `mix` with Hex (Elixir, e.g. `erlef/setup-beam`) | updating `mix.lock` (requirement changes and lockfile-only updates) with `mix deps.update`; without it those mix.exs files fail with `mix not found (needed to update mix.lock)`. Mix evaluates mix.exs and fetches the project's dependencies (git ones are cloned), so use the Elixir/OTP versions the project builds with |
| `gleam` (e.g. `erlef/setup-beam`) | updating `manifest.toml` (requirement changes and lockfile-only updates) with `gleam deps update`; without it those gleam.toml files fail with `gleam not found (needed to update manifest.toml)` |
| `nix` 2.19+ (e.g. `cachix/install-nix-action`, `DeterminateSystems/nix-installer-action`) | updating `flake.lock` with `nix flake update <input>` after flake input updates (also lock file maintenance); without it those flake.nix files fail with `nix not found (needed to update flake.lock)`. deps passes its github.com token to nix as `extra-access-tokens` |
| `uv`, `poetry`, `pipenv` | regenerating `uv.lock`, `poetry.lock`, `Pipfile.lock`; poetry and pipenv fall back to `uvx --from <tool>`, so `uv` (e.g. `astral-sh/setup-uv`) is enough |
| `uv` or `pip-compile` | regenerating `requirements*.txt` written by `uv pip compile` or pip-compile (the tool its header names) after an update of it or of its `.in` inputs; pip-compile falls back to `uvx --from pip-tools pip-compile`. Other hashed requirements files need no tool. |
| `terraform`, `tofu` | refreshing `.terraform.lock.hcl`; see the note below the table |
| Terraform CLI config | private registries: `TF_TOKEN_<host>`, or the `credentials` / `credentials_helper` of `~/.terraformrc` / `$TF_CLI_CONFIG_FILE` and `~/.terraform.d/credentials.tfrc.json` (as written by `terraform login`) |
| Java (`java` on `PATH` or `JAVA_HOME`, e.g. `actions/setup-java`) | Gradle lockfiles and dependency verification metadata, and regenerating the Gradle and Maven wrappers (`./gradlew`/`gradle`, `./mvnw`/`mvn`) |
| Docker credentials | private registries; Docker Hub in repositories with many images (pull limit) |

For lockfiles, deps runs the tool from `PATH`. If it is missing, deps falls back to `npx --yes <tool>[@<packageManager version>]`, so `npm`/`npx` (e.g. from `actions/setup-node`) is enough. With none of them, the lockfile's manifests fail with `<tool> not found (needed to update <lock>)`.

`.terraform.lock.hcl` is refreshed with `terraform init -backend=false` (`tofu` for OpenTofu) when a provider constraint changes or a provider gets a lockfile-only update (`rangeStrategy: update-lockfile`), so put the CLI on the runner (`hashicorp/setup-terraform` or `opentofu/setup-opentofu`). There is no fallback; without it those directories fail with `terraform not found (needed to update .terraform.lock.hcl)`. The runner also needs to reach the provider registries and the sources of remote modules, which init downloads.

**Checkout.** `deps run` requires the checked-out `HEAD` to be the tip of `origin/<base>`. A fresh `actions/checkout` of the default branch satisfies that; shallow clones are fine. `--dry-run` only warns when the base moved since the checkout. To manage a branch other than the default, check it out and pass `--base <branch>`, or list it in [`baseBranches`](#several-base-branches).

**Network.** The runner needs HTTPS access to:

- the forge;
- the upstream hosts of your dependencies: github.com, proxy.golang.org, registries, registry.npmjs.org, index.crates.io and crates.io (plus whatever cargo fetches for `Cargo.lock`), pypi.org, www.python.org (Python releases), api.anaconda.org (conda packages; private channels need `DEPS_CONDA_TOKEN` or a `/t/<token>/` channel URL), gitlab.com or your GitLab instance (`gitlab-packages` custom deps, and the API for the release times of `git-tags` deps hosted there), rubygems.org (and the Gemfiles' `source`s), repo.packagist.org (and composer.json's composer `repositories`, plus whatever Composer fetches for `composer.lock`), package.elm-lang.org, galaxy.ansible.com (Ansible roles and collections, and the `source` servers of requirements files), updates.jenkins.io, which redirects to mirrors.updates.jenkins.io and its mirrors (Jenkins plugins), pub.dev (and the `hosted` repositories of pubspec.yaml; plus whatever pub fetches for `pubspec.lock`), storage.googleapis.com (Dart and Flutter SDK releases), hex.pm (Hex packages; plus repo.hex.pm and whatever Mix or Gleam fetch for `mix.lock` and `manifest.toml`), builds.hex.pm (hexpm-bob custom deps), codeload.github.com and release-assets.githubusercontent.com (Homebrew formula archives and release assets, downloaded to hash the new `sha256`), your Artifactory hosts (`artifactory` custom deps), the apk repositories of `apk` deps' `registryUrl`s (dl-cdn.alpinelinux.org, packages.wolfi.dev, …), bcr.bazel.build (and the `--registry`s of `.bazelrc`; plus whatever Bazel fetches for `MODULE.bazel.lock`), raw.githubusercontent.com (Ruby and Go release lists, vcpkg versions databases), static.rust-lang.org (Rust releases), endoflife.date (`endoflife-date` custom deps), raw.githubusercontent.com/JuliaRegistries/General (Julia packages; plus whatever Pkg fetches for `Manifest.toml`, by default pkg.julialang.org), api.nuget.org (and the nuget.config package sources), builds.dotnet.microsoft.com (.NET SDK releases), the `registryUrl` yum/dnf repositories of `rpm` deps (and the mirrors their `repomd.xml` `xml:base` names), deb.debian.org (deb packages without a registry URL; else the hosts of the deb registry URLs, such as security.debian.org or archive.ubuntu.com), gitlab.com, gitea.com and code.forgejo.org (the default instances of the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags datasources, else their `registryUrl`), repo.maven.apache.org (Maven, Gradle, sbt and Clojure artifacts), repo.clojars.org (Clojure libraries) and repo.scala-sbt.org, which redirects to scala.jfrog.io (sbt plugins; plus the sbt `resolvers`), the git hosts of flake inputs (plus whatever nix fetches for `flake.lock`: api.github.com and codeload.github.com for `github:` inputs, channels.nixos.org and releases.nixos.org for NixOS channels), the `spec.url` of Glasskube `PackageRepository`s (the default packages.dl.glasskube.dev redirects to glasskube.github.io), api.osv.dev for security updates;
- the release download host.

deps honors `HTTPS_PROXY`/`NO_PROXY`.

## Self-managed GitLab

The [`gitlabci`](reference/managers.md#gitlabci) manager resolves `include: {project, ref}` and components whose host is the GitLab instance against one base URL. deps picks it in this order:

1. [`gitlabci.url`](reference/configuration.md#gitlabciurl) in `.deps.yaml`, an absolute http(s) URL without query or fragment. A path prefix is kept, e.g. `https://example.com/gitlab`;
2. `CI_SERVER_URL`, set by GitLab CI;
3. the server of the platform, when `run` detected GitLab from the remote (`https://<host>`);
4. `https://gitlab.com`.

With `--log-level info`, deps logs it as `gitlab url url=<u> source=<config|env|platform|default>`.

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
gitlabci:
  url: https://gitlab.example.com
```

A component path starting with `$CI_SERVER_FQDN` or `$CI_SERVER_HOST` (also `${...}`) is on the instance. So is one whose host, port included, is the instance's host. Components on other hosts are looked up at `https://<host>/<project>`.

**Private instances.** deps clones tags, and lists the releases and tags of the `gitlab-releases` and `gitlab-tags` datasources, with credentials for the instance's host:

1. `DEPS_GITLAB_TOKEN`, a personal, group or project access token with `read_repository` (`read_api` for the releases and tags APIs), as user `oauth2` (the API gets it as `Authorization: Bearer`);
2. otherwise, in a GitLab CI job on the same instance (`CI_SERVER_HOST`, or the host of `CI_SERVER_URL`), `CI_JOB_TOKEN` as user `gitlab-ci-token` (the API gets it as `JOB-TOKEN`). The job token reads only projects that allow it in their job token allowlist.

A platform token (`DEPS_TOKEN` and friends) for the same host takes precedence over both. Without any of them, deps falls back to `git credential fill` on a 401 (the releases and tags APIs also on 403 and 404, which GitLab answers for private projects).

## Several base branches

To keep release branches updated next to the default branch, list them in [`baseBranches`](reference/configuration.md#basebranches) (Renovate's `baseBranchPatterns`, Dependabot's `target-branch` per update entry). One job handles them all; check out the default branch as usual:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
baseBranches: [$default, '/^release\/v\d+$/']
rules:
  - baseBranches: ['/^release\//']   # only patches on release branches
    allowedUpdateTypes: [patch]
```

- deps fetches every other base (shallow in a shallow clone, so the default `fetch-depth: 1` is fine) into a temporary linked work tree and resolves it with the default branch's config; with [`useBaseBranchConfig: merge`](reference/configuration.md#usebasebranchconfig) the base's own `.deps.yaml` is merged over it (the log prints `base    release/v2  config .deps.yaml + release/v2:.deps.yaml`). The token needs write access to those branches like to the default one.
- `deps explain --base release/v2 '<glob>'` shows how a base resolves, and `deps preview` in a pull request job resolves both checkouts for the pull request's target (`$GITHUB_BASE_REF`).
- Branches carry the base: `deps/main/gomod/golang.org-x-net`, `deps/release/v2/gomod/golang.org-x-net`. Run CI on `deps/**` pushes as before.
- Each base has its own PRs and cleanup; the PR limits count all bases together, so raise `--max-new-prs`, `prConcurrentLimit` and `prHourlyLimit` with the number of bases. The dashboard lists each base in its own section.
- Branch protection of the release branches applies as on the default branch (automerge, [branch automerge](#branch-automerge)).

## Shared config and many repositories

### Shared organisation config

Keep the settings every repository shares in one config repository, e.g. `my-org/deps-config` with a `.deps.yaml`, and extend it from each repository's `.deps.yaml` ([`extends`](reference/configuration.md#extends)):

```yaml
# .deps.yaml of my-org/service-a
extends:
  - my-org/deps-config            # its .deps.yaml on the default branch
  - my-org/deps-config//go.yaml#v2 # a preset file at tag v2
rules:                            # these come after the shared rules, so they win
  - names: ['github.com/aws/**']
    group: aws
```

- **Fetching.** deps fetches each file with git (a shallow fetch of the ref, then the file), sending the platform token to the platform host, and fails the config (exit 2, annotated at the `extends` entry) when a file is missing, does not decode strictly, is invalid or extends itself in a cycle.
- **Tokens.** A public config repository needs nothing. A private one needs a token that can read it: on GitHub the job's `GITHUB_TOKEN` reads only its own repository, so pass an App token or PAT with access to the config repository as the action's `token` (it becomes `DEPS_TOKEN`). On Gitea and Forgejo the job token reads other private repositories of the owner only when they allow it under *Settings → Actions → General → Cross-Repository Access*.
- **Pin it.** `#<tag>` or `#<commit>` makes a change of the shared config a reviewed bump in each repository; without a ref every run reads the default branch of the config repository. deps opens those bumps itself: the [`deps-config`](reference/managers.md#deps-config) manager moves a version-tag ref (`#v2` → `#v3`) to newer tags of the config repository and a commit ref to the head of its default branch, as Renovate's renovate-config manager does for presets.
- **Which rule applied.** `deps explain <dep>` prints the file and line of every matching rule, e.g. `#0 names=[actions/*] allow=<7 (from my-org/deps-config//.deps.yaml:3)`.

### One job for many repositories

Instead of a workflow in every repository, one workflow (e.g. in `my-org/deps-config`) can run deps on many repositories with `run --repositories` (Renovate's `repositories` and `autodiscover`):

```yaml
# .github/workflows/deps.yml of my-org/deps-config
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  workflow_dispatch:
concurrency:
  group: deps
  cancel-in-progress: false
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: jakobmoellerdev/deps@v0.5.0
        with:
          token: ${{ secrets.DEPS_TOKEN }} # write access to every repository
          args: run --repositories my-org/*,other-org/tool
          show-skipped: true
          log-level: info
```

- **No checkout needed.** Each repository is cloned shallowly into a temporary directory with the token (as `actions/checkout` would), run, and removed. The forge comes from the job environment.
- **Patterns.** `owner/name`, or `owner/<glob>` on the names of an organization's (else a user's) repositories the token can list. On GitLab entries are project paths (`group/sub/name`) and a glob matches the projects of the group and its subgroups: `*` within one path segment, a `**` segment any number of them (`my-group/**` is every project below the group). Archived repositories and mirrors are skipped. `DEPS_REPOSITORIES` (whitespace- or comma-separated) is the default.
- **Token.** It must be able to push to, and open PRs in, every repository; the job token cannot, so use a GitHub App installation token (one installation covering the repositories) or a bot account's PAT. CI runs on the PRs it opens, as it is not the job token.
- **Config.** Each repository's own `.deps.yaml` applies (they can all `extends` the shared config); `--config <file>` instead applies one file to all of them. Schedules match the cron that started this workflow.
- **Results.** Each repository gets a `repository <owner/name>` line, its own result lines and summary; a failing repository is reported and the others still run (`<n> repositories, <m> failed`, exit 1 if any failed). The step summary has a section per repository, the step outputs are summed, and `--report` holds every repository's report ([Step outputs and report](#step-outputs-and-report)). Annotations name the repository instead of a file.
- **Releases once.** Datasources are shared across the repositories, so a release list or registry lookup is fetched once per run.

## Schedules

A [`schedule`](reference/configuration.md#schedule) entry, top-level or in a rule, is either a **cron trigger** of the workflow (a cron with a fixed minute, `0 5 * * 1`) or a **[time window](#time-windows)** (`before 6am on monday`, or a cron with minute `*`). Cron triggers are compared as text after collapsing whitespace, so write them the same way in both files.

- **Reading the trigger.** deps reads the triggering cron from the event payload: `GITHUB_EVENT_PATH`, field `schedule`. GitHub fills it, and so does Gitea (verified in 28.3).
- **Which groups act.** Only groups whose schedule lists that cron get new PRs. `workflow_dispatch` and other non-cron triggers act on every group. A `push` run is outside every schedule: groups without a schedule get new PRs, scheduled groups only have their open PRs refreshed ([updateNotScheduled](reference/configuration.md#updatenotscheduled)), and security updates are proposed as always.
- **Open PRs outside the schedule.** They keep being updated and rebased ([`updateNotScheduled`](reference/configuration.md#updatenotscheduled), default true).

For a daily run with weekly groups:

```yaml
on:
  schedule:
    - cron: "0 5 * * *" # daily: deps without a schedule
    - cron: "0 5 * * 1" # Mondays: also groups with schedule: ["0 5 * * 1"]
  push:
    branches: [main] # follow-up run after merges: refreshes open PRs, opens no weekly group
  workflow_dispatch:
```

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
schedule: ["0 5 * * *"] # default for every dep
rules:
  - names: ["k8s.io/**"]
    schedule: ["0 5 * * 1"] # Kubernetes only on Mondays
  - updateTypes: [major]
    schedule: ["0 5 * * 1"] # majors only on Mondays
```

Both crons fire on Monday at 05:00, so that day has two runs. The second run finds the PRs of the first up to date; a [concurrency group](#overlapping-runs) keeps the two from overlapping.

- **Runners without the payload.** Pass the cron explicitly with `args: run --trigger-schedule "0 5 * * 1"` (or `DEPS_TRIGGER_SCHEDULE`) in a workflow that has only that cron.
- **A `schedule` event without a readable cron** matches every cron trigger, and deps logs a warning.

Updates waiting for their cron or window are reported as `skipped: outside schedule`. They appear on the dashboard under *Awaiting schedule*, where ticking one creates its PR on the next run.

### Time windows

Renovate's schedules are time windows, and deps supports them as well: `before 6am`, `after 10pm`, `every weekend`, `on monday`, `before 6am on monday`, `every weekday`, `on the first day of the month`, `after 10pm and before 5am every weekday`, and crons with minute `*` such as `* 0-5 * * 1` (the [full syntax](reference/configuration.md#schedule)). A window is checked against the time of the run, in [`timezone`](reference/configuration.md#timezone) (UTC by default), whatever started the run. So the workflow needs crons that fire inside the windows, and a `push` run inside a window opens its PRs while a `workflow_dispatch` outside it does not:

```yaml
on:
  schedule:
    - cron: "0 * * * *" # hourly, so every window gets a run
  push:
    branches: [main]
  workflow_dispatch:
```

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
timezone: Europe/Berlin
schedule: ["after 10pm and before 5am every weekday", "every weekend"] # outside office hours
rules:
  - updateTypes: [major]
    schedule: ["before 6am on monday"]
```

A schedule may mix triggers and windows; the group is in schedule when any entry matches. To check a config, `deps run --dry-run --schedule-time 2026-11-09T05:30:00+01:00` evaluates the windows at that time (`DEPS_SCHEDULE_TIME` does the same); `deps explain <dep>` prints the schedule with its time zone.

## Run on push to the default branch

The example workflows also run on `push` to the default branch (`main` in the examples; set yours). Without it, deps only reacts on the next cron, up to a day later. After a push, the repository has changed in ways the open PRs depend on:

- **Merging a deps PR.** Other open deps PRs may now conflict with the new base (two updates touching the same `go.mod`/`go.sum` or lockfile), or be behind it. The push run rebases them right away ([`rebaseWhen`](reference/configuration.md#rebasewhen)), so the next PR is mergeable without waiting a day.
- **The dashboard.** The dependency dashboard issue is rewritten to match the new state (the merged PR leaves *Open*, the next pending updates show up).
- **Config changes.** A pushed `.deps.yaml` takes effect immediately: PRs that a new `ignore` rule or `allow` constraint makes obsolete are closed, and changed groups are rebuilt.
- **Manual dependency bumps.** If someone updates a dependency by hand, the matching deps PR is closed as obsolete instead of staying open until the next cron.

**What a push run does.** A `push` run is outside every cron trigger of a [schedule](#schedules) (a [time window](#time-windows) still applies by the clock):

| Group | Push run |
|---|---|
| No `schedule` | acts as on the cron: creates, updates and closes PRs |
| With a `schedule`, PR already open | refreshed and rebased ([`updateNotScheduled`](reference/configuration.md#updatenotscheduled), default true) |
| With a `schedule`, no PR yet | `skipped: outside schedule`; waits for its cron |
| Security update | created regardless of schedules |

So merges never open weekly groups early. `workflow_dispatch` (and `repository_dispatch`) is different: a manual run matches every cron trigger, so it acts on every group except those scheduled only by time windows outside the run time. Every other event is a maintenance run like `push`, which makes the triggers below safe to add.

**Notes.**

- **No loops.** deps pushes only to its own branches (`deps/…`), so its pushes don't match `branches: [main]`. Merging a deps PR is a push to `main` and does trigger a run; that is the point. With [branch automerge](#branch-automerge) deps pushes to `main` itself, at most once per run and only for a branch whose checks passed; with an App token or PAT that push starts the next run, which ends the chain once no green branch is left.
- **The token decides whether merges trigger.** On GitHub, pushes made with `GITHUB_TOKEN` (including automerges performed with it) start no workflows, so the follow-up run only happens for merges by a user, an App token or a PAT. Gitea and Forgejo behave the same for their job tokens.
- **Busy repositories.** Every push to `main` starts a run. Keep the [concurrency group](#overlapping-runs) with `cancel-in-progress: false`: bursts queue up behind one run instead of racing. To skip pushes that can't matter, add `paths-ignore` (e.g. `docs/**`), keeping `.deps.yaml` and the dependency files included.
- **Cost.** A run without changes takes seconds (lookups are deduplicated per run; the binary comes from the [action's cache](#the-composite-action)).

### Run when the dashboard is ticked

A ticked [dashboard](#dependency-dashboard) checkbox acts on the next run. To make that run start right away, also trigger on edits of the dashboard issue:

```yaml
on:
  issues:
    types: [edited]
jobs:
  deps:
    if: >-
      github.event_name != 'issues' ||
      (github.event.issue.title == 'Dependency Dashboard' && github.event.sender.type != 'Bot')
```

Use your [`dependencyDashboardTitle`](reference/configuration.md#dependencydashboardtitle) if you set one. The `sender.type` check skips deps' own edits of the dashboard with an App token (edits made with `GITHUB_TOKEN` start no workflows anyway). With a PAT, deps edits the issue as that user: the edit starts one more run, which finds nothing to do and leaves the dashboard unedited, so it ends there. Checkboxes act in any run, so the ticked PR is created, recreated or rebased; other scheduled groups still wait for their cron. Gitea and Forgejo have the same `issues` event.

### Merge right after CI

Without native auto-merge (GitHub with *Allow auto-merge* off), deps merges an [automerge](#automerge) PR itself once its checks are green, on the next run. To have that run follow CI immediately, trigger on the completion of your CI workflow:

```yaml
on:
  workflow_run:
    workflows: [CI] # the name of the workflow that checks deps PRs
    types: [completed]
jobs:
  deps:
    if: github.event_name != 'workflow_run' || startsWith(github.event.workflow_run.head_branch, 'deps/')
```

`check_suite` doesn't work for this: GitHub doesn't start workflows for check suites created by GitHub Actions. The same applies to [branch automerge](#branch-automerge) and to [`prCreation`](#draft-prs-and-prs-after-ci). With native auto-merge the forge merges without deps, and this trigger is not needed.

## Automerge

[`automerge`](reference/configuration.md#automerge), top-level or per rule, asks the forge to merge a PR once its required checks pass. Merging uses [`automergeStrategy`](reference/configuration.md#automergestrategy): `auto` is the repository's default style, or `merge`, `squash` or `rebase`.

**GitHub:**

1. deps enables auto-merge with the GraphQL `enablePullRequestAutoMerge` mutation. GitHub then merges the PR when branch protection or a ruleset is satisfied.
2. The repository needs *Settings → General → Allow auto-merge*, and the base branch needs required status checks.
3. If auto-merge is disabled, or GitHub reports the PR already mergeable (`clean status`), deps merges the PR itself. It does that only when the PR is mergeable and all commit statuses and check runs succeeded, in that run or a later one. Until then the PR is reported with `automerge waiting for checks`.
4. The checks only run when the PR was pushed with an App token or PAT.

**Gitea and Forgejo:**

1. deps calls `POST /pulls/{n}/merge` with `merge_when_checks_succeed`.
2. The forge waits only for the status checks that branch protection marks as required. Without branch protection with required checks, it merges **immediately**.
3. Since the job token doesn't start CI on deps PRs, use a PAT and configure required status checks on the base branch.
4. A fresh push whose mergeability check is still running is reported as `automerge pending` and requested again on the next run.

**GitLab:**

1. deps calls `PUT /merge_requests/{iid}/merge` with `merge_when_pipeline_succeeds` (and `auto_merge`, its name since GitLab 17.11). GitLab merges once the head pipeline succeeds and deletes the branch; the MR shows *Set to auto-merge*. deps reads `merge_when_pipeline_succeeds` back: a run that finds it set reports `automerge enabled` and sends nothing. A push to the branch (update, rebase) cancels auto-merge, so after its own push deps requests it again (`automerge`); GitLab may still cancel that request while it processes the push, or answer 409 (`automerge pending`) before it has, and the next run, finding it unset, requests it again.
2. If the head commit has no pipeline, GitLab merges **immediately**, unless *Settings → Merge requests → Pipelines must succeed* is on. Turn it on, and make CI run on the deps branches or their merge requests.
3. Refusals (draft, conflicts, a failed or missing required pipeline, missing approvals, a head that moved) are reported as `automerge pending` and requested again on the next run.
4. `automergeStrategy` only chooses squashing: `squash` squashes, `merge` and `rebase` don't, `auto` keeps the project's squash default. Merge commit, semi-linear history or fast-forward is the project's merge method.

### Branch automerge

With [`automergeType: branch`](reference/configuration.md#automergetype) (Renovate's branch automerge) updates reach the base branch without a PR: deps pushes the branch, and a later run fast-forwards the base branch to it once the branch's commit statuses and check runs are all green, then deletes the branch. Failed checks, or a base branch that refuses the push, open the PR instead.

```yaml
automerge: true
automergeType: branch
rules:
  - updateTypes: [major]   # majors as PRs, reviewed by hand
    automerge: false
```

1. **CI on the branches.** Your CI workflow must run on pushes to the update branches, or they never get checks and wait forever (`branch automerge waiting for checks`):

   ```yaml
   on:
     push:
       branches: [main, 'deps/**']
   ```

   As for PRs, pushes with the job token start no workflows: give deps an [App token or PAT](#github).
2. **Pushing to the base branch.** The token must be allowed to push to `main`. GitHub branch protection or rulesets that require PRs or status checks refuse it, unless the App or user is on the bypass list. On Gitea and Forgejo, the branch protection must allow pushes (*Enable push*, with the token's user in the allowlist if it is restricted). A refused push opens the PR, which is then automerged like `automergeType: pr`.
3. **One merge per run.** Merging moves `main`, so the other branches are behind it. The next run rebuilds them, their checks run again, and a later run merges them. Run deps often enough (or [on push](#run-on-push-to-the-default-branch)) to work through a backlog.
4. **Waiting branches** are listed on the [dependency dashboard](#dependency-dashboard) under *Pending branch automerge*; tick one to get a PR instead.

## Draft PRs and PRs after CI

[`draftPR: true`](reference/configuration.md#draftpr) opens new update PRs as drafts: GitHub draft PRs, on Gitea and Forgejo PRs whose title starts with `WIP: `, and on GitLab merge requests whose title starts with `Draft: ` (their API has no draft flag). PRs that automerge are never drafts, since forges do not merge drafts.

[`prCreation`](reference/configuration.md#prcreation) delays the PR until the branch's checks are in, so reviewers are only notified about updates whose CI result is known:

```yaml
prCreation: status-success  # or not-pending: also open the PR when a check failed
prNotPendingHours: 25       # not-pending: open the PR anyway this long after the last commit while checks are pending or absent
rules:
  - updateTypes: [major]
    prCreation: not-pending
    draftPR: true
```

1. **CI on the branches.** The first run only pushes the branch; a later run reads its commit statuses and check runs. CI must therefore run on pushes to the update branches, not only on `pull_request`, or the branch waits forever (`PR waiting for checks`). As for [branch automerge](#branch-automerge): `on: push: branches: [main, 'deps/**']`, and an [App token or PAT](#github), since job-token pushes start no workflows.
2. **A later run opens the PR.** deps does not wait within a run. Run it often enough, or [after CI](#merge-right-after-ci) with `workflow_run`, so PRs follow their checks closely.
3. **Waiting branches** are listed on the [dependency dashboard](#dependency-dashboard) under *Pending status checks* (with `checks failed` for a failed branch under `status-success`); tick one to get its PR right away.
4. **No checks.** Under `not-pending`, a branch whose checks are still running or absent gets its PR [`prNotPendingHours`](reference/configuration.md#prnotpendinghours) (default 25, as in Renovate) after its last commit; under `status-success` it waits until ticked.

`prCreation: approval` pushes the branch too, so CI runs on it, but opens the PR only once it is approved on the [dependency dashboard](#dependency-dashboard) (needs `dependencyDashboard: true`): the branch is listed under *Pending approval*, whatever its checks, and ticking it opens the PR on the pushed commit. Unlike [`dashboardApproval`](reference/configuration.md#dashboardapproval), which holds back the branch as well, the checks have already run when you approve.

## Dependency dashboard

With [`dependencyDashboard: true`](reference/configuration.md#dependencydashboard), `deps run` maintains one issue. It lists the updates waiting for approval or their schedule, rate-limited and blocked updates, open PRs, deprecated dependencies (npm `deprecated`, yanked crates and PyPI releases, inactive PyPI projects, archived source repositories of GitHub releases, actions and other git tags, and GitLab/Gitea/Forgejo deps, deprecated GitHub-hosted runner images) and, with [`abandonmentThreshold`](reference/configuration.md#abandonmentthreshold), abandoned ones, errors, and the detected dependencies. Ticking a checkbox makes the next run create, recreate or rebase that PR. The token needs `issues: write`, and the repository must have issues enabled. The title, labels and the exact list are in the [configuration reference](reference/configuration.md#dependencydashboard).

To review some updates before deps opens their PRs, for example majors, set [`dashboardApproval`](reference/configuration.md#dashboardapproval) on a rule. Those updates get no PR; they are listed under *Pending approval*, and ticking one makes the next run create its PR:

```yaml
dependencyDashboard: true
rules:
  - updateTypes: [major]
    dashboardApproval: true
```

## Post-upgrade tasks

[`rules[].postUpgradeTasks`](reference/configuration.md#rulespostupgradetasks) (e.g. `["go generate ./..."]`) run only when the workflow allows their commands with the `allow-tasks` input (`DEPS_ALLOW_TASKS`, `--allow-tasks`): one pattern per line, matched against the whole command. `*` matches any text except command chaining and substitution (`;`, `&`, `|`, backticks, `$(`, line breaks), so `make *` allows `make docs` but not `make docs; curl …` or `make $(id)`; operators written in the pattern match literally (`echo * >> deps.txt`), and the pattern `*` alone allows every command. A PR with a command no pattern allows fails instead of missing its generated files. Install the tools the commands need in a step before deps.

```yaml
      - uses: jakobmoellerdev/deps@v0.5.0
        with:
          allow-tasks: |
            go generate ./...
            make *
```

`*` alone allows every command. Prefer exact commands: the allowlist lives in the workflow, not in the repository's config, so a config change cannot add a command the workflow did not allow.

**Security.** Opting in runs repository code on the runner: `make generate` runs the Makefile, `go generate` the `//go:generate` directives, and whoever can change the default branch's config or those files can run code in the deps job. The commands run in a temporary copy of the checkout that is a throwaway git repository (the checkout's objects shared read-only) and get deps' environment without its tokens, but that is no sandbox: they can still read the real checkout's `.git/config` by path (where `actions/checkout` keeps the push credentials unless `persist-credentials: false`), the file system and the network. Enable it only for repositories whose code you trust like their CI. Tasks run on the base branch plus the update, never on a PR's code.

## Labels, reviewers and assignees

[`labels`](reference/configuration.md#labels), [`reviewers`](reference/configuration.md#reviewers) and [`assignees`](reference/configuration.md#assignees), top-level or per rule (where `addLabels` adds to the labels instead of replacing them), are set on each PR deps creates. A group's PR gets those of all its updates. For example, label every PR for release notes tooling and route Go updates to a team:

```yaml
labels: [dependencies]
rules:
  - managers: [gomod]
    addLabels: [go]
    reviewers: [team:backend]
```

- **Only new PRs.** They are set right after the PR is created; later runs don't change them, so labels, reviewers and assignees someone adds or removes by hand stay. A config change applies to PRs created after it.
- **Failures.** If a part fails (an unknown user, a team without access), the PR is still created and the run reports the error, e.g. `deps/gomod/x: request reviewers of #12: 422 …`.
- **GitHub.** Labels that don't exist are created by GitHub. Teams are `team:<slug>` of the repository's organization. The token needs `pull-requests: write`.
- **Gitea and Forgejo.** Labels must exist in the repository or its organization; missing ones are left out with a warning. Reviewers and assignees need write access to the repository.
- **GitLab.** Labels that don't exist are created in the project. Reviewers and assignees are usernames; GitLab has no team reviewers, so `team:<slug>` fails that part. GitLab Free keeps only one assignee and one reviewer per merge request.

## Rebasing or pausing a PR

Each PR can be steered from the PR itself, without the dashboard:

- **Rebase checkbox.** Every PR body ends with `- [ ] If you want to rebase/retry this PR, check this box`. Tick it, and the next run rebuilds the branch on the current base, even outside its schedule and even when someone else pushed to it (those commits are discarded), like the dashboard's rebase checkbox. The run reports `rebased` (or `updated` when the content changed) and unticks the box. Ticking it changes nothing else: the PR's fingerprint covers the file contents, not the body.
- **[`rebaseLabel`](reference/configuration.md#rebaselabel)** (default `rebase`) does the same as a label; deps removes it after the push.
- **[`stopUpdatingLabel`](reference/configuration.md#stopupdatinglabel)** (default `stop-updating`) pauses the PR: deps no longer pushes or rebases it and doesn't close it as obsolete, so commits you push to the branch stay. The run reports `skipped: stop-updating label`; the dashboard lists the PR under *Ignored or blocked*. Remove the label to hand it back.

The request acts on the next run. To act right away, also run deps on `pull_request` events (`types: [edited, labeled]`) of `deps/` branches; `run` needs the base branch checked out (`actions/checkout` with `ref: ${{ github.event.repository.default_branch }}`), not the PR's merge commit. As with the [dashboard trigger](#run-when-the-dashboard-is-ticked), skip events whose `sender.type` is `Bot`, so deps' own body edits don't start runs. A label must exist before it can be added to a PR; create `rebase` and `stop-updating` (or your names) once.

## Overlapping runs

Two runs of the same repository can race on the same branches: two crons firing together, a manual dispatch during a scheduled run. The push is guarded with `--force-with-lease`, so a race fails with an error instead of losing commits, but the second run's work is wasted. Serialize the runs with a concurrency group:

```yaml
concurrency:
  group: deps
  cancel-in-progress: false
```

`cancel-in-progress: false` lets a running job finish and queues the next one. GitHub, Gitea (since 1.26) and Forgejo support `concurrency`.

## Step summary

`plan`, `apply` and `run` (also `run --dry-run`) append a markdown summary to the job summary file, `GITHUB_STEP_SUMMARY` or else `GITEA_STEP_SUMMARY`. It has the same content as the `Summary:` block on stderr (`deps help output`):

- a headline: dependencies found, updates, PRs (by status for `run`), releases held back by `minimumReleaseAge`, skipped, errors and the duration; or `No updates: <n> dependencies up to date, <k> skipped.`;
- one section per PR, grouped like `run` groups them (`plan` and `apply` show the PRs `run` would open): the branch, the status and link (`run`), automerge and schedule (`waiting for <cron>`), and a `| Dependency | File | From | To | Type |` table, security updates marked with their advisory IDs;
- closed obsolete PRs and the dependency dashboard;
- the releases held back by `minimumReleaseAge`: too new (`too new (released <date>, minimum <age>)`), or, under `minimumReleaseAgeBehaviour: timestamp-required`, without a known release time (`release time unknown (…)`);
- the skip reasons with counts and how to update, for example `ignored by default (indirect; …)` → add a rule with `ignore: false`; the skipped dependencies are in a collapsible `<details>`;
- the errors in full.

GitHub shows it on the run page. On Gitea and Forgejo it shows where the instance supports job summaries; elsewhere the file is ignored.

GitHub rejects a step summary larger than 1 MiB as a whole, so deps keeps the file within that size, counting what earlier commands of the step wrote. A summary that doesn't fit drops, in this order, the list of skipped dependencies, the held-back releases and the PRs' update tables (the counts stay); a summary still too large is cut at a line and ends with `Step summary truncated to its size limit; the job log has everything.` For scale: `plan --show-skipped` on external-secrets (677 updates, 5306 skipped dependencies) writes about 450 KB.

**Annotations and log groups (GitHub, Gitea).** On GitHub and Gitea, errors also become annotations: a failed lookup at its dependency's file and line, each config problem at the line and column of its key in `.deps.yaml`, so a [config check on a pull request](#dry-runs-and-config-checks) marks the changed line in *Files changed*. Warnings (for example obsolete PRs left open because of errors) are warning annotations, security updates notices at their file and line, and `--show-skipped` puts the skip lines into a collapsible group of the log. Each phase (discovery and extraction, resolution, PRs) is a collapsible group too, with a progress line every 30 seconds, and a token from the git credential helper is masked. At most 10 error, 10 warning and 10 notice annotations are printed per step, as many as GitHub shows. Gitea 1.26 and later show groups and severity-labelled annotation lines in the job log (older Gitea prints them as plain lines; `--log-format text` turns them off). Forgejo prints workflow commands literally, so deps prints none there. A re-run with debug logging (`RUNNER_DEBUG=1`) logs at `debug`.

## Step outputs and report

The action has outputs with the counts of the run (`updates`, `prs`, `created`, `updated`, `closed`, `automerged`, `errors`) and `report`, the path of a JSON file with the whole result: every PR with its updates, status and link, the held-back releases, the skip reasons and the errors with their file and line. The fields are listed in `deps help output`. Later steps can use them, for example to notify on new PRs or on errors:

```yaml
      - uses: jakobmoellerdev/deps@v0.5.0
        id: deps
        continue-on-error: true # keep going on lookup errors; the next step decides
        with:
          args: run
          show-skipped: true
      - if: steps.deps.outputs.created != '0' || steps.deps.outputs.errors != '0'
        env:
          REPORT: ${{ steps.deps.outputs.report }}
        run: jq -r '.prs[] | select(.status == "created") | "\(.title) \(.url)"' "$REPORT"
```

Outside the action, `--report <file>` (or `DEPS_REPORT`) writes the same file, and the counts go to `GITHUB_OUTPUT` whenever it is set. No report is written when deps stops before it has a result (an invalid config, exit code 2); the outputs are empty then. An explicit `--report` in `args` takes the place of the action's file, so the `report` output then names a file that wasn't written.

## Dry runs and config checks

These modes only read, so they work with `contents: read`:

- **`deps plan`:** a unified diff of every update, without git or API writes.
- **`deps run --dry-run`:** the PR actions that would happen (`would-create`, `would-update`, `would-rebase`, `would-automerge`).

To validate `.deps.yaml` on every PR, add a job to your CI workflow:

```yaml
on:
  pull_request:
    paths: [".deps.yaml", ".deps.yml"]
permissions:
  contents: read
jobs:
  deps-config:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: jakobmoellerdev/deps@v0.5.0 # full URL on Gitea/Forgejo
        with:
          args: plan
          show-skipped: true
```

Exit codes:

- **2:** an invalid config; every problem is printed with its location.
- **1:** an upstream lookup failed.

To see why a dependency is or isn't updated, use `args: explain '<name glob>'`.

The GitHub mirror of this repository runs `deps plan` the same way (`.github/workflows/deps.yml`): it exercises the action, binary download and lookups on GitHub, and lists the updates in the job summary.

### Preview on pull requests

Three kinds of PR checks are useful, and all only read:

- **Config changes (most repositories).** The `plan` job above, limited to `.deps.yaml` with `paths:`. It checks the PR's config and lists the updates it leads to in the job summary.
- **What a config change does to the PRs.** `deps preview` compares the PR's checkout with the base branch's: which update PRs would be new, which would change and which would no longer be opened. See below.
- **The PRs deps would open.** `run --dry-run` lists each PR as `would-create`, `would-update`, `would-rebase` or `would-automerge`, in the log and the job summary. Run it on a checkout of the base branch, not on the PR's own checkout: it reports against whatever is checked out. If `HEAD` is not the tip of `origin/<base>`, which happens when the base moves while the job runs, the dry run logs a warning and reports against the checkout; only `run` without `--dry-run` fails then. The job token with read access is enough.

**`deps preview`.** It needs both checkouts. On GitHub it also creates the check run *deps preview* on the PR's head commit, with the comparison as its report, so it appears on the PR's *Checks* tab; that takes `checks: write` and an App installation token (`GITHUB_TOKEN` works, a PAT can't create check runs). Without it, and on Gitea and Forgejo, the comparison is in the job summary. The check concludes `success`, or `neutral` when lookups failed; it never blocks a merge.

```yaml
on:
  pull_request:
    paths: [".deps.yaml", ".deps.yml"]
permissions:
  contents: read
  checks: write # GitHub: the deps preview check run
jobs:
  deps-preview:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
        with:
          path: pr
      - uses: actions/checkout@v7.0.1
        with:
          ref: ${{ github.event.pull_request.base.ref }}
          path: base
      - uses: jakobmoellerdev/deps@v0.5.0 # full URL on Gitea/Forgejo
        with:
          args: preview --dir pr --base-dir base
```

On fork PRs `GITHUB_TOKEN` is read-only, so deps prints `note    no check run …` and only writes the job summary.

deps itself previews code changes this way. The `dry-run` job in `.gitea/workflows/deps.yml` builds deps from the PR, runs `plan` on the PR checkout, and runs `run --dry-run` on a checkout of the base branch. So the PR's code runs against the real repository and its open PRs, with the base branch's config. The job has its own concurrency group per PR, so it doesn't queue behind a running deps job:

```yaml
on:
  pull_request:
    branches: [main]
jobs:
  dry-run:
    runs-on: ubuntu-latest
    concurrency:
      group: deps-dry-run-${{ github.event.pull_request.number }}
      cancel-in-progress: true
    permissions:
      contents: read
      pull-requests: read
      issues: read
    steps:
      - uses: actions/checkout@v7.0.1
        with:
          path: pr
      - uses: actions/checkout@v7.0.1
        with:
          ref: ${{ github.event.pull_request.base.ref }}
          path: base
      - uses: actions/setup-go@v7.0.0
        with:
          go-version-file: pr/go.mod
      - run: go build -C pr -trimpath -o "$RUNNER_TEMP/deps" ./cmd/deps
      - run: '"$RUNNER_TEMP/deps" plan --show-skipped --dir pr'
      - run: '"$RUNNER_TEMP/deps" run --dry-run --show-skipped --dir base --base "$BASE"'
        env:
          BASE: ${{ github.event.pull_request.base.ref }}
          DEPS_TOKEN: ${{ github.token }}
```

With the released action instead of a build, use the same two checkouts and `args: run --dry-run --show-skipped --dir base`.

## Dependency graph (GitHub)

GitHub's dependency graph reads go.mod, package.json, the Python manifests, Cargo.toml, pom.xml and workflows by itself, but not Gradle builds, Dockerfiles, Compose and Kubernetes files, or versions in custom places. `deps submit` sends the dependencies deps finds there to the graph, so they appear in *Insights → Dependency graph*, the SBOM export and dependency review, and get Dependabot alerts where GitHub's advisory database covers the ecosystem (Maven, Go, npm, PyPI, crates, Actions; container images are listed but get no alerts). It only extracts, so it takes a second or two. What is submitted and how, including `--all-managers`, is in `deps help submit`.

```yaml
on:
  push:
    branches: [main]
permissions:
  contents: write # the dependency submission API
jobs:
  dependency-graph:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: jakobmoellerdev/deps@v0.5.0
        with:
          args: submit
```

The dependency graph must be enabled for the repository (*Settings → Code security*); otherwise GitHub answers `404 … The Dependency graph is disabled for this repository`. Each workflow job keeps its latest snapshot; a new push replaces it.

## Caching Go modules

`go mod tidy` downloads every module of an updated go.mod. deps also reads release times for [`minimumReleaseAge`](reference/configuration.md#minimumreleaseage) from `GOMODCACHE` (`cache/download/**/@v/*.info`) before asking the proxy. A warm module cache makes both faster. With a go.mod at the repository root, `actions/setup-go` caches it:

```yaml
      - uses: actions/checkout@v7.0.1
      - uses: actions/setup-go@v7.0.0
        with:
          go-version-file: go.mod
          cache: true
      - uses: jakobmoellerdev/deps@v0.5.0
```

For several modules, or without setup-go, use `actions/cache`:

```yaml
      - uses: actions/cache@v6.1.0
        with:
          path: ~/go/pkg/mod
          key: gomod-${{ hashFiles('**/go.sum') }}
          restore-keys: gomod-
```

Gitea and Forgejo serve the same cache actions through their runner's cache server.

## Platform detection

`run` detects the platform in this order:

1. **Environment** (no network):
   1. `FORGEJO_ACTIONS=true` → forgejo, from `FORGEJO_SERVER_URL`, `FORGEJO_API_URL` and `FORGEJO_REPOSITORY`, each falling back to the `GITHUB_*` name.
   2. `GITEA_ACTIONS=true` → gitea, from `GITHUB_SERVER_URL`, `GITHUB_API_URL` and `GITHUB_REPOSITORY`.
   3. `GITHUB_ACTIONS=true` → github (including GHES), from the same variables.
   4. `GITLAB_CI=true` → gitlab, from `CI_SERVER_URL`, `CI_API_V4_URL` and `CI_PROJECT_PATH`.
2. **The `origin` remote**, in the forms `https://host/owner/repo(.git)`, `git@host:owner/repo(.git)` and `ssh://git@host[:port]/owner/repo(.git)`; on GitLab also with nested groups (`host/group/subgroup/repo`):
   - `github.com` → github, API `https://api.github.com`; `gitlab.com` → gitlab, API `https://gitlab.com/api/v4`.
   - Any other host is probed unauthenticated, with a 10s timeout each:
     1. `/api/v4/version` → gitlab (`https://host/api/v4`), also when it answers 401 with a JSON `message`, as GitLab does without a token;
     2. `/api/forgejo/v1/version` → forgejo;
     3. `/api/v1/version` → gitea (both use API `https://host/api/v1`);
     4. `/api/v3/meta` → GHES (`https://host/api/v3`).

     If none matches, deps fails with `cannot detect platform for <host>; pass --platform and --api-url`.
3. **Overrides:** `--platform`, `--api-url` and `--repo` replace the detected values. If only the kind is given, the API URL is derived per kind.

`plan`, `apply` and `explain` only use step 1. With `--log-level info`, `run` logs the result as `platform detected kind=<kind> via=<source> api=<url> repo=<owner/name>`. A self-managed GitLab served over plain http is not probed (probes use https): pass `--platform gitlab --api-url http://host/api/v4`.

## Troubleshooting

Run with `show-skipped: true` and `log-level: debug` to see every HTTP request, rate-limit header, decision and git command. To run deps locally against the same repository without writing anything, use `deps run --dry-run --show-skipped`.

**Tokens and pushes:**

| Symptom | Cause and fix |
|---|---|
| push fails with `refusing to allow a GitHub App to create or update workflow … without workflows permission` (or `OAuth App … without workflow scope`), or `POST …/git/trees: 403 Forbidden: Resource not accessible by integration` | The token can't change `.github/workflows/*`: `GITHUB_TOKEN` never can. Use an App with *Workflows: write* or a PAT with `workflow` scope, for `actions/checkout` too. The second form is a [platform commit](#platform-commits-github). |
| `update refs/heads/<branch>: stale info: the branch is at <sha>, expected <sha>` | Someone pushed to the branch after the run listed it (platform commits; git reports `stale info` the same way). The next run checks the branch again. |
| deps PRs never run CI, or (GitHub) their runs wait for approval; automerge never merges | The PRs were pushed with the job token. Pass an App token or PAT to `actions/checkout` and the action ([GitHub](#github), [Gitea](#gitea)). |
| `deps: no token for <host>` | No `--token`, no `DEPS_TOKEN`/`GITHUB_TOKEN`/`GITEA_TOKEN`/`FORGEJO_TOKEN`, and no git credential. In a workflow, check that the `token` input isn't an empty secret. |
| `403` on `POST …/pulls` or on push (Gitea/Forgejo) | The job token lacks permissions: add `contents: write` and `pull-requests: write`, check the maximum in *Settings → Actions → General*, or use a PAT. |
| `ls-refs https://host/owner/repo: HTTP 401` or `HTTP 404` | A private action repository. Grant cross-repository access (Gitea) or use a token that can read it; for github.com from Gitea/Forgejo set `DEPS_GITHUB_COM_TOKEN`. |
| `rate limited by api.github.com until <time>` | Anonymous github.com lookups from a non-GitHub runner. Set `DEPS_GITHUB_COM_TOKEN`. |

**Checkout and platform:**

| Symptom | Cause and fix |
|---|---|
| `checkout HEAD <sha> is not the tip of origin/<base> (<sha>); check out the latest <base>` | The job checked out a PR ref, a tag or an old commit, or the base moved since the checkout. Check out the default branch (no `ref:`), or pass `--base`. A `--dry-run` only warns, unless `<base>` does not exist on origin. |
| `cannot detect platform for <host>; pass --platform and --api-url` | Running outside Actions against a host whose API probes failed. Pass `--platform gitea --api-url https://host/api/v1` (and `--repo` if the remote is unusual). |

**PR statuses:**

| Symptom | Cause and fix |
|---|---|
| `skipped: branch modified by someone else` | The branch's last commit has another author email. Rebase it via the dashboard checkbox, or close the PR. If the bot identity changed, add the old email to `gitIgnoredAuthors`. |
| `skipped: closed PR for this version` | A PR for the same version (same title) was closed without merging, so deps won't reopen it. Tick *recreate* on the dashboard, or wait for the next version. |
| `skipped: closed PR for this major` | A major update's PR was closed without merging: like Renovate, deps ignores that major (any later version of it) until the next major. Tick *recreate* on the dashboard to get the PR back, or add an `ignore` rule to drop the update for good. |
| `skipped: outside schedule` on every run | The `schedule` entries don't match the workflow's crons: they are compared as text. Check the `trigger` line in the `--log-level info` output for the cron deps saw. |
| `skipped: new PR limit reached` | `--max-new-prs` (default 10), `prConcurrentLimit` or `prHourlyLimit`. The next runs continue; the dashboard lists the waiting ones. |
| obsolete PRs are never closed | deps closes obsolete PRs only after a run without lookup errors (a failed build or push of one PR does not block it). Fix the dependency errors listed at the end of the run. |

**Action download and post-processing:**

| Symptom | Cause and fix |
|---|---|
| `deps: unsupported runner <os>/<arch>` | Use a linux/amd64 or linux/arm64 runner. |
| `deps: no release found at <api>` | The `repository` input points at a repository without releases, or the API is unreachable. Set `version:` and `repository:` explicitly. |
| `go mod tidy in <dir>: …` | Go is missing on the runner, or a private module can't be fetched (set `GOPRIVATE` and git credentials). The module's change is dropped; other updates proceed. |
| `<tool> not found (needed to update <lock>)` | Install Node.js (`actions/setup-node`) so `npm`/`npx` is available. For Python locks install uv (e.g. `astral-sh/setup-uv`); for `.terraform.lock.hcl` install Terraform (`hashicorp/setup-terraform`) or OpenTofu. |
| `cargo not found (needed to update Cargo.lock)` | Install Rust (`dtolnay/rust-toolchain`) so `cargo` is on `PATH`, or the root's Cargo.toml changes are dropped. |
| `cargo update in <dir>: …` | cargo could not resolve the patched workspace: a private registry or git dependency without credentials, a conflicting requirement (for example a feature the new version dropped), or a path dependency outside the repository. The root's change is dropped; other updates proceed. |
| `composer not found (needed to update composer.lock)` | Install PHP with Composer 2.7 or later (`shivammathur/setup-php` with `tools: composer`) so `composer` is on `PATH`, or the composer.json changes are dropped. |
| `composer update in <dir>: …` | Composer could not resolve the update: a private repository without `COMPOSER_AUTH`, a conflicting constraint of another package, or `The "--minimal-changes" option does not exist` from a Composer older than 2.7. The directory's change is dropped; other updates proceed. |
| `bundle not found (needed to update Gemfile.lock)` | Install Ruby with Bundler (`ruby/setup-ruby`) so `bundle` is on `PATH`, or the Gemfile changes are dropped. |
| `bundle lock in <dir>: …` | Bundler could not resolve the patched Gemfile: a private gem server without credentials (set `BUNDLE_<HOST>`), a git or path gem it cannot reach, or a requirement the new version conflicts with. The Gemfile's changes are dropped. |
| `julia not found (needed to update Manifest.toml)` | Install Julia (`julia-actions/setup-julia`) so `julia` is on `PATH`, or the changes of projects with a manifest are dropped. |
| `julia Pkg in <dir>: …` | Pkg could not resolve the patched project: a package of a private registry the depot does not know (add it with `Pkg.Registry.add` in an earlier step), a `[sources]` path outside the repository, or compat entries the new version conflicts with. The scope's changes are dropped; other updates proceed. |
| `mix not found (needed to update mix.lock)` | Install Elixir with Hex (`erlef/setup-beam`) so `mix` is on `PATH`, or the changes of mix.exs files with a mix.lock are dropped. |
| `mix deps.update in <dir>: …` | Mix could not resolve or fetch the patched project: Hex missing (`mix local.hex --force`), a private organization package without `HEX_API_KEY`, a git dependency it cannot clone, or requirements the new version conflicts with. The lockfile directory's changes are dropped; other updates proceed. |
| `gleam not found (needed to update manifest.toml)` | Install Gleam (`erlef/setup-beam` with `gleam-version`) so `gleam` is on `PATH`, or the changes of gleam.toml files with a manifest.toml are dropped. |
| `gleam deps in <dir>: …` | Gleam could not resolve the patched gleam.toml: requirements the new version conflicts with, or a path or git dependency it cannot read. The project's changes are dropped. |
| `bazel not found (needed to update MODULE.bazel.lock)` | Install Bazelisk (`bazel-contrib/setup-bazel`) so `bazel` is on `PATH`, or the `MODULE.bazel` and `.bazelversion` changes of workspaces with a `MODULE.bazel.lock` are dropped. |
| `bazel mod deps --lockfile_mode=update in <dir>: …` | Bazel could not resolve the patched module graph: a registry it cannot reach, a `local_path_override` outside the repository, or a version another module's `max_compatibility_level` excludes. The workspace's changes are dropped. |
| `nix not found (needed to update flake.lock)` | Install Nix (`cachix/install-nix-action`) so `nix` is on `PATH`, or the changes of flakes with a `flake.lock` are dropped. |
| `nix flake update in <dir>: …` | nix could not lock the flake: an input it cannot fetch (a private repository without `access-tokens` in `NIX_CONFIG` or `nix.conf`, GitHub's anonymous API rate limit without a github.com token), or a nix older than 2.19, which does not take input names (`unexpected argument`). The flake's changes are dropped; other updates proceed. |
| `GET <index>/…: HTTP 401` for a crate | The Cargo registry needs credentials: set `CARGO_REGISTRIES_<NAME>_TOKEN` (the `[registries]` name of the index), add the token to `$CARGO_HOME/credentials.toml`, or provide git credentials for the index host. |
| `nuget <feed>/…: HTTP 401` | A private NuGet feed of a `nuget.config` needs credentials: set `NuGetPackageSourceCredentials_<key>` (the source's `key` in nuget.config) to `Username=<user>;Password=<token>`, e.g. from a secret, or put `Username` and `ClearTextPassword` (`%VAR%` references are expanded) under `<packageSourceCredentials>` in nuget.config. An encrypted `Password` only works on Windows (DPAPI); deps skips it with a warning. |
| `terraform init in <dir>: …` | Terraform could not install the providers or modules of that directory (a private registry without `TF_TOKEN_<host>`, an unreachable module source, a `required_version` the runner's CLI does not satisfy). The directory's change is dropped; other updates proceed. |
| `java not found (needed to …)` | A Gradle lockfile, verification metadata or a Gradle/Maven wrapper needs Java to regenerate. Add `actions/setup-java` with the JDK the build requires (Gradle toolchains included). |
| `gradle in <dir>: …` / `mvn in <dir>: …` | The build could not run on the runner (missing toolchain JDK, private repository credentials). That scope's changes are dropped; other updates proceed. |
| the job fails before deps runs, cloning `https://github.com/actions/checkout` | The runner can't reach the actions host. That is runner networking, not deps: fix the network, or use full `uses:` URLs to a reachable host. |

## GitHub end-to-end test

`task e2e:github` builds `bin/deps` and runs `hack/e2e-github/e2e.sh`. The script drives `deps run` against the real repository `<gh user>/deps-e2e` (override with `DEPS_E2E_REPO=owner/name`).

- **Requirements:** network, a `gh` login for github.com with the `repo` and `workflow` scopes (or `GH_TOKEN` set to such a token), and `git`, `go` and `jq`. That is why it is not part of `task ci`.
- **Duration:** about 3 minutes, mostly waiting for the `check` workflow.
- **In CI.** `.github/workflows/e2e.yml` runs it on the GitHub mirror nightly, on `workflow_dispatch`, and when a push to `main` changes the bot, platform or git code or the test itself. It needs the repository secret `DEPS_E2E_TOKEN` (a PAT of the account owning `deps-e2e`, with the scopes above); without it the job only prints a notice. Runs share one concurrency group, because each resets the same test repository. On failure the debug logs of every `deps run` are uploaded as the artifact `e2e-logs`.

**Setup (idempotent, every run).** The repository is created if missing, as public so branch protection works on the free plan. Settings:

- issues on, `allow_auto_merge: true`, `delete_branch_on_merge: true`;
- branch protection on `main` requires the status check `check` (from `.github/workflows/check.yml`, `on: pull_request`);
- `enforce_admins: false` and force pushes allowed, so the test can reset and push to `main`.

**Reset (before and after the scenario).** `main` is force-pushed to a fixed commit of `hack/e2e-github/testdata/repo`, the same SHA every time. That commit contains:

- a never-running workflow with `actions/setup-node@v7.0.0` and `actions/setup-python@v7.0.0`;
- a `go.mod` requiring `github.com/spf13/cobra v1.10.1`;
- a `.deps.yaml` with `dependencyDashboard: true`, `pin: digest` for setup-python, `automerge: true` for patch updates, and `allow` bounds so new upstream releases don't change the update types.

The reset also cleans up:

- open PRs are closed;
- closed unmerged PRs are retitled `[e2e reset] …` and their bodies replaced, so the next run doesn't skip them as "closed PR for this version" or "for this major";
- branches other than `main` are deleted, and open issues are closed.

`hack/e2e-github/e2e.sh reset` runs only the reset.

**Scenario.** Each step runs `deps run --show-skipped` with `DEPS_TOKEN=$(gh auth token)` on a fresh clone of `main` and checks the result through the GitHub API. The script stops at the first mismatch.

1. **First run.** Three PRs are created, on `deps/github-actions/actions-setup-node`, `deps/github-actions/actions-setup-python` and `deps/gomod/github.com-spf13-cobra`. The script checks:
   - the titles;
   - the body: the update table with ages, `### Release notes`, the fingerprint;
   - the digest pin with its `# v7.x.y` comment, and the tidied `go.sum`;
   - that the dashboard issue exists with a rebase checkbox per PR;
   - that GitHub auto-merge is enabled on the cobra patch PR. GitHub must then merge that PR within 5 minutes (`DEPS_E2E_MERGE_TIMEOUT`).
2. **Second run.** The run after the merge reports the other PRs `up-to-date`. One more run must leave the dashboard unedited (same `updated_at`), and the dashboard must be the only open issue.
3. **Rebase checkbox.** Ticking the setup-node rebase box through the API makes the next run report `rebased`. The branch moves to a new commit on top of `main`, the PR number stays the same, and the box is unticked again.
4. **Obsolete PR.** A push to `main` adds a rule ignoring `actions/setup-python`. The next run reports `closed`: the PR is closed and its branch deleted.
5. **Direct merge.** With `allow_auto_merge` disabled, `main` is pushed back to cobra v1.10.1.
   - The run creates the PR with `automerge waiting for checks`.
   - Once the `check` run is green, later runs report `up-to-date, merged`.
   - The PR must be merged by the token's user (deps), and the dashboard must no longer list it.

**Output.** The summary prints the duration of each step and of each `deps run`. Set `DEPS_E2E_LOGS=<dir>` to keep each run's debug log (`--log-level debug`).

**Reading the dashboard.** GitHub's issue list lags a few seconds behind a new issue, so the script reads the dashboard by the number from the run's `dashboard` line. deps itself only lists issues at the start of a run, and scheduled runs are far enough apart for that lag not to matter.

## Releases (maintainers)

Every release is published on both forges:

- **Gitea:** the `release` job below.
- **GitHub mirror:** `.github/workflows/release.yml`. It runs when the push mirror delivers the tag, or via `workflow_dispatch` with a `tag` input to republish an existing tag. It also attests every release file (see [Attestations](#attestations)).

GitHub runners download from the mirror because the Gitea instance's edge blocks GitHub's datacenter IPs (HTTP 403). The Gitea push mirror needs a GitHub token with the `workflow` scope, otherwise GitHub rejects pushes that change `.github/workflows/*`.

**The mirror's deps workflow.** `.github/workflows/deps.yml` on the mirror runs `deps plan` daily and on pushes to `main`. PRs are only made on Gitea, for two reasons:

- pull requests and issues are disabled on the mirror;
- the push mirror prunes foreign branches.

Pushing a `v*` tag runs `.gitea/workflows/ci.yml`. Its `check` job (`task check`) and `test` job (`task cover`) run in parallel, each with Go from `actions/setup-go@v7.0.0` (`go-version-file: go.mod`, i.e. `toolchain go1.27.3`, and `cache: true`); `check` also restores the pinned golangci-lint binary from `actions/cache`. Then the `release` job runs (`needs: [check, test]`, `contents: write`):

1. `task release:build VERSION=<tag>` builds `dist/deps_<os>_<arch>` for `linux`, `darwin` and `windows` (`.exe`) on `amd64` and `arm64` with `CGO_ENABLED=0`, `-trimpath` and `-ldflags "-s -w -buildid= -X main.version=<tag>"`. It writes `dist/checksums.txt` (`sha256sum`).
2. Each Linux binary is checked to be static: `readelf -l` must not show `Requesting program interpreter`, because the action runs it on arbitrary runner images.
3. `task release:notes` runs [git-cliff](https://git-cliff.org) with `cliff.toml` and writes the notes of the tag, `git-cliff --latest --strip header`, to `dist/RELEASE_NOTES.md`.
   - git-cliff is pinned to v2.14.2, installed and sha512-verified by `task changelog:install`.
   - The checkout uses `fetch-depth: 0`, so git-cliff sees all commits and tags.
4. `task release:packages VERSION=<tag>` builds the Debian, RPM, Arch Linux and Alpine packages from those binaries (`release:deb`, `release:rpm`, `release:arch` and `release:apk`), then rewrites `dist/checksums.txt` to cover the binaries and the packages (`task release:checksums`):
   - `dist/deps_<version>_amd64.deb` and `dist/deps_<version>_arm64.deb`, by `packaging/nfpm/build.sh deb`;
   - `dist/deps-<version>-1.x86_64.rpm` and `dist/deps-<version>-1.aarch64.rpm`, by `packaging/nfpm/build.sh rpm`;
   - `dist/deps-<pkgver>-1-x86_64.pkg.tar.zst` and `dist/deps-<pkgver>-1-aarch64.pkg.tar.zst`, by `packaging/nfpm/build.sh archlinux`;
   - `dist/deps-<pkgver>-r0-x86_64.apk` and `dist/deps-<pkgver>-r0-aarch64.apk`, by `packaging/nfpm/build.sh apk`.
5. `packaging/publish.sh dist/*.deb dist/*.rpm dist/*.pkg.tar.zst dist/*.apk` uploads the packages to the owner's Debian, RPM, Arch Linux and Alpine package registries (see below).
6. `gt release create <tag> --verify-tag --notes-file dist/RELEASE_NOTES.md` publishes the release with the notes, the binaries, the packages and `checksums.txt`. `gt` is the Gitea CLI on the runner, authenticated with `GITEA_TOKEN`, the job token. The GitHub release gets the same assets.
7. `task release:attest-from-github TAG=<tag> DIR=dist` waits for the GitHub release's attestations, verifies them against `dist/` and attaches them (see [Attestations](#attestations)). The release is already public at this point, so a failure here only leaves it without attestations until the backfill runs.

git-cliff is installed from its GitHub release instead of through `orhun/git-cliff-action`. That action always sends `github.token` to api.github.com to find the release, and on Gitea that token is a Gitea token, so the request fails with 401.

**Packages.** Each package holds `/usr/bin/deps` and the bash, zsh and fish completions, generated by `deps completion` from the binary of the build machine's architecture (`packaging/common.sh`). None depends on anything for the binary, which is static; `deps run` needs `git`.

| | Debian | RPM | Arch Linux | Alpine |
|---|---|---|---|---|
| Built by | [nfpm](https://nfpm.goreleaser.com) v2.48.0 (`packaging/nfpm/nfpm.yaml`) | nfpm | nfpm | nfpm |
| Architectures | `amd64`, `arm64` | `x86_64`, `aarch64` | `x86_64`, `aarch64` | `x86_64`, `aarch64` |
| Version of `v1.2.3` | `1.2.3` | `1.2.3-1` | `1.2.3-1` | `1.2.3-r0` |
| Version of `v1.2.3-rc.1` | `1.2.3~rc.1` | `1.2.3~rc.1-1` | `1.2.3rc.1-1` | `1.2.3_rc1-r0` |
| Completions | `/usr/share/bash-completion/completions/deps`, `/usr/share/zsh/vendor-completions/_deps`, `/usr/share/fish/vendor_completions.d/deps.fish` | as Arch Linux | bash and fish as Debian, `/usr/share/zsh/site-functions/_deps` | as Arch Linux |
| git | `Recommends: git` | `Recommends: git` | optional dependency | `depend = git` |
| Registry | distribution `stable`, component `main` | the owner's RPM repository (no group) | repository `jakobmoellerdev` | branch `stable`, repository `main` |

- **Version.** The tag without the leading `v`, with the pre-release mapped so that every package manager sorts it before the release (checked with `vercmp`, `apk version -t` and `rpm.vercmp`):
  - Debian and RPM: the first `-` becomes `~`. RPM's release is always `1`.
  - Arch Linux: the `-` is dropped, because pacman reserves it for the package release (`pkgrel`, always `1`). The pre-release must start with a letter: vercmp sorts `1.2.3rc.1` before `1.2.3`, but `1.2.3.1` after it.
  - Alpine: only `alpha`, `beta`, `pre` and `rc`, optionally numbered, become the suffixes `_alpha`, `_beta`, `_pre` and `_rc`. Any other pre-release fails the build, since Alpine has no other suffixes that sort before the release.
- **Reproducible.** Every mtime is `SOURCE_DATE_EPOCH`, by default the commit time of the tag (of `HEAD` if the tag does not exist). nfpm sets owners to root:root and modes are fixed, so rebuilding a tag gives byte-identical packages.
- **nfpm** is pinned to v2.48.0, installed and sha256-verified by `task nfpm:install` (a matching `nfpm` on `PATH` is used as is).
- **Signing.** The packages are unsigned. Gitea signs the Arch Linux packages and database on upload (with the owner's key from `/api/packages/<owner>/arch/repository.key`), the Alpine `APKINDEX.tar.gz` (key at `/api/packages/<owner>/alpine/key`) and the RPM `repomd.xml` (key at `/api/packages/<owner>/rpm/repository.key`), but not the RPMs themselves. apk trusts an unsigned package through its checksum in the signed index, and so does dnf with `gpgcheck=0` and `repo_gpgcheck=1`: the `.repo` file Gitea serves sets `gpgcheck=1`, which rejects the unsigned packages, so the [install guide](install.md#packages) writes its own. apk finds its key by its file name, `<owner>@<sha256 of the key>.rsa.pub`, so the install guide names it explicitly.
- **Registries.** The Alpine branch is a name only: one `stable` branch serves every Alpine release, because the binary is static (installs verified on Alpine 3.22 with apk-tools 2 and 3.24 with apk-tools 3). The Arch repository is named after the owner, because its name is also the `pacman.conf` section, which must not clash with `core` or `extra`.
- **Token.** The uploads are `PUT <server>/api/packages/<owner>/debian/pool/stable/main/upload`, `PUT …/rpm/upload`, `PUT …/arch/jakobmoellerdev` and `PUT …/alpine/stable/main`, with HTTP Basic auth. The job token cannot write packages: Gitea gives the Actions user read access to a user's registry only. The job therefore uses the repository secret `PACKAGES_TOKEN`, a token of `jakobmoellerdev` named `deps-release-packages` with only the `write:package` scope.
- **Re-runs.** If the registry already has a file (HTTP 409), `publish.sh` downloads it. An identical file is skipped with a notice; a different one is deleted and uploaded again with a warning.
- **Trying it without a release:**

  ```sh
  task release:build VERSION=v0.0.0-rc.1 && task release:packages VERSION=v0.0.0-rc.1
  PACKAGES_URL=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev PACKAGES_USER=jakobmoellerdev \
    PACKAGES_TOKEN=$(gt auth token) packaging/publish.sh dist/*.deb dist/*.rpm dist/*.pkg.tar.zst dist/*.apk
  # install it in containers with the snippets from docs/install.md, then delete the test versions:
  p=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev
  for arch in amd64 arm64; do gt api -X DELETE "$p/debian/pool/stable/main/deps/0.0.0~rc.1/$arch"; done
  for arch in x86_64 aarch64; do
    gt api -X DELETE "$p/rpm/package/deps/0.0.0~rc.1-1/$arch"
    gt api -X DELETE "$p/arch/jakobmoellerdev/deps/0.0.0rc.1-1/$arch"
    gt api -X DELETE "$p/alpine/stable/main/$arch/deps-0.0.0_rc1-r0.apk"
  done
  ```

**Release notes and CHANGELOG.md.** `cliff.toml` groups conventional commits:

- `feat`, `fix`, `perf` and `docs`;
- `ci`/`build`;
- `chore`/`refactor`/`style`/`test`;
- `chore(deps)`/`fix(deps)` as Dependencies.

Other commit subjects are kept under "Changes", and merge commits are skipped. Use conventional commit subjects to get grouped notes. To update the changelog in the repository before a release:

```sh
task changelog -- --tag v0.2.0     # names the unreleased section v0.2.0
git add CHANGELOG.md && git commit -m "docs: changelog for v0.2.0"
git tag -a v0.2.0 -m "deps v0.2.0" && git push origin main v0.2.0
```

Workflows that use the action at `@main`, or without a release ref, pick up a new release automatically. Workflows pinned to `@vX.Y.Z` get it when deps (or Renovate) bumps their `uses:` ref.

### Attestations

Users verify downloads with `gh attestation verify` or cosign ([Verify a download](install.md#verify-a-download)). The chain:

1. **GitHub signs.** After `task release:packages`, `.github/workflows/release.yml` runs `task release:sbom`: [syft](https://github.com/anchore/syft) v1.56.0 (installed and sha256-verified by `task syft:install`) writes an SPDX 2.3 SBOM of each binary to `dist/attest/deps_linux_<arch>.spdx.json`. Then [`actions/attest`](https://github.com/actions/attest) v4.2.2 (pinned by commit) creates, keyless with the job's OIDC token, Fulcio and the public Rekor log:
   - one SLSA build provenance attestation (`https://slsa.dev/provenance/v1`) whose subjects are both binaries, all six packages and `checksums.txt`;
   - one SBOM attestation (`https://spdx.dev/Document/v2.3`) per binary.

   The job needs `id-token: write` and `attestations: write` besides `contents: write`; `artifact-metadata: write` is only needed for registry storage records, which deps does not create. The attestations land in the repository's attestations API, and the Sigstore bundles become release assets: `<file>.sigstore.json` for every file (copies of the one provenance bundle; a bundle with several subjects verifies each of them), `deps_linux_<arch>.sbom.sigstore.json` and `deps_linux_<arch>.spdx.json`. The packages are not SBOM-attested on their own: they contain the same binary, whose SBOM describes them.
2. **Gitea reuses them.** Gitea can't sign itself:
   - Gitea 28 Actions has no OIDC tokens ([go-gitea/gitea#39052](https://github.com/go-gitea/gitea/issues/39052), planned for 29.0);
   - the public Fulcio trusts only known OIDC issuers, and no Gitea instance is one of them;
   - Gitea has no attestations API, so `gh attestation verify` without `--bundle` works only against GitHub.

   Both forges build the same commit reproducibly, so the files are byte-identical and GitHub's attestations hold for the Gitea files as well. `packaging/attest-from-github.sh <tag> [dir]` (via `task release:attest-from-github`, which installs cosign v3.1.4 and verifies its sha256):
   1. checks the Gitea files (`dir`, or downloaded from the Gitea release with `gt release download`) against their `checksums.txt`;
   2. polls `https://github.com/jakobmoellerdev/deps/releases/download/<tag>/<asset>` anonymously every 30 s (`ATTEST_INTERVAL`) for up to 30 min (`ATTEST_TIMEOUT`) until every bundle and SBOM exists. The tag first has to reach GitHub through the push mirror, and the GitHub build takes a few minutes;
   3. verifies every bundle against the Gitea file with `cosign verify-blob-attestation` (`--type slsaprovenance1` for provenance, `--type https://spdx.dev/Document/v2.3` for SBOMs), checks that each SBOM asset equals the attested predicate, and fails before uploading anything if one doesn't match. A pass also proves that both builders produced the same bytes;
   4. uploads the bundles and SBOMs to the Gitea release with `gt release upload --clobber`, so re-runs are idempotent.

**Identity.** The certificates are issued by `https://token.actions.githubusercontent.com` to the workflow `https://github.com/jakobmoellerdev/deps/.github/workflows/release.yml@<ref>`, where `<ref>` is `refs/tags/<tag>` for a tag push. A `workflow_dispatch` republish signs as the ref it was dispatched on, normally `refs/heads/main`, even though it builds the tag; the script accepts exactly these two identities, and its provenance names `main` as the workflow ref. Dispatch with `--ref <tag>` to sign as the tag, but only tags whose `release.yml` already attests (after v0.4.0) have the attest steps.

**Republishing old tags.** The build uses the tag's Taskfile, while `release:sbom` comes from the Taskfile of the workflow's own revision (a sparse checkout into `.release-tooling`), so tags older than the SBOM task can be republished and attested.

**Backfill.** `.gitea/workflows/attest.yml` (`workflow_dispatch`, input `tag`) runs the script against the files of an existing Gitea release: for releases made before attestations existed, after a GitHub republish, or when the release job's attest step failed. To attest an older release, republish it on GitHub first, then backfill:

```sh
gh workflow run release.yml -R jakobmoellerdev/deps -f tag=v0.5.0   # wait for it: gh run watch -R jakobmoellerdev/deps
gt workflow run attest.yml --ref main -f tag=v0.5.0 --follow
```
