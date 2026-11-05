Results go to stderr (the diff of `plan` and the report of `explain` go to stdout), in fixed-width lines:

```
update  <file>:<line>  <name>  <from> -> <to>  (<type>[, lockfile][, separate PR])[  security: <id>,<id>]
update  <lockfile>  lock file maintenance  (lockFileMaintenance, separate PR)   (with lockFileMaintenance)
skip    <file>:<line>  <name>  <reason>                       (with -v)
error   <message>
note    go.sum changes are computed by apply/run              (plan, when a gomod update exists)
note    package-lock.json changes are computed by apply/run   (plan, when an npm update exists)
note    Cargo.lock changes are computed by apply/run          (plan, when a cargo update exists)
note    uv.lock, poetry.lock, pdm.lock and Pipfile.lock changes are computed by apply/run   (plan, when a Python update exists)
note    pip-compile output and --hash lines of requirements files are computed by apply/run   (plan, when a compiled requirements file, an input of one or a requirements file with --hash has an update)
note    .terraform.lock.hcl changes are computed by apply/run (plan, when a terraform update exists)
note    Gemfile.lock changes are computed by apply/run        (plan, when a bundler update exists)
note    composer.lock changes are computed by apply/run       (plan, when a composer update exists)
note    Manifest.toml changes are computed by apply/run       (plan, when a julia update exists)
note    MODULE.bazel.lock changes are computed by apply/run   (plan, when a bazel-module update exists)
pr      <status>[, <automerge>]  <branch>  <title>  <url>     (run)
dashboard <status>  <title>  <url>                            (run, with dependencyDashboard)
```

- For digest and pin updates, `<from>`/`<to>` read `<version>@<sha7>`. A lockfile-only update (`, lockfile`) starts at the locked version and changes only the lockfile.
- Per-dependency errors read `<file>:<line>: <name>: <err>`; a fatal error is printed as `deps: <err>`.
- The PR, automerge and dashboard statuses are listed in `deps run --help`; the skip reasons in the managers reference.

**Summary lines:**

```
<n> updates in <m> files, <k> skipped, <e> errors                                                (plan, apply)
<c> created, <u> updated, <x> closed, <s> up-to-date, <k> skipped, <a> automerge, <e> errors     (run)
... (dry run: <w> would change, <m> would automerge)                                              (run --dry-run, appended)
```

`rebased` counts as updated, `deleted` (obsolete branches without a PR) as closed. `<a>` counts auto-merge requests and direct merges, branch merges included (`automerge`, `merged`); the dashboard line is not counted.

**On a terminal** (stderr is a TTY) the `update` lines are left out: the summary block below lists the same updates, grouped by the PR they go to. Everything else is printed as above. Piped or redirected stderr always gets the line format above, byte for byte, so scripts and CI logs see no change. With colors (`--color`, default `auto`: on a terminal unless `NO_COLOR` is set or `TERM=dumb`, and with the `actions` format unless `NO_COLOR` is set; `CLICOLOR_FORCE` or `FORCE_COLOR` force them, also when piped) `error` is red and the summary colors the branches, `<file>:<line>` (dim), the update types (major red, minor yellow, patch green, digest cyan), security advisories (bold red) and the error count (red). Colors never change the layout; `--color always` keeps the piped layout.

Also on a terminal (unless `TERM=dumb`), a **status line** below the output shows the running phase while it runs, e.g. `⠹ resolving 212/308 · npm 18 in flight · 2.1s`: discovering files, extracting (files), resolving (dependencies) and, in `run`, PRs (groups handled), with the requests in flight per upstream pool (`--concurrency`), a rate-limit wait (`rate limited by <host>, <n>s`) and the error count. It is redrawn ten times a second, kept below every log record and removed when the phase ends, so it never stays in the output. Piped stderr never gets it.

**Diagnostics** are written with `log/slog` to stderr, filtered by `--log-level` and formatted by `--log-format`: `auto` (the default) is `actions` on a GitHub or Gitea Actions runner (below), else `pretty` on a terminal and `text` otherwise; `text` is `slog`'s `time=… level=INFO msg=… key=value` format; `json` one JSON object per record; `pretty` a compact line per record for people, `  3.55s WRN <message>  key=value …`: the time since deps started, right-aligned, a three-letter level (`DBG`, `INF`, `WRN`, `ERR`, colored with colors on), the message and the attributes (dim; grouped keys as `group.key`), durations rounded to 0.1ms below a second, else to the millisecond. Records and result lines are written whole, never interleaved. `run` builds up to four PRs at a time; the records of each PR (its build, lockfile tools, push and API calls) are held back and written as one block when the PR is done, so they never interleave with another PR's. What each level logs:

- `debug`: every HTTP request (credential-free URL), every `git`/`go`/`npm`/`cargo` process, credential-helper outcomes (never secrets), and per-dependency and per-PR decisions.
- `info`: the run trigger (`run`), the detected platform and how it was detected, the actions default host and its source (`actions default host host=<h> source=<s>`), phase counts and durations (one `files discovered files=<n> duration=<d> managers="docker 137, helm 134"` line for the walk, listing managers that matched files by count), and the bot's view of the repository.
- `warn` (the default): obsolete PRs left open because the run had errors, a schedule event whose cron cannot be read (every cron trigger matches), a dashboard skipped because issues are disabled, and dashboard labels that do not exist (Gitea/Forgejo).

Text logs round durations to 0.1ms below a second and to 1ms above (`duration=23ms`, `duration=11.003s`); JSON logs keep them in nanoseconds.

**Summary block.** Before the summary line, a block starting with an empty line and `Summary: ` gives the overview:

```
Summary: <n> dependencies, <u> updates in <g> PRs[, PRs: <count> <status>, ...][, <p> held back by minimumReleaseAge], <k> skipped, <e> errors (<duration>)
  PR <branch>  <title>  [<status> <url>; <automerge>; waiting for <schedule entries>]   (run; plan/apply: [automerge; schedule <entries>])
      <name>  <file>:<line>  <from> -> <to>  (<type>[, security: <id>,...])
  <status>  <branch>  <url>                                            (run: closed obsolete PRs and the dashboard)
  Held back by minimumReleaseAge:
      <name>  <file>:<line>  <version>  too new (released <date>, minimum <age>) | release time unknown (minimum <age>, timestamp-required)
  Deprecated or abandoned: <n> (-v lists each)
      <name>  <file>:<line>  deprecated: <reason> | abandoned: <reason>   (with -v)
  Skipped (-v lists each dependency):
    <count>  <reason>  (<how to update>)
```

The PRs are the ones `run` opens: plan and apply group their updates the same way. Without updates, errors or PR actions, the headline reads `No updates: <n> dependencies up to date, <k> skipped.` Errors are not repeated; they are the `error` lines above.

**Step summary.** If `GITHUB_STEP_SUMMARY` (or else `GITEA_STEP_SUMMARY`) is set, the same summary is appended to that file as markdown, under `## deps <command>`: the headline, one section per PR with its branch, status and link (run), automerge and schedule, and a `| Dependency | File | From | To | Type |` table; other PR actions (closed PRs, the dashboard); held-back releases; skip reasons with counts and how to update, with the skipped dependencies in a collapsible `<details>`; and the errors in full. The file stays within GitHub's 1 MiB per step, counting what is already in it: when the summary does not fit, the list of skipped dependencies is left out first, then the held-back releases, then the PRs' update tables; if it still does not fit, it is cut and ends with a note pointing at the job log.

**Actions workflow commands.** With `--log-format actions`, which `auto` selects on GitHub Actions (`GITHUB_ACTIONS=true`) and Gitea Actions (`GITEA_ACTIONS=true`; Gitea 1.26 and later render groups and annotation lines) but not on Forgejo (`FORGEJO_ACTIONS=true`, which prints workflow commands literally), records are `pretty` lines without the elapsed time (the runner timestamps every line), `INF <message>  key=value …`, colored unless `NO_COLOR` is set or `--color never` is given (GitHub and Gitea render ANSI colors; the result lines keep the piped layout, the summary is colored as on a terminal), and deps also prints workflow commands: each `error` line and a fatal error as an `::error` annotation, located at the dependency's file and line, at the file for a file that cannot be read or parsed, or for config errors at the line of the offending key in `.deps.yaml`; `warn` and `error` logs as `::warning`/`::error`; each security update as a `::notice` at its file and line; the `-v` skip lines inside a collapsible `::group::`; every phase as a group (`Discovering and extracting dependency files`, `Resolving <n> dependencies`) holding its records, a `deps: <phase> <done>/<total> · … · <seconds>s` line every 30 seconds while it runs (when stderr is no terminal) and one when it ends; and in `run` the records of each PR as a group `<branch>: <status> (<duration>)` (groups do not nest, so the PR phase has none of its own; its heartbeat and final lines stand between the PR groups). A token deps gets from the git credential helper is masked (`::add-mask::`) before it is used. GitHub shows 10 error, 10 warning and 10 notice annotations per step, so deps prints no more than that; the result lines are complete. Any other `--log-format` prints no workflow commands, also on a runner. With `RUNNER_DEBUG=1` (a re-run with debug logging) the default `--log-level` is `debug`; `$DEPS_LOG_LEVEL` and the flag still win.

**JSON report.** `--report <file>` (or `$DEPS_REPORT`) makes `plan`, `apply` and `run` also write the summary as one JSON object, replacing the file. It is not written when deps fails before it has a result (usage or config errors, exit 2). Fields:

- `command` (`plan`, `apply`, `run`, `run --dry-run`), `dependencies`, `durationMs`;
- `prs[]`: `branch`, `title`, `updates[]` (`name`, `manager`, `file`, `line`, `from`, `to`, `type`, `lockfile`, `advisories` as IDs; a lock file maintenance has type `lockFileMaintenance` and no versions); for `run` also `status`, `automerge`, `prWait` (the PR creation outcome of a branch waiting for its checks), `number`, `url`, `waitsFor` (schedule entries: crons and time windows) and `reason` (why a `skipped: update cannot be applied` update cannot be applied; the label of a `skipped: stop-updating label` PR);
- `otherActions[]`: closed obsolete PRs, obsolete PRs kept open by the `stopUpdatingLabel`, and the dashboard (`branch`, `title`, `status`, `number`, `url`, `dashboard`);
- `heldBack[]` (`name`, `file`, `line`, `version`, `reason`), `warnings[]` (`name`, `file`, `line`, `kind` `deprecated` or `abandoned`, `reason`; see `abandonmentThreshold` and the dependency dashboard), `skipped[]` (`reason`, `count`), `errors[]` (`message`, plus `file` and `line` for a dependency's lookup error, `file` for a file that cannot be read or parsed);
- `counts`: the step outputs below, by camelCase name.

With `run --repositories` the file holds the whole run: `command`, `durationMs`, `repositories[]` (the object above per repository, with `repository` set to its `owner/name`), `errors[]` (repositories that failed before they had a result, a clone or config error, as `<owner/name>: <message>`) and `counts` summed over the repositories (a failed repository counts as one error).

**Step outputs.** If `GITHUB_OUTPUT` is set (GitHub, Gitea and Forgejo runners), deps appends `updates`, `prs`, `held-back`, `skipped` and `errors`, and for `run` also `created`, `updated` (including rebased), `closed` (including deleted branches), `up-to-date`, `automerged` (auto-merge requests and direct merges) and `prs-skipped` (any `skipped: …` status), each as `<name>=<count>`. The dashboard is not counted. `run --repositories` appends them once, summed over its repositories; its step summary has one `## deps run: <owner/name>` section per repository.
