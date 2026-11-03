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
| P7 | `allowedVersions` (regex form `/^\d+\.\d+$/`) | gardenlinux fips | `allow` is a semver constraint only; the docker scheme already drops `amd64-*` tags | open (regex form) |
| P8 | `versioning: 'regex:…'` per rule | OCM cosign `vX.Y.Z+ocm.N` / `-ocm.N` | fixed schemes (tag, go, docker, semver) | open |
| P9 | `versioning: 'loose'` per rule | gardenlinux fips | per-dep scheme chosen by manager | open |
| P10 | `minimumReleaseAge` (global 28d, per-datasource, `0 days` override, exempt digest/pin) | global, npm, cosign | release times exist for PR bodies (see R2) but are not used for filtering | open |
| P11 | `separateMultipleMajor` | global | one PR per dependency with the newest allowed version | open |
| P12 | `separateMinorPatch` | global (overridden per manager) | — | open |
| P13 | built-in monorepo groups (`group:monorepos`, `group:kubernetesMonorepo`, `group:goOpenapi`) | presets | curated built-in list, `monorepos: false` disables | done |
| P14 | `ignorePaths` defaults (`:ignoreModulesAndTests`) | preset | Renovate's default list when `ignorePaths` is absent | done |
| P15 | `updateNotScheduled: true` | global | out-of-window branches are never touched | open |
| P16 | `postUpdateOptions: [gomodTidy]` | golang category, Helm group | always `go mod tidy` | done |
| P17 | rule `minimumReleaseAge: null` for digest/pin/lockfile types | global | follows P10 | open |

## Managers and datasources

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| M1 | `# renovate: datasource=… depName=…` markers | 31 markers in `.env`, workflows, yaml, `envtest.go`, `netlify.toml` | accepted in annotation mode (plus `# deps:`); Renovate group names in regex mode | done |
| M2 | datasource `golang-version` | toolchain, netlify `GO_VERSION` | alias of `go-toolchain` | done |
| M3 | datasource `node-version` | netlify `NODE_VERSION` | — | open |
| M4 | `customDatasources` (`envtest`: YAML from URL + JSONata transform) | controller `.env`, `envtest.go` | — | open |
| M5 | datasource `github-release-attachments` (version + digest of an asset) | sigstore handler `.env` (`COSIGN_CHECKSUMS_SHA256`) | alias of `github-releases`; `checksum.asset` gives asset digests | open (needs P8 for `+ocm.N`, and an end-to-end test) |
| M6 | digest refresh for non-version tags (`latest@sha256:…`) | `.env` OPENSCAP_IMAGE, controller chart `tag: latest@sha256:…` | skipped (`non-version tag` in the docker manager, `non-version value "<v>"` for custom markers) instead of erroring; digest refresh not implemented | open |
| M7 | `with:` version inputs of setup actions (`actions/setup-node node-version`, `setup-go go-version`) | `ci.yml` `node-version: 26` | `with:` is never read | open |
| M8 | npm `engines` / `volta` | `engines.node ^26.0.0` | not extracted | open |
| M9 | `git-submodules` | enabled, but OCM has no `.gitmodules` | — | n/a |
| M10 | `packageManager` field | website `npm@12.2.0` | done | done |

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
| L8 | `:rebaseStalePrs` | preset | rebuild only on conflict or content change | open |
| L9 | `:dependencyDashboard` | preset | — | open |
| L10 | `prConcurrentLimit: 0` / `prHourlyLimit: 0` (unlimited) | global | `--max-new-prs 0` = unlimited | done |
| L11 | `gitAuthor` | ocmbot | `--author-name/--author-email` | done |
| L12 | `gitIgnoredAuthors` (list) | ocmbot | single `--author-email` compared | open |
| L13 | lockFileMaintenance | disabled | not implemented | n/a |

## Operations

| ID | Renovate feature | Used by OCM for | deps today | Status |
|---|---|---|---|---|
| O1 | runs on GitHub with an App token (`renovate.yml`, environment `automation`) | workflow | GitHub client and token input exist; the action is only consumable from Gitea | open (release binaries + GitHub-hosted action) |
| O2 | `hostRules` timeout/abortOnError for registry.npmjs.org | global | retries + rate-limit handling | done |
| O3 | repository cache | workflow | no cache by design (in-run dedupe) | n/a |
| O4 | config translation `renovate.json5` -> `.deps.yaml` for OCM | — | — | open (after P1–P3) |
