# Migrating from Renovate

deps reads `.deps.yaml`, not `renovate.json(5)`. Most options translate one to one. Some behaviors that Renovate gets from presets are built in. This page lists the mapping, the known differences, and a worked translation of open-component-model's config. Feature status is tracked in [renovate-parity.md](renovate-parity.md).

## Compatibility mode and `deps migrate`

deps can do the translation below itself:

- `deps plan|apply|run|explain --renovate` translates the repository's Renovate config in memory and runs from it. `--config` naming a Renovate file (`renovate.json5`, `.renovaterc`, …) does the same.
- `deps migrate` writes that translation as a commented `.deps.yaml` (`--output -` prints it). The Renovate config is not touched.
- `deps init --from-renovate <file>` writes a new `.deps.yaml` from the same translation: the starter file (schema modeline, a comment naming the detected managers) plus the translated settings. `deps config check` validates the result.

Both use one translator, so a migrated file behaves exactly like compatibility mode. It reads every file name Renovate reads (`renovate.json(c|5)`, the same under `.github/`, `.gitlab/`, `.gitea/`, `.forgejo/`, `.renovaterc*`, the `renovate` key of `package.json`) and resolves `extends`: Renovate's built-in presets from a table, `github>` (github.com), `gitlab>` (gitlab.com), `gitea>`, `forgejo>` and `local>` (the repository's forge: GitHub, GitLab, Gitea or Forgejo) presets by fetching them. npm and URL presets are skipped with a warning.

- Settings deps cannot express are reported as `warn <setting>: <msg>`, settings without effect on which updates or PRs exist (labels, PR texts, `groupSlug`, …) as `info`. `migrate` prints them and writes them as the file's head comment; compatibility mode logs them.
- A rule with an untranslatable matcher (`matchJsonata`, a `matchCurrentValue` glob, `matchPackageNames` together with `matchDepNames`) is dropped as a whole, an untranslatable effect only by itself.
- `branchPrefix` follows Renovate (`renovate/` unless set), so deps takes over Renovate's branches (see [below](#branch-prefix-takeover)). `prConcurrentLimit` and `prHourlyLimit` keep Renovate's defaults 10 and 2, `ignorePaths` Renovate's default list.
- Schedules and `timezone` are kept as written: deps evaluates Renovate's time windows (text and crons with minute `*`) against the run time, like Renovate. The workflow must run inside every window, e.g. hourly; `migrate` lists the windows. A cron with a fixed minute is dropped, as Renovate rejects it.
- `matchManagers` and `matchCategories` expand to `rules[].managers` lists, one rule per set of dep types (managers whose dep types are not restricted share one): `github-actions` matches the `github-actions` manager and the `docker` manager's `workflow`/`action` images, `dockerfile` the `docker` manager's `dockerfile` dep type. `enabledManagers`, `<manager>.enabled: false`, Renovate's default-off `pre-commit` and `azure-pipelines` (`azure-pipelines: {enabled: true}` becomes `{managers: [azure-pipelines], ignore: false}`, which also updates its tasks, as Renovate does) and the deps managers Renovate does not have (`conda`, `vcpkg`, `elm`) become trailing `ignore: true` rules.
- Renovate's `/regex/` and `!negated` patterns in package names, file names and custom manager files work in deps as they are.
- `dependencyDashboardApproval` (top level and per rule) becomes `dashboardApproval`, the `rust-toolchain` manager is in the `rust` category and the `devcontainer` manager in the `docker` category.
- `postUpgradeTasks` (top level and per rule; `fileFilters` → `files`), every `rangeStrategy` value, and the `bundler`, `ruby-version` and `swift` managers (categories `ruby`, `swift`) are translated.
- The `bitbucket-pipelines`, `woodpecker`, `droneci`, `circleci` and `azure-pipelines` managers (category `ci`), `composer` (`php`), `julia` (`julia`; Renovate's datasource `julia-general-metadata` is deps' `julia-registry`), `bazel-module` and `bazelisk` (`bazel`) and `nuget` (`dotnet`) are translated, and so are `draftPR`, `prCreation` (`immediate`, `not-pending`, `status-success`) and `abandonmentThreshold` (top level and per rule; a rule's `null` is `0`, and `abandonments:recommended` of `config:best-practices` becomes its rules).
- `github-release-attachments` digests, the common JSONata `transformTemplates` shapes (`$map(<list>, function($v){ {"version": …} })`, `<path>.{"version": <field>}`, with `$keys`, `$distinct` and `$replace(…, /^v/, "")`), `matchCurrentVersion`, `matchCurrentAge`, `extractVersion`, labels, reviewers, assignees, `rebaseLabel`, `stopUpdatingLabel`, `platformCommit` and `minimumReleaseAgeBehaviour` (default `timestamp-required`, as in Renovate 42+) are translated.
- `matchCurrentValue` becomes `rules[].currentVersion`: regexes (`/re/`, `/re/i`, `!/re/`) as they are, an exact string `v1.2.3` as `/^v1\.2\.3$/i` (Renovate compares globs case-insensitively), `!latest` as `!/^latest$/i`. deps matches the locked version when a lockfile has one, else the version as written (for an npm or cargo range its bare version, `1.2.3` of `^1.2.3`), while Renovate matches the manifest value (`^1.2.3`), so regexes on range operators do not carry over. `currentVersion` cannot be combined with `versioning` (nor `currentAge` with `versioning` or `extractVersion`): those fields are dropped from the rule with a warning.
- `prHeader` and `prFooter` (top level and per rule) become deps' Go templates: plain text passes, `{{depName}}`/`{{packageName}}` → `{{.Name}}`, `{{newVersion}}`/`{{newValue}}` → `{{.To}}`, `{{currentVersion}}`/`{{currentValue}}` → `{{.From}}`, `{{groupName}}` → `{{.Group}}`, `{{updateType}}` → `{{.UpdateType}}` (triple braces the same), comments are dropped and `\{{` stays a literal `{{`. A text with any other expression (helpers, `{{#if}}`, `{{#each}}`, other fields) is dropped with a warning.
- PR titles: when the repository (not a built-in preset) sets `prTitle`, `commitMessage`, `commitMessagePrefix`, `commitMessageAction`, `commitMessageTopic`, `commitMessageExtra`, `commitMessageSuffix` or `commitMessageLowerCase`, or `semanticCommits: disabled` (also through `:semanticCommitsDisabled`), the translation composes Renovate's title as one `prTitle` (top level, and per rule for rules and blocks that set one of them, over the top-level settings), converted like `prHeader`. Unset parts take Renovate's defaults:

  | Part | Default in the translation (Renovate's) |
  |---|---|
  | `commitMessagePrefix` | `{{.Type}}{{with .Scope}}({{.}}){{end}}:` (Renovate's `<semanticCommitType>(<semanticCommitScope>):`), nothing with `semanticCommits: disabled` or a set `commitMessagePrefix` |
  | `commitMessageAction` | `Update`, `Pin` for pins (Renovate's `pinDigest` block) |
  | `commitMessageTopic` | `dependency {{.Name}}`, `{{.Name}} digest` for digests; `{{.Group}}` for a group PR of several dependencies (Renovate's `group` config, over every other topic) |
  | `commitMessageExtra` | `to {{.To}}`, left out when the group's versions differ (`.To` empty) |
  | `commitMessageSuffix` | none |

  The title is `prTitle` if set (it may reference `{{commitMessage}}` and the parts), else the first line of `commitMessage` (default: the five parts joined by spaces). A top-level action or topic yields to the pin and digest defaults, as Renovate's update-type config overrides it; a rule's own does not. Semantic titles are lowercased like Renovate's (lowercase `semanticCommitType`, `commitMessageLowerCase` not `never`), but only their literal text: dependency and group names keep their case. deps writes versions as they are (`1.2.3`, where Renovate's `prettyNewVersion` reads `v1.2.3` and majors `v2`), and its `prTitle` is also the commit subject; security PRs keep ` [security]`. Any part with an expression the converter lacks (`{{#if}}`, `{{newMajor}}`, …) is reported with `deps keeps its built-in PR titles`, and that scope keeps deps' titles. `semanticCommits: enabled` or `auto` alone changes nothing: deps always writes conventional commits.
- Replacements: Renovate only replaces packages through `replacements:*` presets and rules. With `replacements:all` (part of `config:recommended`) the translation keeps deps' built-in list (`replacements: true`) and adds rules `{updateTypes: [replacement], ignore: true}` for the built-in presets `replacements:all` does not extend (`clang-format-to-maintenance-fork`, `corentinmusard-otel-cicd-action-to-dash0hq`). Without it, it writes `replacements: false`, and each extended `replacements:<name>` becomes rules with that preset's `replacementName`/`replacementVersion`/`replacementPrefix`. `packageRules[].replacementName`/`replacementVersion`, the prefix form of `replacementNameTemplate` (`replacementPrefix`) and `matchUpdateTypes: [replacement]` translate directly; the top-level `replacement` block becomes a rule with `updateTypes: [replacement]`.
- `vulnerabilityAlerts.groupName` becomes a trailing rule `{securityGroup: <groupName>}` without matchers (Renovate forces `vulnerabilityAlerts` over every package rule), unless security updates are off.

### What compatibility mode cannot translate

Each of these is reported when it occurs; the rest of the config still applies.

| Renovate setting | What happens |
|---|---|
| `extends` from npm packages, URLs, relative files | skipped (warning) |
| `replacements:<name>` presets deps' built-in list lacks (`spectre-cli-to-spectre-console-cli`, nuget; `material-ui-to-mui`, `messageFormat-to-scoped`, defined in Renovate's code), `replacementNameTemplate` other than `{{{replace '<prefix>' '<new prefix>' packageName}}}`, `replacementVersionTemplate` | skipped / the field is dropped (warning) |
| built-in `workarounds:*`, `mergeConfidence:*`, `helpers:*` (except `pinGitHubActionDigests`), `security:openssf-scorecard` | skipped (info) |
| `matchCurrentValue` globs (`1.*`), `matchCurrentValue` together with `matchCurrentVersion`, `matchJsonata`, `matchSourceUrls`, `matchRepositories`, `matchBaseBranches`, other unknown `match*`/`exclude*` keys | the rule is dropped |
| `matchPackageNames` together with `matchDepNames` in one rule | the rule is dropped (deps has one `names` list) |
| managers deps lacks (`pyenv`, `cocoapods`, `bazel`, …), unknown categories and dep types | dropped from the rule; a rule left without any is dropped |
| `prCreation: approval` | dropped (warning); `not-pending` is translated with a warning, as deps has no `prNotPendingHours` fallback |
| update types `lockFileMaintenance`, `rollback`, `bump`, `lockfileUpdate` in `matchUpdateTypes` | dropped; a rule left without update types is dropped |
| `pin`, `rangeStrategy`, `versioning`, `extractVersion`, `replacementName`/`replacementVersion` in a rule with `matchUpdateTypes` | the field is dropped (deps decides them before the update type is known) |
| `versioning` in a rule with `matchCurrentVersion`/`matchCurrentValue`/`matchCurrentAge`, `extractVersion` with `matchCurrentAge` | the field is dropped (deps compares the current version under the versioning other rules decide) |
| `prHeader`/`prFooter` with Handlebars helpers, block expressions or fields other than those above | the field is dropped |
| `postUpgradeTasks` commands with `{{templates}}`; `dataFileTemplate`, `installTools`, `workingDirTemplate` | the tasks are dropped (deps passes the update as `DEPS_*` variables); the other keys are ignored. Translated tasks run only with `--allow-tasks` |
| `automergeType: branch`, `automergeStrategy: fast-forward`/`rebase-merge`, `platformAutomerge: false` | dropped; deps always automerges the PR through the platform |
| `dependencyDashboardApproval` without `dependencyDashboard: true` | dropped: deps' `dashboardApproval` needs the dashboard |
| cron schedules with a fixed minute (`0 5 * * 1`) | the entry is dropped (Renovate rejects it too) |
| schedule texts deps' window parser rejects (`every 2 weeks`) | the entry is dropped; a rule left without entries inherits the top-level schedule |
| custom managers with `customType` other than `regex` and `jsonata`, `matchStringsStrategy: combination`/`recursive`, `{{templates}}` in `datasourceTemplate`/`depNameTemplate`/`packageNameTemplate`/`versioningTemplate`, `currentValueTemplate`, `extractVersionTemplate` (regex managers; templated in jsonata managers), `autoReplaceStringTemplate` | the custom manager is dropped |
| custom datasources with templated `defaultRegistryUrlTemplate`, `format: html`, several or other JSONata `transformTemplates` | the datasource is dropped, and every custom manager and rule that uses it |
| `fileMatch`/`managerFilePatterns` on built-in managers | ignored: deps' managers find their files themselves |
| `hostRules` | ignored: credentials come from the environment (`deps help environment`) |
| other `vulnerabilityAlerts` keys (`labels`, `automerge`, `prConcurrentLimit`, `rangeStrategy`, `vulnerabilityFixStrategy: highest`, a non-null `minimumReleaseAge`, …) | ignored: deps has no settings for security updates only (info for labels, assignees, reviewers and PR texts, warning otherwise); `schedule` is ignored by both |
| PR and commit texts (`prBody*`, `prTitleStrict`, `commitBody`, `groupSlug`, …), onboarding and dashboard texts | ignored (info) |
| `prTitle`/`commitMessage*` parts with Handlebars helpers, block expressions or other fields | the scope keeps deps' built-in titles (warning); with `semanticCommits: disabled` it also warns that deps writes conventional commits |

### With an AI agent

The repository ships an agent skill that runs this migration: translate, resolve each warning, compare the plans, wire up the workflow and cut over. It is [`skills/migrate-from-renovate/SKILL.md`](../skills/migrate-from-renovate/SKILL.md), in the common `SKILL.md` format (Claude Code, Codex, Cursor and other agents that load skills from a directory). Fetch it into the target repository, or into your user skill directory:

```sh
mkdir -p .claude/skills/migrate-from-renovate   # or .agents/skills/…, ~/.claude/skills/…
curl -fsSL 'https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/skills/migrate-from-renovate/SKILL.md?ref=main' \
  -o .claude/skills/migrate-from-renovate/SKILL.md
```

Then ask the agent to "migrate this repository from Renovate to deps". An agent without skill support can be pointed at the same URL. The skill never edits the Renovate config before cutover, and asks before taking over Renovate's branches.

## Option mapping

| Renovate | deps |
|---|---|
| `extends: ["local>org/renovate-config"]`, `github>org/repo//path#ref`, `gitea>…` (shared config presets) | `extends: [org/deps-config]`, `org/repo//path.yaml#ref`, or a git URL (`https://…//path#ref`): a `.deps.yaml`-format file of another repository, decoded strictly, its rules before this file's; see [configuration](reference/configuration.md#extends). `--renovate` and `deps migrate` resolve Renovate presets (built-in ones, `local>`, `github>`, `gitea>`, `forgejo>`) into the translation; `.deps.yaml` does not load Renovate's own presets (`config:recommended`, `group:*`, …): see [built-ins](#presets) |
| `repositories`, `autodiscover` + `autodiscoverFilter` (self-hosted, one job for many repositories) | `deps run --repositories org/a,org/*` (`$DEPS_REPOSITORIES`; `owner/<glob>` lists an organization's or user's repositories, without archived ones and mirrors); see [CI](ci.md#one-job-for-many-repositories). No onboarding PR: a repository without `.deps.yaml` runs with the defaults (or `--config`) |
| `enabled: false` (package rule) | `rules[].ignore: true` |
| `enabled: true` for disabled-by-default deps | `rules[].ignore: false` (indirect, Elm `test-indirect`, `go` directive, peerDependencies, engines, git submodules: `managers: [git-submodules]`, Azure Pipelines tasks: `{managers: [azure-pipelines], depTypes: [task]}`) |
| `matchManagers` | `rules[].managers` (list): `github-actions`, `gomod`, `docker` (covers `dockerfile`, `docker-compose`, `kubernetes`, `helm-values`), `npm`, `helm` (covers `helmv3`, `helm-requirements`, `helmfile`, `argocd`, `flux`), `kustomize`, `pre-commit`, `gitlabci` (covers `gitlabci-include`), `nvm`, `nodenv`, `asdf`, `mise`, `cargo`, `pip_requirements` (covers `pip-compile`: `*.in` inputs are updated and the compiled files regenerated), `pep621`, `poetry`, `pipenv`, `terraform`, `terraform-version`, `tflint-plugin`, `terragrunt`, `maven`, `maven-wrapper`, `gradle`, `gradle-wrapper`, `swift`, `homebrew`, `bundler`, `ruby-version`, `git-submodules`, `rust-toolchain`, `devcontainer`, `conda` (Dependabot's `conda` ecosystem; Renovate has none), `bitbucket-pipelines`, `woodpecker`, `droneci`, `circleci`, `azure-pipelines`, `vcpkg` (Renovate has none; Dependabot's `vcpkg` ecosystem), `elm` (no Renovate manager; Dependabot's `elm`), `composer`, `julia`, `bazel-module`, `bazelisk`, `nuget`, `custom` (covers `custom.regex` and `custom.jsonata`; `depTypes: [jsonata]` narrows to the JSONata managers); `deps migrate` lists the managers of a Renovate rule in one deps rule per set of dep types |
| `matchPackageNames`, `matchDepNames` | `rules[].names` (doublestar globs, `/regex/` and `/regex/i`; `{/,}**` works as in Renovate; `!pattern` excludes, a list of only exclusions matches every other name) |
| `excludePackageNames`, `excludePackagePrefixes` | `!` entries in `rules[].names` (`!name`, `!prefix**`) |
| `matchDepTypes` | `rules[].depTypes` (deps names, see [managers](reference/managers.md); Renovate's `golang` = `go`) |
| `matchFileNames` | `rules[].paths` (`!pattern` excludes, as in `names`) |
| `matchCurrentVersion` | `rules[].currentVersion`: a semver constraint (`< 2`) or `/regex/`, `!/regex/` against the locked version, else the current one; not combinable with `versioning` in the same rule |
| `matchCurrentValue` | `rules[].currentVersion` in the regex form: `/re/`, `/re/i`, `!/re/` as written, an exact string `s` as `/^s$/i`; matched against the locked version, else the version as written (the bare version of a range, not Renovate's `^1.2.3`); globs have no equivalent |
| `matchCurrentAge` | `rules[].currentAge` (`> 1 year`, `< 30 days`; a month is 30 days, a year 365): the release time of the current version, looked up only for deps the rule's other selectors match; unknown times match no rule |
| `matchSourceUrls` | not supported: the source repository is only known after a per-dep (for docker: per-image config) lookup; match the packages with `names` instead |
| `matchDatasources` | `rules[].datasources` (`golang-version`, `github-tags`, `github-release-attachments`, `node`, `bitbucket-tags` accepted; `apk`, `deb`, `artifactory`, `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases`, `forgejo-tags` and `endoflife-date` for custom-manager deps) |
| `matchBaseBranches` | `rules[].baseBranches` (names, `/regex/`; `!` excludes, as in `names`; globs are not supported): the base branch `run` resolves; `plan`, `apply`, `explain` and `preview` have none, so such rules match nothing there |
| `matchUpdateTypes` | `rules[].updateTypes`; `pinDigest` = `pin` |
| `matchCategories` | `deps migrate` expands it to the category's managers; in `.deps.yaml` use `managers`/`datasources` (`golang` ≈ `managers: [gomod]` plus `datasources: [golang-version]`) |
| `allowedVersions` | `rules[].allow` (semver constraint or `/regex/`, `!/regex/`) |
| `versioning` | `rules[].versioning` (`semver`, `loose`, `maven`, `ruby`, `nuget`, `regex:…`, plus deps' `tag`, `go`, `docker`) |
| `extractVersion` | `rules[].extractVersion`, marker `extractVersion=` (RE2 with a `version` group, same syntax) |
| `groupName` | `rules[].group` (`groupSlug` is derived: `slug(group)`) |
| `schedule` | `schedule` / `rules[].schedule`: Renovate's time windows as written (`before 6am on monday`, `every weekend`, `after 10pm and before 5am every weekday`, `on the first day of the month`, crons with minute `*` like `* 0-5 * * 1`), evaluated against the run time; or crons of the workflow's `on.schedule` triggers (see below) |
| `timezone` | `timezone` (IANA name; applies to time windows, not to cron triggers) |
| `updateNotScheduled` | `updateNotScheduled` |
| `abandonmentThreshold` | `abandonmentThreshold` / `rules[].abandonmentThreshold` (same units as `rules[].currentAge`): the dashboard's "Abandoned dependencies" section and a PR body warning. Deprecated npm packages and versions, yanked crates and PyPI releases, inactive PyPI projects and archived GitHub repositories (github-releases with a token) are listed under "Deprecated dependencies" without extra requests |
| `minimumReleaseAge` | `minimumReleaseAge` / `rules[].minimumReleaseAge` (`null` → `0 days`) |
| `minimumReleaseAgeBehaviour` | `minimumReleaseAgeBehaviour` / `rules[].minimumReleaseAgeBehaviour` (`timestamp-required`, `timestamp-optional`); the default differs, see below |
| `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch` | same names, top level and per rule |
| `pinDigests`, `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` (top level or per rule) |
| `rangeStrategy` | `rangeStrategy` / `rules[].rangeStrategy` for npm, cargo and Python (PEP 440, Poetry) ranges, composer constraints, terraform constraints and Gradle dynamic versions: `auto` (default, = `update-lockfile`; `widen` for npm peerDependencies and `||` ranges and composer constraints with alternatives, `replace` for terraform), `bump`, `update-lockfile`, `replace`, `widen`, `pin`, `in-range-only` (`widen` and `pin` are `replace` for terraform and Gradle). Other managers replace the exact version |
| `postUpdateOptions: [gomodTidy]` | always on |
| `postUpdateOptions: [gomodUpdateImportPaths]` | `gomodUpdateImportPaths`, **default true**: major module path updates (`/v92` → `/v93`) rewrite the imports in the module's `.go` files; `false` turns module path updates off |
| `postUpdateOptions: [gomodVendor]` | always on: a module with `vendor/modules.txt` is vendored again after tidy (`go mod vendor`), a workspace with `vendor/modules.txt` next to its `go.work` with `go work vendor`; only changed vendor files are committed. `go.work` `go`/`toolchain` directives are updated; `go work sync` is not run |
| `lockFileMaintenance` | `lockFileMaintenance: {enabled, schedule}` for `Cargo.lock` (`cargo update`, one PR on `<branchPrefix>lock-file-maintenance`); npm and Go lockfiles are regenerated only with an update |
| `postUpgradeTasks: {commands, fileFilters, executionMode}` | `rules[].postUpgradeTasks`: a list of commands (`executionMode: branch`) or `{commands, executionMode, files}` with `branch` (the default) or `update`. They run after the built-in post-processors in a temporary copy of the work tree that is a throwaway git repository of the base (git works; submodule pointers must not change); every changed file below `--dir` not ignored by `.gitignore` is committed. Commands are not templated: update mode passes the update in `DEPS_DEP_NAME`, `DEPS_NEW_VERSION`, `DEPS_PACKAGE_FILE` and friends (`{{{depName}}}` → `"$DEPS_DEP_NAME"`, e.g. `helm dependency update "charts/$DEPS_DEP_NAME"`). `fileFilters` → `files` (doublestar globs relative to `--dir`, `!` excludes; default every non-ignored change); `dataFileTemplate` and `installTools` are not supported |
| `allowedCommands` (self-hosted, `RENOVATE_ALLOWED_COMMANDS`) | `--allow-tasks` (repeatable) / `DEPS_ALLOW_TASKS` / action input `allow-tasks` (one per line): glob patterns instead of regexes, matched whole with `*` for any text without `;`, `&`, `|`, backticks, `$(` or line breaks (`^go generate .*$` → `go generate *`; `.*` → `*`, which alone allows everything); a PR with a command no pattern allows fails |
| `branchPrefix` | `branchPrefix` |
| `baseBranchPatterns` (formerly `baseBranches`; Dependabot `target-branch`) | `baseBranches`: names, `$default` and `/regex/` entries, unfolded as in Renovate; with more than one entry or a regex one, branches are `<branchPrefix><base>/…` (Renovate: `<branchPrefix><base>-…`), and PR state, cleanup and the dashboard are per base. PR titles get no ` (<base>)` suffix. `run --base` overrides it |
| `useBaseBranchConfig` | not supported: every base uses the config of the checkout (Renovate's default `none`) |
| `semanticCommits`, `:semanticPrefixFixDepsChoreOthers` | on by default; `rules[].commitType`, `rules[].commitScope`. `semanticCommits: disabled` is a `prTitle` without the `{{.Type}}(…):` prefix (`deps migrate` writes it) |
| `commitMessage`, `commitMessagePrefix`, `commitMessageAction`, `commitMessageTopic`, `commitMessageExtra`, `commitMessageSuffix`, `commitMessageLowerCase` | `prTitle` / `rules[].prTitle`: one Go template for the commit subject and the PR title, which are always equal (Renovate's default); the commit body stays deps' list of updates. `deps migrate` composes it (see [above](#compatibility-mode-and-deps-migrate)) |
| `prTitle` | `prTitle` / `rules[].prTitle`, a Go `text/template` over `.Type`, `.Scope`, `.Name`, `.Group`, `.From`, `.To`, `.UpdateType`, `.Count`, `.IsSecurity` (`{{depName}}` → `{{.Name}}`, `{{newVersion}}` → `{{.To}}`, see [`prTitle`](reference/configuration.md#prtitle)); security titles keep ` [security]` |
| `prHeader`, `prFooter` | `prHeader`, `prFooter` / `rules[].prHeader`, `rules[].prFooter` (templates over the same fields; `deps migrate` converts plain text and the simple field references) |
| `prBodyTemplate`, `prBodyColumns`, `prBodyDefinitions`, `prBodyNotes` | not supported: `prHeader` and `prFooter` frame deps' fixed body |
| `gitAuthor` | `gitAuthor` (or `DEPS_GIT_AUTHOR`, `--author-name/--author-email`); ignored for platform commits, as in Renovate |
| `gitIgnoredAuthors` | `gitIgnoredAuthors` |
| `platformCommit` (`RENOVATE_PLATFORM_COMMIT`) | `platformCommit` (`DEPS_PLATFORM_COMMIT`): same values and default (`auto`). GitHub only; `auto` also needs no signing key, and `enabled` with a signing key or on Gitea/Forgejo/GitLab is a usage error |
| `prConcurrentLimit`, `prHourlyLimit` | same names; **default 0 (unlimited)** in deps, 10/2 in Renovate; `run --max-new-prs` (default 10) caps each run |
| `rebaseWhen`, `:rebaseStalePrs` | `rebaseWhen` (`conflicted` default, `behind-base-branch`, `never`) |
| `rebaseLabel`, PR body rebase/retry checkbox | `rebaseLabel` (default `rebase`) and the same checkbox in every PR body: either requests a rebuild on the next run (bypassing the schedule, discarding commits by others); the label is removed and the box unticked afterwards |
| `stopUpdatingLabel` | `stopUpdatingLabel` (default `stop-updating`): the PR is not pushed, rebased or closed as obsolete (`skipped: stop-updating label`) |
| `ignorePaths`, `:ignoreModulesAndTests` | `ignorePaths` (Renovate's list is the default when absent) |
| `vulnerabilityAlerts`, `osvVulnerabilityAlerts` | `security` (OSV, default on): Go modules and go.mod `toolchain`/`go` directives (OSV `stdlib`/`toolchain`), npm, crates.io, PyPI, Maven, RubyGems, Packagist, GitHub Actions; no advisories for images, Terraform or Helm (no OSV ecosystem) |
| `vulnerabilityAlerts.groupName` (Dependabot `groups.<name>.applies-to: security-updates`) | `rules[].securityGroup`: security updates of matching deps share one PR on `<prefix>group/<slug>-security`, apart from the non-security group of the same name; titles keep `[security]` |
| `customManagers` (`customType: regex`) | `custom[]` with `match` (Renovate group names accepted) |
| `customManagers` (`customType: jsonata`, `fileFormat`) | `custom[]` with `jsonata` and `fileFormat`, one entry per `matchStrings` query (the same JSONata 2.x language, the same result fields). A literal `extractVersionTemplate` is merged into the query's results (`(<query>) ~> $map(function($d) { $merge([$d, {"extractVersion": …}]) })`), a literal `registryUrlTemplate` becomes `custom[].registryUrl`. Unlike Renovate's templates, `custom[].datasource`/`name`/`versioning`/`registryUrl` are defaults the query's result fields override |
| `managerFilePatterns` / `fileMatch` | `custom[].files` (doublestar globs or `/regex/`; `fileMatch` entries are regexes, so write them as `/…/`) |
| `datasourceTemplate`, `depNameTemplate`, `versioningTemplate` | `custom[].datasource`, `name`, `versioning` (literal values, no templates) |
| `registryUrlTemplate`, regex group `registryUrl` | `custom[].registryUrl` (literal values; a templated one is ignored with a note: capture the URL in a `registryUrl` group instead) and the `registryUrl` group, for `helm`, `gitlab-packages`, `rpm`, `apk`, `deb`, `artifactory`, `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases`, `forgejo-tags` and `endoflife-date` deps. `packageRules[].registryUrls` has no equivalent: set the registry on the custom entry |
| datasource `rpm` (yum/dnf repositories), versioning `rpm` | `rpm` datasource and versioning; rpm deps default to `rpm` versioning, versions are `<ver>-<rel>` as in Renovate |
| datasource `apk` (Alpine/Wolfi repositories), versioning `apk` | `apk` datasource and versioning; apk deps default to `apk` versioning |
| datasource `deb` (APT repositories), versioning `deb` | `deb` datasource and versioning; deb deps default to `deb` versioning; registry URLs as in Renovate (`https://deb.debian.org/debian?suite=bookworm&components=main&binaryArch=amd64`) |
| datasource `artifactory` (Artifactory folder listings) | `artifactory` datasource: the folder path below the `registryUrl`s (whitespace-separated, merged), or the full folder URL without one |
| `customDatasources` (`defaultRegistryUrlTemplate`, `format`, `transformTemplates`) | `customDatasources` (`url`, `format`, `versions` selector + `versionRegex` instead of JSONata) |
| `github-release-attachments` digests | a regex `currentDigest` group with datasource `github-release-attachments` is kept current like Renovate: the asset of the current release with that sha256 is found and the same asset of the new release is used; `custom[].checksum.asset` / `checksum.url` name the asset explicitly |
| `hostRules` (tokens) | env tokens, `.npmrc`, Docker credentials, git credential helper; retries are built in |
| `platform`, `endpoint`, `token` (`RENOVATE_PLATFORM`, `RENOVATE_ENDPOINT`, `RENOVATE_TOKEN`) | detected from the CI environment (`GITLAB_CI`, `GITHUB_ACTIONS`, …) or the `origin` remote (`github.com`, `gitlab.com`, else API probes); override with `run --platform github\|gitea\|forgejo\|gitlab --api-url <endpoint> --token`, or set `DEPS_TOKEN` (`GITLAB_TOKEN` on GitLab). GitLab merge requests use `merge_when_pipeline_succeeds` for `automerge`, pipelines as checks and issues for the dashboard (see [GitLab CI](ci.md#gitlab-ci)); Bitbucket, Azure DevOps and Gerrit are not supported |
| `automerge` | `automerge` / `rules[].automerge` (platform auto-merge once required checks pass; a PR automerges when all its updates do) |
| `automergeType: pr`, `automergeType: branch` | `automergeType: pr` or `branch` (top level and per rule). `branch` pushes the branch without a PR and fast-forwards the base to it once its checks pass; failed checks or a refused push open the PR. `pr-comment` is not offered |
| `draftPR` | `draftPR` (top level and per rule): GitHub draft PRs; on Gitea and Forgejo the title starts with `WIP: `, on GitLab with `Draft: `. Applied when the PR is created; PRs that automerge are never drafts |
| `prCreation: immediate`, `not-pending`, `status-success` | `prCreation` (top level and per rule; a group takes the most waiting mode of its updates): the branch is pushed first and a later run opens the PR once its checks finished or all passed; waiting branches are on the dashboard under *Pending status checks*. `approval` and `prNotPendingHours` are not offered: a branch without checks waits until ticked on the dashboard |
| `automergeStrategy` | `automergeStrategy`: `auto`, `merge`, `squash`, `rebase` (Renovate's `merge-commit` = `merge`; `fast-forward` and `rebase-merge` are not offered) |
| `platformAutomerge` | always on for PRs: deps automerges PRs through the platform (GitHub falls back to a direct merge once checks are green); with `automergeType: branch` it pushes to the base itself |
| `ignoreTests` | not supported: deps never merges a PR without green checks |
| `labels`, `addLabels`, `reviewers`, `assignees` | same names, top level and per rule: a rule's `labels` replaces the labels, its `addLabels` adds to them. deps has no top-level `addLabels`; the translation (`--renovate`, `deps migrate`, `deps init --from-renovate`) appends Renovate's to `labels` |
| `:dependencyDashboard`, `dependencyDashboardTitle`, `dependencyDashboardLabels` | `dependencyDashboard: true`, `dependencyDashboardTitle`, `dependencyDashboardLabels` |
| `dependencyDashboardApproval` (top level or in `packageRules`) | `dashboardApproval: true` at the top level or in `rules` (needs `dependencyDashboard: true`): no PR until the *Pending approval* checkbox is ticked; security updates need no approval, as with Renovate's `vulnerabilityAlerts` default |
| Mend badges, `security:openssf-scorecard` | not supported (release age as text in PR bodies) |
| repository cache | not applicable (no persistent cache) |

Renovate options left in `.deps.yaml` are unknown keys, and the error names the deps equivalent from this table, for example `.deps.yaml:3:5: rules[0].matchPackageNames: unknown key (Renovate's matchPackageNames is names here)`.

## Marker compatibility

Annotation-mode `custom[]` entries accept Renovate's markers unchanged:

```
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_VERSION=v2.5.0
```

- `packageName` wins over `depName`, `versioning=` takes a scheme name, `extractVersion=` maps release names (`extractVersion=^jq-(?<version>.+)$`), `registryUrl=` is the registry of `helm`, `gitlab-packages`, `rpm`, `apk` and `deb` deps, the API base URL of `endoflife-date` deps and the instance of `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases` and `forgejo-tags` (`# renovate: datasource=gitlab-tags depName=group/project registryUrl=https://gitlab.example.com`; ignored for others), and unknown keys are ignored.
- The marker applies to the **next line's** first version token. A Renovate regex that spans more lines needs a regex-mode `custom[]` entry instead.
- deps' own `# deps: <datasource> <name>` form works the same way.

In regex mode, Renovate's group names `currentValue`, `depName`, `packageName` and `currentDigest` are accepted, and so is its `(?<name>…)` syntax. Escaping differs, though: JSON5's `\\d` is YAML's `\d` in single quotes.

A `customManagers` entry's literal `registryUrlTemplate` becomes the entry's `registryUrl` (a templated one is ignored), as does a `(?<registryUrl>…)` group; both apply to `helm`, `gitlab-packages`, `rpm`, `apk` and `endoflife-date` deps and the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags datasources. `datasource=gitlab-packages packageName=<project>:<package>` works as in Renovate; without `registryUrl` deps asks the configured GitLab instance (default gitlab.com).

## Branch prefix takeover

With `branchPrefix: renovate/`, deps manages the branches and PRs Renovate created:

- **Branch names.** deps names branches `renovate/<manager>/<slug>` and `renovate/group/<slug>`, which are not Renovate's names (`renovate/<depName>-<n>.x`, `renovate/<groupSlug>`).
- **Existing PRs.** deps does not adopt Renovate's existing PRs. It opens its own, and once a run completes without lookup errors, it closes every open PR under the prefix that matches none of its branches (a PR whose build or push failed counts as one of them).
- **Branch authors.** Branches whose tip was committed by another author are never touched. If the old bot used another identity, add its email to `gitIgnoredAuthors`.
- **Migration steps.** Disable Renovate first, then run `deps run --dry-run` to see the planned PRs.

## Behavior differences

- **Loose versioning zero-fill.** In `loose`, a missing part compares as 0, so `1877` < `1877.25`. Renovate ranks the shorter version higher.
- **Bucket flags on `updateTypes` rules.** deps evaluates `separateMajorMinor`/`separateMultipleMajor`/`separateMinorPatch` with the policy of each candidate's type, so a rule with `updateTypes` *can* set them. Renovate decides buckets before the update type is known and ignores these flags on `matchUpdateTypes` rules. Configs that set them on manager rules, as Renovate requires, behave the same in deps.
- **`separateMultipleMajor` branch suffix.** deps uses `-v<N>` where Renovate writes `<N>.x`.
- **`rangeStrategy`.** deps supports every Renovate value for npm ranges: `auto`, `bump`, `update-lockfile`, `replace`, `widen`, `pin` and `in-range-only`. `auto` is `update-lockfile`, like Renovate's npm manager for simple ranges: a version the range admits only moves the lockfile, a version outside it replaces the range's version (`^5.2.8` → `^6.0.0`, Renovate's `replace`), and versions are compared with the locked one. Under `auto` peerDependencies are widened (`^1.0.0` → `^1.0.0 || ^2.0.0`), as in Renovate. Locked versions come from `package-lock.json` (v1–v3), `npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock` (classic and berry) and `bun.lock`, as in Renovate. A lockfile-only update locks exactly the proposed version with every tool: for yarn, Renovate runs `yarn upgrade <name>` (classic) or `yarn up -R <name>` (berry), which take the newest version the range admits, while deps uses `yarn set resolution` (berry) or moves the descriptor to a pinned install's entry (classic); for pnpm both run `pnpm update --no-save <name>@<version>`. npm's whole range grammar is rewritten in its own form (`||`, `x`/`*` wildcards, partial versions, `<`, hyphen ranges, `v1.2.3`): `auto` widens `||` ranges (`^1.0.0 || ^2.0.0` → `^1.0.0 || ^2.0.0 || ^3.0.0`) as Renovate does; only `*`/`x` (any version) stays `unsupported range`. `widen` keeps lower bounds and moves upper ones (`>=1.2.3 <2` → `>=1.2.3 <4`) or appends an alternative (`^1.2.3` → `^1.2.3 || ^2.0.0`); `pin` proposes a `pin` update to the locked (else newest admitted) version unless an update in the same PR bucket pins the range anyway, then writes every newer version exactly; `in-range-only` is `bump` limited to versions the range admits. cargo (`=1.4.0`, widen `1.2` → `>=1.2, <3`) and Python (`==1.4.0`, widen `>=1.2,<2` → `>=1.2,<4`, Poetry `^2.13.0` → `^2.13.0 || ^3.0.0`) follow the same rules. OCM's `rangeStrategy: bump` for `golang-version` needs no translation: go.mod `toolchain` and `go` directives are replaced at their precision.
- **Python specifiers.** `rangeStrategy` applies to every PEP 440 and Poetry range (`>=1.2,<2`, `<2`, `~=1.4`, `^1.2`, `>=1.2 <2`, `^1 || ^2`), with the versions `uv.lock`, `poetry.lock`, `pdm.lock` and `Pipfile.lock` lock. deps' `auto` is `update-lockfile` for all Python managers; Renovate's is that for poetry only and `replace` for pep621, pipenv and pip_requirements, so deps also moves those lockfiles for versions the range admits. Ranges are rewritten with Renovate's pep440 rules (`>=1.2,<2` → `>=3.1,<4`); under `bump`, a version outside the range gets the `replace` rewrite too, and a Poetry `||` constraint gets a new alternative instead of Renovate's npm-style rewrite. Poetry and pdm lockfile-only updates move to the newest admitted version (`poetry update --lock <name>`), which can be newer than proposed.
- **Composer constraints.** `rangeStrategy` applies to every composer.json constraint (`^1.2`, `~1.2.3`, `>=1.0 <2.0`, `1.2.*`, `^1.0|^2.0`, `@dev` flags) as Renovate's composer versioning rewrites it, with the versions composer.lock locks; `auto` is `update-lockfile`, `widen` for constraints with alternatives (and `bump` of those widens, as in Renovate). Renovate also widens for `"type": "typo3-cms-extension"`; in deps set `rangeStrategy: widen` for such projects. Lockfile updates run `composer update <pkg>:<version> --with-dependencies --minimal-changes` (Composer 2.7+). See the [composer manager](reference/managers.md#composer).
- **`matchCategories`** is not a `.deps.yaml` key. Write the managers or datasources explicitly (`deps migrate` does).
- **Presets** are only resolved by `--renovate` and `deps migrate`. In `.deps.yaml`, the built-ins below cover the common ones.
- **Schedules.** Renovate's schedules are time windows, and deps evaluates them the same way: against the time of the run, in `timezone`. Text windows support the common subset of Renovate's later syntax (`before`/`after` times, `every weekday`/`weekend`/`<day>`, `on <days>`, `on the <n> day of the month`, `every <n> months`, `at any time`); cron schedules need minute `*`, as in Renovate. The workflow must run inside the windows (e.g. an hourly cron). A cron with a fixed minute (`0 0 * * 0`) is instead a *trigger*: a group is in schedule when the run was started by that cron of the workflow (`workflow_dispatch` matches every trigger, `push` none). Both gate only creating PRs (and updating them with `updateNotScheduled: false`). OCM's `* * * * 0` (Sunday) works as is, given a run on Sundays; the [OCM translation](#workflow-for-ocm) uses the trigger `0 0 * * 0` in both `on.schedule` and the rules instead, next to a daily `0 0 * * *` cron for unscheduled deps, so pushes on Sundays do not open the weekly groups.
- **Release age.** An unknown release time does not block an update by default (`minimumReleaseAgeBehaviour: timestamp-optional`); Renovate 42+ defaults to `timestamp-required`, which holds such releases back. Set `minimumReleaseAgeBehaviour: timestamp-required` to match it. In deps, custom datasources have no release times, and an image has one only when its config carries `created` (or the registry lists times). Image digest refreshes, pins and security updates are always exempt. Go pseudo-version digest updates carry a commit time and wait like Renovate's, so a Renovate `minimumReleaseAge: null` rule for `digest` translates to `updateTypes: [digest], minimumReleaseAge: '0'`. When the newest candidate is too new, deps proposes the newest one that is old enough; Renovate waits.
- **Indirect Go requirements** are ignored by default, as in Renovate. Opt in with `depTypes: [indirect], ignore: false`.
- **Actions pinned to a bare SHA** (`uses: owner/repo@<sha>` without a `# vX` comment). Renovate skips them as `unversioned-reference` (disabled; a `packageRules` `enabled: true` turns on digest updates to the default branch). In deps a SHA that a tag points at is that tag's version and is updated like a pinned ref, writing the new version as a comment (`@<new sha> # v1.2.0`); Renovate leaves it alone. Any other SHA is ignored by default like in Renovate; translate `enabled: true` to a rule with `ignore: false`, which gives the same digest updates to the default branch's `HEAD`.
- **PR limits.** Renovate defaults to `prConcurrentLimit: 10`, `prHourlyLimit: 2`; deps to `0`/`0` (unlimited), plus the per-run `--max-new-prs 10`. The effective limit is the minimum of the non-zero limits.
- **Automerge.** deps always uses the platform's auto-merge for PRs (Renovate's `platformAutomerge: true`) and never merges by itself while checks are pending. On GitHub repositories that disallow auto-merge, deps merges directly, but only a mergeable PR with at least one status or check run, all green; a repository without any checks is never merged that way, and there is no `ignoreTests` to change that. `automergeType: branch` works like Renovate's: one branch per run, only when it is up to date with the base (deps rebuilds it first); failed checks open the PR (there is no `prNotPendingHours`: a branch whose checks never report waits, listed on the dashboard).
- **Dependency dashboard.** The checkboxes are `create` (pending approval, awaiting schedule, rate-limited), `recreate` (closed PRs), `rebase` (open PRs, modified branches) and `pr` (pending branch automerge: open a PR instead, Renovate's "Pending Branch Automerge" section; pending status checks of `prCreation`: open the PR now, Renovate's "Pending Status Checks"). Approving an update (Renovate's "Pending Approval" section) is the same `create` request, so the PR is created right away, regardless of schedule and limits. There are no deprecation warnings; the dashboard additionally lists the run's errors. Detected dependencies are listed per manager and file, capped to stay below GitHub's body limit.
- **One PR per dependency or group.** Renovate's `<depName>-<major>.x` style branch-per-major-line maps to PR buckets.
- **Closed PRs.** As in Renovate, closing a PR without merging ignores its update: for minor and patch updates, digests and pins that version or digest (the next one gets a PR); for a major update (`-major`, `-v<N>` branches) that major, so later versions of it get no PR until the next major (`skipped: closed PR for this major`). deps matches closed PRs by the targets recorded in their body (`deps:versions` marker), not by title, so changing `prTitle` keeps them ignored. A closed PR without security fixes does not hold back a security update. The dashboard lists these updates under *Ignored or blocked*; its `recreate` checkbox lifts the suppression.

### Presets

| Renovate preset | deps |
|---|---|
| `group:monorepos`, `group:kubernetesMonorepo`, `group:goOpenapi` | `monorepos: true` (default; 62 built-in entries) |
| `replacements:all` (part of `config:recommended`) and its `replacements:*` presets | `replacements: true` (default; generated from Renovate's `lib/data/replacements.json`, every preset deps can write); see `deps explain` (`builtin replacement ...`). Not included: the nuget preset and the material-ui and messageformat presets Renovate defines in code |
| `packageRules[].replacementName` / `replacementVersion`, `matchUpdateTypes: [replacement]` | `rules[].replacementName` / `replacementVersion`, `updateTypes: [replacement]`. `replacementNameTemplate: "{{{replace 'a/' 'b/' packageName}}}"` is `replacementPrefix: a/` with `replacementName: b/`; other templates have no equivalent. Unlike Renovate, a replacement gets its own `-replacement` branch and closing its PR brings the dep's usual updates back |
| `:ignoreModulesAndTests` | default `ignorePaths` |
| `:semanticPrefixFixDepsChoreOthers` | default commit types |
| `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` |
| `:rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `config:recommended` / `config:best-practices` | the defaults above plus `security: true` and `separateMajorMinor: true` |

## Worked example: open-component-model

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
| 3, 5: `matchManagers` + `separateMinorPatch: false` + schedule | `managers` rules |
| 4, 6: `matchManagers` + `matchUpdateTypes: [minor, patch]` + `groupName` | `updateTypes: [minor, patch]` + `group` |
| 7: "Digest pins" over `github-actions`, `dockerfile`, `docker-compose` with `[digest, pinDigest]` | two rules: `github-actions`, and `docker` with `depTypes: [dockerfile, compose, workflow, action]` (Renovate's github-actions manager also covers workflow `container:`/`services:` images), both `updateTypes: [digest, pin]` |
| 8: `matchDatasources: [golang-version]`, `rangeStrategy: bump` | `datasources: [golang-version]` (no `rangeStrategy` needed) |
| 9, 20, 21, 22: `enabled: false` for kube-openapi, go-ini/ini, crypto11, `open-component-model/.github` | `ignore: true` rules |
| 10: "Kubernetes dependencies" over `gomod` and `custom.regex` | two rules (`managers: [gomod]`, `managers: [custom]`) with the same `names` and `group` |
| 11, 15, 16, 17: "Docker dependencies", "Golang X Repositories", "Google Golang Repositories", "spf13 repositories" | `names` + `group` + `schedule` |
| 12: "Garden Linux images", 13: "OCM cosign" (`versioning: 'regex:…'`, `minimumReleaseAge: '0 days'`), 14: "Helm" | same values |
| 18: `matchCategories: [golang]`, `enabled: true`, `postUpdateOptions: [gomodTidy]`, schedule | `managers: [gomod]`, `ignore: false`, `schedule` (tidy is always on) |
| 19: indirect majors off | `depTypes: [indirect]`, `updateTypes: [major]`, `ignore: true` |
| 23: "Website CLI-docs generator dependencies" | `paths` + `group` + `separateMinorPatch: false` |
| 24: `matchDepTypes: [golang]` off | `depTypes: [go]`, `ignore: true` |
| 25: npm `minimumReleaseAge: 28 days` | `datasources: [npm]` rule |
| 26: `matchUpdateTypes: [pin, digest, …]`, `minimumReleaseAge: null` | `updateTypes: [pin, digest]`, `minimumReleaseAge: '0'` (Go pseudo-version digests) |
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
      - uses: jakobmoellerdev/deps@v0.4.0
        with:
          token: ${{ steps.app.outputs.token || github.token }}
          version: v0.4.0 # keep this when pin: digest rewrites the ref above to a SHA
          args: ${{ github.event_name == 'pull_request' && 'plan' || 'run' }}
          config: .github/deps.yaml
          show-skipped: true
          log-level: info
```

- **Pull requests** that change the config run `plan` with the job token. That validates the config: an invalid key exits 2. `run --dry-run` needs `HEAD` at the tip of `main`, which a PR merge commit is not. To also list the PRs a config change would cause, use the base/PR checkout pair from [ci.md](ci.md#preview-on-pull-requests).
- **Token.** The App token goes to both `actions/checkout` and the action. The action creates the commits and moves the `deps/*` branches through the API with it; git uses the checkout's credentials only to list and delete branches. `permission-workflows: write` is required, because Action updates change `.github/workflows/*`: GitHub checks it for API commits too (without it, creating the tree fails with `403 Resource not accessible by integration`).
- **Version.** The action downloads the release named by its ref when that ref is a tag. `pin: digest` turns `@v0.4.0` into `@<sha> # v0.4.0` in the first "Digest pins" PR, and from then on the ref no longer names the release. Without `version:`, the action would download the latest release.
- **Signing.** Renovate runs with `RENOVATE_PLATFORM_COMMIT: enabled`, and so does deps: `platformCommit: enabled` in the example config (`auto` would choose it too, because the token is an App installation token). deps then creates the commits through the GitHub API, and GitHub signs them: they show as verified, authored by `ocmbot[bot]` (`<id>+ocmbot[bot]@users.noreply.github.com`), committed by GitHub. `gitAuthor` only applies to git commits. Don't pass `signing-key`: it conflicts with `platformCommit: enabled`. Verified commits satisfy the `required_signatures` rule of both rulesets that cover `deps/*` ("OCM Organization Ruleset" and the repository's "All branches"). The rulesets also restrict branch creation, deletion and non-fast-forward updates, so the branches still need ocmbot's bypass (App 295948 is an `always` bypass actor of both) or an exclusion, see the [cutover](#cutover-for-ocm).
- **Not needed.** Renovate's repository cache steps (deps has no cache) and `RENOVATE_ALLOWED_COMMANDS` (`renovate.json5` has no `postUpgradeTasks`; deps' equivalent is the `allow-tasks` input).

### Comparison with Renovate

The run below went against a fresh clone of OCM `b4b5473`, with a GitHub token. A dry run makes no writes.

```sh
DEPS_GITHUB_COM_TOKEN=$(gh auth token) DEPS_TOKEN=$(gh auth token) \
  deps run --dry-run --show-skipped --config ~/Projects/deps/docs/examples-ocm.deps.yaml --trigger-schedule '0 0 * * 0'
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
3. Compare a weekly run with Renovate's, as above. Then disable `renovate.yml`, set `branchPrefix: renovate/`, drop `dependencyDashboardTitle`, and close #331. deps closes Renovate's leftover `renovate/*` PRs after its first run without lookup errors.

### What is still missing for OCM

Each item comes from the comparison above or from [renovate-parity.md](renovate-parity.md):

1. **Fallback versus waiting.** deps proposes the newest release that is old enough (node 26.11.1, go-winio v0.6.3); Renovate waits for the newest (26.13.0, v0.6.4). This is a design difference, documented under P10, not a bug. It is listed because it changes which PRs OCM sees.
3. **Mend badges and the OpenSSF scorecard.** PR bodies show the release age as text, with no adoption, passing or confidence badges (L5) and no scorecard badge (`security:openssf-scorecard`, L6).
4. **Renovate dashboard extras:** the repository problems list (e.g. hidden Unicode warnings) has no deps equivalent. The "Abandoned Dependencies" section maps to [`abandonmentThreshold`](reference/configuration.md#abandonmentthreshold), and deprecated packages are listed under "Deprecated dependencies".
5. **Cutover steps not done:**
    - a real (non-dry) trial run in OCM;
    - App permissions confirmed for the installation (checks/statuses read, workflows write);
    - the ruleset entry or bypass for `deps/**`;
    - the branch takeover (`branchPrefix: renovate/`) after Renovate is disabled.
