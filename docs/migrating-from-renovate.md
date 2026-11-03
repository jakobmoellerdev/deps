# Migrating from Renovate

deps reads `.deps.yaml`, not `renovate.json(5)`. Most options translate one to one. Some behaviors that Renovate gets from presets are built in. This page lists the mapping, the known differences, and a worked translation of open-component-model's config. Feature status is tracked in [renovate-parity.md](renovate-parity.md).

## Option mapping

| Renovate | deps |
|---|---|
| `extends` (presets) | not supported; see [built-ins](#presets) |
| `enabled: false` (package rule) | `rules[].ignore: true` |
| `enabled: true` for disabled-by-default deps | `rules[].ignore: false` (indirect, `go` directive, peerDependencies, engines) |
| `matchManagers` | `rules[].manager` (one per rule): `github-actions`, `gomod`, `docker` (covers `dockerfile`, `docker-compose`, `kubernetes`, `helm-values`), `npm`, `custom` (covers `custom.regex`) |
| `matchPackageNames`, `matchDepNames` | `rules[].names` (doublestar globs; `{/,}**` works as in Renovate) |
| `matchDepTypes` | `rules[].depTypes` (deps names, see [managers.md](managers.md); Renovate's `golang` = `go`) |
| `matchFileNames` | `rules[].paths` |
| `matchDatasources` | `rules[].datasources` (`golang-version`, `github-tags`, `github-release-attachments`, `node` accepted) |
| `matchUpdateTypes` | `rules[].updateTypes`; `pinDigest` = `pin` |
| `matchCategories` | not supported; use `manager`/`datasources` (`golang` ≈ `manager: gomod` plus `datasources: [golang-version]`) |
| `allowedVersions` | `rules[].allow` (semver constraint or `/regex/`, `!/regex/`) |
| `versioning` | `rules[].versioning` (`semver`, `loose`, `regex:…`, plus deps' `tag`, `go`, `docker`) |
| `groupName` | `rules[].group` (`groupSlug` is derived: `slug(group)`) |
| `schedule` | `schedule` / `rules[].schedule`, as cron windows only; natural-language schedules (`before 6am`) are not supported |
| `timezone` | `timezone` |
| `updateNotScheduled` | `updateNotScheduled` |
| `minimumReleaseAge` | `minimumReleaseAge` / `rules[].minimumReleaseAge` (`null` → `0 days`) |
| `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch` | same names, top level and per rule |
| `pinDigests`, `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` (top level or per rule) |
| `rangeStrategy` | not configurable; npm ranges are always bumped in place (Renovate's `bump`); other managers replace the exact version |
| `postUpdateOptions: [gomodTidy]` | always on |
| `lockFileMaintenance` | not supported (lockfiles are regenerated only with an update) |
| `branchPrefix` | `branchPrefix` |
| `semanticCommits`, `:semanticPrefixFixDepsChoreOthers` | always on; `rules[].commitType`, `rules[].commitScope` |
| `gitAuthor` | `gitAuthor` (or `DEPS_GIT_AUTHOR`, `--author-name/--author-email`) |
| `gitIgnoredAuthors` | `gitIgnoredAuthors` |
| `prConcurrentLimit`, `prHourlyLimit` | same names; **default 0 (unlimited)** in deps, 10/2 in Renovate; `run --max-new-prs` (default 10) caps each run |
| `rebaseWhen`, `:rebaseStalePrs` | `rebaseWhen` (`conflicted` default, `behind-base-branch`, `never`) |
| `ignorePaths`, `:ignoreModulesAndTests` | `ignorePaths` (Renovate's list is the default when absent) |
| `vulnerabilityAlerts`, `osvVulnerabilityAlerts` | `security` (OSV, default on) |
| `customManagers` (`customType: regex`) | `custom[]` with `match` (Renovate group names accepted) |
| `managerFilePatterns` / `fileMatch` | `custom[].files` (doublestar globs, not regexes) |
| `datasourceTemplate`, `depNameTemplate`, `versioningTemplate` | `custom[].datasource`, `name`, `versioning` (literal values, no templates) |
| `customDatasources` (`defaultRegistryUrlTemplate`, `format`, `transformTemplates`) | `customDatasources` (`url`, `format`, `versions` selector + `versionRegex` instead of JSONata) |
| `github-release-attachments` digests | `custom[].checksum.asset` / `checksum.url` |
| `hostRules` (tokens) | env tokens, `.npmrc`, Docker credentials, git credential helper; retries are built in |
| `automerge` | `automerge` / `rules[].automerge` (platform auto-merge once required checks pass; a PR automerges when all its updates do) |
| `automergeType: pr` | `automergeType: pr` (the only accepted value; `branch` is an error) |
| `automergeStrategy` | `automergeStrategy`: `auto`, `merge`, `squash`, `rebase` (Renovate's `merge-commit` = `merge`; `fast-forward` and `rebase-merge` are not offered) |
| `platformAutomerge` | always on: deps only automerges through the platform (GitHub falls back to a direct merge once checks are green) |
| `ignoreTests` | not supported: deps never merges a PR without green checks |
| `:dependencyDashboard`, `dependencyDashboardTitle`, `dependencyDashboardLabels` | `dependencyDashboard: true`, `dependencyDashboardTitle`, `dependencyDashboardLabels` |
| `dependencyDashboardApproval` | not supported (the dashboard's checkboxes create PRs that wait for schedule or limits) |
| Mend badges, `security:openssf-scorecard` | not supported (release age as text in PR bodies) |
| `git-submodules` | not supported |
| repository cache | not applicable (no persistent cache) |

## Marker compatibility

Annotation-mode `custom[]` entries accept Renovate's markers unchanged:

```
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_VERSION=v2.5.0
```

- `packageName` wins over `depName`, `versioning=` takes a scheme name, and `registryUrl` and unknown keys are ignored.
- The marker applies to the **next line's** first version token. A Renovate regex that spans more lines needs a regex-mode `custom[]` entry instead.
- deps' own `# deps: <datasource> <name>` form works the same way.

In regex mode, Renovate's group names `currentValue`, `depName`, `packageName` and `currentDigest` are accepted, and so is its `(?<name>…)` syntax. Escaping differs, though: JSON5's `\\d` is YAML's `\d` in single quotes.

## Branch prefix takeover

With `branchPrefix: renovate/`, deps manages the branches and PRs Renovate created:

- **Branch names.** deps names branches `renovate/<manager>/<slug>` and `renovate/group/<slug>`, which are not Renovate's names (`renovate/<depName>-<n>.x`, `renovate/<groupSlug>`).
- **Existing PRs.** deps does not adopt Renovate's existing PRs. It opens its own, and once a run completes without errors, it closes every open PR under the prefix that matches none of its branches.
- **Branch authors.** Branches whose tip was committed by another author are never touched. If the old bot used another identity, add its email to `gitIgnoredAuthors`.
- **Migration steps.** Disable Renovate first, then run `deps run --dry-run` to see the planned PRs.

## Behavior differences

- **Loose versioning zero-fill.** In `loose`, a missing part compares as 0, so `1877` < `1877.25`. Renovate ranks the shorter version higher.
- **Bucket flags on `updateTypes` rules.** deps evaluates `separateMajorMinor`/`separateMultipleMajor`/`separateMinorPatch` with the policy of each candidate's type, so a rule with `updateTypes` *can* set them. Renovate decides buckets before the update type is known and ignores these flags on `matchUpdateTypes` rules. Configs that set them on manager rules, as Renovate requires, behave the same in deps.
- **`separateMultipleMajor` branch suffix.** deps uses `-v<N>` where Renovate writes `<N>.x`.
- **`rangeStrategy`.** For npm, deps always bumps the version inside the range (`^1.2.3` → `^1.4.0`), which is Renovate's `bump`. It never widens (`^1 || ^2`) or pins ranges, and it skips unsupported range forms (`||`, `x`, hyphen ranges) as `unsupported range`. OCM's `rangeStrategy: bump` for `golang-version` needs no translation: go.mod `toolchain` and `go` directives are replaced at their precision.
- **`matchCategories`** is not supported. Write the managers or datasources explicitly.
- **Presets are not supported.** The built-ins below cover the common ones.
- **Schedules** gate only creating PRs (and updating them with `updateNotScheduled: false`). Natural-language schedules must be rewritten as cron windows with `*` minutes, e.g. `* 0-5 * * 1-5` for weeknights.
- **Release age.** An unknown release time never blocks an update. Digest refreshes, pins and security updates are always exempt, so Renovate's `minimumReleaseAge: null` rule for those update types needs no translation.
- **Indirect Go requirements** are ignored by default, as in Renovate. Opt in with `depTypes: [indirect], ignore: false`.
- **PR limits.** Renovate defaults to `prConcurrentLimit: 10`, `prHourlyLimit: 2`; deps to `0`/`0` (unlimited), plus the per-run `--max-new-prs 10`. The effective limit is the minimum of the non-zero limits.
- **Automerge.** deps always uses the platform's auto-merge (Renovate's `platformAutomerge: true`) and never merges by itself while checks are pending. On GitHub repositories that disallow auto-merge, deps merges directly, but only a mergeable PR with at least one status or check run, all green; a repository without any checks is never merged that way, and there is no `ignoreTests` to change that.
- **Dependency dashboard.** The checkboxes are `create` (awaiting schedule, rate-limited), `recreate` (closed PRs) and `rebase` (open PRs, modified branches). There is no approval mode, no "pending status checks" section and no deprecation warnings; the dashboard additionally lists the run's errors. Detected dependencies are listed per manager and file, capped to stay below GitHub's body limit.
- **One PR per dependency or group.** Renovate's `<depName>-<major>.x` style branch-per-major-line maps to PR buckets.

### Presets

| Renovate preset | deps |
|---|---|
| `group:monorepos`, `group:kubernetesMonorepo`, `group:goOpenapi` | `monorepos: true` (default; 62 built-in entries) |
| `:ignoreModulesAndTests` | default `ignorePaths` |
| `:semanticPrefixFixDepsChoreOthers` | default commit types |
| `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` |
| `:rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `config:recommended` / `config:best-practices` | the defaults above plus `security: true` and `separateMajorMinor: true` |

## Worked example: open-component-model

[`examples-ocm.deps.yaml`](examples-ocm.deps.yaml) translates OCM's `.github/renovate.json5`. It validates as-is:

```sh
deps plan -v --config docs/examples-ocm.deps.yaml --dir ~/Projects/open-component-model > /dev/null
```

How the pieces map:

| renovate.json5 | examples-ocm.deps.yaml |
|---|---|
| `extends: docker:pinDigests, helpers:pinGitHubActionDigests` | `pin: digest` |
| `extends: :rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `extends: group:kubernetesMonorepo, group:goOpenapi` | built-in monorepos (default) |
| `branchPrefix`, `gitAuthor`, `gitIgnoredAuthors`, `prConcurrentLimit: 0`, `prHourlyLimit: 0`, `separateMinorPatch`, `separateMultipleMajor`, `updateNotScheduled`, `minimumReleaseAge: 28 days`, `automerge: false`, `automergeType: pr` | same keys |
| `extends: :dependencyDashboard` | `dependencyDashboard: true` |
| rule `matchUpdateTypes: [minor, patch]`, `automerge: true` | rule `updateTypes: [minor, patch]`, `automerge: true` (rule 20) |
| rule `matchFileNames: [chart/**]` + `groupName` | `paths` + `group` |
| `matchManagers: [github-actions]` + `separateMinorPatch: false` + schedule | `manager: github-actions` rule |
| `matchManagers` + `matchUpdateTypes: [minor, patch]` + `groupName` | `updateTypes: [minor, patch]` + `group` |
| "Digest pins" over `github-actions`, `dockerfile`, `docker-compose` with `[digest, pinDigest]` | two rules (`github-actions`, `docker`) with `updateTypes: [digest, pin]` |
| `matchDatasources: [golang-version]`, `rangeStrategy: bump` | `datasources: [golang-version]` (no `rangeStrategy` needed) |
| `enabled: false` for kube-openapi, go-ini/ini, crypto11, `open-component-model/.github` | `ignore: true` rules |
| `matchCategories: [golang]`, `enabled: true`, `postUpdateOptions: [gomodTidy]` | `manager: gomod`, `ignore: false` (tidy is always on) |
| indirect majors off | `depTypes: [indirect]`, `updateTypes: [major]`, `ignore: true` |
| `matchDepTypes: [golang]` off | `depTypes: [go]`, `ignore: true` (also the default) |
| gardenlinux fips `allowedVersions: '/^\\d+\\.\\d+$/'`, `versioning: loose` | `allow: '/^\d+\.\d+$/'`, `versioning: loose` |
| OCM cosign `versioning: 'regex:…'`, `minimumReleaseAge: '0 days'` | same values |
| `matchUpdateTypes: [pin, digest, …]`, `minimumReleaseAge: null` | built in |
| `customDatasources.envtest` with JSONata | `versions: .releases \| keys`, `versionRegex` |
| `customManagers` for `.env`, `netlify.toml`, workflows, `envtest.go` markers | one annotation-mode `custom[]` entry over those files |
| cosign `.env` regex with `currentDigest` of `cosign_checksums.txt` | regex `custom[]` with `checksum: {asset: cosign_checksums.txt}` |
| `hostRules` for registry.npmjs.org | built-in retries |

The example covers the rules needed for the parity items. A complete OCM config (parity item O4) also needs the remaining groups:

```yaml
rules:
  - names: ['sigs.k8s.io/controller-runtime', 'sigs.k8s.io/controller-tools', 'k8s.io/*', 'envtest']
    group: Kubernetes dependencies
    schedule: ['* * * * 0']
  - names: ['github.com/docker/{/,}**']
    group: Docker dependencies
    schedule: ['* * * * 0']
  - names: ['golang.org/x/{/,}**']
    group: Golang X Repositories
    schedule: ['* * * * 0']
  - names: ['google.golang.org/{/,}**']
    group: Google Golang Repositories
    schedule: ['* * * * 0']
  - names: ['github.com/spf13/{/,}**']
    group: spf13 repositories
    schedule: ['* * * * 0']
```

These are not translated: `security:openssf-scorecard`, `git-submodules` (OCM has no `.gitmodules`) and `lockFileMaintenance` (disabled in OCM anyway).
