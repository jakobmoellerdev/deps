# Worked example: open-component-model


[`examples-ocm.deps.yaml`](examples-ocm.deps.yaml) is a complete translation of OCM's `.github/renovate.json5`, meant to live at `.github/deps.yaml` next to it. Its `rules` keep the order of `packageRules` and are numbered by their index, and every block names the Renovate setting it replaces. Validate it against a checkout:

```sh
cd ~/Projects/open-component-model
DEPS_GITHUB_COM_TOKEN=$(gh auth token) deps plan --show-skipped --config ~/Projects/deps/docs/examples-ocm.deps.yaml > /tmp/ocm.diff
git apply --check /tmp/ocm.diff
```

On 2026-11-04 (OCM `b4b5473`) this exited 0 with `870 dependencies, 36 updates in 14 PRs, 221 held back by minimumReleaseAge, 26 skipped, 0 errors`, and the diff applied. Against the same checkout, the previous deps (without the floating-tag release times and `minimumReleaseAgeBehaviour: timestamp-required`) had 41 updates in 18 PRs and 217 held back; the difference is exactly markdownlint-cli2-action v25, Garden Linux 1877.28 (two files) and envtest (rows below).

How the pieces map:

| renovate.json5 | examples-ocm.deps.yaml |
|---|---|
| `extends: config:recommended, config:best-practices` | the defaults: `builtins.security: true`, `separate: major`, the default `ignorePaths`, semantic commit types |
| `extends: docker:pinDigests, helpers:pinGitHubActionDigests` | `pin: digest` |
| `extends: :rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `extends: group:kubernetesMonorepo, group:goOpenapi` | `builtins.monorepos: true` (the default) |
| `extends: :dependencyDashboard` | `dashboard.enabled: true`, plus `dashboard.title: Dependency Dashboard (deps trial)` while Renovate's issue #331 is open |
| `branchPrefix: renovate/` | `branchPrefix: deps/` for the trial (see [cutover](#cutover-for-ocm)) |
| `gitAuthor`, `gitIgnoredAuthors` (ocmbot) | `git.author`, `git.ignoredAuthors` |
| `prConcurrentLimit: 0`, `prHourlyLimit: 0`, `updateNotScheduled`, `minimumReleaseAge: 28 days`, `automerge: false`, `automergeType: pr` | same keys |
| `separateMinorPatch`, `separateMultipleMajor` | `separate: majors-minor-patch` |
| Renovate's default `minimumReleaseAgeBehaviour: timestamp-required` (Renovate 42+) | `minimumReleaseAgeBehaviour: timestamp-required`, set explicitly since deps defaults to `timestamp-optional` |
| `schedule: ['* * * * 0']` (14 rules) | `schedule: ['0 0 * * 0']`, on the rules, and once per group in `groups` for the groups of packageRules 10, 11, 14-17 and 23; plus the crons `0 0 * * *` and `0 0 * * 0` in the [workflow](#workflow-for-ocm) |
| 0: `matchFileNames: [chart/**]`, group "Kubernetes Controller chart" | `paths` + `group` |
| 1: Garden Linux FIPS `allowedVersions: '/^\\d+\\.\\d+$/'`, `versioning: loose` | `allow: '/^\d+\.\d+$/'`, `versioning: loose` |
| 2: `matchUpdateTypes: [minor, patch]`, `automerge: true` | `updateTypes: [minor, patch]`, `automerge: true` |
| 3, 5: `matchManagers` + `separateMinorPatch: false` + schedule | `managers` rules with `separate: majors` |
| 4, 6: `matchManagers` + `matchUpdateTypes: [minor, patch]` + `groupName` | `updateTypes: [minor, patch]` + `group` |
| 7: "Digest pins" over `github-actions`, `dockerfile`, `docker-compose` with `[digest, pinDigest]` | two rules: `github-actions`, and `docker` with `depTypes: [dockerfile, compose, workflow, action]` (Renovate's github-actions manager also covers workflow `container:`/`services:` images), both `updateTypes: [digest, pin]` (`deps migrate` adds the `install` dep type of Renovate's dockerfile manager, which has no digests) |
| 8: `matchDatasources: [golang-version]`, `rangeStrategy: bump` | `datasources: [golang-version]` (no `rangeStrategy` needed) |
| 9, 20, 21, 22: `enabled: false` for kube-openapi, go-ini/ini, crypto11, `open-component-model/.github` | `ignore: true` rules |
| 10: "Kubernetes dependencies" over `gomod` and `custom.regex` | two rules (`managers: [gomod]`, `managers: [custom]`) with the same `names` and `group` |
| 11, 15, 16, 17: "Docker dependencies", "Golang X Repositories", "Google Golang Repositories", "spf13 repositories" | `names` + `group` + `schedule` |
| 12: "Garden Linux images", 13: "OCM cosign" (`versioning: 'regex:…'`, `minimumReleaseAge: '0 days'`), 14: "Helm" | same values |
| 18: `matchCategories: [golang]`, `enabled: true`, `postUpdateOptions: [gomodTidy]`, schedule | `managers: [gomod]`, `ignore: false`, `schedule` (tidy is always on) |
| 19: indirect majors off | `depTypes: [indirect]`, `updateTypes: [major]`, `ignore: true` |
| 23: "Website CLI-docs generator dependencies" | `paths` + `group` + `separate: majors` |
| 24: `matchDepTypes: [golang]` off | `depTypes: [go]`, `ignore: true` |
| 25: npm `minimumReleaseAge: 28 days` | `datasources: [npm]` rule |
| 26: `matchUpdateTypes: [pin, digest, …]`, `minimumReleaseAge: null` | `updateTypes: [pin, digest]`, `minimumReleaseAge: '0'` (Go pseudo-version digests) |
| `customDatasources.envtest` with JSONata | `versions: .releases \| keys`, `versionRegex` |
| `customManagers` for `.env`, `netlify.toml`, workflow `version:`, `envtest.go` and `.ya?ml` markers, `.env` `*_IMAGE` tag@digest | one annotation-mode `custom[]` entry over those files |
| cosign `.env` regex with `currentDigest` of `cosign_checksums.txt` | regex `custom[]` with `checksum: {asset: cosign_checksums.txt}` |
| `hostRules` for registry.npmjs.org | built-in retries |
| `lockFileMaintenance: {enabled: false}` | the deps default (`lockFileMaintenance.enabled: false`) |
| `git-submodules: {enabled: true}` | `rules: [{managers: [git-submodules], ignore: false}]` (disabled by default, as in Renovate); OCM has no `.gitmodules` |
| `extends: security:openssf-scorecard` | not supported |

Two details carry over unchanged:

- **Rule 18 undoes rule 9.** `enabled: true` on the whole golang category comes after the `k8s.io/kube-openapi` rule, so Renovate updates kube-openapi anyway: its dashboard lists it in "kubernetes dependencies (minor)". The translation keeps the order, so deps updates it too: its pseudo-version gets digest updates, in the Kubernetes group.
- **Rule 18 enables indirect requirements.** Renovate disables them by default, and `enabled: true` turns them on again; rule 19 then keeps only their majors off. `ignore: false` does the same in deps.

**Helm.** OCM has charts (`bindings/go/kubernetes/controller/chart` and two conformance charts), but none of them has Chart.yaml `dependencies`. The helm manager has nothing to update there and needs no config. The controller image in `chart/values.yaml` (`tag: latest@sha256:…`) is a docker dep (DepType `helm`) and gets digest refreshes.

## Workflow for OCM

This workflow replaces `.github/workflows/renovate.yml`. It uses the same App (ocmbot, secrets `OCMBOT_APP_ID`/`OCMBOT_PRIV_KEY`) and the same `automation` environment:

```yaml
# .github/workflows/deps.yml
name: deps
on:
  schedule:
    - cron: "0 0 * * *" # daily (renovate.yml's cron): updates without a schedule
    - cron: "0 0 * * 0" # weekly: rules and groups with schedule ['0 0 * * 0'] (Renovate's '* * * * 0')
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
      - uses: jakobmoellerdev/deps@v0.15.0
        with:
          token: ${{ steps.app.outputs.token || github.token }}
          version: v0.15.0 # keep this when pin: digest rewrites the ref above to a SHA
          args: ${{ github.event_name == 'pull_request' && 'plan' || 'run' }}
          config: .github/deps.yaml
          show-skipped: true
          log-level: info
```

- **Pull requests** that change the config run `plan` with the job token. That validates the config: an invalid key exits 2. `run --dry-run` reports against the checkout, so on a PR merge commit it would only warn that `HEAD` is not the tip of `main` and list the PRs of the PR's tree as if it were `main`. To list the PRs a config change would cause, use the base/PR checkout pair from [ci.md](ci.md#preview-on-pull-requests).
- **Token.** The App token goes to both `actions/checkout` and the action. The action creates the commits and moves the `deps/*` branches through the API with it; git uses the checkout's credentials only to list and delete branches. `permission-workflows: write` is required, because Action updates change `.github/workflows/*`: GitHub checks it for API commits too (without it, creating the tree fails with `403 Resource not accessible by integration`).
- **Version.** The action downloads the release named by its ref when that ref is a tag. `pin: digest` turns `@v0.6.0` into `@<sha> # v0.15.0` in the first "Digest pins" PR, and from then on the ref no longer names the release. Without `version:`, the action would download the latest release.
- **Signing.** Renovate runs with `RENOVATE_PLATFORM_COMMIT: enabled`, and so does deps: `git.platformCommit: enabled` in the example config (`auto` would choose it too, because the token is an App installation token). deps then creates the commits through the GitHub API, and GitHub signs them: they show as verified, authored by `ocmbot[bot]` (`<id>+ocmbot[bot]@users.noreply.github.com`), committed by GitHub. `git.author` only applies to git commits. Don't pass `signing-key`: it conflicts with `git.platformCommit: enabled`. Verified commits satisfy the `required_signatures` rule of both rulesets that cover `deps/*` ("OCM Organization Ruleset" and the repository's "All branches"). The rulesets also restrict branch creation, deletion and non-fast-forward updates, so the branches still need ocmbot's bypass (App 295948 is an `always` bypass actor of both) or an exclusion, see the [cutover](#cutover-for-ocm).
- **Not needed.** Renovate's repository cache steps (deps has no cache) and `RENOVATE_ALLOWED_COMMANDS` (`renovate.json5` has no `postUpgradeTasks`; deps' equivalent is the `allow-tasks` input).

## Comparison with Renovate

The run below went against a fresh clone of OCM `b4b5473`, with a GitHub token. A dry run makes no writes.

```sh
DEPS_GITHUB_COM_TOKEN=$(gh auth token) DEPS_TOKEN=$(gh auth token) \
  deps run --dry-run --show-skipped --config ~/Projects/deps/docs/examples-ocm.deps.yaml --trigger-schedule '0 0 * * 0'
```

The result was `19 would-create, 200 held back by minimumReleaseAge, 91 skipped, 0 errors`. With `--trigger-schedule '0 0 * * *'` (the daily run), 7 PRs would be created: node minor and patch, js-yaml, undici, two golang.org/x/net security PRs, and Garden Linux. The other 12 were `skipped: outside schedule`. Since P18 (locked versions, lockfile-only updates), the js-yaml and undici security PRs are gone from both runs, and the npm group has the same 2 deps as Renovate's (rows below). Since P10 (floating-tag release times, `minimumReleaseAgeBehaviour: timestamp-required` in the example), markdownlint-cli2-action, Garden Linux and envtest are held back as in Renovate (rechecked with `deps plan` on `b4b5473`, which is no longer the tip of main).

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
| github.com/google/go-github/v92 → v93 | pending | held back by `minimumReleaseAge` (v93.0.0 is from 2026-10-15) | match (rechecked after M15). deps' PR also rewrites the import in `bindings/go/github/internal/download/client.go` (`gomod.updateImportPaths`; OCM doesn't enable Renovate's `gomodUpdateImportPaths`); the package builds with v93 |
| 36 others (helm v4.3.1, k8s.io/* v0.37.2, controller-tools v0.23.0, docker/cli, ginkgo, prometheus, oras, registry 3.1.2, npm 12.2.1, sigstore/scaffolding, markdownlint-cli2, open-component-model/ocm, dawidd6/action-download-artifact v28, …) | pending | held back by `minimumReleaseAge` | match |

## Cutover for OCM

1. Add `.github/deps.yaml` (this example) and `.github/workflows/deps.yml` (above) next to Renovate. Their PRs don't collide: deps' branches are `deps/*`, its dashboard has its own title, and deps closes obsolete PRs only under its own prefix.
2. Grant the ocmbot installation *Checks* and *Commit statuses* read, if it doesn't have them yet. renovate.yml requests `checks: write` and `statuses: write`, so the App likely has them. Pushes to `deps/*` rely on ocmbot's ruleset bypass. The repository ruleset "All branches" excludes only `refs/heads/renovate/**` and `refs/heads/dependabot/**`, so either add `refs/heads/deps/**` there or keep the bypass.
3. Compare a weekly run with Renovate's, as above. Then disable `renovate.yml`, set `branchPrefix: renovate/`, drop `dashboard.title`, and close #331. deps closes Renovate's leftover `renovate/*` PRs after its first run without lookup errors.

## What is still missing for OCM

Each item comes from the comparison above or from [renovate-parity.md](renovate-parity.md):

1. **Fallback versus waiting.** deps proposes the newest release that is old enough (node 26.11.1, go-winio v0.6.3); Renovate waits for the newest (26.13.0, v0.6.4). This is a design difference, documented under P10, not a bug. It is listed because it changes which PRs OCM sees.
3. **Mend badges and the OpenSSF scorecard.** PR bodies show the release age as text, with no adoption, passing or confidence badges (L5) and no scorecard badge (`security:openssf-scorecard`, L6).
4. **Renovate dashboard extras:** the repository problems list (e.g. hidden Unicode warnings) has no deps equivalent. The "Abandoned Dependencies" section maps to [`abandonmentThreshold`](reference/configuration.md#abandonmentthreshold), and deprecated packages are listed under "Deprecated dependencies".
5. **Cutover steps not done:**
    - a real (non-dry) trial run in OCM;
    - App permissions confirmed for the installation (checks/statuses read, workflows write);
    - the ruleset entry or bypass for `deps/**`;
    - the branch takeover (`branchPrefix: renovate/`) after Renovate is disabled.
