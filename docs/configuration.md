# Configuration reference

`deps` reads `.deps.yaml` (then `.deps.yml`) from `--dir`, or the file given with `--config`, which must exist. Without a file the defaults below apply. An empty file also yields the defaults.

The authoritative key list is the JSON Schema printed by `deps schema`. Every property in it is documented here. For editor completion:

```sh
deps schema > deps.schema.json
```

```yaml
# yaml-language-server: $schema=./deps.schema.json
```

## Loading and validation

- Decoding is strict. An unknown key is an error that names its line: `.deps.yaml: yaml: unmarshal errors:\n  line 1: field pinn not found in type config.Config`.
- All validation errors are reported together, one per line, as `<path>: <field>: <message>`, for example `.deps.yaml: rules[2].allow: invalid constraint "<<7": ...`.
- A config error exits with code 2 (see [cli.md](cli.md#exit-codes)).

## Top-level keys

| Key | Type | Default | Validation |
|---|---|---|---|
| `version` | integer | `1` | must be `1` if set |
| `pin` | `keep` \| `digest` | `keep` | enum |
| `schedule` | list of cron strings | `[]` (every run) | valid 5-field crons |
| `security` | bool | `true` | |
| `branchPrefix` | string | `deps/` | must end with `/` and be a valid git ref prefix |
| `rebaseWhen` | `conflicted` \| `behind-base-branch` \| `never` | `conflicted` | enum |
| `monorepos` | bool | `true` | |
| `gitAuthor` | string | `deps[bot] <deps-bot@noreply.invalid>` | `Name <email>` |
| `gitIgnoredAuthors` | list of emails | `[]` | each must be an email address |
| `prConcurrentLimit` | integer | `0` (unlimited) | `>= 0` |
| `prHourlyLimit` | integer | `0` (unlimited) | `>= 0` |
| `actions.defaultHost` | string | `""` (auto) | |
| `minimumReleaseAge` | string | `""` (off) | age syntax (below) |
| `ignorePaths` | list of globs | built-in list (below) | valid doublestar globs |
| `separateMajorMinor` | bool | `true` | |
| `separateMultipleMajor` | bool | `false` | |
| `separateMinorPatch` | bool | `false` | |
| `updateNotScheduled` | bool | `true` | |
| `automerge` | bool | `false` | |
| `automergeStrategy` | `auto` \| `merge` \| `squash` \| `rebase` | `auto` | enum |
| `automergeType` | `pr` | `pr` | only `pr`; `branch` is an error |
| `dependencyDashboard` | bool | `false` | |
| `dependencyDashboardTitle` | string | `Dependency Dashboard` | non-blank, one line |
| `dependencyDashboardLabels` | list of strings | `[]` | non-blank, no `,` |
| `rules` | list of [rules](#rules) | `[]` | per field, below |
| `custom` | list of [custom managers](#custom-managers) | `[]` | per field, below |
| `customDatasources` | map name → [datasource](#custom-datasources) | `{}` | per field, below |

### `version`

Config format version. Only `1` exists; `version: 2` fails with `version: must be 1, got 2`.

### `pin`

The default pin mode for github-actions and docker deps. `rules[].pin` overrides it per dep.

- `keep`: keep each reference's current style. Refs that are already pinned (`@<sha> # v4.2.0`, `image:tag@sha256:…`) stay pinned, and both the digest and the version are refreshed.
- `digest`:
  - github-actions: tag refs are pinned to the tag's commit SHA, with ` # <tag>` appended.
  - docker: `@sha256:<digest>` is appended after the tag (the index digest for multi-platform images).

gomod, npm and custom deps ignore `pin`.

```yaml
pin: digest
```

### `schedule`

`schedule` lists cron expressions that are **cron triggers of the workflow running deps**. deps never evaluates them as time windows: a group is in schedule when the run was started by one of its crons.

- The trigger is read from the CI environment: `GITHUB_EVENT_NAME` (GitHub, Gitea and Forgejo set it) and, for `schedule` events, the `schedule` field of the event payload at `GITHUB_EVENT_PATH`. GitHub and Gitea (verified in 28.3) put the triggering cron there. `--trigger-schedule <cron>` or `DEPS_TRIGGER_SCHEDULE` override the detection (for local tests or runners that lack the payload).
- Crons are compared as strings after collapsing whitespace: `0 0 * * 0` matches `0  0 * * 0` but not the equivalent `0 0 * * 7`. Copy them verbatim from `on.schedule`.
- Runs not started by a cron (`workflow_dispatch`, `push`, a local `deps run`) act immediately: every schedule matches. A `schedule` event whose cron cannot be read also matches every schedule, with a warning.
- Cron triggers of CI systems are UTC, so there is no time zone setting.
- Each entry must be a valid 5-field cron (`minute hour day-of-month month day-of-week`; `*`, `n`, `a-b`, comma lists, `/step` on `*` or a range; day of week `0-7`; no names). Errors look like `schedule[0]: "60 5 * * *": minute: value 60 out of range 0-59`.
- The schedule only gates creating new PRs and, with `updateNotScheduled: false`, updating existing ones. Security updates ignore schedules.

A daily workflow cron plus a weekly one, with the weekly cron reserved for some groups:

```yaml
# .github/workflows/deps.yml
on:
  schedule:
    - cron: "0 0 * * *"   # daily
    - cron: "0 0 * * 0"   # weekly, Sunday
```

```yaml
# .deps.yaml
rules:
  - updateTypes: [digest, pin]
    group: digest-pins
    schedule: ["0 0 * * 0"]   # PRs are created only by the Sunday run
```

Other deps have no schedule, so both crons create their PRs.

### `security`

When `true` (the default), deps queries [OSV](https://osv.dev) for Go, npm and GitHub Actions deps. A vulnerable dep is moved to the smallest compatible release at or above the minimum fixed version. Security updates bypass `allow`, `updates`, schedules, `minimumReleaseAge` and rules with `updateTypes`. They never bypass a pre-policy `ignore`. They always use commit type `fix`, get a `[security]` title suffix, and are never put into a group. A failed OSV lookup is reported as an error (`security advisories: <err>`).

```yaml
security: false
```

### `branchPrefix`

The prefix of every update branch: `<prefix><manager>/<slug(name)>[-<bucket>]` and `<prefix>group/<slug(group)>[-<bucket>]`. deps only manages branches and PRs under this prefix, and never touches others.

- It must end with `/`.
- It must not contain spaces, control characters, any of `~^:?*[\`, `..` or `@{`.
- No component may be empty, start with `.` or end with `.lock`.

To take over Renovate's open PRs, set `branchPrefix: renovate/` (see [migrating-from-renovate.md](migrating-from-renovate.md#branch-prefix-takeover)).

### `rebaseWhen`

When an open PR whose *content is unchanged* is rebuilt on the current base branch:

| Value | Rebuilt when |
|---|---|
| `conflicted` (default) | the platform reports the PR as not mergeable |
| `behind-base-branch` | also when the branch is not a single commit on the current base head (status `rebased`) |
| `never` | never; only content changes update the PR |

A PR whose content changed is always updated. Branches with commits by other authors are never rebuilt (see `gitIgnoredAuthors`).

### `monorepos`

Built-in monorepo groups. When `true`, deps released from one source repository share a PR, with group `<name> monorepo`. The 62 built-in entries include kubernetes (`k8s.io/` staging modules), go-openapi, opentelemetry-go, opentelemetry-go-contrib, aws-sdk-go-v2, azure-sdk-for-go, google-cloud-go, moby, etcd, sigstore, typescript-eslint, eslint, babel, jest, vitest, angular, react, vue, storybook, tailwindcss, playwright, codeql-action and actions-cache. The full list is in `internal/config/monorepos.go`.

They are applied as implicit rules *before* the user rules, so any rule that sets `group` overrides them. `deps explain` shows them as `builtin  monorepo <name> -> group "<name> monorepo"`.

```yaml
monorepos: false
```

### `gitAuthor`, `gitIgnoredAuthors`

- `gitAuthor` is the author and committer of deps commits, written as `Name <email>`. GitHub bot noreply addresses such as `ocmbot[bot] <1+ocmbot[bot]@users.noreply.github.com>` are accepted.
  - Precedence, per field: `--author-name`/`--author-email` > `$DEPS_GIT_AUTHOR` > `gitAuthor` > `deps[bot] <deps-bot@noreply.invalid>`.
  - An invalid value fails with `gitAuthor: must be 'Name <email>', got "<v>"`.
- `gitIgnoredAuthors` lists further emails, compared case-insensitively. A branch whose tip was committed by one of them still counts as the bot's own, so it is updated and rebased instead of `skipped: branch modified by someone else`.

```yaml
gitAuthor: 'ocmbot[bot] <125909804+ocmbot[bot]@users.noreply.github.com>'
gitIgnoredAuthors: ['125909804+ocmbot[bot]@users.noreply.github.com']
```

### `prConcurrentLimit`, `prHourlyLimit`

- `prConcurrentLimit` is the maximum number of open managed PRs (under `branchPrefix`). New PRs are only created while the open count plus PRs created this run stay below it.
- `prHourlyLimit` is the maximum number of managed PRs created in the last hour. Open and closed PRs are counted by their creation time.
- `0` means unlimited, and both default to `0`. **Renovate defaults to 10 and 2**, so set them explicitly to keep Renovate's throttling.
- The `run` flag `--max-new-prs` (default `10`, `0` = unlimited) is a separate per-run cap. A run creates at most the minimum of every non-zero limit. Groups over the limit get `skipped: new PR limit reached`. Existing PRs are always updated.

```yaml
prConcurrentLimit: 10
prHourlyLimit: 2
```

### `actions.defaultHost`

The host used for `owner/repo@ref` actions, which have no explicit `https://host/`. First match wins:

1. `actions.defaultHost` (source `config`);
2. `$DEPS_DEFAULT_ACTIONS_URL`, its hostname (source `env`);
3. the platform default (source `platform default`): `data.forgejo.org` on Forgejo, otherwise `github.com`.

With `--log-level info`, deps logs the result as `actions default host host=<h> source=<s>`. Gitea instances with a non-default `DEFAULT_ACTIONS_URL` should set this key.

```yaml
actions:
  defaultHost: gitea.com
```

### `minimumReleaseAge`

A new version is proposed only once it has been released for at least this long.

- **Syntax:**
  - `<n> <unit>`, where the unit is minutes, hours, days or weeks, written singular, plural or as the first letter, with the space optional: `28 days`, `1 day`, `3d`, `12h`, `90m`, `2w`;
  - `0`;
  - a Go duration: `1h30m`.

  Negative or unparsable values are errors (`invalid age "<v>": want e.g. "3 days", "12h", "90m" or "0"`).
- **Fallback:** a too-new candidate is rejected as `too new (released <date>, minimum <age>)`, and the next older candidate in the same PR bucket is tried.
- **Unknown release times are allowed.** This covers datasources without release times (Go toolchains, `custom.<name>`) and failed time lookups.
- **Exempt:** digest refreshes, pins and security updates.
- `rules[].minimumReleaseAge` overrides the value per dep. With `updateTypes` it overrides it per update type. `0` or `0 days` turns the age off for matching deps.

```yaml
minimumReleaseAge: 28 days
rules:
  - names: ['open-component-model/cosign']
    minimumReleaseAge: 0 days
```

### `ignorePaths`

Doublestar globs on slash paths relative to `--dir`. Matching files and directories are not scanned.

- When the key is **absent**, Renovate's `:ignoreModulesAndTests` list applies: `**/node_modules/**`, `**/bower_components/**`, `**/vendor/**`, `**/examples/**`, `**/__tests__/**`, `**/test/**`, `**/tests/**` and `**/__fixtures__/**`.
- Any explicit list, including `[]` or a null value, replaces that default.
- Independently of `ignorePaths`, discovery always prunes `.git`, `node_modules`, `vendor` and `testdata` directories.

```yaml
ignorePaths: ['**/node_modules/**', 'docs/**']
```

### `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch`

These keys split one dep's candidate updates into **PR buckets**. Each bucket yields at most one update, and each update gets its own branch (suffix) and PR:

| Setting | Buckets (branch suffix) |
|---|---|
| `separateMajorMinor: false` | one bucket, the newest update of any kind (no suffix) |
| default (`separateMajorMinor: true`) | newest major (`-major`); newest minor/patch (no suffix) |
| `+ separateMultipleMajor: true` | one per newer major *N*, the newest release of each (`-v<N>`); minor/patch (no suffix) |
| `+ separateMinorPatch: true` | newest minor (`-minor`); newest patch of the current minor (`-patch`) |

- The bucket of a candidate comes from the policy of its update type. A rule with `updateTypes` can therefore set these flags for its types, unlike in Renovate (see [migrating-from-renovate.md](migrating-from-renovate.md#behavior-differences)).
- `plan`/`apply` write one update per dep: the newest non-major one, else the lowest major. The others are reported with `, separate PR`.
- A policy group inherits its members' bucket suffix: `deps/group/actions-patch`.

### `updateNotScheduled`

- `true` (the default): open PRs outside their schedule are still updated (content changes, conflicts, `rebaseWhen`). Only *creating* a PR waits for a run triggered by one of their crons.
- `false`: out-of-schedule groups are left untouched (`skipped: outside schedule`).

Out-of-schedule PRs are never closed as obsolete.

### `automerge`, `automergeStrategy`, `automergeType`

`automerge: true` makes `deps run` ask the platform to merge an update PR once its required checks pass (platform auto-merge). deps itself never merges red or pending PRs. Rules override `automerge` and `automergeStrategy` per dep or per update type, so Renovate's `matchUpdateTypes: [minor, patch], automerge: true` is:

```yaml
rules:
  - updateTypes: [minor, patch]
    automerge: true
```

- A PR automerges only when **every** update in it does; a group that mixes an automerged minor with a major does not. The strategy comes from the PR's first update.
- `automergeStrategy`: `merge`, `squash` or `rebase`. `auto` (the default) uses the repository's default: Gitea/Forgejo `default_merge_style`, and on GitHub the first method the repository allows (merge commit, then squash, then rebase).
- `automergeType` exists for Renovate compatibility. Only `pr` is accepted. `automergeType: branch` (merging the branch without a PR) fails with `automergeType: branch is not supported: deps automerges through the platform's pull request auto-merge; use automergeType: pr`.
- The request is sent after the PR is created, updated or rebased, and on every run for an up-to-date automerge PR, until the platform reports it scheduled. Security updates follow the same policy.
- Platform behavior and the GitHub fallback are described in [design.md](design.md#automerge).

### `dependencyDashboard`, `dependencyDashboardTitle`, `dependencyDashboardLabels`

`dependencyDashboard: true` makes `deps run` maintain one open issue titled `dependencyDashboardTitle`. It lists, in this order:

1. **Awaiting schedule** and **Rate-limited**: updates without a PR because of `schedule` or the PR limits (`--max-new-prs`, `prConcurrentLimit`, `prHourlyLimit`). Awaiting-schedule items name the crons they wait for, e.g. ``(waits for `0 0 * * 0`)``. Each has a checkbox; ticking it makes the next run create the PR regardless of schedule and limits.
2. **Ignored or blocked**: updates whose PR was closed without merging (checkbox: recreate the PR), and branches someone else pushed to (checkbox: discard those commits and rebuild the branch).
3. **Open**: the open update PRs, each with a checkbox that forces a rebuild of its branch.
4. **Errors** of the run (at most 20, each cut to 300 characters).
5. **Detected dependencies**: every extracted dependency (skipped ones too), as `name version` per manager and file in collapsed `<details>` blocks.

- The body is capped at 60 000 bytes (GitHub allows 65 536 characters). Lists that do not fit end with a `… not listed` note.
- The body is deterministic: the issue is only edited when its content changes. Every run regenerates it, so ticked checkboxes are unticked again once handled.
- The issue is found by its exact title and the `<!-- deps:dashboard -->` marker in its body, among the first 1 000 (GitHub) or 500 (Gitea/Forgejo) open issues.
- `dependencyDashboardLabels` are set when the issue is created. GitHub creates missing labels; Gitea and Forgejo look the names up in the repository's and its organization's labels and leave out unknown ones (with a warning).
- deps never closes the dashboard. `dependencyDashboard: false` leaves an existing dashboard untouched.
- A repository without issues (disabled, or an external tracker on Gitea/Forgejo) reports `dashboard skipped: issues disabled` and is not an error.

## Rules

`rules` is an ordered list. A rule **matches** a dep when every selector it sets matches. Every field it sets then **overrides** the value from earlier rules (field-wise); `updates` replaces the earlier list, it is not merged.

### Selectors

| Field | Type | Matches when |
|---|---|---|
| `manager` | `github-actions` \| `gomod` \| `docker` \| `npm` \| `custom` | equal to the dep's manager |
| `names` | list of doublestar globs | any glob matches the dep name (`*` within a segment, `**` across segments, `{a,b}` alternatives) |
| `depTypes` | list | contains the dep type ([managers.md](managers.md) lists them per manager) |
| `paths` | list of doublestar globs | any glob matches the file (slash path relative to `--dir`), e.g. `charts/**` |
| `datasources` | list | contains the dep's datasource. Renovate names are normalized: `golang-version` = `go-toolchain`, `github-tags` = `git-tags` (so it also matches actions), `github-release-attachments` = `github-releases`, `node` = `node-version`. `custom.<name>` must be defined in `customDatasources`. |
| `updateTypes` | list of `major` `minor` `patch` `digest` `pin` | the *candidate update* has one of these types (below). Must not be empty. |

Dep types: `direct indirect go toolchain action setup dockerfile compose workflow kubernetes helm dependencies devDependencies optionalDependencies peerDependencies packageManager engines volta annotation regex`.

### Actions

| Field | Type | Effect |
|---|---|---|
| `ignore` | bool | `true`: do not update. `false`: opt in a default-ignored dep type. With `updateTypes`: reject (or allow) updates of those types only. |
| `pin` | `keep` \| `digest` | pin mode (github-actions, docker). Not allowed with `updateTypes`. |
| `allow` | string | a Masterminds semver constraint (`< 7`, `>= 1.2, < 2`), or Renovate's regex form `/<RE2>/` (`!/<RE2>/` rejects matches; only flag `i`) |
| `updates` | list of update types | allowed update types; replaces the earlier list |
| `versioning` | `tag` \| `go` \| `docker` \| `semver` \| `loose` \| `regex:<RE2>` | replaces the manager's scheme. Not allowed with `updateTypes`. |
| `group` | string | all deps with this group share one branch `<prefix>group/<slug>` and one PR |
| `schedule` | list of cron strings | overrides the top-level schedule; `[]` means every run |
| `minimumReleaseAge` | string | overrides the top-level age; `0` disables it |
| `commitType` | `fix` `chore` `build` `ci` `docs` `feat` `perf` `refactor` `revert` `style` `test` | conventional commit type of the commit and PR title |
| `commitScope` | string | scope; must match `^[A-Za-z0-9][A-Za-z0-9._/-]*$`; `""` drops the scope (`chore: update …`) |
| `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch` | bool | override the top-level bucket options; with `updateTypes`, for candidates of those types |
| `automerge` | bool | overrides the top-level `automerge`; with `updateTypes`, for updates of those types |
| `automergeStrategy` | `auto` \| `merge` \| `squash` \| `rebase` | overrides the top-level strategy |
| `automergeType` | `pr` | accepted for Renovate compatibility; only `pr` |

### Evaluation

1. The policy of a dep starts as `{pin: <top-level>, updates: all five types, schedule: <top-level>, minimumReleaseAge: <top-level>, bucket flags: <top-level>}`. The commit type starts as `fix` for production deps (gomod `direct`, npm `dependencies`) and `chore` otherwise, and the scope as `deps`.
2. **Default ignores.** Dep types `indirect` (Go), `go` (the go.mod `go` directive), `peerDependencies` and `engines` (npm) are ignored unless a rule sets `ignore: false`. The skip reason is `ignored by default (<type>; enable with a rule setting ignore: false)`.
3. **Built-in monorepo groups** set `group: <name> monorepo` (unless `monorepos: false`).
4. **Pre-policy.** Rules *without* `updateTypes` are evaluated top to bottom. Together they decide `ignore`, `pin`, `versioning`, and whether the dep is looked up at all. An ignored dep is skipped as `ignored by rule #<i>`.
5. **Per-type policy.** Once a candidate's type is known, *all* rules are evaluated again in file order, and rules with `updateTypes` participate when they list that type. The candidate is accepted when that policy does not ignore it, its type is in `updates`, and it satisfies `allow`. Otherwise the next candidate of the bucket is tried, so ignoring indirect majors keeps their minor and patch updates. Rejections read `ignored by rule #<i> for <type>`, `update type <type> not allowed` or `outside allow <allow>`.
6. The chosen update takes `group`, `schedule`, `commitType`, `commitScope`, `automerge` and `automergeStrategy` from the policy of its type. Security updates take only the commit scope and the automerge settings.

`updateTypes: [patch]` together with `ignore: false` opts a default-ignored or ignored dep in for those types only.

```yaml
rules:
  - manager: gomod                 # opt indirect requirements in
    depTypes: [indirect]
    ignore: false
  - manager: gomod                 # but never their majors
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - manager: github-actions        # minor+patch in one PR, majors apart
    updateTypes: [minor, patch]
    group: GitHub Actions
```

### `allow`

- A **constraint** (`< 7`, `~1.2`, `>= 1.20, < 2`) is checked against the scheme's Masterminds conversion of the candidate. Versions that cannot be represented are rejected.
- A **regex** (`/^\d+\.\d+$/`, `!/-rc/`, `/^v1\./i`) matches the candidate *as written* (tag, image tag, module version) and never converts it.
- Invalid values are config errors: `rules[i].allow: invalid constraint "<v>": …`, `invalid regex "/(/": …`, `unknown flags "g" (only i)`.

### `versioning`

Overrides the scheme used to validate the current value, rank candidates, classify update kinds, convert versions for `allow`, and compute security fixes. A current value that the new scheme cannot parse is skipped as `non-version value "<v>"`.

| Scheme | Accepts | Notes |
|---|---|---|
| `tag` | `^(v?)\d+(\.\d+)?(\.\d+)?$` | candidates need the same prefix and the same number of parts (`v4` → `v5`, `v4.1.0` → `v4.2.0`); no suffixes or prereleases |
| `go` | Go module semver | no pseudo-versions; prereleases only from a prerelease; `+incompatible` only from `+incompatible`; same major except v0 → v1 |
| `docker` | `^(v?)(\d+(\.\d+)*)(.*)$` | same prefix, part count and suffix (`1.27-alpine` → `1.28-alpine`); `latest` is invalid |
| `semver` | Masterminds loose semver | any precision; prereleases only from a prerelease; keeps the `v` style; the default of `custom[]` and npm |
| `loose` | 1–6 numeric parts, optional `v`, optional `-`/`_` suffix | the suffix counts as a prerelease. Missing parts compare as 0 (`1877` < `1877.25`); then fewer parts sort below more (`1.2` < `1.2.0`). Any precision is a candidate, so combine it with an `allow` regex. |
| `regex:<RE2>` | the expression's named groups | `major` (required), `minor`, `patch`, `build`, `revision` (numeric, absent = 0), `prerelease`, `compatibility`; other names are errors. `(?<name>)` and `(?P<name>)` both work. Candidates must share `compatibility`. |

Errors: `rules[i].versioning: regex: must have a (?<major>...) group`, `regex: unknown group "x" (allowed: ...)`, `regex: invalid RE2 regexp: ...`, and `cannot be combined with updateTypes: ...`.

```yaml
rules:
  - names: ['ghcr.io/gardenlinux/gardenlinux/fips']
    versioning: loose
    allow: '/^\d+\.\d+$/'
  - names: ['open-component-model/cosign']
    versioning: 'regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$'
```

### Groups and titles

- **Groups.** All non-security updates whose policy has the same `group` share a branch `<prefix>group/<slug(group)>` and one PR. The title is `<type>(<scope>): update <group> (<n> dependencies)`, or `update <group> to <v>` when every member moves to one version.
- **Single deps.** Every other update gets the branch `<prefix><manager>/<slug(name)>`, with titles `<p> update <name> to <ver>`, `<p> pin <name> to <sha7>` and `<p> update <name> digest to <sha7>`.
- `slug` lowercases the name, maps bytes outside `[a-z0-9._-]` to `-`, and collapses and trims `-`.
- **Commit type in groups.** A group uses `fix` if any member's type is `fix`, else the first member's type. It always uses the first member's scope.

## Custom managers

`custom[]` entries find versions in arbitrary files.

| Field | Type | Default | Notes |
|---|---|---|---|
| `files` | list of doublestar globs | required | must not be empty |
| `match` | RE2 | `""` (annotation mode) | regex mode; needs a `version` or `currentValue` group |
| `datasource` | datasource name or `custom.<name>` | | regex mode: required unless `match` has a `datasource` group |
| `name` | string | | regex mode: required unless `match` has a `name`/`depName`/`packageName` group |
| `versioning` | scheme | `semver` | as `rules[].versioning` |
| `checksum.asset` | template | | regex mode, `github-releases` only; exclusive with `url` |
| `checksum.url` | template | | URL of a checksum document; exclusive with `asset` |
| `checksum.file` | template | `""` | `url` only: entry in a sha256sum-style list; `""` = first hex token |

Datasources: `git-tags`, `github-releases`, `go`, `go-toolchain`, `docker`, `npm`, `node-version`, the Renovate aliases `golang-version`, `github-tags`, `github-release-attachments` and `node`, and `custom.<name>`.

### Annotation mode (no `match`)

A comment marker annotates the **next line**, and that line's first version token (`v?N.N[.N…][-suffix]`, not glued to other word characters) is the version. Comment starters are `#`, `//`, `--`, `;` and `<!--`.

- `deps: <datasource> <name> [versioning=<scheme>]`
- `renovate: datasource=<ds> depName=<name> [packageName=<p>] [versioning=<v>] [registryUrl=<url>]`. Fields can come in any order. `packageName` wins over `depName`, and `registryUrl` and unknown keys are ignored.
- With the docker datasource, the next line's first tagged image reference is used: the tag is the version, and an adjacent `@sha256:` digest is rewritten with it.
- A marker's `versioning=` takes a scheme name (not `regex:`). Without it, the entry's `versioning` applies.
- Skips: `no version on next line`, `renovate marker without datasource`, `renovate marker without depName`.

```yaml
custom:
  - files: ['Dockerfile', '**/*.mk', '.env']
```

```dockerfile
# deps: github-releases golangci/golangci-lint
ARG GOLANGCI_VERSION=v2.5.0
# renovate: datasource=go depName=golang.org/x/tools
RUN go install golang.org/x/tools/cmd/goimports@v0.38.0
```

### Regex mode (`match` set)

`match` runs over the whole file, and each match is one dep.

| Group | Aliases | Meaning |
|---|---|---|
| `version` | `currentValue` | required; the replaced version |
| `name` | `depName`, `packageName` (wins over `depName`) | overrides `name` |
| `datasource` | | overrides `datasource` |
| `versioning` | | scheme name |
| `digest` | `currentDigest` | replaced together with the version |
| `checksum` | | requires `checksum:` (and vice versa) |

Empty groups skip the match as `empty version`, `empty name` or `empty datasource`. A checksum that is not 64 or 128 hex digits is skipped as `checksum is not 64 or 128 hex digits`.

```yaml
custom:
  - files: ['Makefile']
    match: 'GOLANGCI_VERSION \?= (?P<version>v\S+)'
    datasource: github-releases
    name: golangci/golangci-lint
```

### Checksums

A regex entry can keep a sha256/sha512 checksum next to the version. This needs a `(?P<checksum>…)` group plus a `checksum:` block with exactly one source:

- `asset` (github-releases only): the release asset's API `digest`. This costs no extra request.
- `url`: a document downloaded once per URL (1 MiB cap). With `file`, the matching `<hex>  <name>` / `<hex> *<name>` line is used, else the first hex token in the document.

The only template variable is `{{version}}`, the new version as written into the file. Others are errors (`unknown template variable {{x}} (only {{version}})`). The new checksum must have the same length as the current one and keeps its letter case. If fetching it fails, the dep is not updated, so version and checksum always change together.

```yaml
custom:
  - files: [Dockerfile]
    match: 'ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: cli/cli
    checksum:
      url: 'https://github.com/cli/cli/releases/download/v{{version}}/gh_{{version}}_checksums.txt'
      file: 'gh_{{version}}_linux_amd64.tar.gz'
```

## Custom datasources

`customDatasources.<name>` defines a version list that deps refer to as datasource `custom.<name>`. Names must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`. The document is fetched once per run (unauthenticated, at most 10 MiB) and shared by every dep. It has no release times and no digests.

| Field | Type | Default | Notes |
|---|---|---|---|
| `url` | http(s) URL | required | Renovate's `defaultRegistryUrlTemplate` |
| `format` | `json` \| `yaml` \| `plain` | `json` | `plain`: one version per line; empty lines and `#` comments are skipped |
| `versions` | selector | `.releases[].version` | json/yaml only (`not allowed with format plain`) |
| `versionRegex` | RE2 | `""` | values must match; the `version` group, else the whole match, is the version |

**Selector grammar** (a jq subset):

- A selector is a sequence of stages joined by `|`.
- A stage is either `keys` (the sorted keys of an object) or a path of steps.
- Steps:
  - `.` (identity);
  - `.field`, where field is `[A-Za-z_][A-Za-z0-9_-]*`;
  - `[]` or `.[]` (array elements or object values). A path may also start with `[]`.
- As in jq:
  - a missing field, or a field of null, is null; nulls are dropped;
  - iterating null, or indexing or iterating the wrong type, is an error.

Examples: `.releases[].version`, `.releases | keys`, `[].tag_name`.

Errors read `custom.<name>: GET <url>: HTTP 404` or `custom.<name>: <url>: selector ".a[]": cannot iterate over null`. An empty result is `no versions`.

```yaml
customDatasources:
  envtest:
    url: https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/master/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
    versionRegex: '^v?(?P<version>\d+\.\d+\.\d+)$'
custom:
  - files: ['.env']
    match: 'ENVTEST_K8S_VERSION=(?P<version>\d+\.\d+\.\d+)'
    datasource: custom.envtest
    name: envtest
```

## Full example

```yaml
# yaml-language-server: $schema=./deps.schema.json
version: 1
schedule: ["0 5 * * 1-5"]          # create PRs in the weekday run (a cron of the workflow)
pin: digest                         # pin actions to SHAs and images to digests
minimumReleaseAge: 3 days
security: true
branchPrefix: deps/
rebaseWhen: behind-base-branch
gitAuthor: 'deps[bot] <deps-bot@noreply.invalid>'
prConcurrentLimit: 10
prHourlyLimit: 2
separateMinorPatch: false
dependencyDashboard: true
dependencyDashboardLabels: [dependencies]
separateMultipleMajor: true
ignorePaths: ['**/node_modules/**', '**/testdata/**', 'third_party/**']
rules:
  - manager: github-actions         # minor+patch of all actions in one PR
    updateTypes: [minor, patch]
    group: actions
  - updateTypes: [minor, patch]     # automerge minor and patch updates once checks pass
    automerge: true
    automergeStrategy: squash
  - datasources: [git-tags, docker] # digest refreshes in one weekly PR
    updateTypes: [digest, pin]
    group: digest-pins
    schedule: ['0 5 * * 0']         # the workflow's Sunday cron
  - manager: gomod                  # include indirect requirements...
    depTypes: [indirect]
    ignore: false
  - manager: gomod                  # ...without their majors
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - names: ['golang.org/x/**']
    group: golang-x
  - manager: docker
    names: ['postgres']
    allow: '< 18'
  - manager: npm
    depTypes: [devDependencies]
    group: npm-dev
    commitScope: dev-deps
  - datasources: [npm]
    minimumReleaseAge: 7 days
custom:
  - files: ['Makefile', '.env']     # '# deps:' / '# renovate:' markers
  - files: ['Dockerfile']
    match: 'ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: cli/cli
    checksum: {asset: 'gh_{{version}}_linux_amd64.tar.gz'}
customDatasources:
  envtest:
    url: https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/master/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
```
