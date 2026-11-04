Detects the platform (GitHub, Gitea or Forgejo, from the CI environment, else from the `origin` remote), resolves every dependency, builds one commit per group on a temporary index (no checkout; the working tree is never modified), pushes it with `--force-with-lease`, and creates or updates the PRs.

- PRs whose updates all automerge (config key `automerge`) get platform auto-merge requested.
- With `dependencyDashboard`, the dashboard issue is read first (its ticked checkboxes apply to this run) and written last.
- Obsolete PRs under `branchPrefix` are closed and their branches deleted, but only when the run had no errors.
- The local `HEAD` must be the tip of `origin/<base>`.

**Token:** `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`, which uses the API anonymously. Git pushes use the checkout's credentials.

**Author precedence:** an explicitly set `--author-name`/`--author-email`, then `$DEPS_GIT_AUTHOR`, then the config key `gitAuthor`, then the defaults. Each field is resolved separately. An invalid `DEPS_GIT_AUTHOR` exits 2.

**Trigger:** schedules (config key `schedule`) are matched against what started the run, not against the clock: `--trigger-schedule`, then `$DEPS_TRIGGER_SCHEDULE`, then `$GITHUB_EVENT_NAME` with the `schedule` field of the payload at `$GITHUB_EVENT_PATH`. Runs that were not triggered by a cron (`workflow_dispatch`, `push`, a local run without CI variables) are in schedule for every group. Logged at info as `trigger event=<event> cron=<cron>`. The value of `--trigger-schedule` is validated as a 5-field cron; an invalid one exits 2.

**New-PR limit:** the minimum of `--max-new-prs` and the room left below `prConcurrentLimit`/`prHourlyLimit`, counting only the non-zero ones. Existing PRs are always updated.

**PR statuses** (the `pr` lines, see `deps help output`):

| Status | Meaning |
|---|---|
| `created` | new PR |
| `updated` | content changed or the PR was unmergeable; force-pushed and PR edited |
| `rebased` | unchanged content rebuilt on a moved base (`rebaseWhen: behind-base-branch`), or on request from the dashboard |
| `up-to-date` | nothing to do |
| `closed` | obsolete PR closed and branch deleted |
| `would-create`, `would-update`, `would-rebase` | `--dry-run` |
| `skipped: closed PR for this version` | an unmerged closed PR has the same branch and title (the dashboard's recreate checkbox overrides it) |
| `skipped: outside schedule` | the run was triggered by a cron that is not in the group's `schedule` (the dashboard's create checkbox overrides it) |
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

**Dashboard statuses** (the `dashboard` line): `created`, `updated`, `up-to-date` (body unchanged, not edited), `would-create`, `would-update` (`--dry-run`), and `skipped: issues disabled` (the repository has no issue tracker; not an error).
