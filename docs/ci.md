# Running deps in CI

deps behaves the same locally and in CI. In CI it detects the forge from the job environment, reads the cron that triggered the run, and uses the job token unless you pass another. This guide covers GitHub Actions, Gitea Actions and Forgejo Actions. Every input, flag and config key is listed in the generated references: [action inputs](reference/action.md), [CLI flags and environment](reference/cli.md), [configuration keys](reference/configuration.md).

## Getting started

1. **Add the workflow.** Copy the example for your forge: [GitHub](#github), [Gitea](#gitea) or [Forgejo](#forgejo). Each one is a job with `actions/checkout` and the deps action. The first run opens one PR per outdated dependency or group.
2. **Choose the token.** On Gitea and Forgejo the job token works, but CI doesn't run on PRs it opens. On GitHub use an App token or a PAT, because `GITHUB_TOKEN` can't push changes to workflow files. Details: [Tokens and permissions](#tokens-and-permissions).
3. **Set the schedule.** The workflow's `on.schedule` cron decides when deps runs. The examples also run [on push to the default branch](#run-on-push-to-the-default-branch), so open PRs are rebased right after a merge. To open some PRs only on certain crons, see [Schedules](#schedules).
4. **Optional: add a `.deps.yaml`.** Without one, every direct dependency is updated. Rules, groups and limits are in the [configuration reference](reference/configuration.md). To check the config on pull requests, see [Preview on pull requests](#preview-on-pull-requests).

Then, as needed: [automerge](#automerge), the [dependency dashboard](#dependency-dashboard), [signed commits](#signed-commits), [self-hosted runners](#self-hosted-runners-and-actions-hosts), and [troubleshooting](#troubleshooting).

## Contents

- [Getting started](#getting-started)
- [The composite action](#the-composite-action)
- [GitHub](#github)
- [Gitea](#gitea)
- [Forgejo](#forgejo)
- [Tokens and permissions](#tokens-and-permissions)
- [Signed commits](#signed-commits)
- [Self-hosted runners and actions hosts](#self-hosted-runners-and-actions-hosts)
- [Schedules](#schedules)
- [Run on push to the default branch](#run-on-push-to-the-default-branch)
- [Automerge](#automerge)
- [Dependency dashboard](#dependency-dashboard)
- [Overlapping runs](#overlapping-runs)
- [Step summary](#step-summary)
- [Dry runs and config checks](#dry-runs-and-config-checks)
  - [Preview on pull requests](#preview-on-pull-requests)
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
4. **Run.** It runs `deps <args>` with `DEPS_TOKEN` set to the `token` input.

Go is not needed to run it; only go.mod updates need `go` for `go mod tidy`.

The inputs (`token`, `signing-key`, `args`, `version`, `repository`, `use-preinstalled`, `cache`), the outputs (`version`, `cache-hit`), where the binary is downloaded from, and the supported runners (linux/amd64 and linux/arm64 only) are described in the generated [action reference](reference/action.md). This section covers how the steps behave.

**Binary cache.** Once the version is known, the action looks for that binary in two places before downloading:

1. **The runner's tool cache**, `$RUNNER_TOOL_CACHE/deps/<version>/<arch>`. Self-hosted runners that keep it between jobs reuse the binary without any network request (only the latest-release lookup remains, unless `version` or a release-tag ref is set). The log shows `deps: cache hit, using <version> from the tool cache`.
2. **The forge's Actions cache**, key `deps-<version>-linux-<arch>`, restored into that directory with `actions/cache/restore`. GitHub serves it from its cache service, Gitea and Forgejo from the runner's cache server (act_runner `cache:` settings). The log shows `Cache restored from key: deps-<version>-linux-<arch>` and `deps: cache hit, using <version> restored from the Actions cache`.

On a miss the binary is downloaded and verified, then saved under that key (`Cache saved with key: …`). Each cached binary has its checksum line from the release's `checksums.txt` stored next to it (`deps.sha256`), and it runs only if `sha256sum -c` passes; a mismatch logs a warning and downloads again. A new release is a new key, so nothing has to be evicted by hand. If the runner has no cache server, the restore and save steps only log a warning and the action downloads as before.

**Version pinning.** Pin a release tag (`@v0.1.0`) so runs are reproducible; `@main` downloads the latest release.

- deps updates its own `uses:` line like any other action.
- With [`pin: digest`](reference/configuration.md#pin), deps rewrites that line to `@<sha> # v0.1.0`. `github.action_ref` is then a SHA, and the action downloads the *latest* release. Set `version:` if the binary must match the pin exactly.

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
      - uses: jakobmoellerdev/deps@v0.2.5
        with:
          token: ${{ steps.app.outputs.token }}
          args: run -v --log-level info
```

Pass the token to **both** steps:

- **`actions/checkout`** stores it as the git credential. deps pushes branches with the checkout's credentials, so the push needs it.
- **The action's `token` input** is the API token: deps lists, creates, edits and closes PRs with it.

**Why not `GITHUB_TOKEN`:**

1. **Workflow files.** `GITHUB_TOKEN` cannot push commits that change `.github/workflows/*`; GitHub rejects the push with `refusing to allow a GitHub App to create or update workflow … without workflows permission`. Action updates are exactly such commits.
2. **CI on the PRs.** PRs and pushes made with `GITHUB_TOKEN` do not trigger workflows (`pull_request`, `push`), so CI never runs on deps PRs, and [automerge](#automerge) would wait forever.

`permissions: actions: write` does not help. It covers the Actions API (cancel or re-run runs, dispatch workflows, artifacts, caches), not git pushes. The `workflows` permission that the push error asks for exists only for GitHub Apps and PATs; the [`permissions` key](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions) has no `workflows` entry. Verified on 2026-11-04: a job with `contents: write, actions: write, pull-requests: write` was refused when pushing a change to `.github/workflows/check.yml`.

**Only `GITHUB_TOKEN`:** if you can't create an App or PAT, exclude workflow-file updates so the pushes succeed, and accept that the PRs get no CI (no automerge):

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main
rules:
  - manager: github-actions
    ignore: true
```

Workflow permissions: `contents: write`, `pull-requests: write` (and `issues: write` for the dashboard).

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

By default commits are authored by `deps[bot] <deps-bot@noreply.invalid>`. To have them appear as the App, set [`gitAuthor`](reference/configuration.md#gitauthor) in `.deps.yaml`, or set `DEPS_GIT_AUTHOR` in the job, to the App's noreply identity. Add this step after the token step; job-level environment variables reach the action's steps:

```yaml
      - name: deps commit identity
        env:
          GH_TOKEN: ${{ steps.app.outputs.token }}
          SLUG: ${{ steps.app.outputs.app-slug }}
        run: |
          id=$(gh api "/users/${SLUG}%5Bbot%5D" --jq .id)
          echo "DEPS_GIT_AUTHOR=${SLUG}[bot] <${id}+${SLUG}[bot]@users.noreply.github.com>" >> "$GITHUB_ENV"
```

deps updates only branches whose last commit has its own author email. If the identity changes, list the old email in [`gitIgnoredAuthors`](reference/configuration.md#gitignoredauthors). Otherwise existing branches are reported as `skipped: branch modified by someone else`.

### Without the action

If the action repository isn't reachable, install the binary with `go install` through the Go module proxy, then run it:

```yaml
      - uses: actions/setup-go@v7.0.0
        with:
          go-version: stable
      - run: go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@v0.2.5
      - run: deps run -v --log-level info
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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.2.5
        with:
          args: run -v --log-level info
```

**`uses:` needs the full URL.** A short `jakobmoellerdev/deps@v0.2.5` is resolved against the instance's `DEFAULT_ACTIONS_URL`, which is github.com by default. This repository's own workflow (`.gitea/workflows/deps.yml`) is this example plus the bot token, signing key and identity, pinned to the latest release; deps opens the PR that bumps that pin after each release. It also has a `dry-run` job for pull requests ([Preview on pull requests](#preview-on-pull-requests)).

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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.2.5
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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.2.5
```

- **Detection.** deps detects Forgejo from `FORGEJO_ACTIONS=true` and reads `FORGEJO_SERVER_URL`, `FORGEJO_API_URL` and `FORGEJO_REPOSITORY`, falling back to the `GITHUB_*` names. API calls use the Gitea-compatible `/api/v1`.
- **The automatic token.** It is `FORGEJO_TOKEN`, also available as `GITHUB_TOKEN` and `github.token`, so the action's default `token` uses it. It can write to its repository, except in workflows on fork PRs, and read the repositories associated with the workflow. The Forgejo workflow reference has no `permissions:` key, so the example has no `permissions:` block.
- **CI and automerge.** As on Gitea, use a PAT secret for both steps when CI must run on deps PRs.
- **Default actions host.** Short `owner/repo@ref` actions resolve against `data.forgejo.org`, the default `DEFAULT_ACTIONS_URL` of Forgejo, and deps looks them up there too. `actions/checkout@v7.0.1` exists there. If your instance uses another `DEFAULT_ACTIONS_URL`, see [Self-hosted runners and actions hosts](#self-hosted-runners-and-actions-hosts).
- **Runner image.** The download step needs `bash`, `curl`, `git` and `sha256sum`. Debian-based images such as `node:<version>-bookworm`, a common image for Forgejo's `docker` label, have them; Alpine images need `apk add bash curl git`.

## Tokens and permissions

| | GitHub | Gitea | Forgejo |
|---|---|---|---|
| default token | `GITHUB_TOKEN`: can't change workflow files, and its PRs don't run CI | `GITEA_TOKEN`: its PRs don't run CI | the automatic token: its PRs don't run CI |
| recommended token | a GitHub App token or PAT, given to `actions/checkout` and the action | the job token, or a PAT secret when CI must run on deps PRs | as on Gitea |
| push `<branchPrefix>*` branches, delete obsolete ones | `contents: write`; *Workflows* (App) or `workflow` (PAT) when workflow files change | `contents: write` | automatic token |
| list, create, edit and close PRs | `pull-requests: write` | `pull-requests: write` | automatic token |
| request auto-merge (`automerge`) | merge rights on the base branch; *Allow auto-merge* in the settings; `statuses: read` and `checks: read` for the direct-merge fallback | merge rights on the base branch | merge rights on the base branch |
| find, create and edit the dashboard issue (`dependencyDashboard`) | `issues: write` | `issues: write` | automatic token |
| read private actions, modules and release notes | read access to those repositories | Cross-Repository Access or a PAT | a PAT |
| CI runs on deps PRs | App token or PAT | PAT | PAT |

`deps run --dry-run` and `deps plan` only read. `--dry-run` works anonymously.

**Token sources:**

1. **Platform API.** deps takes `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`.
   - GitHub requests send `Authorization: Bearer <t>`.
   - Gitea and Forgejo requests send `Authorization: token <t>`.
2. **Git push.** deps uses the checkout's git credentials (`actions/checkout` stores its `token:`), not the API token.
3. **Lookups:**
   - The platform token is sent to the platform host as Basic `x-access-token:<t>`.
   - `DEPS_GITHUB_COM_TOKEN` is used for github.com when the platform is elsewhere.
   - On a 401 without a static token, deps runs `git credential fill` once per host and retries. deps never prompts (`GIT_TERMINAL_PROMPT=0`).
4. **npm and Docker.** npm uses the `.npmrc` `_authToken` entries; docker uses the Docker credential chain (`~/.docker/config.json` or credential helpers, e.g. after `docker login`).
5. **Go modules.** `go` itself, through `GOPRIVATE`, `GONOPROXY`, `GOPROXY` and git credentials. Private modules need `GOPRIVATE` set in the job, and git credentials for their host. The checkout's credentials cover only the platform host; for other hosts use, e.g., `git config --global url."https://x-access-token:${TOKEN}@host/".insteadOf "https://host/"`.

## Signed commits

deps builds its commits with `git commit-tree`, which signs only when asked to. Pass a private key and deps signs every commit it pushes. Branch protection that requires signed commits then accepts deps' PRs.

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

**GitHub.** Add the public key as a *signing key* (GPG or SSH) to the account whose verified email is the committer, usually a machine user. A GitHub App has no signing keys, so commits under its `[bot]` identity can't be signed by deps. Branch protection: *Require signed commits*.

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
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main
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
| `git` | always for `run`: remote URL, `ls-remote`, commits via plumbing, push |
| `go` | only for `go mod tidy` after go.mod updates, and `go list` for `GOPRIVATE`/direct modules. `go env` falls back to the environment without it. `GOTOOLCHAIN=auto` fetches a newer toolchain when go.mod requires one. |
| `npm`, `pnpm`, `yarn` | regenerating lockfiles; see the note below the table |
| Docker credentials | private registries |

For lockfiles, deps runs the tool from `PATH`. If it is missing, deps falls back to `npx --yes <tool>[@<packageManager version>]`, so `npm`/`npx` (e.g. from `actions/setup-node`) is enough. With none of them, the lockfile's manifests fail with `<tool> not found (needed to update <lock>)`.

**Checkout.** `deps run` requires the checked-out `HEAD` to be the tip of `origin/<base>`. A fresh `actions/checkout` of the default branch satisfies that; shallow clones are fine. To manage a branch other than the default, check it out and pass `--base <branch>`.

**Network.** The runner needs HTTPS access to:

- the forge;
- the upstream hosts of your dependencies: github.com, proxy.golang.org, registries, registry.npmjs.org, api.osv.dev for security updates;
- the release download host.

deps honors `HTTPS_PROXY`/`NO_PROXY`.

## Schedules

Each [`schedule`](reference/configuration.md#schedule) entry, top-level or in a rule, must be one of the workflow's cron triggers. Crons are compared as text after collapsing whitespace, so write them the same way in both files.

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
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main
schedule: ["0 5 * * *"] # default for every dep
rules:
  - names: ["k8s.io/**"]
    schedule: ["0 5 * * 1"] # Kubernetes only on Mondays
  - updateTypes: [major]
    schedule: ["0 5 * * 1"] # majors only on Mondays
```

Both crons fire on Monday at 05:00, so that day has two runs. The second run finds the PRs of the first up to date; a [concurrency group](#overlapping-runs) keeps the two from overlapping.

- **Runners without the payload.** Pass the cron explicitly with `args: run --trigger-schedule "0 5 * * 1"` (or `DEPS_TRIGGER_SCHEDULE`) in a workflow that has only that cron.
- **A `schedule` event without a readable cron** matches every schedule, and deps logs a warning.

Updates waiting for their cron are reported as `skipped: outside schedule`. They appear on the dashboard under *Awaiting schedule*, where ticking one creates its PR on the next run.

## Run on push to the default branch

The example workflows also run on `push` to the default branch (`main` in the examples; set yours). Without it, deps only reacts on the next cron, up to a day later. After a push, the repository has changed in ways the open PRs depend on:

- **Merging a deps PR.** Other open deps PRs may now conflict with the new base (two updates touching the same `go.mod`/`go.sum` or lockfile), or be behind it. The push run rebases them right away ([`rebaseWhen`](reference/configuration.md#rebasewhen)), so the next PR is mergeable without waiting a day.
- **The dashboard.** The dependency dashboard issue is rewritten to match the new state (the merged PR leaves *Open*, the next pending updates show up).
- **Config changes.** A pushed `.deps.yaml` takes effect immediately: PRs that a new `ignore` rule or `allow` constraint makes obsolete are closed, and changed groups are rebuilt.
- **Manual dependency bumps.** If someone updates a dependency by hand, the matching deps PR is closed as obsolete instead of staying open until the next cron.

**What a push run does.** A `push` run is outside every [schedule](#schedules):

| Group | Push run |
|---|---|
| No `schedule` | acts as on the cron: creates, updates and closes PRs |
| With a `schedule`, PR already open | refreshed and rebased ([`updateNotScheduled`](reference/configuration.md#updatenotscheduled), default true) |
| With a `schedule`, no PR yet | `skipped: outside schedule`; waits for its cron |
| Security update | created regardless of schedules |

So merges never open weekly groups early. `workflow_dispatch` is different: a manual run acts on every group.

**Notes.**

- **No loops.** deps pushes only to its own branches (`deps/…`), never to the default branch, so its pushes don't match `branches: [main]`. Merging a deps PR is a push to `main` and does trigger a run; that is the point.
- **The token decides whether merges trigger.** On GitHub, pushes made with `GITHUB_TOKEN` (including automerges performed with it) start no workflows, so the follow-up run only happens for merges by a user, an App token or a PAT. Gitea and Forgejo behave the same for their job tokens.
- **Busy repositories.** Every push to `main` starts a run. Keep the [concurrency group](#overlapping-runs) with `cancel-in-progress: false`: bursts queue up behind one run instead of racing. To skip pushes that can't matter, add `paths-ignore` (e.g. `docs/**`), keeping `.deps.yaml` and the dependency files included.
- **Cost.** A run without changes takes seconds (lookups are deduplicated per run; the binary comes from the [action's cache](#the-composite-action)).

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

## Dependency dashboard

With [`dependencyDashboard: true`](reference/configuration.md#dependencydashboard), `deps run` maintains one issue. It lists the updates waiting for their schedule, rate-limited and blocked updates, open PRs, errors, and the detected dependencies. Ticking a checkbox makes the next run create, recreate or rebase that PR. The token needs `issues: write`, and the repository must have issues enabled. The title, labels and the exact list are in the [configuration reference](reference/configuration.md#dependencydashboard).

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
- the releases held back by `minimumReleaseAge`;
- the skip reasons with counts and how to update, for example `ignored by default (indirect; …)` → add a rule with `ignore: false`; the skipped dependencies are in a collapsible `<details>`;
- the errors in full.

GitHub shows it on the run page. On Gitea and Forgejo it shows where the instance supports job summaries; elsewhere the file is ignored.

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
      - uses: jakobmoellerdev/deps@v0.2.5 # full URL on Gitea/Forgejo
        with:
          args: plan -v
```

Exit codes:

- **2:** an invalid config; every problem is printed with its location.
- **1:** an upstream lookup failed.

To see why a dependency is or isn't updated, use `args: explain '<name glob>'`.

The GitHub mirror of this repository runs `deps plan` the same way (`.github/workflows/deps.yml`): it exercises the action, binary download and lookups on GitHub, and lists the updates in the job summary.

### Preview on pull requests

Two kinds of PR checks are useful, and both only read:

- **Config changes (most repositories).** The `plan` job above, limited to `.deps.yaml` with `paths:`. It checks the PR's config and lists the updates it leads to in the job summary.
- **The PRs deps would open.** `run --dry-run` lists each PR as `would-create`, `would-update`, `would-rebase` or `would-automerge`, in the log and the job summary. It doesn't work on the PR's own checkout: `run` requires `HEAD` to be the tip of `origin/<base>` and otherwise fails with `checkout HEAD <sha> is not the tip of origin/<base>`. Check out the base branch for it. The job token with read access is enough.

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
      - run: '"$RUNNER_TEMP/deps" plan -v --dir pr'
      - run: '"$RUNNER_TEMP/deps" run --dry-run -v --dir base --base "$BASE"'
        env:
          BASE: ${{ github.event.pull_request.base.ref }}
          DEPS_TOKEN: ${{ github.token }}
```

With the released action instead of a build, use the same two checkouts and `args: run --dry-run -v --dir base`.

## Caching Go modules

`go mod tidy` downloads every module of an updated go.mod. deps also reads release times for [`minimumReleaseAge`](reference/configuration.md#minimumreleaseage) from `GOMODCACHE` (`cache/download/**/@v/*.info`) before asking the proxy. A warm module cache makes both faster. With a go.mod at the repository root, `actions/setup-go` caches it:

```yaml
      - uses: actions/checkout@v7.0.1
      - uses: actions/setup-go@v7.0.0
        with:
          go-version-file: go.mod
          cache: true
      - uses: jakobmoellerdev/deps@v0.2.5
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
2. **The `origin` remote**, in the forms `https://host/owner/repo(.git)`, `git@host:owner/repo(.git)` and `ssh://git@host[:port]/owner/repo(.git)`:
   - `github.com` → github, API `https://api.github.com`.
   - Any other host is probed unauthenticated, with a 10s timeout each:
     1. `/api/forgejo/v1/version` → forgejo;
     2. `/api/v1/version` → gitea (both use API `https://host/api/v1`);
     3. `/api/v3/meta` → GHES (`https://host/api/v3`).

     If none matches, deps fails with `cannot detect platform for <host>; pass --platform and --api-url`.
3. **Overrides:** `--platform`, `--api-url` and `--repo` replace the detected values. If only the kind is given, the API URL is derived per kind.

`plan`, `apply` and `explain` only use step 1. With `--log-level info`, `run` logs the result as `platform detected kind=<kind> via=<source> api=<url> repo=<owner/name>`.

## Troubleshooting

Run with `args: run -v --log-level debug` to see every HTTP request, rate-limit header, decision and git command. To run deps locally against the same repository without writing anything, use `deps run --dry-run -v`.

**Tokens and pushes:**

| Symptom | Cause and fix |
|---|---|
| push fails with `refusing to allow a GitHub App to create or update workflow … without workflows permission` (or `OAuth App … without workflow scope`) | The token can't change `.github/workflows/*`: `GITHUB_TOKEN` never can. Use an App with *Workflows: write* or a PAT with `workflow` scope, for `actions/checkout` too. |
| deps PRs never run CI; automerge never merges | The PRs were pushed with the job token. Pass an App token or PAT to `actions/checkout` and the action ([GitHub](#github), [Gitea](#gitea)). |
| `deps: no token for <host>` | No `--token`, no `DEPS_TOKEN`/`GITHUB_TOKEN`/`GITEA_TOKEN`/`FORGEJO_TOKEN`, and no git credential. In a workflow, check that the `token` input isn't an empty secret. |
| `403` on `POST …/pulls` or on push (Gitea/Forgejo) | The job token lacks permissions: add `contents: write` and `pull-requests: write`, check the maximum in *Settings → Actions → General*, or use a PAT. |
| `ls-refs https://host/owner/repo: HTTP 401` or `HTTP 404` | A private action repository. Grant cross-repository access (Gitea) or use a token that can read it; for github.com from Gitea/Forgejo set `DEPS_GITHUB_COM_TOKEN`. |
| `rate limited by api.github.com until <time>` | Anonymous github.com lookups from a non-GitHub runner. Set `DEPS_GITHUB_COM_TOKEN`. |

**Checkout and platform:**

| Symptom | Cause and fix |
|---|---|
| `checkout HEAD <sha> is not the tip of origin/<base> (<sha>); check out the latest <base>` | The job checked out a PR ref, a tag or an old commit. Check out the default branch (no `ref:`), or pass `--base`. |
| `cannot detect platform for <host>; pass --platform and --api-url` | Running outside Actions against a host whose API probes failed. Pass `--platform gitea --api-url https://host/api/v1` (and `--repo` if the remote is unusual). |

**PR statuses:**

| Symptom | Cause and fix |
|---|---|
| `skipped: branch modified by someone else` | The branch's last commit has another author email. Rebase it via the dashboard checkbox, or close the PR. If the bot identity changed, add the old email to `gitIgnoredAuthors`. |
| `skipped: closed PR for this version` | A PR with the same title was closed without merging, so deps won't reopen it. Tick *recreate* on the dashboard, or wait for the next version. |
| `skipped: outside schedule` on every run | The `schedule` entries don't match the workflow's crons: they are compared as text. Check the `trigger` line in the `--log-level info` output for the cron deps saw. |
| `skipped: new PR limit reached` | `--max-new-prs` (default 10), `prConcurrentLimit` or `prHourlyLimit`. The next runs continue; the dashboard lists the waiting ones. |
| obsolete PRs are never closed | deps closes obsolete PRs only after a run without errors. Fix the errors listed at the end of the run. |

**Action download and post-processing:**

| Symptom | Cause and fix |
|---|---|
| `deps: unsupported runner <os>/<arch>` | Use a linux/amd64 or linux/arm64 runner. |
| `deps: no release found at <api>` | The `repository` input points at a repository without releases, or the API is unreachable. Set `version:` and `repository:` explicitly. |
| `go mod tidy in <dir>: …` | Go is missing on the runner, or a private module can't be fetched (set `GOPRIVATE` and git credentials). The module's change is dropped; other updates proceed. |
| `<tool> not found (needed to update <lock>)` | Install Node.js (`actions/setup-node`) so `npm`/`npx` is available. |
| the job fails before deps runs, cloning `https://github.com/actions/checkout` | The runner can't reach the actions host. That is runner networking, not deps: fix the network, or use full `uses:` URLs to a reachable host. |

## GitHub end-to-end test

`task e2e:github` builds `bin/deps` and runs `hack/e2e-github/e2e.sh`. The script drives `deps run` against the real repository `<gh user>/deps-e2e` (override with `DEPS_E2E_REPO=owner/name`).

- **Requirements:** network, a `gh` login for github.com with the `repo` and `workflow` scopes, and `git`, `go` and `jq`. That is why it is not part of `task ci`.
- **Duration:** about 3 minutes, mostly waiting for the `check` workflow.

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
- closed unmerged PRs are retitled `[e2e reset] …`, so the next run doesn't skip them as "closed PR for this version";
- branches other than `main` are deleted, and open issues are closed.

`hack/e2e-github/e2e.sh reset` runs only the reset.

**Scenario.** Each step runs `deps run -v` with `DEPS_TOKEN=$(gh auth token)` on a fresh clone of `main` and checks the result through the GitHub API. The script stops at the first mismatch.

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

Pushing a `v*` tag runs `.gitea/workflows/ci.yml`. The `test` job runs `task ci` once, with Go from `actions/setup-go@v7.0.0` (`go-version-file: go.mod`, i.e. `toolchain go1.27.3`, and `cache: true`). Then the `release` job runs (`needs: test`, `contents: write`):

1. `task release:build VERSION=<tag>` builds `dist/deps_linux_amd64` and `dist/deps_linux_arm64` with `CGO_ENABLED=0`, `-trimpath` and `-ldflags "-s -w -buildid= -X main.version=<tag>"`. It writes `dist/checksums.txt` (`sha256sum`).
2. Each binary is checked to be static: `readelf -l` must not show `Requesting program interpreter`, because the action runs it on arbitrary runner images.
3. `task release:notes` runs [git-cliff](https://git-cliff.org) with `cliff.toml` and writes the notes of the tag, `git-cliff --latest --strip header`, to `dist/RELEASE_NOTES.md`.
   - git-cliff is pinned to v2.14.2, installed and sha512-verified by `task changelog:install`.
   - The checkout uses `fetch-depth: 0`, so git-cliff sees all commits and tags.
4. `task release:packages VERSION=<tag>` builds the Debian, Arch Linux and Alpine packages from those binaries (`release:deb`, `release:arch` and `release:apk`), then rewrites `dist/checksums.txt` to cover the binaries and the packages (`task release:checksums`):
   - `dist/deps_<version>_amd64.deb` and `dist/deps_<version>_arm64.deb`, by `packaging/deb/build.sh`;
   - `dist/deps-<pkgver>-1-x86_64.pkg.tar.zst` and `dist/deps-<pkgver>-1-aarch64.pkg.tar.zst`, by `packaging/nfpm/build.sh archlinux`;
   - `dist/deps-<pkgver>-r0-x86_64.apk` and `dist/deps-<pkgver>-r0-aarch64.apk`, by `packaging/nfpm/build.sh apk`.
5. `packaging/publish.sh dist/*.deb dist/*.pkg.tar.zst dist/*.apk` uploads the packages to the owner's Debian, Arch Linux and Alpine package registries (see below).
6. `gt release create <tag> --verify-tag --notes-file dist/RELEASE_NOTES.md` publishes the release with the notes, the binaries, the packages and `checksums.txt`. `gt` is the Gitea CLI on the runner, authenticated with `GITEA_TOKEN`, the job token. The GitHub release gets the same assets.
7. `task release:attest-from-github TAG=<tag> DIR=dist` waits for the GitHub release's attestations, verifies them against `dist/` and attaches them (see [Attestations](#attestations)). The release is already public at this point, so a failure here only leaves it without attestations until the backfill runs.

git-cliff is installed from its GitHub release instead of through `orhun/git-cliff-action`. That action always sends `github.token` to api.github.com to find the release, and on Gitea that token is a Gitea token, so the request fails with 401.

**Packages.** Each package holds `/usr/bin/deps` and the bash, zsh and fish completions, generated by `deps completion` from the binary of the build machine's architecture (`packaging/common.sh`). None depends on anything for the binary, which is static; `deps run` needs `git`.

| | Debian | Arch Linux | Alpine |
|---|---|---|---|
| Built by | `dpkg-deb` | [nfpm](https://nfpm.goreleaser.com) v2.48.0 (`packaging/nfpm/nfpm.yaml`) | nfpm |
| Architectures | `amd64`, `arm64` | `x86_64`, `aarch64` | `x86_64`, `aarch64` |
| Version of `v1.2.3` | `1.2.3` | `1.2.3-1` | `1.2.3-r0` |
| Version of `v1.2.3-rc.1` | `1.2.3~rc.1` | `1.2.3rc.1-1` | `1.2.3_rc1-r0` |
| Completions | `/usr/share/bash-completion/completions/deps`, `/usr/share/zsh/vendor-completions/_deps`, `/usr/share/fish/vendor_completions.d/deps.fish` | bash and fish as Debian, `/usr/share/zsh/site-functions/_deps` | as Arch Linux |
| git | `Recommends: git` | optional dependency | `depend = git` |
| Registry | distribution `stable`, component `main` | repository `jakobmoellerdev` | branch `stable`, repository `main` |

- **Version.** The tag without the leading `v`, with the pre-release mapped so that every package manager sorts it before the release (checked with `vercmp` and `apk version -t`):
  - Debian: the first `-` becomes `~`.
  - Arch Linux: the `-` is dropped, because pacman reserves it for the package release (`pkgrel`, always `1`). The pre-release must start with a letter: vercmp sorts `1.2.3rc.1` before `1.2.3`, but `1.2.3.1` after it.
  - Alpine: only `alpha`, `beta`, `pre` and `rc`, optionally numbered, become the suffixes `_alpha`, `_beta`, `_pre` and `_rc`. Any other pre-release fails the build, since Alpine has no other suffixes that sort before the release.
- **Reproducible.** Every mtime is `SOURCE_DATE_EPOCH`, by default the commit time of the tag (of `HEAD` if the tag does not exist). Owners are root:root (`dpkg-deb --root-owner-group`; nfpm always) and modes are fixed, so rebuilding a tag gives byte-identical packages.
- **nfpm** is pinned to v2.48.0, installed and sha256-verified by `task nfpm:install` (a matching `nfpm` on `PATH` is used as is).
- **Signing.** The packages are unsigned. Gitea signs the Arch Linux packages and database on upload (with the owner's key from `/api/packages/<owner>/arch/repository.key`) and the Alpine `APKINDEX.tar.gz` (key at `/api/packages/<owner>/alpine/key`); apk trusts an unsigned package through its checksum in the signed index. apk finds that key by its file name, `<owner>@<sha256 of the key>.rsa.pub`, so the [install guide](install.md#packages) names it explicitly.
- **Registries.** The Alpine branch is a name only: one `stable` branch serves every Alpine release, because the binary is static (installs verified on Alpine 3.22 with apk-tools 2 and 3.24 with apk-tools 3). The Arch repository is named after the owner, because its name is also the `pacman.conf` section, which must not clash with `core` or `extra`.
- **Token.** The uploads are `PUT <server>/api/packages/<owner>/debian/pool/stable/main/upload`, `PUT …/arch/jakobmoellerdev` and `PUT …/alpine/stable/main`, with HTTP Basic auth. The job token cannot write packages: Gitea gives the Actions user read access to a user's registry only. The job therefore uses the repository secret `PACKAGES_TOKEN`, a token of `jakobmoellerdev` named `deps-release-packages` with only the `write:package` scope.
- **Re-runs.** If the registry already has a file (HTTP 409), `publish.sh` downloads it. An identical file is skipped with a notice; a different one is deleted and uploaded again with a warning.
- **Trying it without a release:**

  ```sh
  task release:build VERSION=v0.0.0-rc.1 && task release:packages VERSION=v0.0.0-rc.1
  PACKAGES_URL=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev PACKAGES_USER=jakobmoellerdev \
    PACKAGES_TOKEN=$(gt auth token) packaging/publish.sh dist/*.deb dist/*.pkg.tar.zst dist/*.apk
  # install it in containers with the snippets from docs/install.md, then delete the test versions:
  p=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev
  for arch in amd64 arm64; do gt api -X DELETE "$p/debian/pool/stable/main/deps/0.0.0~rc.1/$arch"; done
  for arch in x86_64 aarch64; do
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

**Identity.** The certificates are issued by `https://token.actions.githubusercontent.com` to the workflow `https://github.com/jakobmoellerdev/deps/.github/workflows/release.yml@<ref>`, where `<ref>` is `refs/tags/<tag>` for a tag push. A `workflow_dispatch` republish signs as the ref it was dispatched on, normally `refs/heads/main`, even though it builds the tag; the script accepts exactly these two identities, and its provenance names `main` as the workflow ref. Dispatch with `--ref <tag>` to sign as the tag, but only tags whose `release.yml` already attests (after v0.2.5) have the attest steps.

**Republishing old tags.** The build uses the tag's Taskfile, while `release:sbom` comes from the Taskfile of the workflow's own revision (a sparse checkout into `.release-tooling`), so tags older than the SBOM task can be republished and attested.

**Backfill.** `.gitea/workflows/attest.yml` (`workflow_dispatch`, input `tag`) runs the script against the files of an existing Gitea release: for releases made before attestations existed, after a GitHub republish, or when the release job's attest step failed. To attest an older release, republish it on GitHub first, then backfill:

```sh
gh workflow run release.yml -R jakobmoellerdev/deps -f tag=v0.2.5   # wait for it: gh run watch -R jakobmoellerdev/deps
gt workflow run attest.yml --ref main -f tag=v0.2.5 --follow
```
