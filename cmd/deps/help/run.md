Detects the platform (GitHub, Gitea or Forgejo, from the CI environment, else from the `origin` remote), resolves every dependency, builds one commit per group on a temporary index (no checkout; the working tree is never modified), pushes it with `--force-with-lease`, and creates or updates the PRs. With platform commits (GitHub, see below) the commits are created and the branches moved through the GitHub API instead.

- PRs whose updates all automerge (config key `automerge`) get platform auto-merge requested. With `automergeType: branch` there is no PR: the branch is pushed, and once its checks pass the base branch is fast-forwarded to it (one branch per run); failed checks open the PR.
- With `dependencyDashboard`, the dashboard issue is read first (its ticked checkboxes apply to this run) and written last.
- With `lockFileMaintenance`, one more PR on `<branchPrefix>lock-file-maintenance` refreshes every lockfile deps maintains (`Cargo.lock`, npm, pnpm, yarn and bun locks, `uv.lock`, `poetry.lock`, `pdm.lock`, `Pipfile.lock`, Gradle lockfiles, `.terraform.lock.hcl`, and the `// indirect` requirements of `go.mod` with `go.sum`) with the ecosystem's tool; it is closed when the lockfiles are current.
- Obsolete PRs under `branchPrefix` are closed and their branches deleted, and obsolete branches without any PR (branch automerge) are deleted, but only when the run had no errors.
- On an open PR, its ticked rebase checkbox or the `rebaseLabel` (default `rebase`) requests a rebuild like the dashboard's rebase checkbox; deps unticks the box and removes the label afterwards. The `stopUpdatingLabel` (default `stop-updating`) makes deps leave the PR alone.
- The local `HEAD` must be the tip of `origin/<base>`.

**Token:** `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`, which uses the API anonymously. Git pushes use the checkout's credentials.

**Author precedence:** an explicitly set `--author-name`/`--author-email`, then `$DEPS_GIT_AUTHOR`, then the config key `gitAuthor`, then the defaults. Each field is resolved separately. An invalid `DEPS_GIT_AUTHOR` exits 2. Platform commits ignore it: GitHub authors them as the token's account.

**Platform commits** (config key `platformCommit`, overridden by `$DEPS_PLATFORM_COMMIT`; `auto`, `enabled` or `disabled`): on GitHub, deps uploads the changed files as blobs, creates tree and commit through the REST API on top of the base, and moves the branch with the GraphQL `updateRefs` mutation, compare-and-swap against the tip listed at the start of the run (`stale info` if it moved). GitHub signs commits created with a GitHub App installation token (`ghs_…`, including `GITHUB_TOKEN`), so they show as verified, authored by the App's bot; with other tokens they stay unsigned (warned). `auto` enables them for an App token without a signing key. `enabled` on Gitea/Forgejo, or together with a signing key, exits 2; without a token (`--dry-run`) it falls back to git. Logged at info as `platform commits author=<name> <email>`. A branch tip GitHub links to the token's account, or with the configured git author, counts as deps' own.

**Commit signing:** with `--signing-key` (or `$DEPS_SIGNING_KEY`), every commit deps creates is signed. The value is the private key itself, as a CI secret holds it, or the path of a file holding it; the format is detected from the armor header:

- `-----BEGIN OPENSSH PRIVATE KEY-----`: SSH signature (`gpg.format=ssh`) with a `0600` temporary copy of the key; needs `ssh-keygen`.
- `-----BEGIN PGP PRIVATE KEY BLOCK-----`: OpenPGP signature; the key is imported once into a temporary `GNUPGHOME`; needs `gpg`.

Only keys without a passphrase are supported; a protected key fails before any work is done. The temporary key material is removed when `run` exits, also on errors and on SIGINT/SIGTERM. The key is logged at info by fingerprint only, as `commit signing format=ssh|openpgp key=<fingerprint>`. A value that is neither format nor a readable file exits 2; a missing `ssh-keygen`/`gpg` exits 1. The forge shows the signature as verified only when the committer (`gitAuthor`) is the key owner's verified email. Without a key, git's own configuration applies (`commit.gpgSign` and `user.signingKey` of the checkout).

**Trigger:** schedules (config key `schedule`) are matched against what started the run, not against the clock: `--trigger-schedule`, then `$DEPS_TRIGGER_SCHEDULE`, then `$GITHUB_EVENT_NAME` with the `schedule` field of the payload at `$GITHUB_EVENT_PATH`. `workflow_dispatch`, `repository_dispatch` and local runs without CI variables are in schedule for every group. Every other event (`push`, `issues`, `workflow_run`, …) is a maintenance run outside every schedule: only groups without a schedule (and security updates) get new PRs; open PRs are refreshed per `updateNotScheduled`; dashboard checkboxes still act. Logged at info as `trigger event=<event> cron=<cron>`. The value of `--trigger-schedule` is validated as a 5-field cron; an invalid one exits 2.

**New-PR limit:** the minimum of `--max-new-prs` and the room left below `prConcurrentLimit`/`prHourlyLimit`, counting only the non-zero ones. Existing PRs are always updated.

**PR statuses** (the `pr` lines, see `deps help output`; for a branch automerge group without a PR they describe its branch):

| Status | Meaning |
|---|---|
| `created` | new PR, or new branch (branch automerge) |
| `updated` | content changed or the PR was unmergeable; force-pushed and PR edited. A branch automerge branch is also rebuilt when the base moved |
| `rebased` | unchanged content rebuilt on a moved base (`rebaseWhen: behind-base-branch`), or on request (the PR's rebase checkbox or `rebaseLabel`, or the dashboard) |
| `up-to-date` | nothing to do |
| `closed` | obsolete PR closed and branch deleted |
| `deleted` | obsolete branch without a PR deleted (branch automerge) |
| `would-create`, `would-update`, `would-rebase` | `--dry-run` |
| `skipped: closed PR for this version` | an unmerged closed PR has the same branch and title, or the same versions of a major update (the dashboard's recreate checkbox overrides it) |
| `skipped: closed PR for this major` | an unmerged closed PR of the same major branch (`-major`, `-v<N>`) targeted the same majors: closing a major PR ignores that major until the next one (the dashboard's recreate checkbox overrides it) |
| `skipped: outside schedule` | the run was triggered by a cron that is not in the group's `schedule` (the dashboard's create checkbox overrides it) |
| `skipped: new PR limit reached` | a PR limit is exhausted (dashboard checkboxes override it) |
| `skipped: branch modified by someone else` | the branch tip's author is neither the bot nor in `gitIgnoredAuthors` (a rebase request discards those commits) |
| `skipped: stop-updating label` | the open PR has the `stopUpdatingLabel`: not pushed, rebased or closed as obsolete |

**Automerge outcomes** (appended to the PR status, e.g. `created, automerge`):

| Outcome | Meaning |
|---|---|
| `automerge` | platform auto-merge requested (or already scheduled); the forge merges once required checks pass |
| `automerge enabled` | GitHub reports auto-merge enabled already; not requested again |
| `automerge pending` | Gitea/Forgejo cannot schedule it yet (mergeability check of a fresh push still running); the next run asks again |
| `automerge waiting for checks` | GitHub will not auto-merge (auto-merge disallowed, or nothing to wait for) and the PR is not mergeable yet or its statuses/check runs are not all green; the next run checks again |
| `merged` | merged directly: the GitHub fallback (mergeable with all statuses and check runs green), or a branch automerge branch fast-forwarded into the base and deleted |
| `branch automerge waiting for checks` | branch automerge: the branch has no PR and its checks have not finished or none are reported; the next run checks again |
| `branch automerge next run` | branch automerge: the checks passed, but another branch was merged in this run or the base moved; the next run rebases or merges it |
| `would-automerge` | `--dry-run` |

**Dashboard statuses** (the `dashboard` line): `created`, `updated`, `up-to-date` (body unchanged, not edited), `would-create`, `would-update` (`--dry-run`), and `skipped: issues disabled` (the repository has no issue tracker; not an error).
