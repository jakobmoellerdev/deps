---
name: migrate-from-renovate
description: Migrate a repository from Renovate to deps (git.jakob-moeller.cloud/jakobmoellerdev/deps). Use when asked to replace Renovate with deps, translate renovate.json/renovate.json5/.renovaterc into .deps.yaml, run deps from an existing Renovate config, or review what of a Renovate config deps cannot express.
---

# Migrate a repository from Renovate to deps

deps can read the Renovate config itself. `deps migrate` writes it as a commented `.deps.yaml`; `deps plan|apply|run|explain --renovate` run from the Renovate config directly. Both use the same translator, so a migrated file behaves exactly like compatibility mode. Your job is to run the translation, resolve what it reports, prove the result on the real repository, and cut over without two bots fighting over the same branches.

Reference while working, fetch when you need it: the migration guide `https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/docs/migrating-from-renovate.md?ref=main` (option mapping, "What compatibility mode cannot translate", behavior differences, a worked example for open-component-model), the config reference `https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/docs/reference/configuration.md?ref=main`, and `deps help environment` / `deps migrate --help` locally.

## 0. Preconditions

- A deps binary with `deps migrate` on PATH (`deps migrate --help` must work; v0.3.0 and older lack it). Install the newest release (`docs/install.md`), or build the default branch: `go install git.jakob-moeller.cloud/jakobmoellerdev/deps/cmd/deps@main`.
- A checkout of the target repository at the tip of its default branch, with an `origin` remote (needed for `local>` presets).
- Tokens for lookups, as for any deps run: `DEPS_GITHUB_COM_TOKEN=$(gh auth token)` avoids GitHub rate limits; private registries use the variables in `deps help environment`. Renovate `hostRules` are **not** translated; map each one to an environment variable or credential helper.
- Never edit or delete the Renovate config during the migration. It stays the source of truth until cutover.

## 1. Translate

```sh
deps migrate -v --output /tmp/deps.yaml            # -v also prints info notes
# or: deps migrate --config .github/renovate.json5 --output -
```

It finds the config like Renovate (`renovate.json(c|5)`, the same under `.github/`, `.gitlab/`, `.gitea/`, `.forgejo/`, `.renovaterc*`, `package.json`'s `renovate` key), resolves `extends` (built-in presets; `github>`, `gitea>`, `forgejo>`, `local>` fetched) and prints:

- `warn    <setting>: <msg>`: deps cannot express it, or translated it approximately. **Every warning needs a decision** (step 2).
- `info    <setting>: <msg>`: no effect on which updates or PRs exist (labels on presets, PR texts, `groupSlug`, built-in preset settings). Skim; no action unless the user relies on it.
- The summary line, and `schedules need these workflow crons (on.schedule): …`. Write those crons down; the workflow needs them (step 4).

Exit 2 means a config problem (unparsable file, `enabled: false`, a preset that cannot be fetched); fix the cause, don't work around it.

## 2. Resolve every warning

Work through the warnings one by one and record the decision for the user. Typical ones:

| Warning | What to do |
|---|---|
| `branchPrefix: deps run closes open PRs under renovate/ …` | Expected. Decide the cutover mode in step 5; do not ignore it. |
| a manager deps lacks (`git-submodules`, `composer`, `devcontainer`, …) | Check whether the repository has such files (`git ls-files`). None: say so and move on. Some: tell the user these deps will no longer be updated. |
| `rule dropped` (`matchCurrentValue` glob or together with `matchCurrentVersion`, `matchJsonata`, `matchSourceUrls`, package and dep names together, …) | Read the original rule's intent. Rewrite it in `.deps.yaml` with `names`, `paths`, `datasources`, `depTypes`, `currentVersion` (a constraint or `/regex/`) or `currentAge`, or split it into two rules. |
| custom datasource dropped (templated URL, `html`, JSONata) | Write `customDatasources.<name>` by hand: `url`, `format`, `versions` (jq-subset selector such as `.releases \| keys` or `.items[].tag_name`) and `versionRegex`. The custom managers that used it were dropped too; re-add them once it exists. |
| custom manager dropped (templates, `combination` strategy, non-regex `customType`) | Rewrite as a `custom[]` entry: regex mode with literal `datasource`/`name`/`versioning`, or annotation mode if the files carry `# renovate: datasource=… depName=…` markers (annotation mode reads those unchanged). |
| schedule entry dropped (`every 2 weeks`, …) | Pick the nearest workflow cron with the user; deps schedules are crons of the workflow trigger, not time windows. |
| `timezone` ignored | Convert the intended times to UTC in the crons. |
| `automergeType: branch`, `platformAutomerge: false`, `dependencyDashboardApproval` | Explain the deps behavior (platform automerge of the PR; no approval gate) and confirm with the user. |
| `pin`/`rangeStrategy`/`versioning` dropped next to `matchUpdateTypes` | Move the field to a rule without `updateTypes` (deps decides these before the update type is known). |
| `versioning` (or `extractVersion`) dropped next to `currentVersion`/`currentAge` | Move `versioning` to a rule that selects the same deps without `currentVersion`/`currentAge`, if the deps need that scheme. |
| `prHeader`/`prFooter`: `unsupported Handlebars expression …` | Rewrite the text as a Go template over `.Type`, `.Scope`, `.Name`, `.Group`, `.From`, `.To`, `.UpdateType`, `.Count`, `.IsSecurity` (`{{if}}` and `{{with}}` work), or drop it. |
| `…; deps keeps its built-in PR titles` (a `prTitle`/`commitMessage*` part with `{{#if}}`, `{{newMajor}}`, …) | Ask whether the title format matters. If it does, write `prTitle` (top level or per rule) as a Go template over the same fields; else accept deps' titles (`<type>(<scope>): update <name> to <version>`). |

Warnings marked `(from a built-in preset)` are info-level: they come from Renovate's own presets, not from the repository.

Do not add settings the Renovate config doesn't have. Keep the translated rule order: rules apply top to bottom, later fields override earlier ones, as in `packageRules`.

## 3. Prove it on the repository

Run both modes and compare. They must agree byte for byte:

```sh
deps plan -v --renovate           > /tmp/compat.diff   2> /tmp/compat.err
deps plan -v --config /tmp/deps.yaml > /tmp/migrated.diff 2> /tmp/migrated.err
cmp /tmp/compat.diff /tmp/migrated.diff && git apply --check /tmp/compat.diff
tail -1 /tmp/compat.err           # "<n> updates in <m> files, <k> skipped, <e> errors"
```

- Different diffs mean a deps bug: report it with both files; don't paper over it in the YAML.
- After hand edits from step 2, re-run `plan --config` and explain every change in updates against the compatibility-mode run.
- `error` lines: transient (`rate limited`, `timeout`) → re-run; persistent → investigate. Use `deps explain '<name-glob>' --config /tmp/deps.yaml` to see the matching rules, the policy and why candidates were rejected.
- Compare with Renovate's dependency dashboard issue if there is one: every open or pending Renovate update should be in `compat.err` (as `update`, or held back by `minimumReleaseAge` / schedule). Known differences are listed under "Behavior differences" in the migration guide (deps proposes the newest old-enough release where Renovate waits; unknown release times follow `minimumReleaseAgeBehaviour`).
- Then simulate the bot itself with the crons from step 1: `deps run --dry-run -v --config /tmp/deps.yaml --trigger-schedule '<cron>'` (needs a platform token and `HEAD` at the tip of the default branch). It lists the branches and PRs deps would create, update and close.

## 4. Wire up the workflow

Put the config at `.deps.yaml` (or `.github/deps.yaml` with `--config`) and add a workflow (README "Quick start" and `docs/ci.md` per forge; `docs/migrating-from-renovate.md` has a complete GitHub App example):

- `on.schedule`: every cron from step 1, plus one for unscheduled updates (Renovate's own workflow cron, commonly daily).
- `push` to the default branch and `workflow_dispatch`; on `pull_request` touching the config, run `plan` to validate it (exit 2 on an invalid key).
- The same identity Renovate used (GitHub App token to both `actions/checkout` and the deps action, with workflows write so Action updates can be pushed); keep `gitAuthor`/`gitIgnoredAuthors` from the translation.
- Pin the action by tag and set `version:` to the same tag, so a later `pin: digest` rewrite of the ref keeps the release.

Alternatively run straight from the Renovate config with `args: run --renovate`. That keeps one config file during a trial, at the price of the translation notes appearing only in the log.

## 5. Cut over

Choose with the user:

- **Trial next to Renovate (recommended for busy repositories):** set `branchPrefix: deps/` and `dependencyDashboardTitle: Dependency Dashboard (deps trial)` in `.deps.yaml`, so deps neither closes Renovate's PRs nor collides with its dashboard. Compare a few scheduled runs, then switch.
- **Direct takeover:** keep `branchPrefix: renovate/`. Disable Renovate first (its workflow or app). deps does not adopt Renovate's PRs: it opens its own branches (`renovate/<manager>/<slug>`, `renovate/group/<slug>`) and, after a run without lookup errors, closes every open PR under the prefix it does not manage and deletes those branches.

At cutover: disable Renovate, set the final `branchPrefix`, drop the trial dashboard title, close Renovate's dashboard issue, then delete the Renovate config in a separate commit once deps runs cleanly.

## Report to the user

- the translation summary (rules, custom managers, datasources, warnings, notes) and the required crons;
- each warning with its decision and any hand-written replacement;
- the plan comparison (identical diffs, update counts, errors and their cause);
- what changes for the team: PR branch names, PR limits (Renovate defaults 10 concurrent / 2 per hour are kept), schedule semantics, anything no longer updated;
- the open cutover steps.
