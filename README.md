<p align="center">
  <img src="docs/assets/logo.svg" width="96" height="96" alt="deps logo">
</p>
<h1 align="center">deps</h1>
<p align="center">
  Dependency updates for GitHub, Gitea, Forgejo and GitLab: one static Go binary, one strict config file, one pull request per update.
</p>
<p align="center">
  <a href="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions?workflow=ci.yml"><img src="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions/workflows/ci.yml/badge.svg?branch=main" alt="ci"></a>
</p>

deps is a Renovate replacement written in Go. It finds the dependencies in a repository, looks up newer versions, and opens one branch and pull request per dependency or group. It changes only the version text, so your files are never reformatted. It runs the same on your machine and in GitHub Actions, Gitea Actions or Forgejo Actions, and it detects the forge by itself.

## Features

- **Many dependency types.** GitHub/Gitea/Forgejo Actions (tags, SHA pins with `# vX` comments, setup-* versions), Go modules (with `go mod tidy`), container images (Dockerfiles, compose, Kubernetes, Helm values), npm `package.json` with its lockfile, Python (`requirements*.txt`, `pyproject.toml` for PEP 621/uv and Poetry, `Pipfile`) with `uv.lock`, `poetry.lock` and `Pipfile.lock`, Helm charts, Rust crates in `Cargo.toml` (with `Cargo.lock`) and the Rust toolchain (`rust-toolchain.toml`), Ruby gems in `Gemfile`/`gems.rb` and `*.gemspec` (with `Gemfile.lock`/`gems.locked`) and the Ruby version (`.ruby-version`, the Gemfile `ruby` directive, asdf/mise, one PR with `Gemfile.lock`'s `RUBY VERSION`), Terraform/OpenTofu providers, modules, versions and resources (with `.terraform.lock.hcl`), Terragrunt sources, tflint plugins, Maven `pom.xml`, Gradle builds and version catalogs (with `gradle.lockfile`), the Gradle and Maven wrappers, git submodules (opt-in, as in Renovate), Swift packages in `Package.swift` (with `Package.resolved`), Dev Container images, features and feature versions in `devcontainer.json`, CI pipelines (Bitbucket Pipelines images and pipes, Woodpecker, Drone, CircleCI images and orbs, Azure Pipelines containers, repositories and tasks), vcpkg baselines and port versions (`vcpkg.json`, `vcpkg-configuration.json`), and your own `# deps:` or `# renovate:` markers and regex managers. See [managers and datasources](docs/reference/managers.md).
- **A small, strict config.** Rules are read top to bottom, and later rules override single fields. Unknown keys are errors with a line number, and an editor schema gives completion.
- **Pull requests that stay tidy.** One PR per dependency or group, split into major/minor/patch. Unchanged PRs are never pushed again, and obsolete PRs are closed only after an error-free run. Schedules, PR limits, automerge and a dashboard issue are built in.
- **Security updates** from OSV advisories for Go, npm, Rust crates, PyPI, Maven, RubyGems and GitHub Actions. They skip `allow`, schedules and the minimum release age.
- **Release notes** in every PR: notes, release ages and a compare link.
- **Fast and light:** one directory walk, shared and deduplicated lookups, and commits without a checkout or worktree.

## Quick start

**Gitea.** Add `.gitea/workflows/deps.yml`:

```yaml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  push:
    branches: [main] # your default branch: re-run right after a deps PR is merged
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
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.3.0
```

`uses:` needs the full URL, because a short `owner/repo@ref` is looked up on the instance's default actions host (github.com). CI doesn't run on PRs opened with the job token; to get CI (and automerge), pass a personal access token to both steps ([Gitea](docs/ci.md#gitea)).

**Forgejo.** Use the same file as `.forgejo/workflows/deps.yml`, with two changes: drop the `permissions:` block (Forgejo's automatic token can already write), and set `runs-on:` to one of your runner's labels, e.g. `docker`. The runner image needs `bash`, `curl`, `git` and `sha256sum` ([Forgejo](docs/ci.md#forgejo)).

**GitLab.** Add a `deps` job to `.gitlab-ci.yml` that downloads the release binary and runs `deps run` on a pipeline schedule, with a project access token (scopes `api`, `write_repository`) in the CI/CD variable `DEPS_TOKEN`. deps opens merge requests, uses `merge_when_pipeline_succeeds` for automerge and pipelines as checks ([GitLab CI](docs/ci.md#gitlab-ci)).

**GitHub.** Add `.github/workflows/deps.yml`. It uses a GitHub App token, because `GITHUB_TOKEN` can't push changes to workflow files and its PRs don't run CI:

```yaml
name: deps
on:
  schedule:
    - cron: "0 5 * * *"
  push:
    branches: [main] # your default branch: re-run right after a deps PR is merged
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
      - uses: jakobmoellerdev/deps@v0.3.0
        with:
          token: ${{ steps.app.outputs.token }}
```

The App needs *Contents*, *Pull requests* and *Workflows* set to write (and *Issues* for the dashboard). How to create it, a PAT instead, or the job token alone: [GitHub](docs/ci.md#github).

**Configuration (optional).** Without a `.deps.yaml`, every direct dependency is updated. A small config:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main
version: 1
dependencyDashboard: true
rules:
  - manager: github-actions # minor and patch bumps in one PR
    updateTypes: [minor, patch]
    group: actions
```

The first line turns on completion and checks in editors that use yaml-language-server.

## Next steps

- **Running in CI** ([docs/ci.md](docs/ci.md)):
  - [tokens and permissions](docs/ci.md#tokens-and-permissions)
  - [schedules](docs/ci.md#schedules) and [running on push](docs/ci.md#run-on-push-to-the-default-branch)
  - [automerge](docs/ci.md#automerge) and the [dependency dashboard](docs/ci.md#dependency-dashboard)
  - [signed commits](docs/ci.md#signed-commits)
  - [self-hosted runners and actions hosts](docs/ci.md#self-hosted-runners-and-actions-hosts)
  - [preview on pull requests](docs/ci.md#preview-on-pull-requests)
  - [troubleshooting](docs/ci.md#troubleshooting)
- **Reference** (generated from the code):
  - [configuration](docs/reference/configuration.md): every `.deps.yaml` key
  - [CLI](docs/reference/cli.md): commands, flags, environment, output and exit codes
  - [managers and datasources](docs/reference/managers.md): what is found, skip reasons and lookups
  - [action](docs/reference/action.md): inputs and outputs of the composite action
- **[Install and verify](docs/install.md)**: Debian, RPM, Arch Linux and Alpine packages, the static binary, `go install`, verifying a download, and running deps locally.
- **[Migrating from Renovate](docs/migrating-from-renovate.md)**: option mapping and differences.
- **Background:** [design](docs/design.md), [Renovate parity](docs/renovate-parity.md), [changelog](CHANGELOG.md) ([how releases are made](docs/ci.md#releases-maintainers)).

## Development

```sh
task ci           # task check (golangci-lint incl. govet, gofmt, go mod tidy, generated docs check), then race tests with coverage gate
task docs         # regenerate docs/reference/* and deps.schema.json
task changelog    # regenerate CHANGELOG.md with git-cliff (cliff.toml)
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
