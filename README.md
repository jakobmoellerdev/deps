# deps

Dependency update automation written in Go: GitHub/Gitea/Forgejo Actions, container images,
and custom (annotation/regex) dependencies, with a small, strictly validated config file.

Status: milestone 1 — Actions `uses:` and Go modules, one branch and pull request per dependency
on GitHub, Gitea and Forgejo. Design and roadmap: [docs/design.md](docs/design.md).

```sh
deps plan            # unified diff of all updates on stdout, report on stderr
deps apply           # write the updates (runs go mod tidy for go.mod changes)
deps run --dry-run   # show which pull requests would be created or updated
deps run             # push deps/<manager>/<name> branches and open/update PRs
```

In a Gitea/Forgejo workflow:

```yaml
permissions: {contents: write, pull-requests: write}
steps:
  - uses: actions/checkout@v6
  - uses: jakobmoellerdev/deps@main
```

Configuration lives in `.deps.yaml`; see the config reference in the design document.
