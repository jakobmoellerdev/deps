# deps

Dependency update automation written in Go, with a small, strictly validated config file.

- GitHub/Gitea/Forgejo Actions `uses:` (tags, SHA pins with `# vX` comments)
- Go modules: requirements, `go`/`toolchain` directives, `go mod tidy`
- Container images: Dockerfile `FROM`, `COPY --from`/`RUN --mount=from` and `ARG` defaults, compose, workflow `container:`/`services:`/`docker://`, `runs.image`,
  Kubernetes manifests and Helm values; `@sha256:` pinning
- npm `package.json`: dependencies, devDependencies, optionalDependencies, peerDependencies (opt-in) and `packageManager`;
  ranges are bumped in place (`^1.2.3` → `^1.4.0`), `.npmrc` registries and tokens; workspaces, with
  `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock` regenerated
- Custom dependencies via `# deps: <datasource> <name>` annotations, Renovate `# renovate: datasource=… depName=…` markers
  or RE2 regex managers (Renovate group names `currentValue`/`depName`/`packageName`/`currentDigest` accepted;
  datasources `git-tags`, `github-releases`, `go`, `go-toolchain`, `docker`, `npm` and the Renovate aliases
  `golang-version`, `github-tags`, `github-release-attachments`), optionally pinning a release checksum next to the version
- OSV security updates (Go, GitHub Actions, npm) that bypass `allow`/`updates`/schedules
- Groups and cron schedules per rule; built-in monorepo groups (kubernetes, opentelemetry-go, babel, ...; `monorepos: false` disables)
- Renovate-style defaults: `ignorePaths` skips node_modules, vendor, examples and test directories unless set;
  semantic titles (`fix(deps):` for production deps, `chore(deps):` otherwise; `commitType`/`commitScope` per rule)
- HTTP retries with backoff and rate-limit handling (`Retry-After`)
- One branch and pull request per dependency or group on GitHub, Gitea and Forgejo (auto-detected), under a
  configurable `branchPrefix` (default `deps/`), with release ages and release notes in the PR body

Design and roadmap: [docs/design.md](docs/design.md).

```sh
deps plan            # unified diff of all updates on stdout, report on stderr
deps apply           # write the updates (go mod tidy for go.mod, npm/pnpm/yarn for lockfiles)
deps run --dry-run   # show which pull requests would be created or updated
deps run             # push deps/<manager>/<name> and deps/group/<group> branches, open/update PRs
deps explain 'actions/*'   # why a dependency is (not) updated
deps schema > deps.schema.json
```

In a Gitea/Forgejo workflow:

```yaml
permissions: {contents: write, pull-requests: write}
steps:
  - uses: actions/checkout@v6
  - uses: jakobmoellerdev/deps@main
```

Configuration lives in `.deps.yaml`:

```yaml
# yaml-language-server: $schema=./deps.schema.json
schedule: ["* 0-5 * * 1-5"]   # windows, minute must be *
rules:
  - manager: github-actions
    pin: digest
    group: actions
  - manager: gomod
    updates: [minor, patch]
custom:
  - files: ["Makefile"]
    match: 'GOLANGCI_VERSION \?= (?P<version>v\S+)'
    datasource: github-releases
    name: golangci/golangci-lint
```

See the config reference in the design document.
