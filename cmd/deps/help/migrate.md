Translates the repository's Renovate config into a `.deps.yaml` and writes it to `<dir>/.deps.yaml` (`--output` elsewhere, `-` to stdout). The Renovate config is never modified or deleted. An existing output file is only replaced with `--force` (else exit 2).

The config is the first of `renovate.json`, `renovate.jsonc`, `renovate.json5`, the same names under `.github/`, `.gitlab/`, `.gitea/` and `.forgejo/`, `.renovaterc`, `.renovaterc.json(c|5)` and the `renovate` key of `package.json`; `--config` names it explicitly. JSON, JSONC and JSON5 are accepted. Its `extends` presets are resolved: Renovate's built-in presets from a table, `github>`, `gitea>`, `forgejo>` and `local>` presets by fetching them (credentials as for lookups, see `deps help environment`).

`deps plan|apply|run|explain --renovate` run the same translation in memory, so a migrated file behaves exactly like compatibility mode. Settings deps cannot express are listed on stderr as `warn    <setting>: <msg>`, settings without effect on which updates or PRs exist as `info    <setting>: <msg>` (with `-v`); both also head the written file as comments.

- The branch prefix follows Renovate (`renovate/` by default): `deps run` closes open PRs under it that it does not manage, so turn Renovate off before the first run.
- Renovate schedules are time windows, deps schedules are the crons of the workflow triggers that may create PRs. Each entry becomes the cron of the window's start (`* * * * 0` and `before 6am on sunday` become `0 0 * * 0`); the summary lists the crons the workflow's `on.schedule` needs.
- Renovate's regex (`/re/`) and negated (`!pattern`) name, path and file patterns are kept as they are.

The summary line reads `wrote <file> from <renovate config>: <n> rules, <n> custom managers, <n> custom datasources; <n> warnings, <n> notes`.
