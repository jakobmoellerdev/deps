# Migrating from Renovate

deps reads `.deps.yaml`, not `renovate.json(5)`. Most options translate one to one. Some behaviors that Renovate gets from presets are built in. This page explains the translation, the known differences, and a worked translation of open-component-model's config; the option mapping is the generated [Renovate options](reference/renovate-options.md) reference. Feature status is tracked in [renovate-parity.md](renovate-parity.md).

## Compatibility mode and `deps migrate`

deps can do the translation below itself:

- `deps plan|apply|run|explain --renovate` translates the repository's Renovate config in memory and runs from it. `--config` naming a Renovate file (`renovate.json5`, `.renovaterc`, …) does the same.
- `deps migrate` writes that translation as a commented `.deps.yaml` (`--output -` prints it). The Renovate config is not touched.
- `deps init --from-renovate <file>` writes a new `.deps.yaml` from the same translation: the starter file (schema modeline, a comment naming the detected managers) plus the translated settings. `deps config check` validates the result.

Both use one translator, so a migrated file behaves exactly like compatibility mode. It reads every file name Renovate reads (`renovate.json(c|5)`, the same under `.github/`, `.gitlab/`, `.gitea/`, `.forgejo/`, `.renovaterc*`, the `renovate` key of `package.json`) and resolves `extends`: Renovate's built-in presets from a table, `github>` (github.com), `gitlab>` (gitlab.com), `gitea>`, `forgejo>` and `local>` (the repository's forge: GitHub, GitLab, Gitea or Forgejo) presets by fetching them. npm and URL presets are skipped with a warning. `ignorePresets` entries are skipped wherever they appear in the `extends` chain, matched exactly as written (with parameters).

- Settings deps cannot express are reported as `warn <setting>: <msg>`, settings without effect on which updates or PRs exist (labels, PR texts, `groupSlug`, …) as `info`. `migrate` prints them and writes them as the file's head comment; compatibility mode logs them.
- A rule with an untranslatable matcher (`matchJsonata`, a `matchCurrentValue` glob, `matchPackageNames` together with `matchDepNames`) is dropped as a whole, an untranslatable effect only by itself.
- `branchPrefix` follows Renovate (`renovate/` unless set), so deps takes over Renovate's branches (see [below](#branch-prefix-takeover)). `prConcurrentLimit` and `prHourlyLimit` keep Renovate's defaults 10 and 2, `ignorePaths` Renovate's default list. Renovate's `additionalBranchPrefix` (top level and per rule) is kept when it uses only literal text and `{{manager}}`, `{{parentDir}}` and `{{packageFileDir}}`; other templates are dropped with a warning.
- Schedules and `timezone` are kept as written: deps evaluates Renovate's time windows (text and crons with minute `*`) against the run time, like Renovate. The workflow must run inside every window, e.g. hourly; `migrate` lists the windows. A cron with a fixed minute is dropped, as Renovate rejects it.
- `matchManagers` and `matchCategories` expand to `rules[].managers` lists, one rule per set of dep types (managers whose dep types are not restricted share one): `github-actions` matches the `github-actions` manager and the `docker` manager's `workflow`/`action` images, `dockerfile` the `docker` manager's `dockerfile` and `install` dep types. `enabledManagers`, `<manager>.enabled: false`, Renovate's default-off `pre-commit`, `azure-pipelines` and `nix` (`azure-pipelines: {enabled: true}` becomes `{managers: [azure-pipelines], ignore: false}`, which also updates its tasks, as Renovate does; `nix: {enabled: true}` simply drops the exclusion, since deps enables nix by default) and the deps managers Renovate does not have (`conda`, `vcpkg`, `elm`) become trailing `ignore: true` rules.
- Renovate's `/regex/` and `!negated` patterns in package names, file names and custom manager files work in deps as they are.
- Renovate's dashboard, git and gomod/composer options land under the `dashboard`, `git`, `gomod` and `composer` sections (`dependencyDashboard` → `dashboard.enabled`, `gitAuthor` → `git.author`, …); `dependencyDashboardApproval` (top level and per rule) becomes `dashboardApproval`, the `rust-toolchain` manager is in the `rust` category and the `devcontainer` manager in the `docker` category.
- `postUpgradeTasks` (top level and per rule; `fileFilters` → `files`), every `rangeStrategy` value, and the `bundler`, `ruby-version`, `swift`, `mint` and `xcodegen` managers (categories `ruby`, `swift`) are translated, and so are `pyenv` and `runtime-version` (category `python`; deps reads them with the `python-version` datasource where Renovate uses `docker`).
- The `bun-version` (category `js`; Renovate's dep name `Bun` is deps' `bun`, the npm package both look up) and `terragrunt-version` (`terraform`) managers are translated.
- The `bitbucket-pipelines`, `woodpecker`, `crow`, `droneci`, `circleci`, `azure-pipelines`, `cloudbuild`, `velaci`, `travis`, `buildkite`, `bitrise` and `jenkins` managers (category `ci`; travis majors stay disabled by default as in Renovate; Buildkite plugins on Bitbucket, Renovate's `bitbucket-tags`, use `git-tags`; datasources `bitrise` and `jenkins-plugins`), `composer` (`php`), `julia` (`julia`; Renovate's datasource `julia-general-metadata` is deps' `julia-registry`), `bazel-module` and `bazelisk` (`bazel`), `nuget`, `cake` and `paket` (`dotnet`), `homeassistant-manifest` (`python`), `ansible` and `ansible-galaxy` (`ansible`, `iac`; datasources `galaxy` and `galaxy-collection`), `sbt`, `scalafmt`, `osgi`, `smithy`, `kotlin-script`, `leiningen`, `deps-edn` and `ant` (`java`; datasource `clojure`), `mix` and `gleam` (`elixir`), `cocoapods` (`swift`; datasource `pod`), `glasskube` (`kubernetes`, `cd`; datasource `glasskube-packages`, versioning `glasskube`), `nix` (no category), `quadlet` (`docker`), `crossplane` (`kubernetes`, `iac`), `tekton` (`ci`, `cd`), `fleet` (`cd`, `kubernetes`), `helmsman` (`cd`, `helm`, `kubernetes`) and `vendir` (`helm`, `kubernetes`), each a helm manager dep type, `jsonnet-bundler` (`kubernetes`) and `copier` (`python`) are translated, and so are `draftPR`, `prCreation` (`immediate`, `not-pending`, `status-success`, `approval`), `prNotPendingHours` and `abandonmentThreshold` (top level and per rule; a rule's `null` is `0`, and `abandonments:recommended` of `config:best-practices` becomes its rules).
- The `ocb` manager (category `golang`) is translated; Renovate's `exports` dep type is deps' `exporters`. Renovate's ocb matches no file until `managerFilePatterns` is set, while deps reads every YAML file with a top-level `dist` mapping and `gomod:` entries.
- `github-release-attachments` digests, the common JSONata `transformTemplates` shapes (`$map(<list>, function($v){ {"version": …} })`, `<path>.{"version": <field>}`, with `$keys`, `$distinct` and `$replace(…, /^v/, "")`), `matchCurrentVersion`, `matchCurrentAge`, `extractVersion`, labels, reviewers, assignees, `rebaseLabel`, `stopUpdatingLabel`, `platformCommit` (→ `git.platformCommit`) and `minimumReleaseAgeBehaviour` (default `timestamp-required`, as in Renovate 42+) are translated.
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
- Replacements: Renovate only replaces packages through `replacements:*` presets and rules. With `replacements:all` (part of `config:recommended`) the translation keeps deps' built-in list (`builtins.replacements: true`) and adds rules `{updateTypes: [replacement], ignore: true}` for the built-in presets `replacements:all` does not extend (`clang-format-to-maintenance-fork`, `corentinmusard-otel-cicd-action-to-dash0hq`). Without it, it writes `builtins.replacements: false`, and each extended `replacements:<name>` becomes rules with that preset's `replacementName`/`replacementVersion`/`replacementPrefix`. `packageRules[].replacementName`/`replacementVersion`, the prefix form of `replacementNameTemplate` (`replacementPrefix`) and `matchUpdateTypes: [replacement]` translate directly; the top-level `replacement` block becomes a rule with `updateTypes: [replacement]`.
- `vulnerabilityAlerts.groupName` becomes a trailing rule `{securityGroup: <groupName>}` without matchers (Renovate forces `vulnerabilityAlerts` over every package rule), unless security updates are off.
- Settings every rule of a `groupName` repeats (at least two rules, also the two a `github-actions` matcher expands to) move into [`groups.<name>`](reference/configuration.md#groups) when every one of them sets the same value and the move changes no dep's policy: group settings apply after every rule, so a key stays on the rules when a later rule could give the group's deps another value or move them into a group that does not set it.

### What compatibility mode cannot translate

Each of these is reported when it occurs; the rest of the config still applies.

| Renovate setting | What happens |
|---|---|
| `extends` from npm packages, URLs, relative files | skipped (warning) |
| `replacements:<name>` presets deps' built-in list lacks (`spectre-cli-to-spectre-console-cli`, nuget; `material-ui-to-mui`, `messageFormat-to-scoped`, defined in Renovate's code), `replacementNameTemplate` other than `{{{replace '<prefix>' '<new prefix>' packageName}}}`, `replacementVersionTemplate` | skipped / the field is dropped (warning) |
| built-in `workarounds:*`, `mergeConfidence:*`, `helpers:*` (except `pinGitHubActionDigests`), `security:openssf-scorecard` | skipped (info) |
| `matchCurrentValue` globs (`1.*`), `matchCurrentValue` together with `matchCurrentVersion`, `matchJsonata`, `matchSourceUrls`, `matchRepositories`, `matchBaseBranches` with only globs, other unknown `match*`/`exclude*` keys | the rule is dropped |
| glob entries of `matchBaseBranches` and `baseBranchPatterns` | the entry is dropped (warning) |
| `matchPackageNames` together with `matchDepNames` in one rule | the rule is dropped (deps has one `names` list) |
| managers deps lacks (`pyenv`, `cocoapods`, `bazel`, …), unknown categories and dep types | dropped from the rule; a rule left without any is dropped |
| `prCreation: approval` without `dashboard.enabled: true` | dropped (warning), as approval happens on the dashboard |
| update types `lockFileMaintenance`, `rollback`, `bump`, `lockfileUpdate` in `matchUpdateTypes` | dropped; a rule left without update types is dropped |
| `pin`, `rangeStrategy`, `versioning`, `extractVersion`, `replacementName`/`replacementVersion` in a rule with `matchUpdateTypes` | the field is dropped (deps decides them before the update type is known) |
| `versioning` in a rule with `matchCurrentVersion`/`matchCurrentValue`/`matchCurrentAge`, `extractVersion` with `matchCurrentAge` | the field is dropped (deps compares the current version under the versioning other rules decide) |
| `prHeader`/`prFooter` with Handlebars helpers, block expressions or fields other than those above | the field is dropped |
| `postUpgradeTasks` commands with `{{templates}}`; `dataFileTemplate`, `installTools`, `workingDirTemplate` | the tasks are dropped (deps passes the update as `DEPS_*` variables); the other keys are ignored. Translated tasks run only with `--allow-tasks` |
| `automergeType: branch`, `automergeStrategy: fast-forward`/`rebase-merge`, `platformAutomerge: false` | dropped; deps always automerges the PR through the platform |
| `dependencyDashboardApproval` without `dashboard.enabled: true` | dropped: deps' `dashboardApproval` needs the dashboard |
| cron schedules with a fixed minute (`0 5 * * 1`) | the entry is dropped (Renovate rejects it too) |
| schedule texts deps' window parser rejects (`on the 1st through 7th day of the month`) | the entry is dropped; a rule left without entries inherits the top-level schedule |
| custom managers with `customType` other than `regex` and `jsonata`, `matchStringsStrategy: combination`/`recursive`, `{{templates}}` in `datasourceTemplate`/`depNameTemplate`/`packageNameTemplate`/`versioningTemplate`, `currentValueTemplate`, `extractVersionTemplate` (regex managers; in jsonata managers a template using helpers or blocks such as `{{#if}}`/`{{replace}}`), `autoReplaceStringTemplate` | the custom manager is dropped |
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

[Renovate options](reference/renovate-options.md) maps every Renovate option deps knows to its `.deps.yaml` equivalent. `deps docs` generates it from the table the translator copies renamed options through and unknown-key errors hint from: Renovate options left in `.deps.yaml` are unknown keys, and the error names the deps equivalent, for example `.deps.yaml:3:5: rules[0].matchPackageNames: unknown key (Renovate's matchPackageNames is names here)`.

## Marker compatibility

Annotation-mode `custom[]` entries accept Renovate's markers unchanged:

```
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_VERSION=v2.5.0
```

- `packageName` wins over `depName`, `versioning=` takes a scheme name, `extractVersion=` maps release names (`extractVersion=^jq-(?<version>.+)$`), `registryUrl=` is the registry of `helm`, `gitlab-packages`, `rpm`, `apk`, `deb` and `glasskube-packages` deps, the API base URL of `endoflife-date` deps and the instance of `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases` and `forgejo-tags` (`# renovate: datasource=gitlab-tags depName=group/project registryUrl=https://gitlab.example.com`; ignored for others), and unknown keys are ignored.
- The marker applies to the **next line's** first version token. A Renovate regex that spans more lines needs a regex-mode `custom[]` entry instead.
- deps' own `# deps: <datasource> <name>` form works the same way.

In regex mode, Renovate's group names `currentValue`, `depName`, `packageName` and `currentDigest` are accepted, and so is its `(?<name>…)` syntax. Escaping differs, though: JSON5's `\\d` is YAML's `\d` in single quotes.

A `customManagers` entry's literal `registryUrlTemplate` becomes the entry's `registryUrl` (a templated one is ignored in regex managers and translated to JSONata in jsonata managers), as does a `(?<registryUrl>…)` group; both apply to `helm`, `gitlab-packages`, `rpm`, `apk`, `endoflife-date`, `galaxy-collection`, `maven`, `sbt-package` and `sbt-plugin` and `glasskube-packages` deps and the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags datasources. `datasource=gitlab-packages packageName=<project>:<package>` works as in Renovate; without `registryUrl` deps asks the configured GitLab instance (default gitlab.com). The `galaxy`, `galaxy-collection`, `maven`, `sbt-package`, `sbt-plugin` and `github-runners` datasources of deps' managers work in custom managers too, with those managers' default versioning (`tag`, `pep440`, `maven`, `ivy`, `docker`).

## Branch prefix takeover

With `branchPrefix: renovate/`, deps manages the branches and PRs Renovate created:

- **Branch names.** deps names branches `renovate/<additionalBranchPrefix><slug(depName)>[-<bucket>]` and `renovate/<additionalBranchPrefix>group/<slug(groupName)>[-<bucket>]`. As with Renovate's `renovate/<additionalBranchPrefix><depName>-<n>.x`, one dependency name updated by several managers and files (an action, a Homebrew formula and a custom match of `anchore/syft`) gets one PR. The names differ from Renovate's only in the bucket suffix (`-major`, `-v<N>`, `-minor`, `-patch` or none where Renovate writes `-<n>.x`, and `-<bucket>` after the group where Renovate prefixes `major-`) and the `group/` segment of groups, so Renovate's open PRs are not reused.
- **Upgrading deps.** deps 0.7 and earlier named branches `<prefix><manager>/<slug>`. The first run of a newer deps opens the PRs under the new names and closes the per-manager ones as obsolete; `additionalBranchPrefix: '{{manager}}/'` keeps the old names (see [`additionalBranchPrefix`](reference/configuration.md#additionalbranchprefix)).
- **Existing PRs.** deps does not adopt Renovate's existing PRs. It opens its own, and once a run completes without lookup errors, it closes every open PR under the prefix that matches none of its branches (a PR whose build or push failed counts as one of them).
- **Branch authors.** Branches whose tip was committed by another author are never touched. If the old bot used another identity, add its email to `git.ignoredAuthors`.
- **Migration steps.** Disable Renovate first, then run `deps run --dry-run` to see the planned PRs.

## Behavior differences

- **Loose versioning zero-fill.** In `loose`, a missing part compares as 0, so `1877` < `1877.25`. Renovate ranks the shorter version higher.
- **Bucket flags on `updateTypes` rules.** deps evaluates `separate` with the policy of each candidate's type, so a rule with `updateTypes` *can* set it. Renovate decides buckets before the update type is known and ignores these flags on `matchUpdateTypes` rules. Configs that set them on manager rules, as Renovate requires, behave the same in deps.
- **`separate: majors` branch suffix.** deps uses `-v<N>` where Renovate writes `<N>.x`.
- **`rangeStrategy`.** deps supports every Renovate value for npm ranges: `auto`, `bump`, `update-lockfile`, `replace`, `widen`, `pin` and `in-range-only`. `auto` is `update-lockfile`, like Renovate's npm manager for simple ranges: a version the range admits only moves the lockfile, a version outside it replaces the range's version (`^5.2.8` → `^6.0.0`, Renovate's `replace`), and versions are compared with the locked one. Under `auto` peerDependencies are widened (`^1.0.0` → `^1.0.0 || ^2.0.0`), as in Renovate. Locked versions come from `package-lock.json` (v1–v3), `npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock` (classic and berry) and `bun.lock`, as in Renovate. A lockfile-only update locks exactly the proposed version with every tool: for yarn, Renovate runs `yarn upgrade <name>` (classic) or `yarn up -R <name>` (berry), which take the newest version the range admits, while deps uses `yarn set resolution` (berry) or moves the descriptor to a pinned install's entry (classic); for pnpm both run `pnpm update --no-save <name>@<version>`. npm's whole range grammar is rewritten in its own form (`||`, `x`/`*` wildcards, partial versions, `<`, hyphen ranges, `v1.2.3`): `auto` widens `||` ranges (`^1.0.0 || ^2.0.0` → `^1.0.0 || ^2.0.0 || ^3.0.0`) as Renovate does; only `*`/`x` (any version) stays `unsupported range`. `widen` keeps lower bounds and moves upper ones (`>=1.2.3 <2` → `>=1.2.3 <4`) or appends an alternative (`^1.2.3` → `^1.2.3 || ^2.0.0`); `pin` proposes a `pin` update to the locked (else newest admitted) version unless an update in the same PR bucket pins the range anyway, then writes every newer version exactly; `in-range-only` is `bump` limited to versions the range admits. cargo (`=1.4.0`, widen `1.2` → `>=1.2, <3`) and Python (`==1.4.0`, widen `>=1.2,<2` → `>=1.2,<4`, Poetry `^2.13.0` → `^2.13.0 || ^3.0.0`) follow the same rules. OCM's `rangeStrategy: bump` for `golang-version` needs no translation: go.mod `toolchain` and `go` directives are replaced at their precision.
- **npm git dependencies, aliases and overrides.** As in Renovate, git dependencies on a tag (`github:o/r#v1.2.3`, `git+https://…#v1.2.3`, `#semver:^1.2`) get tag updates (never digest-pinned), those on a commit (7 to 40 hex digits) digest updates of the default branch written at the pinned length, `npm:<name>@<range>` aliases update `<name>`, and `overrides`, `resolutions` and `pnpm.overrides` are updated. Unlike Renovate, git dependencies are looked up on any git host (Renovate: GitHub only, else `unspecified-version`), and aliases and overrides are not read from the lockfile, so a version their range admits is proposed only under `bump`. `packageManager` Corepack hashes move with the version in the algorithm written (Renovate always writes `sha512`); yarn 2 and later is looked up as `@yarnpkg/cli-dist` as in Renovate, and `bun@x` is updated.
- **Python specifiers.** `rangeStrategy` applies to every PEP 440 and Poetry range (`>=1.2,<2`, `<2`, `~=1.4`, `^1.2`, `>=1.2 <2`, `^1 || ^2`), with the versions `uv.lock`, `poetry.lock`, `pdm.lock` and `Pipfile.lock` lock. deps' `auto` is `update-lockfile` for all Python managers; Renovate's is that for poetry only and `replace` for pep621, pipenv and pip_requirements, so deps also moves those lockfiles for versions the range admits. Ranges are rewritten with Renovate's pep440 rules (`>=1.2,<2` → `>=3.1,<4`); under `bump`, a version outside the range gets the `replace` rewrite too, and a Poetry `||` constraint gets a new alternative instead of Renovate's npm-style rewrite. Poetry and pdm lockfile-only updates land on exactly the proposed version (the lock runs with the dep pinned, then with the manifest restored), as uv's do; pipenv's `upgrade` can still resolve a newer admitted one.
- **Python extraction beyond Renovate.** pep621 also extracts uv's `constraint-dependencies`, `override-dependencies` and `build-constraint-dependencies` (ignored by default; `ignore: false` enables them); Poetry multiple-constraint arrays are one dep per constraint (Renovate skips them as `multiple-constraint-dep`); git dependencies pinned to a tag are `git-tags` deps in pep621 (`name @ git+https://…@v1`, uv `git` sources with `tag`), pip_requirements, conda `pip:` lists and Pipfiles (`ref`), as in Renovate's pip_requirements and poetry managers (Renovate uses github-tags/gitlab-tags for those hosts; deps' `git-tags` covers every http(s) host). `[build-system] requires` is extracted by pep621 only (Renovate's poetry manager extracts it a second time).
- **Composer constraints.** `rangeStrategy` applies to every composer.json constraint (`^1.2`, `~1.2.3`, `>=1.0 <2.0`, `1.2.*`, `^1.0|^2.0`, `@dev` flags) as Renovate's composer versioning rewrites it, with the versions composer.lock locks; `auto` is `update-lockfile`, `widen` for constraints with alternatives (and `bump` of those widens, as in Renovate) and for every constraint of a `"type": "typo3-cms-extension"` composer.json, as in Renovate. Under `auto`, versions a constraint with alternatives admits still move composer.lock (lockfile-only updates); Renovate does that for `^1.0|^2.0` (its `update-lockfile`) but drops them for ` || ` alternatives (its `widen`). Lockfile updates run `composer update <pkg>:<version> --with-dependencies --minimal-changes` (Composer 2.7+). See the [composer manager](reference/managers.md#composer).
- **Pub constraints.** `rangeStrategy` applies to every pubspec.yaml constraint (`^1.2.3`, `>=1.2.3 <2.0.0`, exact versions) and to the `environment` SDK constraints. deps' `auto` is `replace`, as Renovate's pub manager resolves it (no lockfile-only updates), and pubspec.lock is updated with `dart pub upgrade <name>` as Renovate's artifacts do; set `rangeStrategy: update-lockfile` to also get lockfile-only updates for versions a constraint admits. Renovate's pub manager uses npm versioning; deps uses pub's own (`^0.0.3` is `<0.1.0`, `1.2.3+1` sorts after `1.2.3`, a new 0.x minor is a major update).
- **Deno.** The deno manager reads the same `deno.json`/`deno.jsonc` dep types as Renovate's, with the `jsr:` and `npm:` requirements rewritten like npm ranges (`auto` = `update-lockfile`; `widen` = `replace`: Renovate's deno versioning has no `widen` either, as a specifier cannot hold `||`). deno.lock is updated with `deno install --lockfile-only` (Deno 2.6+; Renovate runs `deno install`), and every update, lockfile-only ones included, locks exactly the proposed version (Renovate lets deno pick the newest the new requirement admits). Updates of task specifiers only change the configuration (Renovate fails their lockfile update). The `deno` datasource reads `cdn.deno.land` instead of Renovate's `apiland.deno.dev`, which no longer resolves; a mirror is set with `DEPS_DENO_URL`, not with a rule's `registryUrls`. Not read: `importMap` files the configuration names and the `package.json` files of a Deno workspace's node-compat members (the npm manager updates those with npm's lockfiles).
- **Helm chart ranges.** Chart versions that are no exact version (`~1.2.0`, `^1`, `1.2.x`, `>= 1.2, < 2`) are rewritten as Renovate's helm versioning (npm's) does, in Chart.yaml, requirements.yaml, helmfiles, Flux HelmReleases and OCIRepository `spec.ref.semver`, Argo CD, k0s, Sveltos, kustomize `helmCharts` and terraform `helm_release`; `auto` is `replace`, as in Renovate, and the Chart.lock entry takes the new version. Renovate rejects comma-separated constraints (`>= 1.2, < 2`) as npm ranges; deps rewrites them, keeping the commas. Chart.yaml `@alias` repositories resolve through `registryAliases` (Renovate's helm-v3 default `stable` included), and also through a committed `repositories.yaml` or the Chart.lock, which Renovate does not read. Flux `GitRepository` `spec.ref.tag` is a `git-tags` dep (Renovate: `github-tags`/`gitlab-tags`/`git-tags`); `spec.ref.commit` is skipped (Renovate updates the commit by git-refs). See the [helm manager](reference/managers.md#helm).
- **`matchCategories`** is not a `.deps.yaml` key. Write the managers or datasources explicitly (`deps migrate` does).
- **Presets** are only resolved by `--renovate` and `deps migrate`. In `.deps.yaml`, the built-ins below cover the common ones.
- **Schedules.** Renovate's schedules are time windows, and deps evaluates them the same way: against the time of the run, in `timezone`. Text windows support Renovate's later syntax for `before`/`after`/`at` times, `every weekday`/`weekend`/`<day>`, `on <days>`, `on the <n> day of the month` or `of the week`, `every <n> weeks`, `every <n> months`, `at any time`, and `also`/`except` between windows; cron schedules need minute `*`, as in Renovate. Exceptions differ in one point: deps excludes every time an exception contains, while later skips some time-of-day exceptions (with `on monday except before 6am`, Renovate is in schedule on Mondays at 5am). The workflow must run inside the windows (e.g. an hourly cron). A cron with a fixed minute (`0 0 * * 0`) is instead a *trigger*: a group is in schedule when the run was started by that cron of the workflow (`workflow_dispatch` matches every trigger, `push` none). Both gate only creating PRs (and updating them with `updateNotScheduled: false`). OCM's `* * * * 0` (Sunday) works as is, given a run on Sundays; the [OCM translation](#workflow-for-ocm) uses the trigger `0 0 * * 0` in both `on.schedule` and the rules instead, next to a daily `0 0 * * *` cron for unscheduled deps, so pushes on Sundays do not open the weekly groups.
- **Release age.** An unknown release time does not block an update by default (`minimumReleaseAgeBehaviour: timestamp-optional`); Renovate 42+ defaults to `timestamp-required`, which holds such releases back. Set `minimumReleaseAgeBehaviour: timestamp-required` to match it. In deps, custom datasources have no release times, and an image has one only when its config carries `created` (or the registry lists times); an OCI Helm chart without the `org.opencontainers.image.created` annotation is dated by its chart archive (Renovate: Docker Hub's `tag_last_pushed` only). `git-tags` deps (pre-commit hooks, git-hosted Terraform modules, cargo git deps, …) have times on github.com, GitLab (gitlab.com and the `gitlabci.url` instance) and Gitea/Forgejo, where Renovate's `git-tags` has none; on other hosts they are unknown. Image digest refreshes, pins and security updates are always exempt. Go pseudo-version digest updates carry a commit time and wait like Renovate's, so a Renovate `minimumReleaseAge: null` rule for `digest` translates to `updateTypes: [digest], minimumReleaseAge: '0'`. When the newest candidate is too new, deps proposes the newest one that is old enough; Renovate waits.
- **Indirect Go requirements** are ignored by default, as in Renovate. Opt in with `depTypes: [indirect], ignore: false`.
- **Tool versions (asdf, mise, nvm, nodenv).** A partial version (`golangci-lint 2.5`, `protoc 29`, `"npm:pnpm" = "11"`) keeps its precision (`2.5` → `2.6`); Renovate replaces it with a full version. A Node.js version with an operator or x-range (`>=20`, `20.x`) keeps it and moves the version (`>=24`, `24.x`); Renovate's node versioning treats these as npm ranges under `rangeStrategy`. A mise `go:<package>` tool resolves to the module providing the package, as `go install` does; Renovate falls back to the repository's tags. Aliases such as `lts/*`, `lts/iron`, `node`, `stable` are not updated, as in Renovate.
- **Actions pinned to a bare SHA** (`uses: owner/repo@<sha>` without a `# vX` comment). Renovate skips them as `unversioned-reference` (disabled; a `packageRules` `enabled: true` turns on digest updates to the default branch). In deps a SHA that a tag points at is that tag's version and is updated like a pinned ref, writing the new version as a comment (`@<new sha> # v1.2.0`); Renovate leaves it alone. Any other SHA is ignored by default like in Renovate; translate `enabled: true` to a rule with `ignore: false`, which gives the same digest updates to the default branch's `HEAD`.
- **Dockerfile package pins.** `RUN apk add pkg=<version>` and `apt-get install pkg=<version>` pins are extracted with dep type `install`, as in Renovate. Renovate skips all of them as `unknown-registry` until a `packageRules` entry sets `registryUrls`; deps looks apk pins up in the Alpine release of the stage's base image (`alpine:3.21`, `node:22-alpine3.21`; Wolfi for Chainguard images) and skips only those whose release it cannot tell (`alpine:latest`, `node:22-alpine`, `--repository`, `pkg@tag`), and apt pins (`unknown registry`) until `rules[].registryUrls` (translated from `registryUrls`) names a suite. apk constraints other than `=` (`~`, `=~`, `<`, `>=`) are skipped as `version constraint` (Renovate updates `~` constraints); `dnf install` pins are not extracted (Renovate neither).
- **pre-commit.** Hook repositories on any git host are looked up without host rules (Renovate skips a host it does not detect as GitHub, GitLab or Forgejo as `unknown-registry`). A bare `rev: <sha>` is resolved like an action's bare SHA above, writing `# frozen: <version>` (Renovate skips it); a `# frozen: <tag>` naming a non-version tag (`nightly`) gets digest updates when the tag moves. `additional_dependencies` of hooks with `language: node`, `python` or `golang` are updated with Renovate's dep types `pre-commit-node`, `pre-commit-python` and `pre-commit-golang`; a golang package path (`…/cmd/shellcheck@v0.11.1`) is looked up as its module, as `go install` resolves it. Under `auto` a version a range admits is not proposed (Renovate's `replace`).
- **PR limits.** Renovate defaults to `prConcurrentLimit: 10`, `prHourlyLimit: 2`; deps to `0`/`0` (unlimited), plus the per-run `--max-new-prs 10`. The effective limit is the minimum of the non-zero limits.
- **Automerge.** deps always uses the platform's auto-merge for PRs (Renovate's `platformAutomerge: true`) and never merges by itself while checks are pending. On GitHub repositories that disallow auto-merge, deps merges directly, but only a mergeable PR with at least one status or check run, all green; a repository without any checks is never merged that way, and there is no `ignoreTests` to change that. `automergeType: branch` works like Renovate's: one branch per run, only when it is up to date with the base (deps rebuilds it first); failed checks open the PR (there is no `prNotPendingHours`: a branch whose checks never report waits, listed on the dashboard).
- **Dependency dashboard.** The checkboxes are `create` (pending approval, awaiting schedule, rate-limited), `recreate` (closed PRs), `rebase` (open PRs, modified branches) and `pr` (pending branch automerge: open a PR instead, Renovate's "Pending Branch Automerge" section; pending status checks of `prCreation`: open the PR now, Renovate's "Pending Status Checks"). Approving an update (Renovate's "Pending Approval" section) is the same `create` request, so the PR is created right away, regardless of schedule and limits. There are no deprecation warnings; the dashboard additionally lists the run's errors. Detected dependencies are listed per manager and file, capped to stay below GitHub's body limit.
- **One PR per dependency or group.** Renovate's `<depName>-<major>.x` style branch-per-major-line maps to PR buckets; like Renovate, one dependency name shares its branch across managers and files unless `additionalBranchPrefix` tells them apart.
- **Closed PRs.** As in Renovate, closing a PR without merging ignores its update: for minor and patch updates, digests and pins that version or digest (the next one gets a PR); for a major update (`-major`, `-v<N>` branches) that major, so later versions of it get no PR until the next major (`skipped: closed PR for this major`). deps matches closed PRs by the targets recorded in their body (`deps:versions` marker), not by title, so changing `prTitle` keeps them ignored. A closed PR without security fixes does not hold back a security update. The dashboard lists these updates under *Ignored or blocked*; its `recreate` checkbox lifts the suppression.
- **Ignored files.** Renovate reads the repository through the forge's API and git, so it only ever sees tracked files. deps walks the checkout and would also see untracked build output, so `gitIgnore` (default true) applies the repository's ignore rules to discovery. It does not read the index, so a tracked file an ignore rule matches (`git add -f`) is skipped; `gitIgnore: false` scans it again.

### Presets

| Renovate preset | deps |
|---|---|
| `group:monorepos`, `group:kubernetesMonorepo`, `group:goOpenapi` | `builtins.monorepos: true` (default; 62 built-in entries) |
| `replacements:all` (part of `config:recommended`) and its `replacements:*` presets | `builtins.replacements: true` (default; generated from Renovate's `lib/data/replacements.json`, every preset deps can write); see `deps explain` (`builtin replacement ...`). Not included: the nuget preset and the material-ui and messageformat presets Renovate defines in code |
| `packageRules[].replacementName` / `replacementVersion`, `matchUpdateTypes: [replacement]` | `rules[].replacementName` / `replacementVersion`, `updateTypes: [replacement]`. `replacementNameTemplate: "{{{replace 'a/' 'b/' packageName}}}"` is `replacementPrefix: a/` with `replacementName: b/`; other templates have no equivalent. Unlike Renovate, a replacement gets its own `-replacement` branch and closing its PR brings the dep's usual updates back |
| `:ignoreModulesAndTests` | default `ignorePaths` |
| `:semanticPrefixFixDepsChoreOthers` | default commit types |
| `docker:pinDigests`, `helpers:pinGitHubActionDigests` | `pin: digest` |
| `:rebaseStalePrs` | `rebaseWhen: behind-base-branch` |
| `config:recommended` / `config:best-practices` | the defaults above plus `builtins.security: true` and `separate: major` |

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

### Workflow for OCM

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
      - uses: jakobmoellerdev/deps@v0.13.0
        with:
          token: ${{ steps.app.outputs.token || github.token }}
          version: v0.13.0 # keep this when pin: digest rewrites the ref above to a SHA
          args: ${{ github.event_name == 'pull_request' && 'plan' || 'run' }}
          config: .github/deps.yaml
          show-skipped: true
          log-level: info
```

- **Pull requests** that change the config run `plan` with the job token. That validates the config: an invalid key exits 2. `run --dry-run` reports against the checkout, so on a PR merge commit it would only warn that `HEAD` is not the tip of `main` and list the PRs of the PR's tree as if it were `main`. To list the PRs a config change would cause, use the base/PR checkout pair from [ci.md](ci.md#preview-on-pull-requests).
- **Token.** The App token goes to both `actions/checkout` and the action. The action creates the commits and moves the `deps/*` branches through the API with it; git uses the checkout's credentials only to list and delete branches. `permission-workflows: write` is required, because Action updates change `.github/workflows/*`: GitHub checks it for API commits too (without it, creating the tree fails with `403 Resource not accessible by integration`).
- **Version.** The action downloads the release named by its ref when that ref is a tag. `pin: digest` turns `@v0.6.0` into `@<sha> # v0.13.0` in the first "Digest pins" PR, and from then on the ref no longer names the release. Without `version:`, the action would download the latest release.
- **Signing.** Renovate runs with `RENOVATE_PLATFORM_COMMIT: enabled`, and so does deps: `git.platformCommit: enabled` in the example config (`auto` would choose it too, because the token is an App installation token). deps then creates the commits through the GitHub API, and GitHub signs them: they show as verified, authored by `ocmbot[bot]` (`<id>+ocmbot[bot]@users.noreply.github.com`), committed by GitHub. `git.author` only applies to git commits. Don't pass `signing-key`: it conflicts with `git.platformCommit: enabled`. Verified commits satisfy the `required_signatures` rule of both rulesets that cover `deps/*` ("OCM Organization Ruleset" and the repository's "All branches"). The rulesets also restrict branch creation, deletion and non-fast-forward updates, so the branches still need ocmbot's bypass (App 295948 is an `always` bypass actor of both) or an exclusion, see the [cutover](#cutover-for-ocm).
- **Not needed.** Renovate's repository cache steps (deps has no cache) and `RENOVATE_ALLOWED_COMMANDS` (`renovate.json5` has no `postUpgradeTasks`; deps' equivalent is the `allow-tasks` input).

### Comparison with Renovate

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

### Cutover for OCM

1. Add `.github/deps.yaml` (this example) and `.github/workflows/deps.yml` (above) next to Renovate. Their PRs don't collide: deps' branches are `deps/*`, its dashboard has its own title, and deps closes obsolete PRs only under its own prefix.
2. Grant the ocmbot installation *Checks* and *Commit statuses* read, if it doesn't have them yet. renovate.yml requests `checks: write` and `statuses: write`, so the App likely has them. Pushes to `deps/*` rely on ocmbot's ruleset bypass. The repository ruleset "All branches" excludes only `refs/heads/renovate/**` and `refs/heads/dependabot/**`, so either add `refs/heads/deps/**` there or keep the bypass.
3. Compare a weekly run with Renovate's, as above. Then disable `renovate.yml`, set `branchPrefix: renovate/`, drop `dashboard.title`, and close #331. deps closes Renovate's leftover `renovate/*` PRs after its first run without lookup errors.

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
