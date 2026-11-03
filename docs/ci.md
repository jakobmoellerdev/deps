# Running deps in CI

deps behaves the same locally and in CI. In CI it detects the platform from the runner environment and uses the job token, unless you pass another.

## Composite action

The repository root has a composite `action.yml`. It downloads a released static binary for the runner, verifies it against the release's `checksums.txt` with `sha256sum`, prints `deps version`, and runs it. Go is not needed to run the action. The same `action.yml` works unchanged on GitHub Actions and on Gitea/Forgejo Actions with default settings.

| Input | Default | Meaning |
|---|---|---|
| `token` | `${{ github.token }}` (the job token on GitHub, Gitea and Forgejo) | exported as `DEPS_TOKEN`; needs contents:write and pull-requests:write (issues:write for the dashboard) |
| `args` | `run` | arguments for `deps` |
| `version` | the action's ref if it is a release tag (`@v0.1.0`), else the latest release | release tag to download, e.g. `v0.1.0` |
| `repository` | `https://git.jakob-moeller.cloud/jakobmoellerdev/deps` | where releases are downloaded from: a Gitea/Forgejo repository URL (latest via `<host>/api/v1/repos/<repo>/releases/latest`) or a `https://github.com/<owner>/<repo>` URL (latest via `api.github.com`) |

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

## Releases (maintainers)

Pushing a `v*` tag runs `.gitea/workflows/ci.yml`: the `test` job runs `task ci` once (Go from `actions/setup-go@v7.0.0` with `go-version-file: go.mod`, i.e. `toolchain go1.27.3`, and `cache: true`), then the `release` job (`needs: test`, `contents: write`):

1. `task release:build VERSION=<tag>` builds `dist/deps_linux_amd64` and `dist/deps_linux_arm64` with `CGO_ENABLED=0`, `-trimpath` and `-ldflags "-s -w -buildid= -X main.version=<tag>"`, and writes `dist/checksums.txt` (`sha256sum`).
2. Each binary is checked to be static (no `Requesting program interpreter` in `readelf -l`), because the action runs it on arbitrary runner images.
3. `gt release create <tag> --verify-tag` (the Gitea CLI on the runner, authenticated with `GITEA_TOKEN` = the job token) publishes the release with the three assets.

To release:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

The first release is [v0.1.0](https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/tag/v0.1.0). Workflows that use the action at `@main` or without a release ref pick up a new release automatically; workflows pinned to `@vX.Y.Z` get it when deps (or Renovate) bumps their `uses:` ref.
