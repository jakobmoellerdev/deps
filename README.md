<p align="center">
  <img src="docs/assets/logo.svg" width="96" height="96" alt="deps logo">
</p>
<h1 align="center">deps</h1>
<p align="center">
  Dependency updates for GitHub, Gitea and Forgejo: one static Go binary, one strict config file, one pull request per update.
</p>
<p align="center">
  <a href="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions?workflow=ci.yml"><img src="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions/workflows/ci.yml/badge.svg?branch=main" alt="ci"></a>
</p>

deps is a Renovate replacement written in Go. It finds the dependencies in a repository, looks up newer versions, and pushes one branch and pull request per dependency or group. It edits only the version bytes, so files are never reformatted. It runs the same on a laptop and in GitHub Actions, Gitea Actions or Forgejo Actions, and it detects the forge on its own.

- [Features](#features)
- [Quick start: GitHub Actions](#quick-start-github-actions)
- [Quick start: Gitea Actions](#quick-start-gitea-actions)
- [Quick start: Forgejo Actions](#quick-start-forgejo-actions)
- [Tokens and permissions](#tokens-and-permissions)
- [Schedules, automerge and the dashboard](#schedules-automerge-and-the-dashboard)
- [Action inputs](#action-inputs)
- [Install](#install)
- [Local usage](#local-usage)
- [Configuration](#configuration)
- [Documentation](#documentation)

## Features

- **Managers.** Each manager edits only the version bytes ([reference](docs/reference/managers.md)).
  - **GitHub/Gitea/Forgejo Actions:** `uses:` tags, and SHA pins with `# vX` comments. It also updates the version inputs of setup-node, setup-go, setup-helm, setup-kubectl, setup-terraform and golangci-lint-action.
  - **Go modules:** requirements and the `go`/`toolchain` directives, followed by `go mod tidy`.
  - **Container images:** Dockerfile `FROM`, `COPY --from`, `RUN --mount=from` and `ARG` defaults; compose files; workflow `container:`, `services:` and `docker://`; `runs.image`; Kubernetes manifests; Helm values. Images can be pinned to `@sha256:` digests.
  - **npm `package.json`:**
    - dependencies, dev/optional/peer dependencies, `packageManager`, `engines` and `volta`;
    - ranges are bumped in place (`^1.2.3` → `^1.4.0`), and workspaces are supported;
    - `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock` is regenerated.
  - **Custom:** `# deps:` annotations, Renovate `# renovate:` markers, and RE2 regex managers, optionally with a release checksum next to the version.
- **Datasources:**
  - git tags (native smart-HTTP `ls-refs`, no `git` process per lookup);
  - GitHub releases;
  - the Go proxy and Go toolchains;
  - OCI registries;
  - npm registries (`.npmrc`) and Node.js releases;
  - user-defined JSON, YAML or text documents.
- **Policy:** ordered rules with field-wise override.
  - Rules match on manager, name, dep type, path, datasource and update type.
  - They can set ignore, allow (semver or `/regex/`), versioning (`semver`, `loose`, `regex:`), groups, schedules, minimum release age, automerge and the commit type/scope.
  - Monorepo groups are built in.
- **PR bot:**
  - one branch and PR per dependency or group, with separate major/minor/patch buckets;
  - `rebaseWhen`, PR limits, platform automerge, and a dependency dashboard issue;
  - a fingerprint, so unchanged PRs are never pushed again;
  - obsolete PRs are closed only after an error-free run.
- **Security:** OSV advisories for Go, npm and GitHub Actions. Security updates bypass `allow`, schedules and the release age.
- **Release notes:** every PR body has the release notes, release ages and a compare link.
- **Fast and memory-bounded:** one pruned directory walk, deduplicated concurrent lookups over one HTTP/2 client, and no checkout or worktree for commits.

## Quick start: GitHub Actions

Add `.github/workflows/deps.yml`:

```yaml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
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
      - uses: jakobmoellerdev/deps@v0.1.0
        with:
          token: ${{ steps.app.outputs.token }}
```

Use a GitHub App token (or a PAT) instead of `GITHUB_TOKEN`, and pass it to both `actions/checkout` and the action:

- **Workflow files.** `GITHUB_TOKEN` can't push changes to `.github/workflows/*`, and updating those files is most of what deps does. `permissions: actions: write` doesn't change that; the `workflows` permission exists only for Apps and PATs. The App needs the *Workflows: write* permission; a classic PAT needs the `workflow` scope. Without either, see [only `GITHUB_TOKEN`](docs/ci.md#github).
- **CI on the PRs.** PRs and pushes made with `GITHUB_TOKEN` don't trigger workflows, so CI wouldn't run on deps PRs, and `automerge` would never see green checks.

The App needs *Contents*, *Pull requests* and *Workflows* set to write. Add *Issues: write* for the dashboard, and *Checks* and *Commit statuses* set to read for automerge. deps pushes with the credentials that `actions/checkout` stores, and calls the API with the action's `token` input.

If the repository has no workflow files to update and doesn't need CI on deps PRs, the job token is enough. Drop the App step and both `token:` inputs, and set `permissions: {contents: write, pull-requests: write}`.

On github.com the action downloads the release binary from [github.com/jakobmoellerdev/deps](https://github.com/jakobmoellerdev/deps), the GitHub mirror of this repository. More options are in [docs/ci.md](docs/ci.md#github): `go install` instead of the action, the App's commit identity, and GHES.

## Quick start: Gitea Actions

Add `.gitea/workflows/deps.yml`:

```yaml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
```

- **`uses:` needs the full URL.** A short `jakobmoellerdev/deps@…` is resolved against the instance's `DEFAULT_ACTIONS_URL`, which is github.com by default.
- **What the job token (`GITEA_TOKEN`) can do.** It can push branches and open PRs in its own repository. It can't read other private repositories unless they allow it under *Settings → Actions → General → Cross-Repository Access*.
- **CI on deps PRs.** As on GitHub, PRs opened with the job token don't start `pull_request` workflows. If CI must run on deps PRs (for automerge, for example), store a personal access token as the secret `DEPS_TOKEN` and pass it to both steps:

```yaml
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ secrets.DEPS_TOKEN }}
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
        with:
          token: ${{ secrets.DEPS_TOKEN }}
```

deps looks up short `owner/repo@ref` actions on github.com, the Gitea default. If your instance sets another `DEFAULT_ACTIONS_URL`, set [`actions.defaultHost`](docs/reference/configuration.md#actionsdefaulthost) or `DEPS_DEFAULT_ACTIONS_URL`.

## Quick start: Forgejo Actions

Forgejo reads `.forgejo/workflows/deps.yml`:

```yaml
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
    runs-on: docker # a label of your runner
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
```

- **Permissions.** Forgejo's automatic token can write to its repository (except in fork PRs), so the workflow has no `permissions:` block; see [docs/ci.md](docs/ci.md#forgejo).
- **Default actions host.** deps resolves short `owner/repo@ref` actions against `data.forgejo.org`, the Forgejo default.
- **Token and CI.** The same token caveat as on Gitea applies.
- **Runner image.** It needs `bash`, `curl`, `git` and `sha256sum`.

## Tokens and permissions

| | GitHub | Gitea | Forgejo |
|---|---|---|---|
| default token | `GITHUB_TOKEN` (can't change workflow files; its PRs don't run CI) | `GITEA_TOKEN` (its PRs don't run CI) | automatic token |
| recommended token | GitHub App token or PAT, given to `actions/checkout` and the action | the job token, or a PAT in a secret when CI must run on PRs | as on Gitea |
| push branches | `contents: write` (App: Contents, Workflows) | `contents: write` | automatic |
| open/edit/close PRs | `pull-requests: write` | `pull-requests: write` | automatic |
| dashboard issue | `issues: write` | `issues: write` | automatic |
| automerge | App/PAT, *Allow auto-merge*, required checks; `checks`/`statuses: read` | merge rights, required status checks in branch protection | as on Gitea |

`deps run --dry-run` and `deps plan` need only read access. The per-forge details, private action and module repositories, and `DEPS_GITHUB_COM_TOKEN` for github.com lookups from Gitea are in [docs/ci.md](docs/ci.md#tokens-and-permissions).

**Signed commits.** For branch protection that requires signed commits, pass a passphrase-less OpenSSH or OpenPGP private key in the `signing-key` input (`DEPS_SIGNING_KEY`); deps then signs its commits. The forge marks them verified only if the committer identity (`gitAuthor`/`DEPS_GIT_AUTHOR`) is a verified email of the account that owns the key, so use a bot account. Merge deps' PRs fast-forward, or let the forge sign its merge commits. Setup per forge: [docs/ci.md](docs/ci.md#signed-commits).

## Schedules, automerge and the dashboard

**Schedules.** Each entry in [`schedule`](docs/reference/configuration.md#schedule), top-level or in a rule, must be one of the workflow's `on.schedule` crons, written the same way.

- deps reads the cron that triggered the run from the event payload.
- New PRs are created only for groups whose schedule lists that cron.
- `workflow_dispatch`, `push` and local runs act on every group.

A daily workflow with weekly groups:

```yaml
on:
  schedule:
    - cron: "0 5 * * *" # deps without a schedule
    - cron: "0 5 * * 1" # also deps whose schedule is ["0 5 * * 1"]
  workflow_dispatch:
```

```yaml
rules:
  - manager: gomod
    schedule: ["0 5 * * 1"]
```

**Automerge.** With [`automerge`](docs/reference/configuration.md#automerge), deps asks the forge to merge the PR once its required checks pass.

- **GitHub:**
  - turn on *Allow auto-merge* and require status checks on the base branch;
  - use an App/PAT token, so CI runs on the PR;
  - if GitHub refuses auto-merge (disabled in the settings, or nothing left to wait for), deps merges the PR directly once it is mergeable and all its checks are green, in that run or a later one.
- **Gitea/Forgejo:** the PR is scheduled with *merge when checks succeed*. The forge waits only for the status checks that branch protection requires; without required checks it merges right away. Use a PAT so CI runs on the PR.

**Dependency dashboard.** [`dependencyDashboard: true`](docs/reference/configuration.md#dependencydashboard) maintains one issue. It lists the updates awaiting their schedule, rate-limited and blocked updates, open PRs, errors, and the detected dependencies. Ticking a checkbox makes the next run create, recreate or rebase that PR. It needs `issues: write` and issues enabled in the repository.

## Action inputs

| Input | Default | Meaning |
|---|---|---|
| `token` | `${{ github.token }}` | exported as `DEPS_TOKEN`: the platform API token |
| `signing-key` | empty (unsigned) | exported as `DEPS_SIGNING_KEY`: private key deps signs its commits with; the committer (`gitAuthor`) must be the key owner's verified email |
| `args` | `run` | arguments for `deps`, e.g. `run -v --log-level info`, `plan`, `run --dry-run` |
| `version` | the action's ref if it is a release tag, else the latest release | release to download, e.g. `v0.1.0` |
| `repository` | `https://github.com/jakobmoellerdev/deps` on github.com, the Gitea repository elsewhere | where the release binary is downloaded from |
| `cache` | `true` | reuse a cached binary of the same version instead of downloading it; `false` downloads every run |

Outputs: `version` (the release that ran) and `cache-hit` (`true` if the binary came from a cache).

The action downloads the static binary for linux/amd64 or linux/arm64, verifies it against `checksums.txt`, and runs it. The runner needs no Go, except for `go mod tidy` on go.mod updates. A binary of the same version is reused from the runner's tool cache (self-hosted runners) or the forge's Actions cache (key `deps-<version>-linux-<arch>`), and only if it still matches its recorded checksum. Full reference: [docs/reference/action.md](docs/reference/action.md).

## Install

Every release is published to the Debian, Arch Linux and Alpine package registries on git.jakob-moeller.cloud, which need no login. Each package installs `/usr/bin/deps` with bash, zsh and fish completions, and the package files are also attached to each release.

**Debian and Ubuntu (amd64, arm64).**

```sh
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian/repository.key -o /etc/apt/keyrings/deps.asc
echo "deb [signed-by=/etc/apt/keyrings/deps.asc] https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian stable main" | sudo tee /etc/apt/sources.list.d/deps.list
sudo apt-get update && sudo apt-get install deps
```

The package recommends `git`, which `deps run` needs.

**Arch Linux (x86_64, aarch64).** The repository is signed with the registry's key, which pacman has to trust:

```sh
curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/repository.key -o /tmp/deps.key
sudo pacman-key --add /tmp/deps.key
sudo pacman-key --lsign-key "$(gpg --show-keys --with-colons /tmp/deps.key | awk -F: '$1 == "fpr" { print $10; exit }')"
printf '\n[jakobmoellerdev]\nSigLevel = Required\nServer = https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/$repo/$arch\n' | sudo tee -a /etc/pacman.conf
sudo pacman -Syu deps
```

`git` is an optional dependency, needed by `deps run`.

**Alpine Linux (x86_64, aarch64).** One repository serves every Alpine release, since the binary is static. As root:

```sh
wget -qO /etc/apk/keys/jakobmoellerdev@406384923acbad2a78f7a8f09acbfa3ae0bd4acf33360d688568adaa4cd205a3.rsa.pub https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/key
echo https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/stable/main >> /etc/apk/repositories
apk add deps
```

apk checks the index signature against the key file name, so keep the name. The package depends on `git`, which `deps run` needs.

**Static binary (linux/amd64, linux/arm64).** Download it from the [releases](https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases) and verify it:

```sh
base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.1.0
curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt"
grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 ~/.local/bin/deps
```

**From source.** `go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@latest`, or `task build` in a clone, which builds a static `bin/deps`.

## Local usage

```sh
deps plan                  # unified diff on stdout, one line per update or error on stderr
deps plan -v               # also list skipped deps and why
deps apply                 # write the updates (plus go mod tidy and lockfiles)
deps run --dry-run         # which PRs would be created, updated or closed
deps run                   # push branches and open or update PRs
deps explain 'actions/*'   # rules, candidates and the decision for matching deps
deps schema                # JSON Schema of .deps.yaml
```

```
update  .github/workflows/ci.yml:12  actions/checkout  v6.0.0 -> v7.0.1  (major)
1 updates in 1 files, 0 skipped, 0 errors
```

Exit codes: 0 success, 1 errors, 2 invalid flags or config.

- **Where `run` gets its settings:** the platform comes from the `origin` remote; the token comes from `--token`, `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN` or the git credential helper.
- **More:** every flag and environment variable is in the [CLI reference](docs/reference/cli.md).

## Configuration

Without a `.deps.yaml`, the defaults apply: every direct dependency is updated. Indirect Go requirements, the `go` directive, npm peerDependencies and engines are ignored. A typical config:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main
schedule: ["0 5 * * 1-5"]       # new PRs only in runs triggered by this workflow cron
pin: digest                      # pin actions to SHAs and images to digests
minimumReleaseAge: 3 days
dependencyDashboard: true
rules:
  - manager: github-actions      # minor and patch bumps in one PR
    updateTypes: [minor, patch]
    group: actions
    automerge: true
  - manager: gomod
    updates: [minor, patch]
  - names: ["postgres"]
    allow: "< 18"
custom:
  - files: ["Makefile", ".env"]  # '# deps:' / '# renovate:' markers
```

- **Strict validation.** Unknown keys are errors, and all validation problems are reported together with their location.
- **Editor support.** The schema comment on the first line enables completion and validation in editors that use yaml-language-server. [`deps.schema.json`](deps.schema.json) is generated from the code (`deps schema` prints the same).
- **Full reference:** [docs/reference/configuration.md](docs/reference/configuration.md).

## Documentation

| Document | Content |
|---|---|
| [Running in CI](docs/ci.md) | GitHub, Gitea and Forgejo Actions in depth: tokens, Apps, private repositories, self-hosted runners, schedules, caching, troubleshooting |
| [Configuration reference](docs/reference/configuration.md) | every `.deps.yaml` key (generated from the schema) |
| [CLI reference](docs/reference/cli.md) | commands, flags, environment, output, exit codes (generated) |
| [Action reference](docs/reference/action.md) | inputs and behavior of the composite action (generated) |
| [Managers and datasources](docs/reference/managers.md) | what is extracted, skip reasons, lookups (generated) |
| [Migrating from Renovate](docs/migrating-from-renovate.md) | option mapping and differences |
| [Design](docs/design.md) | architecture, performance and the PR lifecycle |
| [Renovate parity](docs/renovate-parity.md) | feature status against a real Renovate setup |
| [Changelog](CHANGELOG.md) | generated by git-cliff ([releases](docs/ci.md#releases-maintainers)) |

## Development

```sh
task ci           # vet, golangci-lint, race tests with coverage gate, gofmt, go mod tidy, generated docs check
task docs         # regenerate docs/reference/* and deps.schema.json
task changelog    # regenerate CHANGELOG.md with git-cliff (cliff.toml)
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
