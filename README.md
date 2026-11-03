# deps

Dependency update automation written in Go, with a small, strictly validated config file.

- GitHub/Gitea/Forgejo Actions `uses:` (tags, SHA pins with `# vX` comments)
- Go modules: requirements, `go`/`toolchain` directives, `go mod tidy`
- Container images: Dockerfile `FROM`, compose, workflow `container:`/`services:`/`docker://`, `runs.image`; `@sha256:` pinning
- npm `package.json`: dependencies, devDependencies, optionalDependencies, peerDependencies (opt-in) and `packageManager`;
  ranges are bumped in place (`^1.2.3` → `^1.4.0`), `.npmrc` registries and tokens, `package-lock.json` regenerated
- Custom dependencies via `# deps: <datasource> <name>` annotations or RE2 regex managers
  (datasources `git-tags`, `github-releases`, `go`, `docker`)
- OSV security updates (Go, GitHub Actions, npm) that bypass `allow`/`updates`/schedules
- Groups and cron schedules per rule
- One branch and pull request per dependency or group on GitHub, Gitea and Forgejo (auto-detected)

Design and roadmap: [docs/design.md](docs/design.md).

```sh
deps plan            # unified diff of all updates on stdout, report on stderr
deps apply           # write the updates (go mod tidy for go.mod, npm for package-lock.json)
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
