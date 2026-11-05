<p align="center">
  <img src="docs/assets/logo.svg" width="96" height="96" alt="deps logo">
</p>
<h1 align="center">deps</h1>
<p align="center">
  <b>Renovate-style dependency PRs from one static Go binary.</b><br>
  No Node runtime, no hosted app, no reformatted files. One strict config, one pull request per update.
</p>
<p align="center">
  <a href="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions?workflow=ci.yml"><img src="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/actions/workflows/ci.yml/badge.svg?branch=main" alt="ci"></a>
  <a href="https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases"><img src="https://img.shields.io/gitea/v/release/jakobmoellerdev/deps?gitea_url=https%3A%2F%2Fgit.jakob-moeller.cloud&label=release" alt="latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache-2.0"></a>
</p>
<p align="center">
  <img src="https://cdn.simpleicons.org/github/_/white" height="22" alt="GitHub" title="GitHub">&nbsp;&nbsp;
  <img src="https://cdn.simpleicons.org/gitea" height="22" alt="Gitea" title="Gitea">&nbsp;&nbsp;
  <img src="https://cdn.simpleicons.org/forgejo" height="22" alt="Forgejo" title="Forgejo">
</p>

<p align="center">
  <a href="#getting-started">Getting started</a> ·
  <a href="#supported-ecosystems">Supported ecosystems</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#why-deps">Why deps</a> ·
  <a href="#compared-with-renovate-and-dependabot">Comparison</a> ·
  <a href="#performance">Performance</a> ·
  <a href="#next-steps">Docs</a> ·
  <a href="#development">Development</a>
</p>

deps finds the dependencies in a repository, looks up newer versions, and opens one branch and pull request per dependency or group. It rewrites only the version bytes, so formatting and comments stay as they are.

<details open>
<summary><img src="https://cdn.simpleicons.org/linux/_/white" height="16" alt=""> <img src="https://cdn.simpleicons.org/apple/_/white" height="16" alt=""> <b>Linux / macOS</b> (binary, amd64 and arm64)</summary>

```sh
mkdir -p ~/.local/bin && curl -fsSL -o ~/.local/bin/deps "https://github.com/jakobmoellerdev/deps/releases/latest/download/deps_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" && chmod +x ~/.local/bin/deps
```

</details>

<details>
<summary><img src="https://cdn.simpleicons.org/debian" height="16" alt=""> <img src="https://cdn.simpleicons.org/ubuntu" height="16" alt=""> <b>Debian / Ubuntu</b> (apt)</summary>

```sh
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian/repository.key -o /etc/apt/keyrings/deps.asc
echo "deb [signed-by=/etc/apt/keyrings/deps.asc] https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/debian stable main" | sudo tee /etc/apt/sources.list.d/deps.list
sudo apt-get update && sudo apt-get install deps
```

</details>

<details>
<summary><img src="https://cdn.simpleicons.org/fedora" height="16" alt=""> <img src="https://cdn.simpleicons.org/redhat" height="16" alt=""> <b>Fedora / RHEL</b> (dnf)</summary>

```sh
printf '[deps]\nname=deps\nbaseurl=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/rpm\nenabled=1\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/rpm/repository.key\n' | sudo tee /etc/yum.repos.d/deps.repo
sudo dnf install deps
```

</details>

<details>
<summary><img src="https://cdn.simpleicons.org/archlinux" height="16" alt=""> <b>Arch Linux</b> (pacman)</summary>

```sh
curl -fsSL https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/repository.key -o /tmp/deps.key
sudo pacman-key --add /tmp/deps.key
sudo pacman-key --lsign-key "$(gpg --show-keys --with-colons /tmp/deps.key | awk -F: '$1 == "fpr" { print $10; exit }')"
printf '\n[jakobmoellerdev]\nSigLevel = Required\nServer = https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/arch/$repo/$arch\n' | sudo tee -a /etc/pacman.conf
sudo pacman -Syu deps
```

</details>

<details>
<summary><img src="https://cdn.simpleicons.org/alpinelinux" height="16" alt=""> <b>Alpine</b> (apk, as root)</summary>

```sh
wget -qO /etc/apk/keys/jakobmoellerdev@406384923acbad2a78f7a8f09acbfa3ae0bd4acf33360d688568adaa4cd205a3.rsa.pub https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/key
echo https://git.jakob-moeller.cloud/api/packages/jakobmoellerdev/alpine/stable/main >> /etc/apk/repositories
apk add deps
```

</details>

<details>
<summary>🪟 <b>Windows</b> (PowerShell)</summary>

```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
iwr "https://github.com/jakobmoellerdev/deps/releases/latest/download/deps_windows_$arch.exe" -OutFile deps.exe
```

</details>

<details>
<summary><img src="https://cdn.simpleicons.org/go" height="16" alt=""> <b>From source</b> (Go)</summary>

```sh
go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@latest
```

</details>

Then, in any repository:

```sh
deps plan   # preview every update as a diff, no config needed
```

Packages install `/usr/bin/deps` with shell completions. Checksums and Sigstore verification: [install](docs/install.md).

**Coming from Renovate?** Disable Renovate, add `branchPrefix: renovate/` to `.deps.yaml`, then `deps run --dry-run` to see which PRs deps would open. Once a real run completes without errors, deps closes Renovate's old PRs under that prefix. Option mapping: [migrating from Renovate](docs/migrating-from-renovate.md).

## Getting started

### GitHub

Add `.github/workflows/deps.yml`. It uses a GitHub App token, because `GITHUB_TOKEN` can't push changes to workflow files and its PRs don't run CI:

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

<details>
<summary><b>Gitea</b></summary>

Add `.gitea/workflows/deps.yml`:

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

</details>

<details>
<summary><b>Forgejo</b></summary>

Use the Gitea workflow as `.forgejo/workflows/deps.yml`, with two changes: drop the `permissions:` block (Forgejo's automatic token can already write), and set `runs-on:` to one of your runner's labels, e.g. `docker`. The runner image needs `bash`, `curl`, `git` and `sha256sum` ([Forgejo](docs/ci.md#forgejo)).

</details>

### Configuration (optional)

Without a `.deps.yaml`, every direct dependency is updated. A small config:

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

## Supported ecosystems

| | Ecosystem | Manifests | Lockfiles kept in sync |
|:-:|---|---|---|
| <img src="https://cdn.simpleicons.org/githubactions" width="20" alt=""> | **CI actions**<br>GitHub, Gitea, Forgejo | `uses:` tags, SHA pins (with or without `# vX` comments), `setup-*` versions | |
| <img src="https://cdn.simpleicons.org/go" width="20" alt=""> | **Go** | `go.mod` requires and `toolchain` | `go.sum` (`go mod tidy`) |
| <img src="https://cdn.simpleicons.org/docker" width="20" alt=""> | **Containers** | Dockerfiles, Compose, workflow containers, Kubernetes manifests, Helm values | |
| <img src="https://cdn.simpleicons.org/kubernetes" width="20" alt=""> | **Kubernetes** | Kustomize images, charts and remote refs | |
| <img src="https://cdn.simpleicons.org/helm/_/white" width="20" alt=""> | **Helm** | `Chart.yaml`, helmfiles, chart custom resources (classic repos and OCI) | `Chart.lock` |
| <img src="https://cdn.simpleicons.org/nodedotjs" width="20" alt=""> | **Node.js**<br>npm, pnpm, Yarn, Bun | `package.json` (deps, `packageManager`, `engines`, `volta`), `.nvmrc`, `.node-version` | `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lock` |
| <img src="https://cdn.simpleicons.org/python" width="20" alt=""> | **Python**<br>pip, pip-tools, uv, Poetry, pdm, Pipenv | `requirements*.txt` and pip-compile inputs, `pyproject.toml` (PEP 621/735), `Pipfile` | `uv.lock`, `poetry.lock`, `pdm.lock`, `Pipfile.lock`, pip-compile outputs |
| <img src="https://cdn.simpleicons.org/ruby" width="20" alt=""> | **Ruby**<br>Bundler | `Gemfile`/`gems.rb`, `*.gemspec`, Ruby version (`ruby` directive, `.ruby-version`, asdf/mise) | `Gemfile.lock`/`gems.locked` (incl. `RUBY VERSION`) |
| <img src="https://cdn.simpleicons.org/rust/_/white" width="20" alt=""> | **Rust** | `Cargo.toml`, with well-known crate monorepos grouped | `Cargo.lock` |
| <img src="https://cdn.simpleicons.org/swift" width="20" alt=""> | **Swift** | `Package.swift` (`from:`, `exact:`, ranges) | `Package.resolved` |
| <img src="https://cdn.simpleicons.org/terraform" width="20" alt=""> <img src="https://cdn.simpleicons.org/opentofu" width="20" alt=""> | **Terraform / OpenTofu** | providers, modules, `required_version`, Terragrunt sources, tflint plugins | `.terraform.lock.hcl` |
| <img src="https://cdn.simpleicons.org/apachemaven" width="20" alt=""> <img src="https://cdn.simpleicons.org/gradle/_/white" width="20" alt=""> | **JVM**<br>Maven, Gradle | `pom.xml`, Gradle builds and version catalogs, Maven and Gradle wrappers | `gradle.lockfile` |
| <img src="https://cdn.simpleicons.org/gitlab" width="20" alt=""> | **GitLab CI** | images, project includes, CI/CD components | |
| <img src="https://cdn.simpleicons.org/precommit" width="20" alt=""> | **pre-commit** | hook `rev`s, including frozen SHAs | |
| <img src="https://cdn.simpleicons.org/git" width="20" alt=""> | **Git submodules** | `.gitmodules` (tracked branch or version tag; opt-in, as in Renovate) | |
| 🧰 | **Tool versions** | asdf `.tool-versions`, mise `[tools]` | |
| ✏️ | **Anything else** | `# deps:` and `# renovate:` markers, regex managers | |

File patterns, dep types and skip reasons for every manager: [managers and datasources](docs/reference/managers.md).

## How it works

```mermaid
flowchart LR
  A[Scan<br>one directory walk, every manager] --> B[Look up<br>registries, deduplicated and shared]
  B --> C[Apply rules<br>.deps.yaml, top to bottom]
  C --> D[Patch<br>only the version bytes, refresh lockfiles]
  D --> E[Open PRs<br>one per dep or group, closed when obsolete]
```

## Why deps

> [!NOTE]
> **We love Renovate.** deps exists because of it: it reads Renovate's markers, takes over its branches, and borrows many of its ideas. But with AI-assisted development, code and dependencies change many times a day, and the update bot has to keep up. deps is built for that: a binary that starts in milliseconds, a run that finishes in seconds, and a re-run on every push to the default branch, so dependency PRs react to your changes instead of waiting for the next scheduled run.

- **Drop-in for Renovate users.** Reads `# renovate:` markers, takes over `renovate/` branches, and maps most options one to one ([migration guide](docs/migrating-from-renovate.md)).
- **Diffs you can trust.** Only version bytes change: no reformatting, no reordered keys, comments kept.
- **A small, strict config.** Rules are read top to bottom, later rules override single fields. Unknown keys are errors with a line number, and a JSON Schema gives editor completion.
- **Quiet pull requests.** One PR per dependency or group, split into major/minor/patch. Unchanged PRs are never pushed again; obsolete PRs are closed only after an error-free run. Schedules, PR limits, automerge and a dashboard issue are built in.
- **Security updates** from OSV advisories for Go, npm, Rust, PyPI, Maven, RubyGems and GitHub Actions, bypassing schedules and the minimum release age.
- **Release notes in every PR:** notes, release ages and a compare link.
- **Fast and light.** One directory walk, deduplicated lookups, commits without a checkout or worktree. See the numbers below.
- **Runs anywhere.** Same binary on your laptop and in GitHub, Gitea or Forgejo Actions; the forge is detected automatically.

### Compared with Renovate and Dependabot

| | deps | Renovate | Dependabot |
|---|---|---|---|
| Runtime | one static Go binary | Node.js app or container image | hosted by GitHub |
| GitHub | ✅ | ✅ | ✅ |
| Gitea / Forgejo | ✅ | ✅ | ❌ |
| GitLab, Bitbucket | 🟡 partial¹ | ✅ | ❌ |
| Config | one strict `.deps.yaml`, unknown keys are errors | JSON with presets | `dependabot.yml` |
| `# renovate:` markers | ✅ | ✅ | ❌ |
| Single static binary, no runtime | ✅ | ❌ | ❌ |

¹ deps opens PRs on GitHub, Gitea and Forgejo only. In those repos it still updates what points at GitLab and Bitbucket: GitLab CI images, includes and components, and Terraform/Terragrunt modules and Kustomize remotes hosted there.

Renovate covers more platforms and managers; deps trades that breadth for a single binary, a smaller config and predictable diffs. Feature-by-feature status: [Renovate parity](docs/renovate-parity.md).

### Performance

`deps plan` (scan, all registry lookups, patched diff) on large open-source repos, limited to 4 CPUs (`taskset -c 0-3`, `GOMAXPROCS=4`) like a standard CI runner, median of 3 runs:

| Repository | Ecosystems | Tracked files | Updates found | Wall time | CPU time | Peak memory |
|---|---|--:|--:|--:|--:|--:|
| openclaw/openclaw 🦞 | pnpm, Swift, Ruby, Actions | 64,398 | 475 | 3.0 s | 1.9 s | 164 MB |
| vercel/next.js | pnpm, Rust, Actions | 35,236 | 1,591 | 1.9 s | 2.7 s | 208 MB |
| kubernetes/kubernetes | Go, containers | 31,705 | 112 | 1.9 s | 0.7 s | 52 MB |
| grafana/grafana | Go, Yarn, containers, Actions | 24,678 | 1,178 | 12.1 s | 2.6 s | 144 MB |
| apache/airflow | Python, uv, containers | 15,376 | 590 | ~6 min¹ | 3.0 s | 160 MB |
| backstage/backstage | Yarn workspaces | 12,453 | 1,217 | 2.3 s | 1.9 s | 190 MB |
| spring-projects/spring-boot | Gradle, Maven | 11,978 | 39 | 0.8 s | 0.3 s | 35 MB |
| astral-sh/ruff | Cargo, Python, npm | 11,298 | 254 | 1.2 s | 0.8 s | 64 MB |
| can1357/oh-my-pi | Bun, Rust, Python | 10,283 | 154 | 1.8 s | 0.4 s | 51 MB |
| istio/istio | Go, containers, Helm | 6,775 | 117 | 5.0 s | 0.8 s | 53 MB |
| argoproj/argo-cd | Go, Yarn, containers | 6,220 | 152 | 11.0 s | 1.0 s | 73 MB |
| open-component-model | Go, containers, Helm | 3,046 | 87 | 2.2 s | 0.4 s | 46 MB |
| kubernetes-sigs/cluster-api | Go, containers | 2,404 | 31 | 0.9 s | 0.4 s | 56 MB |

CPU time is deps itself; the rest of the wall time is registry latency, mostly container registries paging through tag lists. ¹ airflow waits on `public.ecr.aws/lambda/python`'s tag list (16 pages of about 3 s each) and on `docker.elastic.co`, whose tag list takes minutes to answer; when it times out instead, the run takes about 1 min and reports 2 errors.

`deps run` adds the lockfile tools (`go mod tidy`, npm/pnpm/yarn, uv, cargo …) for the PRs it updates; PRs whose content didn't change are not pushed again.

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
