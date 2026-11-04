Results go to stderr (the diff of `plan` and the report of `explain` go to stdout), in fixed-width lines:

```
update  <file>:<line>  <name>  <from> -> <to>  (<type>[, separate PR])[  security: <id>,<id>]
skip    <file>:<line>  <name>  <reason>                       (with -v)
error   <message>
note    go.sum changes are computed by apply/run              (plan, when a gomod update exists)
note    package-lock.json changes are computed by apply/run   (plan, when an npm update exists)
pr      <status>[, <automerge>]  <branch>  <title>  <url>     (run)
dashboard <status>  <title>  <url>                            (run, with dependencyDashboard)
```

- For digest and pin updates, `<from>`/`<to>` read `<version>@<sha7>`.
- Per-dependency errors read `<file>:<line>: <name>: <err>`; a fatal error is printed as `deps: <err>`.
- The PR, automerge and dashboard statuses are listed in `deps run --help`; the skip reasons in the managers reference.

**Summary lines:**

```
<n> updates in <m> files, <k> skipped, <e> errors                                                (plan, apply)
<c> created, <u> updated, <x> closed, <s> up-to-date, <k> skipped, <a> automerge, <e> errors     (run)
... (dry run: <w> would change, <m> would automerge)                                              (run --dry-run, appended)
```

`rebased` counts as updated. `<a>` counts auto-merge requests and direct merges (`automerge`, `merged`); the dashboard line is not counted.

**Diagnostics** are written with `log/slog` to stderr, filtered by `--log-level` and formatted by `--log-format`:

- `debug`: every HTTP request (credential-free URL), every `git`/`go`/`npm` process, credential-helper outcomes (never secrets), and per-dependency and per-PR decisions.
- `info`: the run trigger (`run`), the detected platform and how it was detected, the actions default host and its source (`actions default host host=<h> source=<s>`), phase counts and durations, and the bot's view of the repository.
- `warn` (the default): obsolete PRs left open because the run had errors, a schedule event whose cron cannot be read (every schedule matches), a dashboard skipped because issues are disabled, and dashboard labels that do not exist (Gitea/Forgejo).

**Step summary.** If `GITHUB_STEP_SUMMARY` (or else `GITEA_STEP_SUMMARY`) is set, a `## deps` markdown table is appended to that file:

- `plan`/`apply`: `| Dependency | File | From | To | Type |`;
- `run`: `| Dependency | Status | PR |`.
