# deps

Dependency update automation in one Go binary: a Renovate replacement with a small, strictly validated config file (`.deps.yaml`). It works the same on a laptop and in CI, and opens pull requests on GitHub, Gitea and Forgejo, which it detects automatically.

## Features

- **Managers.** deps edits only the version bytes; files are never reformatted.
  - **GitHub/Gitea/Forgejo Actions:** `uses:` tags and SHA pins with `# vX` comments, plus the version inputs of setup actions (setup-node, setup-go, setup-helm, setup-kubectl, setup-terraform, golangci-lint-action).
  - **Go modules:** requirements and the `go`/`toolchain` directives, followed by `go mod tidy`.
  - **Container images:** Dockerfile `FROM`, `COPY --from`, `RUN --mount=from` and `ARG` defaults; compose; workflow `container:`/`services:`/`docker://`; `runs.image`; Kubernetes manifests; Helm values. Images can be pinned to `@sha256:` digests.
  - **npm `package.json`:** dependencies, dev/optional/peer dependencies, `packageManager`, `engines`, `volta`. Ranges are bumped in place (`^1.2.3` → `^1.4.0`), workspaces are supported, and `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock` is regenerated.
  - **Custom:** `# deps:` annotations, Renovate `# renovate:` markers, and RE2 regex managers, optionally with a release checksum next to the version.
- **Datasources:** git tags (native smart-HTTP `ls-refs`), GitHub releases, the Go proxy, Go toolchains, OCI registries, npm registries (`.npmrc`), Node.js releases, and user-defined JSON/YAML/text documents.
- **Policy:** ordered rules with field-wise override. They match on manager, name, dep type, path, datasource and update type, and they can set ignore, allow (semver or `/regex/`), versioning (`semver`, `loose`, `regex:`), groups, cron schedules, minimum release age and commit type/scope. Monorepo groups are built in.
- **PR bot:** one branch and PR per dependency or group, with separate major/minor/patch buckets, `rebaseWhen`, PR limits, and a fingerprint so unchanged PRs are never re-pushed. Obsolete PRs are closed only after an error-free run.
- **Security:** OSV advisories for Go, npm and GitHub Actions. Security updates bypass `allow`, schedules and release age.
- **Release notes:** release bodies, release ages and a compare link in every PR body.
- **Fast and memory-bounded:** one pruned directory walk, deduplicated concurrent lookups over one HTTP/2 client, and no checkout or worktree for commits.

## Install

Download a static binary (linux/amd64, linux/arm64) from the [releases](https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases) and verify it against `checksums.txt`:

```sh
base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.1.0
curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt"
grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 ~/.local/bin/deps
```

`go install` still works:

```sh
go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@latest
```

or from a clone, `task build` builds a static `bin/deps` (`CGO_ENABLED=0`, `-trimpath`).

In GitHub, Gitea or Forgejo Actions, use the composite action. It downloads the released binary for the runner and verifies its checksum, so the runner needs no Go (only for `go mod tidy` on go.mod updates):

```yaml
permissions: {contents: write, pull-requests: write}
steps:
  - uses: actions/checkout@v7.0.1
  - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0
```

GitHub workflows cannot `uses:` a Gitea-hosted action; [docs/ci.md](docs/ci.md#github) shows a mirror and a plain download step, plus tokens, runner tools and the release process.

## Quickstart

```sh
deps plan                  # unified diff on stdout, one line per update/error on stderr
deps plan -v               # also list skipped deps and why
deps apply                 # write the updates (plus go mod tidy and lockfiles)
deps run --dry-run         # which PRs would be created, updated or closed
deps run                   # push <branchPrefix><manager>/<name> branches and open/update PRs
deps explain 'actions/*'   # rules, candidates and decision for matching deps
deps schema > deps.schema.json
```

`plan` output:

```
update  .github/workflows/ci.yml:12  actions/checkout  v6.0.0 -> v7.0.1  (major)
1 updates in 1 files, 0 skipped, 0 errors
```

Exit codes: 0 success, 1 errors, 2 invalid flags or config.

## Configuration

Without a `.deps.yaml` the defaults apply: every direct dependency is updated, while indirect Go requirements, the `go` directive, npm peerDependencies and engines are ignored. A typical config:

```yaml
# yaml-language-server: $schema=./deps.schema.json
schedule: ["* 0-5 * * 1-5"]      # weekday nights (minute must be *)
timezone: Europe/Berlin
pin: digest                       # pin actions to SHAs and images to digests
minimumReleaseAge: 3 days
rules:
  - manager: github-actions       # minor and patch bumps in one PR
    updateTypes: [minor, patch]
    group: actions
  - manager: gomod
    updates: [minor, patch]
  - names: ['postgres']
    allow: '< 18'
custom:
  - files: ['Makefile', '.env']   # '# deps:' / '# renovate:' markers
```

Unknown keys are errors. All validation problems are reported together with their location.

## Documentation

- [Configuration reference](docs/configuration.md): every key, rule semantics, versioning, schedules, groups, custom managers
- [Managers and datasources](docs/managers.md): what is extracted, skip reasons, edits, post-processing, endpoints
- [CLI reference](docs/cli.md): commands, flags, environment, output formats, exit codes
- [Running in CI](docs/ci.md): Gitea/Forgejo action, GitHub workflow, tokens, permissions
- [Migrating from Renovate](docs/migrating-from-renovate.md): option mapping and differences
- [Design](docs/design.md): architecture, performance and the PR lifecycle
- [Renovate parity](docs/renovate-parity.md): feature status against open-component-model's Renovate setup

## Development

```sh
task ci    # vet, golangci-lint, race tests with coverage gate, gofmt, go mod tidy check
```
