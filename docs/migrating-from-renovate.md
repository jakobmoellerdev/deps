# Migrating from Renovate

deps reads `.deps.yaml`, not `renovate.json(5)`. Most options translate one to one. Some behaviors that Renovate gets from presets are built in. This page lists the mapping, the known differences, and a worked translation of open-component-model's config. Feature status is tracked in [renovate-parity.md](renovate-parity.md).

## Option mapping

| Renovate | deps |
|---|---|
| `extends` (presets) | not supported; see [built-ins](#presets) |
| `enabled: false` (package rule) | `rules[].ignore: true` |
| `enabled: true` for disabled-by-default deps | `rules[].ignore: false` (indirect, `go` directive, peerDependencies, engines) |
| `matchManagers` | `rules[].manager` (one per rule): `github-actions`, `gomod`, `docker` (covers `dockerfile`, `docker-compose`, `kubernetes`, `helm-values`), `npm`, `helm`, `cargo`, `pip_requirements`, `pep621`, `poetry`, `pipenv`, `maven`, `maven-wrapper`, `gradle`, `gradle-wrapper`, `custom` (covers `custom.regex`) |
| `matchPackageNames`, `matchDepNames` | `rules[].names` (doublestar globs; `{/,}**` works as in Renovate) |
| `matchDepTypes` | `rules[].depTypes` (deps names, see [managers](reference/managers.md); Renovate's `golang` = `go`) |
| `matchFileNames` | `rules[].paths` |
| `matchDatasources` | `rules[].datasources` (`golang-version`, `github-tags`, `github-release-attachments`, `node` accepted) |
| `matchUpdateTypes` | `rules[].updateTypes`; `pinDigest` = `pin` |
| `matchCategories` | not supported; use `manager`/`datasources` (`golang` ≈ `manager: gomod` plus `datasources: [golang-version]`) |
| `allowedVersions` | `rules[].allow` (semver constraint or `/regex/`, `!/regex/`) |
| `versioning` | `rules[].versioning` (`semver`, `loose`, `regex:…`, plus deps' `tag`, `go`, `docker`) |
| `extractVersion` | `rules[].extractVersion`, marker `extractVersion=` (RE2 with a `version` group, same syntax) |
| `groupName` | `rules[].group` (`groupSlug` is derived: `slug(group)`) |
| `schedule` | `schedule` / `rules[].schedule`, as crons of the workflow's `on.schedule` triggers (see below) |
| `timezone` | not supported: workflow crons are UTC |
| `updateNotScheduled` | `updateNotScheduled` |
| `minimumReleaseAge` | `minimumReleaseAge` / `rules[].minimumReleaseAge` (`null` → `0 days`) |
| `minimumReleaseAgeBehaviour` | `minimumReleaseAgeBehaviour` / `rules[].minimumReleaseAgeBehaviour` (`timestamp-required`, `timestamp-optional`); the default differs, see below |
| `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch` | same names, top level and per rule |
| `pinDigests`, `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` (top level or per rule) |
| `rangeStrategy` | `rangeStrategy` / `rules[].rangeStrategy` for npm, cargo and Python (PEP 440, Poetry) ranges: `auto` (default, = `update-lockfile`), `bump`, `update-lockfile`; `widen`, `pin`, `replace` and `in-range-only` are not supported. Other managers replace the exact version |
| `postUpdateOptions: [gomodTidy]` | always on |
| `postUpdateOptions: [gomodUpdateImportPaths]` | `gomodUpdateImportPaths`, **default true**: major module path updates (`/v92` → `/v93`) rewrite the imports in the module's `.go` files; `false` turns module path updates off |
| `lockFileMaintenance` | `lockFileMaintenance: {enabled, schedule}` for `Cargo.lock` (`cargo update`, one PR on `<branchPrefix>lock-file-maintenance`); npm and Go lockfiles are regenerated only with an update |
| `branchPrefix` | `branchPrefix` |
| `semanticCommits`, `:semanticPrefixFixDepsChoreOthers` | always on; `rules[].commitType`, `rules[].commitScope` |
| `gitAuthor` | `gitAuthor` (or `DEPS_GIT_AUTHOR`, `--author-name/--author-email`); ignored for platform commits, as in Renovate |
| `gitIgnoredAuthors` | `gitIgnoredAuthors` |
| `platformCommit` (`RENOVATE_PLATFORM_COMMIT`) | `platformCommit` (`DEPS_PLATFORM_COMMIT`): same values and default (`auto`). GitHub only; `auto` also needs no signing key, and `enabled` with a signing key or on Gitea/Forgejo is a usage error |
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

- `packageName` wins over `depName`, `versioning=` takes a scheme name, `extractVersion=` maps release names (`extractVersion=^jq-(?<version>.+)$`), and `registryUrl` and unknown keys are ignored.
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
- **`rangeStrategy`.** deps supports `auto`, `bump` and `update-lockfile` for npm ranges. `auto` is `update-lockfile`, like Renovate's npm manager for simple ranges: a version the range admits only moves `package-lock.json`, a version outside it replaces the range's version (`^5.2.8` → `^6.0.0`, Renovate's `replace`), and versions are compared with the locked one. Renovate widens peerDependencies under `auto`; deps leaves their in-range versions alone and replaces otherwise. Lockfile-only updates need `package-lock.json` (v2/v3): with `pnpm-lock.yaml`, `yarn.lock` or a v1 lock, deps bumps instead (Renovate reads and updates those too). deps never widens (`^1 || ^2`) or pins ranges, and it skips unsupported range forms (`||`, `x`, hyphen ranges) as `unsupported range`. OCM's `rangeStrategy: bump` for `golang-version` needs no translation: go.mod `toolchain` and `go` directives are replaced at their precision.
- **Python specifiers.** `rangeStrategy` applies to every PEP 440 and Poetry range (`>=1.2,<2`, `<2`, `~=1.4`, `^1.2`, `>=1.2 <2`, `^1 || ^2`), with the versions `uv.lock`, `poetry.lock`, `pdm.lock` and `Pipfile.lock` lock. deps' `auto` is `update-lockfile` for all Python managers; Renovate's is that for poetry only and `replace` for pep621, pipenv and pip_requirements, so deps also moves those lockfiles for versions the range admits. Ranges are rewritten with Renovate's pep440 rules (`>=1.2,<2` → `>=3.1,<4`); under `bump`, a version outside the range gets the `replace` rewrite too, and a Poetry `||` constraint gets a new alternative instead of Renovate's npm-style rewrite. Poetry and pdm lockfile-only updates move to the newest admitted version (`poetry update --lock <name>`), which can be newer than proposed.
- **`matchCategories`** is not supported. Write the managers or datasources explicitly.
- **Presets are not supported.** The built-ins below cover the common ones.
- **Schedules** are not time windows. A `schedule` entry is a cron trigger of the workflow that runs deps, and a run creates PRs for a group only when it was started by one of the group's crons (manual and push runs act on everything). They gate only creating PRs (and updating them with `updateNotScheduled: false`). Translate a Renovate window into a workflow cron inside it plus the same string in `schedule`: OCM's `* * * * 0` (Sunday) becomes `0 0 * * 0` in both `on.schedule` and the rules, next to a daily `0 0 * * *` cron for unscheduled deps. Natural-language schedules (`before 6am`) and `timezone` have no equivalent; crons are UTC.
- **Release age.** An unknown release time does not block an update by default (`minimumReleaseAgeBehaviour: timestamp-optional`); Renovate 42+ defaults to `timestamp-required`, which holds such releases back. Set `minimumReleaseAgeBehaviour: timestamp-required` to match it. In deps, custom datasources have no release times, and an image has one only when its config carries `created` (or the registry lists times). Digest refreshes, pins and security updates are always exempt, so Renovate's `minimumReleaseAge: null` rule for those update types needs no translation. When the newest candidate is too new, deps proposes the newest one that is old enough; Renovate waits.
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

[`examples-ocm.deps.yaml`](examples-ocm.deps.yaml) is a complete translation of OCM's `.github/renovate.json5`, meant to live at `.github/deps.yaml` next to it. Its `rules` keep the order of `packageRules` and are numbered by their index, and every block names the Renovate setting it replaces. Validate it against a checkout:

```sh
cd ~/Projects/open-component-model
DEPS_GITHUB_COM_TOKEN=$(gh auth token) deps plan -v --config ~/Projects/deps/docs/examples-ocm.deps.yaml > /tmp/ocm.diff
git apply --check /tmp/ocm.diff
```

On 2026-11-04 (OCM `b4b5473`) this exited 0 with `870 dependencies, 36 updates in 14 PRs, 221 held back by minimumReleaseAge, 26 skipped, 0 errors`, and the diff applied. Against the same checkout, the previous deps (without the floating-tag release times and `minimumReleaseAgeBehaviour: timestamp-required`) had 41 updates in 18 PRs and 217 held back; the difference is exactly markdownlint-cli2-action v25, Garden Linux 1877.28 (two files) and envtest (rows below).

How the pieces map:

| renovate.json5 | examples-ocm.deps.yaml |
|---|---|
| `extends: config:recommended, config:best-practices` | the defaults: `security: true`, `separateMajorMinor: true`, the default `ignorePaths`, semantic commit types |
| `extends: docker:pinDigests, helpers:pinGitHubActionDigests` | `pin: digest` |
| `extends: :rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `extends: group:kubernetesMonorepo, group:goOpenapi` | `monorepos: true` (the default) |
| `extends: :dependencyDashboard` | `dependencyDashboard: true`, plus `dependencyDashboardTitle: Dependency Dashboard (deps trial)` while Renovate's issue #331 is open |
| `branchPrefix: renovate/` | `branchPrefix: deps/` for the trial (see [cutover](#cutover-for-ocm)) |
| `gitAuthor`, `gitIgnoredAuthors` (ocmbot), `prConcurrentLimit: 0`, `prHourlyLimit: 0`, `separateMinorPatch`, `separateMultipleMajor`, `updateNotScheduled`, `minimumReleaseAge: 28 days`, `automerge: false`, `automergeType: pr` | same keys |
| Renovate's default `minimumReleaseAgeBehaviour: timestamp-required` (Renovate 42+) | `minimumReleaseAgeBehaviour: timestamp-required`, set explicitly since deps defaults to `timestamp-optional` |
| `schedule: ['* * * * 0']` (14 rules) | `schedule: ['0 0 * * 0']`, plus the crons `0 0 * * *` and `0 0 * * 0` in the [workflow](#workflow-for-ocm) |
| 0: `matchFileNames: [chart/**]`, group "Kubernetes Controller chart" | `paths` + `group` |
| 1: Garden Linux FIPS `allowedVersions: '/^\\d+\\.\\d+$/'`, `versioning: loose` | `allow: '/^\d+\.\d+$/'`, `versioning: loose` |
| 2: `matchUpdateTypes: [minor, patch]`, `automerge: true` | `updateTypes: [minor, patch]`, `automerge: true` |
| 3, 5: `matchManagers` + `separateMinorPatch: false` + schedule | `manager` rules |
| 4, 6: `matchManagers` + `matchUpdateTypes: [minor, patch]` + `groupName` | `updateTypes: [minor, patch]` + `group` |
| 7: "Digest pins" over `github-actions`, `dockerfile`, `docker-compose` with `[digest, pinDigest]` | two rules: `github-actions`, and `docker` with `depTypes: [dockerfile, compose, workflow, action]` (Renovate's github-actions manager also covers workflow `container:`/`services:` images), both `updateTypes: [digest, pin]` |
| 8: `matchDatasources: [golang-version]`, `rangeStrategy: bump` | `datasources: [golang-version]` (no `rangeStrategy` needed) |
| 9, 20, 21, 22: `enabled: false` for kube-openapi, go-ini/ini, crypto11, `open-component-model/.github` | `ignore: true` rules |
| 10: "Kubernetes dependencies" over `gomod` and `custom.regex` | two rules (`manager: gomod`, `manager: custom`) with the same `names` and `group` |
| 11, 15, 16, 17: "Docker dependencies", "Golang X Repositories", "Google Golang Repositories", "spf13 repositories" | `names` + `group` + `schedule` |
| 12: "Garden Linux images", 13: "OCM cosign" (`versioning: 'regex:…'`, `minimumReleaseAge: '0 days'`), 14: "Helm" | same values |
| 18: `matchCategories: [golang]`, `enabled: true`, `postUpdateOptions: [gomodTidy]`, schedule | `manager: gomod`, `ignore: false`, `schedule` (tidy is always on) |
| 19: indirect majors off | `depTypes: [indirect]`, `updateTypes: [major]`, `ignore: true` |
| 23: "Website CLI-docs generator dependencies" | `paths` + `group` + `separateMinorPatch: false` |
| 24: `matchDepTypes: [golang]` off | `depTypes: [go]`, `ignore: true` |
| 25: npm `minimumReleaseAge: 28 days` | `datasources: [npm]` rule |
| 26: `matchUpdateTypes: [pin, digest, …]`, `minimumReleaseAge: null` | built in |
| `customDatasources.envtest` with JSONata | `versions: .releases \| keys`, `versionRegex` |
| `customManagers` for `.env`, `netlify.toml`, workflow `version:`, `envtest.go` and `.ya?ml` markers, `.env` `*_IMAGE` tag@digest | one annotation-mode `custom[]` entry over those files |
| cosign `.env` regex with `currentDigest` of `cosign_checksums.txt` | regex `custom[]` with `checksum: {asset: cosign_checksums.txt}` |
| `hostRules` for registry.npmjs.org | built-in retries |
| `lockFileMaintenance: {enabled: false}` | the deps default (`lockFileMaintenance.enabled: false`) |
| `git-submodules: {enabled: true}` | not supported; OCM has no `.gitmodules` |
| `extends: security:openssf-scorecard` | not supported |

Two details carry over unchanged:

- **Rule 18 undoes rule 9.** `enabled: true` on the whole golang category comes after the `k8s.io/kube-openapi` rule, so Renovate updates kube-openapi anyway: its dashboard lists it in "kubernetes dependencies (minor)". The translation keeps the order, so deps updates it too: its pseudo-version gets digest updates, in the Kubernetes group.
- **Rule 18 enables indirect requirements.** Renovate disables them by default, and `enabled: true` turns them on again; rule 19 then keeps only their majors off. `ignore: false` does the same in deps.

**Helm.** OCM has charts (`bindings/go/kubernetes/controller/chart` and two conformance charts), but none of them has Chart.yaml `dependencies`. The helm manager has nothing to update there and needs no config. The controller image in `chart/values.yaml` (`tag: latest@sha256:…`) is a docker dep (DepType `helm`) and gets digest refreshes.

### Workflow for OCM

This workflow replaces `.github/workflows/renovate.yml`. It uses the same App (ocmbot, secrets `OCMBOT_APP_ID`/`OCMBOT_PRIV_KEY`) and the same `automation` environment:

```yaml
# .github/workflows/deps.yml
name: deps
on:
  schedule:
    - cron: "0 0 * * *" # daily (renovate.yml's cron): updates without a schedule
    - cron: "0 0 * * 0" # weekly: rules with schedule ['0 0 * * 0'] (Renovate's '* * * * 0')
  push:
    branches: [main] # rebase and refresh open PRs; opens only unscheduled ones
  workflow_dispatch: # in schedule for every group (renovate.yml's ignoreSchedule)
  pull_request:
    branches: [main]
    paths: [.github/deps.yaml, .github/workflows/deps.yml]
concurrency:
  group: deps-${{ github.event_name == 'pull_request' && github.ref || 'main' }}
  cancel-in-progress: false
permissions:
  contents: read
jobs:
  deps:
    runs-on: ubuntu-26.04-arm
    environment:
      name: automation
      deployment: false
    steps:
      - if: github.event_name != 'pull_request' && github.repository_owner == 'open-component-model'
        id: app
        uses: actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1 # v3.2.0
        with:
          client-id: ${{ secrets.OCMBOT_APP_ID }}
          private-key: ${{ secrets.OCMBOT_PRIV_KEY }}
          repositories: ${{ github.event.repository.name }}
          permission-contents: write # push deps/* branches, delete obsolete ones
          permission-pull-requests: write # open, update and close PRs
          permission-issues: write # the dependency dashboard
          permission-workflows: write # Action updates change .github/workflows/*
          permission-checks: read # the automerge fallback reads check runs
          permission-statuses: read # and commit statuses
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          token: ${{ steps.app.outputs.token || github.token }} # deps pushes with these credentials
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7
        with:
          go-version-file: bindings/go/go.mod # go mod tidy; GOTOOLCHAIN=auto covers other modules
          cache: false
      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7
        with:
          node-version: 26 # package-lock.json regeneration (npm via npx at packageManager's version)
      - uses: jakobmoellerdev/deps@v0.3.0
        with:
          token: ${{ steps.app.outputs.token || github.token }}
          version: v0.3.0 # keep this when pin: digest rewrites the ref above to a SHA
          args: >-
            ${{ github.event_name == 'pull_request' && 'plan' || 'run' }}
            -v --log-level info --config .github/deps.yaml
```

- **Pull requests** that change the config run `plan` with the job token. That validates the config: an invalid key exits 2. `run --dry-run` needs `HEAD` at the tip of `main`, which a PR merge commit is not. To also list the PRs a config change would cause, use the base/PR checkout pair from [ci.md](ci.md#preview-on-pull-requests).
- **Token.** The App token goes to both `actions/checkout` and the action. The action creates the commits and moves the `deps/*` branches through the API with it; git uses the checkout's credentials only to list and delete branches. `permission-workflows: write` is required, because Action updates change `.github/workflows/*`: GitHub checks it for API commits too (without it, creating the tree fails with `403 Resource not accessible by integration`).
- **Version.** The action downloads the release named by its ref when that ref is a tag. `pin: digest` turns `@v0.3.0` into `@<sha> # v0.3.0` in the first "Digest pins" PR, and from then on the ref no longer names the release. Without `version:`, the action would download the latest release.
- **Signing.** Renovate runs with `RENOVATE_PLATFORM_COMMIT: enabled`, and so does deps: `platformCommit: enabled` in the example config (`auto` would choose it too, because the token is an App installation token). deps then creates the commits through the GitHub API, and GitHub signs them: they show as verified, authored by `ocmbot[bot]` (`<id>+ocmbot[bot]@users.noreply.github.com`), committed by GitHub. `gitAuthor` only applies to git commits. Don't pass `signing-key`: it conflicts with `platformCommit: enabled`. Verified commits satisfy the `required_signatures` rule of both rulesets that cover `deps/*` ("OCM Organization Ruleset" and the repository's "All branches"). The rulesets also restrict branch creation, deletion and non-fast-forward updates, so the branches still need ocmbot's bypass (App 295948 is an `always` bypass actor of both) or an exclusion, see the [cutover](#cutover-for-ocm).
- **Not needed.** Renovate's repository cache steps (deps has no cache) and `RENOVATE_ALLOWED_COMMANDS` (`renovate.json5` has no `postUpgradeTasks`).

### Comparison with Renovate

The run below went against a fresh clone of OCM `b4b5473`, with a GitHub token. A dry run makes no writes.

```sh
DEPS_GITHUB_COM_TOKEN=$(gh auth token) DEPS_TOKEN=$(gh auth token) \
  deps run --dry-run -v --config ~/Projects/deps/docs/examples-ocm.deps.yaml --trigger-schedule '0 0 * * 0'
```

The result was `19 would-create, 200 held back by minimumReleaseAge, 91 skipped, 0 errors`. With `--trigger-schedule '0 0 * * *'` (the daily run), 7 PRs would be created: node minor and patch, js-yaml, undici, two golang.org/x/net security PRs, and Garden Linux. The other 12 were `skipped: outside schedule`. Since P18 (locked versions, lockfile-only updates), the js-yaml and undici security PRs are gone from both runs, and the npm group has the same 2 deps as Renovate's (rows below). Since P10 (floating-tag release times, `minimumReleaseAgeBehaviour: timestamp-required` in the example), markdownlint-cli2-action, Garden Linux and envtest are held back as in Renovate (rechecked with `deps plan` on `b4b5473`; `run --dry-run` now refuses that checkout, which is no longer the tip of main).

Renovate had no open PRs at that time. Its dashboard (#331, updated 2026-11-04 13:43 UTC) listed 8 updates "Awaiting Schedule" and 51 "Pending Status Checks", the latter held back by `minimumReleaseAge`.

| Update | Renovate (dashboard #331) | deps (weekly dry run) | Why they differ |
|---|---|---|---|
| GitHub Actions: mikepenz/action-junit-report v6.6.0, sourcemeta/jsonschema v17.2.0 | awaiting schedule, `renovate/github-actions` | `deps/group/github-actions` | match |
| Website CLI-docs generator (the same 9 modules) | awaiting schedule | `deps/group/website-cli-docs-generator-dependencies` | match |
| aws-sdk-go-v2 monorepo patch (the same 7 modules) | awaiting schedule | `deps/group/aws-sdk-go-v2-monorepo-patch` | match |
| github.com/aws/smithy-go v1.28.4, go-git/v6 v6.0.0-beta.1, fxamacker/cbor/v2 v2.9.6 | awaiting schedule | one PR each | match |
| npm dependencies | awaiting schedule: js-yaml, typescript-eslint | the same two: js-yaml 5.4.2 → 5.4.3 (`website`, exact; `.github/scripts`, lockfile-only) and typescript-eslint 8.71.0 → 8.71.1 | match (rechecked after P18). The 7 deps deps used to add (@fontsource-variable/inter, quicksand, @tabler/icons, @thulite/doks-core, @thulite/seo, mermaid, thulite) are already locked at the new version in `website/package-lock.json`; under the default `rangeStrategy: auto` (`update-lockfile`) deps compares with the locked version, as Renovate does, so they are up to date |
| js-yaml 5.0.0 → 5.4.3, undici 8.3.0 → 8.12.2 in `.github/scripts` | — | — | match (rechecked after P18). OSV is queried with the locked versions (js-yaml 5.4.2, undici 8.12.2), which have no advisories, so the 2 security PRs deps used to propose are gone |
| golang.org/x/net v0.62.0 (GO-2026-6860, GO-2026-6866) | pending in "golang x repositories" | 2 security PRs (`bindings/go`, website generator) | deps-only. Security updates skip schedule, group and release age; Renovate's `osvVulnerabilityAlerts` is off by default |
| DavidAnson/markdownlint-cli2-action v24 → v25 | pending | held back: `too new (released 2026-10-25, minimum 28 days)` | match (rechecked after P10). `v25` has no GitHub release of its own; deps dates it by the v25.0.0 release, which points to the same commit |
| ghcr.io/gardenlinux/gardenlinux/fips 1877.28 | pending | held back: `release time unknown (minimum 28 days, timestamp-required)`, in both files | match (rechecked after P10). The image config has no `created` time; the example sets Renovate's default `minimumReleaseAgeBehaviour: timestamp-required` (deps' default is `timestamp-optional`, which would propose it) |
| envtest 1.37.0 and 1.36.2 | pending in the Kubernetes groups (minor, patch), with k8s.io/* v0.37.2 | held back: `release time unknown (minimum 28 days, timestamp-required)` | match (rechecked after P10). The envtest custom datasource has no release times. The k8s.io modules are held back in both |
| node: Renovate 26.13.0; deps 26.11.1 (minor) and 26.5.1 (patch) | pending | 2 PRs | deps-only. 26.13.0 is too new in both. deps falls back to the newest release older than 28 days; Renovate waits for the newest |
| github.com/Microsoft/go-winio: Renovate v0.6.4, deps v0.6.3 | pending | would create | deps-only, the same fallback |
| pin ghcr.io/open-component-model/kubernetes/controller (`chart/test-values.yaml`, `tag: latest`) | — | `deps/group/kubernetes-controller-chart` | deps-only. Renovate's helm-values manager reads only `values.yaml`; deps reads any YAML with `image:`/`repository:` |
| `pin registry docker tag to 325b4b2` | awaiting schedule ("Digest pins") | — | Renovate-only. The dashboard doesn't show the file. The only `registry` docker dep is the controller `.env` `REGISTRY_IMAGE_VERSION=3.1.1`, a marker without a digest, which deps doesn't pin [inference] |
| Go pseudo-version digests: azure/go-ansiterm, google/pprof, ianlancetaylor/demangle, lufia/plan9stats, cyberphone/json-canonicalization, digitorus/pkcs7, google.golang.org/genproto (and k8s.io/utils, golang.org/x/exp, k8s.io/kube-openapi inside groups) | pending | held back by `minimumReleaseAge` (the `@latest` commits are 0–24 days old) | match (rechecked after M14; with `minimumReleaseAge: 0 days` deps proposes all of them as `digest` updates) |
| github.com/google/go-github/v92 → v93 | pending | held back by `minimumReleaseAge` (v93.0.0 is from 2026-10-15) | match (rechecked after M15). deps' PR also rewrites the import in `bindings/go/github/internal/download/client.go` (`gomodUpdateImportPaths`, which OCM doesn't enable in Renovate); the package builds with v93 |
| 36 others (helm v4.3.1, k8s.io/* v0.37.2, controller-tools v0.23.0, docker/cli, ginkgo, prometheus, oras, registry 3.1.2, npm 12.2.1, sigstore/scaffolding, markdownlint-cli2, open-component-model/ocm, dawidd6/action-download-artifact v28, …) | pending | held back by `minimumReleaseAge` | match |

### Cutover for OCM

1. Add `.github/deps.yaml` (this example) and `.github/workflows/deps.yml` (above) next to Renovate. Their PRs don't collide: deps' branches are `deps/*`, its dashboard has its own title, and deps closes obsolete PRs only under its own prefix.
2. Grant the ocmbot installation *Checks* and *Commit statuses* read, if it doesn't have them yet. renovate.yml requests `checks: write` and `statuses: write`, so the App likely has them. Pushes to `deps/*` rely on ocmbot's ruleset bypass. The repository ruleset "All branches" excludes only `refs/heads/renovate/**` and `refs/heads/dependabot/**`, so either add `refs/heads/deps/**` there or keep the bypass.
3. Compare a weekly run with Renovate's, as above. Then disable `renovate.yml`, set `branchPrefix: renovate/`, drop `dependencyDashboardTitle`, and close #331. deps closes Renovate's leftover `renovate/*` PRs after its first error-free run.

### What is still missing for OCM

Each item comes from the comparison above or from [renovate-parity.md](renovate-parity.md):

1. **Fallback versus waiting.** deps proposes the newest release that is old enough (node 26.11.1, go-winio v0.6.3); Renovate waits for the newest (26.13.0, v0.6.4). This is a design difference, documented under P10, not a bug. It is listed because it changes which PRs OCM sees.
3. **Mend badges and the OpenSSF scorecard.** PR bodies show the release age as text, with no adoption, passing or confidence badges (L5) and no scorecard badge (`security:openssf-scorecard`, L6).
4. **Renovate dashboard extras:** the "Abandoned Dependencies" section and the repository problems list (e.g. hidden Unicode warnings) have no deps equivalent.
5. **Cutover steps not done:**
    - a real (non-dry) trial run in OCM;
    - App permissions confirmed for the installation (checks/statuses read, workflows write);
    - the ruleset entry or bypass for `deps/**`;
    - the branch takeover (`branchPrefix: renovate/`) after Renovate is disabled.
