# Migrating from Renovate

deps reads `.deps.yaml`, not `renovate.json(5)`. `deps migrate` translates your Renovate config for you, and `--renovate` runs from it untranslated, so a migration is a few commands plus a look at the warnings. Details live next door: [what cannot be translated and how deps behaves differently](renovate-differences.md), the per-option mapping in [Renovate options](reference/renovate-options.md), the feature status in [renovate-parity.md](renovate-parity.md), and a complete translated config in the [open-component-model example](examples-ocm.md).

## The migration, in five steps

```sh
deps migrate                      # 1. writes .deps.yaml from renovate.json(5), and lists what it dropped
deps config check                 # 2. validates it
deps plan --renovate > /tmp/r.diff && deps plan > /tmp/d.diff && diff /tmp/r.diff /tmp/d.diff
                                  # 3. same updates from the Renovate config and from .deps.yaml?
deps run --dry-run                # 4. the PRs deps would open
```

5. Disable Renovate, commit `.deps.yaml` and the [workflow](ci.md), and let deps run. It keeps Renovate's `renovate/` branch prefix, and after its first error-free run closes Renovate's open PRs that it does not own (see [branch prefix takeover](#branch-prefix-takeover)).

Each warning `migrate` prints is an option deps cannot express; the [table below](renovate-differences.md#what-compatibility-mode-cannot-translate) says what happens to it. An [agent skill](#with-an-ai-agent) walks the same five steps for you.

## The three commands

- `deps migrate` writes the translation as a commented `.deps.yaml` and leaves `renovate.json` alone (`--output -` prints it).
- `deps init --from-renovate <file>` writes a new starter `.deps.yaml` with the same translation inside.
- `deps plan|apply|run|explain --renovate` skips the file entirely and runs from the Renovate config in memory; `--config renovate.json5` does the same.

All three share one translator, so a migrated file behaves exactly like `--renovate`. It reads every file name Renovate reads (`renovate.json(c|5)`, the same under `.github/`, `.gitlab/`, `.gitea/`, `.forgejo/`, `.renovaterc*`, the `renovate` key of `package.json`) and resolves `extends`, fetching `github>`, `gitlab>`, `gitea>`, `forgejo>` and `local>` presets; npm and URL presets are skipped with a warning.

What it reports while translating:

- `warn <setting>: <msg>` — deps cannot express this; the [differences page](renovate-differences.md#what-compatibility-mode-cannot-translate) says what was dropped.
- `info <setting>: <msg>` — translated away because it changes no update or PR (labels, PR texts, `groupSlug`, …).

`migrate` prints both and writes them as the file's head comment; `--renovate` logs them.

The translation keeps **Renovate's** defaults, not deps': the `renovate/` branch prefix (hence the [takeover](#branch-prefix-takeover)), `prConcurrentLimit: 10`, `prHourlyLimit: 2`, Renovate's `ignorePaths`, and Renovate's schedules evaluated as time windows at run time. Matchers become `rules[]` entries one to one — `matchManagers`/`matchCategories` to `managers`, `matchCurrentValue` to `currentVersion`, `matchFileNames` to `paths`, `groupName` to `group`, with repeated settings lifted into [`groups`](reference/configuration.md#groups) — and `commitMessage*`, `prTitle`, `prHeader` and `prFooter` are converted to Go templates. Every option and the key it becomes: [Renovate options](reference/renovate-options.md).

## With an AI agent

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
