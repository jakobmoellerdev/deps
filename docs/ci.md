# Running deps in CI

deps behaves the same locally and in CI. In CI it detects the platform from the runner environment and uses the job token, unless you pass another.

## Gitea / Forgejo: composite action

The repository root has a composite `action.yml`. It builds the binary from the action's own checkout, so no release artifacts are needed, and then runs it:

| Input | Default | Meaning |
|---|---|---|
| `token` | `${{ github.token }}` | exported as `DEPS_TOKEN`; needs contents:write and pull-requests:write |
| `args` | `run` | arguments for `deps` |

The action also sets `DEPS_DEFAULT_ACTIONS_URL` from `${{ gitea.gitea_default_actions_url }}`. The Gitea 28 runner leaves that expression empty in practice, so the platform default applies (`github.com`, or `data.forgejo.org` on Forgejo). Instances with a non-default `DEFAULT_ACTIONS_URL` should set [`actions.defaultHost`](configuration.md#actionsdefaulthost).

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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@main
        with:
          args: run -v --log-level info
```

Use the full URL in `uses:`. A short `jakobmoellerdev/deps@main` is resolved against the instance's `DEFAULT_ACTIONS_URL`, which is github.com by default. Inside this repository, the dogfood workflow uses `uses: ./`.

If the job token cannot push, open PRs, or read private action/module repositories (`GITEA_TOKEN` cannot read other private repos by default), store a personal access token as a secret and pass it to both steps:

```yaml
      - uses: actions/checkout@v7.0.1
        with: {token: "${{ secrets.DEPS_TOKEN }}"}
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@main
        with: {token: "${{ secrets.DEPS_TOKEN }}"}
```

## GitHub

A GitHub workflow cannot `uses:` an action hosted on Gitea, so it runs deps with `go run`:

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
      - uses: actions/checkout@v6
        with:
          token: ${{ steps.app.outputs.token }}
      - uses: actions/setup-go@v6
        with: {go-version: stable}
      - run: go run git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@main run
        env:
          DEPS_TOKEN: ${{ steps.app.outputs.token }}
```

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
| `go` | building the action or `go run`; `go mod tidy` for go.mod changes; `go env` (falls back to the environment without it); `go list` for GOPRIVATE/direct modules |
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
