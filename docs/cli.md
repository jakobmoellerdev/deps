# CLI reference

```
deps [command] [flags]
```

| Command | Purpose |
|---|---|
| `deps plan` | print the updates as a unified diff (stdout) and a report (stderr); writes nothing |
| `deps apply` | write the updates, including `go mod tidy` and lockfile regeneration |
| `deps run` | push one branch and open or update one PR per dependency or group |
| `deps explain <name-glob>...` | explain the rules, candidates and decision for matching deps |
| `deps schema` | print the JSON Schema (draft 2020-12) of `.deps.yaml` |
| `deps version` | print the version (set with `-ldflags "-X main.version=…"`, default `dev`; release binaries print their tag, e.g. `v0.1.0`, and the action prints it after download) |
| `deps completion <shell>` | shell completion script (cobra) |

## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | repository directory to scan |
| `--config` | `<dir>/.deps.yaml`, then `.deps.yml` | config file; an explicit path must exist |
| `-v`, `--verbose` | `false` | also print `skip` lines |
| `--log-level` | `$DEPS_LOG_LEVEL`, else `warn` | `debug`, `info`, `warn`, `error` |
| `--log-format` | `$DEPS_LOG_FORMAT`, else `text` | `text`, `json` |
| `--concurrency` | `32` | maximum concurrent network requests and lookups; must be at least 1 |

Hidden flags for profiling: `--cpuprofile <file>` writes a CPU profile, and `--memprofile <file>` writes an allocation profile on exit (pprof format).

## `deps plan`, `deps apply`

Neither takes arguments. Both resolve every dep and print the same report on stderr. Then:

- **`plan`** writes a unified diff per changed file to stdout (`a/<path>` → `b/<path>`); `git apply` accepts it. Post-processors do not run, so `go.sum` and lockfile changes are missing from the diff.
- **`apply`** writes the changed files, including `go mod tidy` output and regenerated `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`.

A dep with updates in several PR buckets contributes one update to the files: the newest non-major one, else the lowest major. Its other updates are reported with `, separate PR`.

The platform is only detected from CI environment variables. Locally, lookups run without a platform token unless one of the token variables is set.

## `deps run`

Without arguments, `run` detects the platform, resolves, builds one commit per group on a temporary index (no checkout, and the working tree is never modified), pushes `--force-with-lease`, and creates or updates PRs. PRs whose updates all [automerge](configuration.md#automerge-automergestrategy-automergetype) get platform auto-merge requested. With [`dependencyDashboard`](configuration.md#dependencydashboard-dependencydashboardtitle-dependencydashboardlabels), the dashboard issue is read first (its ticked checkboxes apply to this run) and written last. Obsolete PRs under `branchPrefix` are closed and their branches deleted, but only when the run had no errors. The local `HEAD` must be the tip of `origin/<base>`.

| Flag | Default | Meaning |
|---|---|---|
| `--platform` | `auto` | `auto`, `github`, `gitea`, `forgejo` |
| `--api-url` | detected | API base URL |
| `--repo` | detected | `owner/name` |
| `--token` | token chain (below) | platform API token; git pushes use the checkout's credentials |
| `--base` | the repository's default branch | base branch |
| `--author-name` | `deps[bot]` | commit author name |
| `--author-email` | `deps-bot@noreply.invalid` | commit author email |
| `--max-new-prs` | `10` | PRs created per run; `0` = unlimited |
| `--dry-run` | `false` | report without pushing or calling write APIs (no PR, merge or issue writes); works without a token |

**Author precedence:** an explicitly set `--author-name`/`--author-email`, then `$DEPS_GIT_AUTHOR`, then [`gitAuthor`](configuration.md#gitauthor-gitignoredauthors), then the defaults. Each field is resolved separately. An invalid `DEPS_GIT_AUTHOR` exits 2.

**New-PR limit:** the minimum of `--max-new-prs` and the room left below [`prConcurrentLimit`/`prHourlyLimit`](configuration.md#prconcurrentlimit-prhourlylimit), counting only the non-zero ones.

## `deps explain`

Takes one or more name globs (doublestar syntax as in rule `names`, for example `'actions/*'` or `'k8s.io/*'`). Only matching deps are resolved. The report goes to stdout, one block per occurrence:

```
actions/checkout  .gitea/workflows/ci.yml:10  (github-actions, action, git-tags, tag)
  current   v7.0.1
  rules     none
  policy    pin=keep allow=- updates=major,minor,patch,digest,pin group=- schedule=always minAge=-
  candidates 68
  rejected  not newer: 58 (v7.0.1, v7.0.0, v6.1.0, ...)  precision: 7 (v7, v6, v5, ...)  invalid: 2 (v2-beta, v6-beta)  prefix: 1 (1.0.0)
  result    up to date

github.com/spf13/pflag  go.mod:28  (gomod, indirect, go, go)
  current   v1.0.10
  rules     none
  policy    pin=keep allow=- updates=major,minor,patch,digest,pin group=- schedule=always minAge=-
  result    skipped: ignored by default (indirect; enable with a rule setting ignore: false)
```

The other lines in a block:

- `builtin   monorepo <name> -> group "<name> monorepo"` appears for monorepo members.
- `rules` lists the matching rules as `#<i> <fields>`. Rules with `updateTypes` that applied to an update follow as `#<i> <fields> (applies to <type>)`.
- `policy` is followed by one `<type>: <policy>` line per such type. `versioning=` is appended when a rule set it.
- `rejected` groups versions by reason, most frequent first, with up to 3 newest examples each.
- `result` is an update line per PR bucket, for example `update v6.0.0 -> v7.0.1 (major, bucket major)`.
- Each advisory is printed as `security  <id>  <summary>  <url>`.

- `rules none` is printed when no rule matched.
- `result` reads `skipped: <reason>`, `error: <err>`, `up to date`, or one `update` line per PR bucket.
- Rejection reasons include `ignored by rule #4 for major`, `outside allow /^\d+\.\d+$/`, `update type major not allowed`, `too new (released 2026-11-01, minimum 28 days)`, `older than current`, `prerelease`, `precision` and `not newer`.
- Errors go to stderr as `error   <msg>`.
- A glob that matches no dep fails with `no dependency matches <globs>` (exit 1). No globs, or an invalid glob, exits 2.

## `deps schema`

Prints the JSON Schema of `.deps.yaml`, reflected from the config types. Unknown keys are rejected (`additionalProperties: false`), as in the loader.

```sh
deps schema > deps.schema.json    # then: # yaml-language-server: $schema=./deps.schema.json
```

## Output

Results go to stderr (the diff and `explain` go to stdout), in fixed-width lines:

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
- Per-dep errors read `<file>:<line>: <name>: <err>`.

**Summary lines:**

```
<n> updates in <m> files, <k> skipped, <e> errors                                  (plan, apply)
<c> created, <u> updated, <x> closed, <s> up-to-date, <k> skipped, <a> automerge, <e> errors     (run)
... (dry run: <w> would change, <m> would automerge)                                              (run --dry-run, appended)
```

`rebased` counts as updated. `<a>` counts auto-merge requests and direct merges (`automerge`, `merged`); the dashboard line is not counted.

**PR statuses:**

| Status | Meaning |
|---|---|
| `created` | new PR |
| `updated` | content changed or the PR was unmergeable; force-pushed and PR edited |
| `rebased` | unchanged content rebuilt on a moved base (`rebaseWhen: behind-base-branch`), or on request from the dashboard |
| `up-to-date` | nothing to do |
| `closed` | obsolete PR closed and branch deleted |
| `would-create`, `would-update`, `would-rebase` | `--dry-run` |
| `skipped: closed PR for this version` | an unmerged closed PR has the same branch and title (the dashboard's recreate checkbox overrides it) |
| `skipped: outside schedule` | outside the schedule window (the dashboard's create checkbox overrides it) |
| `skipped: new PR limit reached` | a PR limit is exhausted (dashboard checkboxes override it) |
| `skipped: branch modified by someone else` | the branch tip's author is neither the bot nor in `gitIgnoredAuthors` (the dashboard's rebase checkbox discards those commits) |

**Automerge outcomes** (appended to the PR status, e.g. `created, automerge`):

| Outcome | Meaning |
|---|---|
| `automerge` | platform auto-merge requested (or already scheduled); the forge merges once required checks pass |
| `automerge enabled` | GitHub reports auto-merge enabled already; not requested again |
| `automerge pending` | Gitea/Forgejo cannot schedule it yet (mergeability check of a fresh push still running); the next run asks again |
| `automerge waiting for checks` | GitHub will not auto-merge (auto-merge disallowed, or nothing to wait for) and the PR is not mergeable yet or its statuses/check runs are not all green; the next run checks again |
| `merged` | GitHub fallback: merged directly, mergeable with all statuses and check runs green |
| `would-automerge` | `--dry-run` |

**Dashboard statuses:** `created`, `updated`, `up-to-date` (body unchanged, not edited), `would-create`, `would-update` (`--dry-run`), and `skipped: issues disabled` (the repository has no issue tracker; not an error).

**Diagnostics.** Diagnostics are written with `log/slog` to stderr, filtered by `--log-level`.
- `info`: the detected platform and how it was detected, the actions default host and its source, phase counts and durations, and the bot's view of the repository.
- `debug`: every HTTP request (credential-free URL), every `git`/`go`/`npm` process, credential-helper outcomes (never secrets), and per-dep and per-PR decisions.
- `warn`: obsolete PRs left open because the run had errors, a dashboard skipped because issues are disabled, and dashboard labels that do not exist (Gitea/Forgejo).

**Step summary.** If `GITHUB_STEP_SUMMARY` (or else `GITEA_STEP_SUMMARY`) is set, a `## deps` markdown table is appended to that file:
- `plan`/`apply`: `| Dependency | File | From | To | Type |`;
- `run`: `| Dependency | Status | PR |`.

## Exit codes

| Code | When |
|---|---|
| `0` | success, no errors |
| `1` | any error, including per-dep lookup errors and per-PR errors (already printed as `error` lines), or a fatal error printed as `deps: <err>` |
| `2` | usage error: bad flag or argument, invalid `--log-level`/`--log-format`/`--platform`/`--concurrency`, invalid `DEPS_GIT_AUTHOR`, or any config error |

## Environment

| Variable | Used by | Meaning |
|---|---|---|
| `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN` | all | platform token, first non-empty wins; `run --token` takes precedence. `run` falls back to `git credential fill` for the platform host, and without any token fails with `no token for <host>` (except `--dry-run`). |
| `DEPS_GITHUB_COM_TOKEN` | all | token for github.com lookups when the platform is not github.com (avoids anonymous rate limits for actions hosted on GitHub) |
| `DEPS_DEFAULT_ACTIONS_URL` | all | default host of `owner/repo@ref` actions (hostname, or a URL whose host is used); overridden by `actions.defaultHost` |
| `DEPS_NODE_DIST_URL` | `node-version` | base URL of a nodejs.org `dist` mirror (default `https://nodejs.org/dist`) |
| `DEPS_GIT_AUTHOR` | `run` | commit author `Name <email>`; between the flags and `gitAuthor` |
| `DEPS_LOG_LEVEL`, `DEPS_LOG_FORMAT` | all | defaults of `--log-level`, `--log-format` |
| `NPM_CONFIG_REGISTRY`, `npm_config_registry` | `npm` | registry when no `.npmrc` sets one (default `https://registry.npmjs.org/`) |
| `GOPROXY`, `GONOPROXY`, `GOPRIVATE`, `GOMODCACHE` | `go` | read via `go env -json`. `GOMODCACHE`'s `cache/download/**/@v/*.info` files provide release times without network. |
| `GITHUB_ACTIONS`, `GITEA_ACTIONS`, `FORGEJO_ACTIONS`, `GITHUB_SERVER_URL`, `GITHUB_API_URL`, `GITHUB_REPOSITORY`, `FORGEJO_*` | all | platform detection ([ci.md](ci.md#platform-detection)) |
| `GITHUB_STEP_SUMMARY`, `GITEA_STEP_SUMMARY` | all | step summary file |

On a 401 without a static token, git tag lookups call `git credential fill` once per host, with `GIT_TERMINAL_PROMPT=0` and a 10s timeout. Every `git` process deps runs has `GIT_TERMINAL_PROMPT=0`, so deps never prompts.
