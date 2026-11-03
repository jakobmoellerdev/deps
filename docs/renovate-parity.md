# Renovate parity for open-component-model

Gap list between OCM's Renovate setup (`open-component-model/.github/renovate.json5`, the presets it extends, and the `# renovate:` markers in the repository) and `deps`. Work through it top to bottom; each item is self-contained. Status: `done`, `open`, `config` (expressible today with `.deps.yaml`, no code needed), `n/a` (not used by OCM or not applicable).

## Matching and policy

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| P1 | `matchUpdateTypes` driving `groupName`/`schedule`/`automerge` | Actions minor+patch group, npm minor+patch group, "Digest pins" group, automerge minor/patch | `updateTypes`: per-kind policy, evaluated in file order once a candidate's type is known (group, schedule, commit type, ignore/updates/allow per type); automerge follows L7 | done |
| P2 | `matchFileNames` | Kubernetes Controller chart group, website CLI-docs generator group | `paths` (doublestar globs on the file) | done |
| P3 | `matchDatasources` | gardenlinux rule, "Golang versions", npm release age | `datasources` (Renovate names `golang-version`, `github-tags`, `github-release-attachments` normalized) | done |
| P4 | `**` globs in package names (`golang.org/x/{/,}**`) | 6 groups | — | done |
| P5 | `matchDepTypes` | indirect majors off, `golang` directive off | `depTypes` | done |
| P6 | `enabled: false` per package | kube-openapi, go-ini/ini, ThalesIgnite/crypto11, `open-component-model/.github` | `ignore: true` | config |
| P7 | `allowedVersions` (regex form `/^\d+\.\d+$/`) | gardenlinux fips | `allow` takes a semver constraint or the regex form `/<RE2>/` (`!/<RE2>/` negates, flag `i`), matched against the version as written; rejections read `outside allow /…/` | done |
| P8 | `versioning: 'regex:…'` per rule | OCM cosign `vX.Y.Z+ocm.N` / `-ocm.N` | `rules[].versioning: 'regex:<RE2>'` with Renovate's groups (major required, minor, patch, build, revision, prerelease, compatibility); Renovate's `(?<name>)` syntax works as is (Go RE2); compiled at load, errors as `rules[i].versioning: …`; also for `custom[].versioning` | done |
| P9 | `versioning: 'loose'` per rule | gardenlinux fips | `rules[].versioning` (any scheme, pre-policy only) and the `loose` scheme (1–6 numeric parts, `-`/`_` suffix = prerelease); markers accept `versioning=loose` | done |
| P10 | `minimumReleaseAge` (global 28d, per-datasource, `0 days` override, exempt digest/pin) | global, npm, cosign | `minimumReleaseAge` top-level and per rule (`28 days`, `3d`, `12h`, Go durations; `0`/`0 days` disables), per update type via `updateTypes`; too-new candidates fall back to the next older one; unknown release times are allowed | done |
| P11 | `separateMultipleMajor` | global | `separateMajorMinor` (default true), `separateMultipleMajor` top level and per rule: one update per PR bucket, branch suffix `-major` / `-v<N>` | done |
| P12 | `separateMinorPatch` | global (overridden per manager) | top level and per rule (`-minor` / `-patch` branches); `plan`/`apply` write one update per dep and mark the others `separate PR` | done |
| P13 | built-in monorepo groups (`group:monorepos`, `group:kubernetesMonorepo`, `group:goOpenapi`) | presets | curated built-in list, `monorepos: false` disables | done |
| P14 | `ignorePaths` defaults (`:ignoreModulesAndTests`) | preset | Renovate's default list when `ignorePaths` is absent | done |
| P15 | `updateNotScheduled: true` | global | `updateNotScheduled` (default true): open PRs outside their schedule are still updated and rebased, only new PRs wait; `false` skips them | done |
| P16 | `postUpdateOptions: [gomodTidy]` | golang category, Helm group | always `go mod tidy` | done |
| P17 | rule `minimumReleaseAge: null` for digest/pin/lockfile types | global | built in: digest refreshes, pins and security updates are always exempt; lockfile regeneration has no version | done |
| P18 | `rangeStrategy: bump` | "Golang versions" rule (`matchDatasources: [golang-version]`) | not configurable: npm ranges are always bumped in place (`^1.2.3` → `^1.4.0`, Renovate's `bump`); go.mod `go`/`toolchain` directives, setup inputs and markers replace the exact version at its precision, which is what `bump` does for `golang-version` | done |
| P19 | `matchCategories` | `[golang]` rule (enable + gomodTidy + schedule) | not supported; `manager: gomod` (plus `datasources: [golang-version]` where toolchain markers should match) expresses OCM's rule; tidy is always on | config |

## Managers and datasources

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| M1 | `# renovate: datasource=… depName=…` markers | 31 markers in `.env`, workflows, yaml, `envtest.go`, `netlify.toml` | accepted in annotation mode (plus `# deps:`); Renovate group names in regex mode | done |
| M2 | datasource `golang-version` | toolchain, netlify `GO_VERSION` | alias of `go-toolchain` | done |
| M3 | datasource `node-version` | netlify `NODE_VERSION` | `node-version` (alias `node`): nodejs.org `dist/index.json` once per run (`DEPS_NODE_DIST_URL` for mirrors), release dates as release times, derived `X`/`X.Y` for partial values; `v` style of the file kept | done |
| M4 | `customDatasources` (`envtest`: YAML from URL + JSONata transform) | controller `.env`, `envtest.go` | `customDatasources.<name>` with `url`, `format` (json/yaml/plain), `versions` (jq-subset selector, e.g. `.releases \| keys`) and `versionRegex` instead of JSONata; deps use `custom.<name>`; fetched once per run | done |
| M5 | datasource `github-release-attachments` (version + digest of an asset) | sigstore handler `.env` (`COSIGN_CHECKSUMS_SHA256`) | alias of `github-releases`; `checksum.asset` gives asset digests; P8's regex versioning orders `+ocm.N`; `docs/examples-ocm.deps.yaml` has the cosign entry (a manual run moved `v3.1.4+ocm.5` to `+ocm.6` with the release's real checksum); `cmd/deps` test `TestApplyCosignVersionAndChecksum` covers plan + apply end to end | done |
| M6 | digest refresh for non-version tags (`latest@sha256:…`) | `.env` OPENSCAP_IMAGE, controller chart `tag: latest@sha256:…` | docker-datasource deps with a non-version tag (docker manager, custom markers with a digest) get digest-only resolution: pinned → digest refresh, unpinned + `pin: digest` → pin, else skipped as `non-version tag`; other non-version values are skipped as `non-version value "<v>"` | done |
| M7 | `with:` version inputs of setup actions (`actions/setup-node node-version`, `setup-go go-version`) | `ci.yml` `node-version: 26` | DepType `setup` for setup-node, setup-go, setup-helm, setup-kubectl, setup-terraform, golangci-lint-action; full versions semver, partial ones tag versioning (26 → 27) | done |
| M8 | npm `engines` / `volta` | `engines.node ^26.0.0` | DepTypes `engines` (ignored by default, like Renovate) and `volta`; node via `node-version`, npm/pnpm/yarn via `npm`; `^26` partials bumped with tag versioning | done |
| M9 | `git-submodules` | enabled, but OCM has no `.gitmodules` | — | n/a |
| M10 | `packageManager` field | website `npm@12.2.0` | DepType `packageManager` (`npm@x`, `pnpm@x`, `yarn@x`; hash suffixes skipped); its version also selects the `npx` fallback for lockfile regeneration | done |

## PR lifecycle

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| L1 | `branchPrefix: renovate/` | global | `branchPrefix` | done |
| L2 | semantic commit types (`:semanticPrefixFixDepsChoreOthers`) | preset | `fix(deps)` for production deps, `chore(deps)` otherwise; `commitType`/`commitScope` per rule | done |
| L3 | release notes in PR body | preset | GitHub/Gitea release bodies, compare link | done |
| L4 | age column in PR body (Mend "Age" badge) | preset | release time as text | done |
| L5 | Mend adoption / passing / confidence badges | preset | needs Mend's service | open (decide: external badge URLs or drop) |
| L6 | OpenSSF scorecard badge (`security:openssf-scorecard`) | preset | — | open |
| L7 | `automerge` / `automergeType: pr` | minor+patch | — | open |
| L8 | `:rebaseStalePrs` | preset | `rebaseWhen: behind-base-branch` (also `conflicted`, the default, and `never`); unchanged PRs are rebuilt on the moved base and force-pushed (`rebased`) | done |
| L9 | `:dependencyDashboard` | preset | — | open |
| L10 | `prConcurrentLimit: 0` / `prHourlyLimit: 0` (unlimited) | global | `prConcurrentLimit`/`prHourlyLimit` (0 = unlimited, the deps default; Renovate defaults to 10/2); `--max-new-prs` stays a per-run cap; the smallest non-zero limit wins | done |
| L10a | `prConcurrentLimit` | global | open managed PRs + PRs created this run must stay below it | done |
| L10b | `prHourlyLimit` | global | managed PRs (open and closed) created in the last hour, by `created_at`, count against it | done |
| L11 | `gitAuthor` | ocmbot | `gitAuthor: 'Name <email>'`; precedence `--author-name`/`--author-email` > `$DEPS_GIT_AUTHOR` > `gitAuthor` > `deps[bot] <deps-bot@noreply.invalid>` | done |
| L12 | `gitIgnoredAuthors` (list) | ocmbot | `gitIgnoredAuthors`: branch tips by these emails (case-insensitive) count as the bot's own and are updated/rebased | done |
| L13 | lockFileMaintenance | disabled | not implemented | n/a |

## Operations

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| O1 | runs on GitHub with an App token (`renovate.yml`, environment `automation`) | workflow | GitHub client and token input exist; the action is only consumable from Gitea | open (release binaries + GitHub-hosted action) |
| O2 | `hostRules` timeout/abortOnError for registry.npmjs.org | global | retries + rate-limit handling | done |
| O3 | repository cache | workflow | no cache by design (in-run dedupe) | n/a |
| O4 | config translation `renovate.json5` -> `.deps.yaml` for OCM | — | `docs/examples-ocm.deps.yaml` covers the parity rules; `docs/migrating-from-renovate.md` lists the remaining groups (Kubernetes, Docker, golang.org/x, google.golang.org, spf13) | open (merge into one final config) |
