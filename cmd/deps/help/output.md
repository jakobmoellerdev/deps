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

**Diagnostics** are written with `log/slog` to stderr, filtered by `--log-level` and formatted by `--log-format`:

- `debug`: every HTTP request (credential-free URL), every `git`/`go`/`npm`/`cargo` process, credential-helper outcomes (never secrets), and per-dependency and per-PR decisions.
- `info`: the run trigger (`run`), the detected platform and how it was detected, the actions default host and its source (`actions default host host=<h> source=<s>`), phase counts and durations, and the bot's view of the repository.
- `warn` (the default): obsolete PRs left open because the run had errors, a schedule event whose cron cannot be read (every schedule matches), a dashboard skipped because issues are disabled, and dashboard labels that do not exist (Gitea/Forgejo).

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

**Step summary.** If `GITHUB_STEP_SUMMARY` (or else `GITEA_STEP_SUMMARY`) is set, the same summary is appended to that file as markdown, under `## deps <command>`: the headline, one section per PR with its branch, status and link (run), automerge and schedule, and a `| Dependency | File | From | To | Type |` table; other PR actions (closed PRs, the dashboard); held-back releases; skip reasons with counts and how to update, with the skipped dependencies in a collapsible `<details>`; and the errors in full.
