Detects the platform (GitHub, Gitea or Forgejo, from the CI environment, else from the `origin` remote), resolves every dependency, builds one commit per group on a temporary index (no checkout; the working tree is never modified), pushes it with `--force-with-lease`, and creates or updates the PRs. With platform commits (GitHub, see below) the commits are created and the branches moved through the GitHub API instead.

- PRs whose updates all automerge (config key `automerge`) get platform auto-merge requested. With `automergeType: branch` there is no PR: the branch is pushed, and once its checks pass the base branch is fast-forwarded to it (one branch per run); failed checks open the PR.
- With `prCreation: not-pending` or `status-success` the branch is pushed without a PR, and a later run opens the PR once its checks finished or all passed. With `draftPR` new PRs are drafts (GitHub's draft flag; a `WIP: ` title prefix on Gitea and Forgejo, `Draft: ` on GitLab), unless they automerge.
- With `dependencyDashboard`, the dashboard issue is read first (its ticked checkboxes apply to this run) and written last.
- With `lockFileMaintenance`, one more PR on `<branchPrefix>lock-file-maintenance` refreshes every lockfile deps maintains (`Cargo.lock`, npm, pnpm, yarn and bun locks, `uv.lock`, `poetry.lock`, `pdm.lock`, `Pipfile.lock`, `Gemfile.lock`, `composer.lock`, Julia manifests, `pubspec.lock`, `MODULE.bazel.lock`, `mix.lock`, Gleam `manifest.toml`, `flake.lock`, Gradle lockfiles, `.terraform.lock.hcl`, and the `// indirect` requirements of `go.mod` with `go.sum`) with the ecosystem's tool; it is closed when the lockfiles are current.
- Obsolete PRs of the base under `branchPrefix` are closed and their branches deleted when the base's resolution had no errors, and obsolete branches without any PR (branch automerge, `prCreation`) are deleted when no base had any. PRs to other branches are left alone.
- On an open PR, its ticked rebase checkbox or the `rebaseLabel` (default `rebase`) requests a rebuild like the dashboard's rebase checkbox; deps unticks the box and removes the label afterwards. The `stopUpdatingLabel` (default `stop-updating`) makes deps leave the PR alone.
- With `--base` or the default branch, the local `HEAD` must be the tip of `origin/<base>`.

**Base branches:** `--base`, else the config key `baseBranches`, else the repository's default branch. `baseBranches` lists names, `$default` and `/<RE2>/` regexes over the branches on origin (names origin lacks are skipped with a warning). Each base is resolved from its own tip, with the checkout's config, its `rules[].baseBranches` matching that base: the checkout serves the base it is the tip of, every other base gets a temporary linked work tree of its fetched tip. With more than one entry, or a regex one, branches carry the base (`<branchPrefix><base>/<manager>/<name>`), the log prints `base    <base>` before each base's updates, and the dashboard has a section per base; the PR limits count all bases together. `--base` overrides `baseBranches` with one base and the plain branch names.

**Token:** `--token`, then `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, `GITLAB_TOKEN`, then the password from `git credential fill` for the platform host. Without any token, `run` fails with `no token for <host>`, except with `--dry-run`, which uses the API anonymously. Git pushes use the checkout's credentials. On GitLab, PRs are merge requests and `--repo` takes the project path (`group/subgroup/name`).

**Several repositories** (`--repositories`, or `$DEPS_REPOSITORIES`): instead of the checkout in `--dir`, `run` works on each listed repository of the forge in turn, so one job can serve a whole organization. An entry is `owner/name`, or `owner/<glob>` (`path.Match` syntax on the name, e.g. `my-org/*` or `my-org/svc-*`) for the repositories of an organization, else a user, that the token can list (GitHub, Gitea and Forgejo; on GitLab name each project); archived repositories and mirrors are left out, and a repository is run once even if several entries name it. The forge comes from the CI environment, else from the `origin` remote of `--dir` with `--platform`/`--api-url` applying as usual; `--repo` cannot be combined with it. Each repository is cloned shallowly (its default branch, or `--base`) into a temporary directory with the token as git credentials (`AUTHORIZATION: basic` of `x-access-token:<token>`, masked in the CI log), which also push its branches, and is removed afterwards. Its own `.deps.yaml` applies, or the `--config` file for all of them; datasources are shared, so a release list is fetched once for all repositories. Each repository starts with a `repository <owner/name>` line and ends with its own summary and counts line; a repository that fails (clone, config or run errors) does not stop the others, and the run ends with `<n> repositories, <m> failed` (exit 1 when `m` is not 0). In a multi-repository run annotations name the repository instead of a file, and `--report`, the step outputs and the step summary cover all repositories (see `deps help output`). The token needs write access to every repository.

**Author precedence:** an explicitly set `--author-name`/`--author-email`, then `$DEPS_GIT_AUTHOR`, then the config key `gitAuthor`, then the defaults. Each field is resolved separately. An invalid `DEPS_GIT_AUTHOR` exits 2. Platform commits ignore it: GitHub authors them as the token's account.

**Platform commits** (config key `platformCommit`, overridden by `$DEPS_PLATFORM_COMMIT`; `auto`, `enabled` or `disabled`): on GitHub, deps uploads the changed files as blobs, creates tree and commit through the REST API on top of the base, and moves the branch with the GraphQL `updateRefs` mutation, compare-and-swap against the tip listed at the start of the run (`stale info` if it moved). GitHub signs commits created with a GitHub App installation token (`ghs_…`, including `GITHUB_TOKEN`), so they show as verified, authored by the App's bot; with other tokens they stay unsigned (warned). `auto` enables them for an App token without a signing key. `enabled` on Gitea/Forgejo, or together with a signing key, exits 2; without a token (`--dry-run`) it falls back to git. Logged at info as `platform commits author=<name> <email>`. A branch tip GitHub links to the token's account, or with the configured git author, counts as deps' own.

**Post-upgrade tasks** (config key `rules[].postUpgradeTasks`) run only when a pattern of `--allow-tasks` (repeatable; `$DEPS_ALLOW_TASKS`, one per line) matches each command whole: `--allow-tasks 'go generate ./...'`, `--allow-tasks 'make *'`, `--allow-tasks '*'`. `*` matches any text except command chaining and substitution (`;`, `&`, `|`, backticks, `$(`, line breaks), so `make *` allows `make docs` but not `make docs; curl …` or `make $(id)`; operators written in the pattern match literally (`echo * >> deps.txt`), and the pattern `*` alone allows every command. Otherwise the group fails before anything runs, naming the command. After the post-processors, each group's commands (once per update with `executionMode: update`, then the branch-mode ones) run in a temporary copy of the work tree holding its changes, which is a throwaway git repository of the base (git works in it; nothing it writes reaches the checkout's repository), and every file below `--dir` they add, change or delete that `.gitignore` does not ignore (and the task's `files` globs match, if set) joins the commit. Submodule content never does, and moving a submodule's HEAD fails the group, as does a failing command. They run repository code; see `docs/ci.md`.

**Commit signing:** with `--signing-key` (or `$DEPS_SIGNING_KEY`), every commit deps creates is signed. The value is the private key itself, as a CI secret holds it, or the path of a file holding it; the format is detected from the armor header:

- `-----BEGIN OPENSSH PRIVATE KEY-----`: SSH signature (`gpg.format=ssh`) with a `0600` temporary copy of the key; needs `ssh-keygen`.
- `-----BEGIN PGP PRIVATE KEY BLOCK-----`: OpenPGP signature; the key is imported once into a temporary `GNUPGHOME`; needs `gpg`.

Only keys without a passphrase are supported; a protected key fails before any work is done. The temporary key material is removed when `run` exits, also on errors and on SIGINT/SIGTERM. The key is logged at info by fingerprint only, as `commit signing format=ssh|openpgp key=<fingerprint>`. A value that is neither format nor a readable file exits 2; a missing `ssh-keygen`/`gpg` exits 1. The forge shows the signature as verified only when the committer (`gitAuthor`) is the key owner's verified email. Without a key, git's own configuration applies (`commit.gpgSign` and `user.signingKey` of the checkout).

**Trigger:** cron triggers in `schedule` (crons with a fixed minute) are matched against what started the run, not against the clock: `--trigger-schedule`, then `$DEPS_TRIGGER_SCHEDULE`, then `$GITHUB_EVENT_NAME` with the `schedule` field of the payload at `$GITHUB_EVENT_PATH`. `workflow_dispatch`, `repository_dispatch` and local runs without CI variables match every trigger. Every other event (`push`, `issues`, `workflow_run`, …) is a maintenance run outside every trigger: only groups without a schedule (and security updates) get new PRs; open PRs are refreshed per `updateNotScheduled`; dashboard checkboxes still act. The value of `--trigger-schedule` is validated as a 5-field cron; an invalid one exits 2.

**Time windows** in `schedule` (`before 6am on monday`, crons with minute `*`) are evaluated against the run time in the config's `timezone`, whatever the event: `--schedule-time` (RFC 3339, e.g. `2026-11-09T05:30:00+01:00`), then `$DEPS_SCHEDULE_TIME`, then now; an invalid value exits 2. Logged at info as `trigger event=<event> cron=<cron> time=<RFC 3339>`.

**New-PR limit:** the minimum of `--max-new-prs` and the room left below `prConcurrentLimit`/`prHourlyLimit`, counting only the non-zero ones. Existing PRs are always updated.

**PR statuses** (the `pr` lines, see `deps help output`; for a group without a PR, branch automerge or waiting for `prCreation`, they describe its branch):

| Status | Meaning |
|---|---|
| `created` | new PR, or new branch (branch automerge, `prCreation`) |
| `updated` | content changed or the PR was unmergeable; force-pushed and PR edited. A branch automerge branch is also rebuilt when the base moved |
| `rebased` | unchanged content rebuilt on a moved base (`rebaseWhen: behind-base-branch`), or on request (the PR's rebase checkbox or `rebaseLabel`, or the dashboard) |
| `up-to-date` | nothing to do |
| `closed` | obsolete PR closed and branch deleted |
| `deleted` | obsolete branch without a PR deleted (branch automerge, `prCreation`) |
| `would-create`, `would-update`, `would-rebase` | `--dry-run` |
| `skipped: closed PR for this version` | an unmerged closed PR has the same branch and title, or the same versions of a major update (the dashboard's recreate checkbox overrides it) |
| `skipped: closed PR for this major` | an unmerged closed PR of the same major branch (`-major`, `-v<N>`) targeted the same majors: closing a major PR ignores that major until the next one (the dashboard's recreate checkbox overrides it) |
| `skipped: awaiting dashboard approval` | the group needs `dashboardApproval` and has no PR or branch yet: listed under *Pending approval* on the dashboard, whose create checkbox approves it |
| `skipped: outside schedule` | the run was triggered by a cron that is not in the group's `schedule`, or by another event, and no time window of it contains the run time (the dashboard's create checkbox overrides it) |
| `skipped: new PR limit reached` | a PR limit is exhausted (dashboard checkboxes override it) |
| `skipped: branch modified by someone else` | the branch tip's author is neither the bot nor in `gitIgnoredAuthors` (a rebase request discards those commits) |
| `skipped: stop-updating label` | the open PR has the `stopUpdatingLabel`: not pushed, rebased or closed as obsolete |

**Automerge outcomes** (appended to the PR status, e.g. `created, automerge`):

| Outcome | Meaning |
|---|---|
| `automerge` | platform auto-merge requested (or already scheduled); the forge merges once required checks pass |
| `automerge enabled` | GitHub reports auto-merge enabled already; not requested again |
| `automerge pending` | Gitea/Forgejo cannot schedule it yet (mergeability check of a fresh push still running), or GitLab refused it for now (draft, conflicts, failed or missing required pipeline, approvals, head moved); the next run asks again |
| `automerge waiting for checks` | GitHub will not auto-merge (auto-merge disallowed, or nothing to wait for) and the PR is not mergeable yet or its statuses/check runs are not all green; the next run checks again |
| `merged` | merged directly: the GitHub fallback (mergeable with all statuses and check runs green), or a branch automerge branch fast-forwarded into the base and deleted |
| `branch automerge waiting for checks` | branch automerge: the branch has no PR and its checks have not finished or none are reported; the next run checks again |
| `branch automerge next run` | branch automerge: the checks passed, but another branch was merged in this run or the base moved; the next run rebases or merges it |
| `would-automerge` | `--dry-run` |

**PR creation outcomes** (appended to the status of a branch without a PR under `prCreation: not-pending` or `status-success`, e.g. `created, PR waiting for checks`):

| Outcome | Meaning |
|---|---|
| `PR waiting for checks` | the branch's checks have not finished or none are reported; the next run checks again |
| `PR waiting for green checks` | `status-success`: a check failed; the PR is opened once they all pass, or when ticked on the dashboard |

**Dashboard statuses** (the `dashboard` line): `created`, `updated`, `up-to-date` (body unchanged, not edited), `would-create`, `would-update` (`--dry-run`), and `skipped: issues disabled` (the repository has no issue tracker; not an error).
