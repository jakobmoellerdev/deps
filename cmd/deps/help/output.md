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
note    --hash lines of requirements files are computed by apply/run   (plan, when a requirements file with --hash has an update)
note    .terraform.lock.hcl changes are computed by apply/run (plan, when a terraform update exists)
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

**On a terminal** (stderr is a TTY) the `update` lines are left out: the summary block below lists the same updates, grouped by the PR they go to. Everything else is printed as above. Piped or redirected stderr always gets the line format above, byte for byte, so scripts and CI logs see no change. With colors (`--color`, default `auto`: on a terminal unless `NO_COLOR` is set or `TERM=dumb`; `CLICOLOR_FORCE` or `FORCE_COLOR` force them, also when piped) `error` is red and the summary colors the branches, `<file>:<line>` (dim), the update types (major red, minor yellow, patch green, digest cyan), security advisories (bold red) and the error count (red). Colors never change the layout; `--color always` keeps the piped layout.

**Diagnostics** are written with `log/slog` to stderr, filtered by `--log-level` and formatted by `--log-format`: `auto` (the default) is `pretty` on a terminal and `text` otherwise; `text` is `slog`'s `time=… level=INFO msg=… key=value` format; `json` one JSON object per record; `pretty` a compact line per record for people, `  3.55s WRN <message>  key=value …`: the time since deps started, right-aligned, a three-letter level (`DBG`, `INF`, `WRN`, `ERR`, colored with colors on), the message and the attributes (dim; grouped keys as `group.key`), durations rounded to 0.1ms below a second, else to the millisecond. Records and result lines are written whole, never interleaved. What each level logs:

- `debug`: every HTTP request (credential-free URL), every `git`/`go`/`npm`/`cargo` process, credential-helper outcomes (never secrets), and per-dependency and per-PR decisions.
- `info`: the run trigger (`run`), the detected platform and how it was detected, the actions default host and its source (`actions default host host=<h> source=<s>`), phase counts and durations (one `files discovered files=<n> duration=<d> managers="docker 137, helm 134"` line for the walk, listing managers that matched files by count), and the bot's view of the repository.
- `warn` (the default): obsolete PRs left open because the run had errors, a schedule event whose cron cannot be read (every schedule matches), a dashboard skipped because issues are disabled, and dashboard labels that do not exist (Gitea/Forgejo).

Text logs round durations to 0.1ms below a second and to 1ms above (`duration=23ms`, `duration=11.003s`); JSON logs keep them in nanoseconds.

**Summary block.** Before the summary line, a block starting with an empty line and `Summary: ` gives the overview:

```
Summary: <n> dependencies, <u> updates in <g> PRs[, PRs: <count> <status>, ...][, <p> held back by minimumReleaseAge], <k> skipped, <e> errors (<duration>)
  PR <branch>  <title>  [<status> <url>; <automerge>; waiting for <cron>]   (run; plan/apply: [automerge; schedule <crons>])
      <name>  <file>:<line>  <from> -> <to>  (<type>[, security: <id>,...])
  <status>  <branch>  <url>                                            (run: closed obsolete PRs and the dashboard)
  Held back by minimumReleaseAge:
      <name>  <file>:<line>  <version>  too new (released <date>, minimum <age>) | release time unknown (minimum <age>, timestamp-required)
  Skipped (-v lists each dependency):
    <count>  <reason>  (<how to update>)
```

The PRs are the ones `run` opens: plan and apply group their updates the same way. Without updates, errors or PR actions, the headline reads `No updates: <n> dependencies up to date, <k> skipped.` Errors are not repeated; they are the `error` lines above.

**Step summary.** If `GITHUB_STEP_SUMMARY` (or else `GITEA_STEP_SUMMARY`) is set, the same summary is appended to that file as markdown, under `## deps <command>`: the headline, one section per PR with its branch, status and link (run), automerge and schedule, and a `| Dependency | File | From | To | Type |` table; other PR actions (closed PRs, the dashboard); held-back releases; skip reasons with counts and how to update, with the skipped dependencies in a collapsible `<details>`; and the errors in full. The file stays within GitHub's 1 MiB per step, counting what is already in it: when the summary does not fit, the list of skipped dependencies is left out first, then the held-back releases, then the PRs' update tables; if it still does not fit, it is cut and ends with a note pointing at the job log.

**GitHub Actions annotations.** On GitHub Actions (`GITHUB_ACTIONS=true`, but not on Gitea or Forgejo runners, which don't render them) deps also prints workflow commands: each `error` line and a fatal error as an `::error` annotation, located at the dependency's file and line, or for config errors at the line of the offending key in `.deps.yaml`; `warn` and `error` logs as `::warning`/`::error`; and the `-v` skip lines inside a collapsible `::group::`. GitHub shows 10 error and 10 warning annotations per step, so deps prints no more than that; the result lines are complete.

**JSON report.** `--report <file>` (or `$DEPS_REPORT`) makes `plan`, `apply` and `run` also write the summary as one JSON object, replacing the file. It is not written when deps fails before it has a result (usage or config errors, exit 2). Fields:

- `command` (`plan`, `apply`, `run`, `run --dry-run`), `dependencies`, `durationMs`;
- `prs[]`: `branch`, `title`, `updates[]` (`name`, `manager`, `file`, `line`, `from`, `to`, `type`, `lockfile`, `advisories` as IDs; a lock file maintenance has type `lockFileMaintenance` and no versions); for `run` also `status`, `automerge`, `number`, `url`, `waitsFor` (crons) and `reason` (why a `skipped: update cannot be applied` update cannot be applied);
- `otherActions[]`: closed obsolete PRs and the dashboard (`branch`, `title`, `status`, `number`, `url`, `dashboard`);
- `heldBack[]` (`name`, `file`, `line`, `version`, `reason`), `skipped[]` (`reason`, `count`), `errors[]` (`message`, plus `file` and `line` for a dependency's lookup error);
- `counts`: the step outputs below, by camelCase name.

**Step outputs.** If `GITHUB_OUTPUT` is set (GitHub, Gitea and Forgejo runners), deps appends `updates`, `prs`, `held-back`, `skipped` and `errors`, and for `run` also `created`, `updated` (including rebased), `closed` (including deleted branches), `up-to-date`, `automerged` (auto-merge requests and direct merges) and `prs-skipped` (any `skipped: …` status), each as `<name>=<count>`. The dashboard is not counted.
