Resolves two checkouts of the repository, `--dir` (the pull request) and `--base-dir` (its base branch), each with its own `.deps.yaml`, and reports how the update PRs `deps run` would open differ: new PRs, changed PRs (other updates or another title) and PRs no longer opened (open ones would be closed as obsolete). A PR without a base counterpart whose dependency file the base checkout failed to extract, or whose dependency it failed to look up, is reported as not compared instead of new. Lookups are shared between the two checkouts, so each release list is fetched once. Nothing is written to the repository or the forge, except the check run below.

Output lines (stderr):

```
base    <base>[  config <files>]  (with a base branch, see below)
new     <branch>  <title>
changed <branch>  <title>
unknown <branch>  <title>             (not compared: the base checkout failed on its dependencies)
removed <branch>  <title>
error   <message>                    (lookup errors of --dir; those of --base-dir prefixed "base checkout:")
check   <url>                        (the check run, GitHub)
<n> new, <c> changed, <r> no longer opened, <s> the same[, <u> not compared] (<duration>)
```

**Base branch.** `--base <branch>` (default: `$GITHUB_BASE_REF`, then `$CI_MERGE_REQUEST_TARGET_BRANCH_NAME`) is the branch the pull request targets; both checkouts are resolved for it as `deps run` resolves that base: their `rules[].baseBranches` match it. When it is not the default branch (origin's `HEAD`) and the default branch's config file sets `useBaseBranchConfig: merge` (or `merge-if-present`), the config file of each checkout is merged over the default branch's (fetched from origin), as `run` merges the base's file over the default branch's checkout. The `base` line and the step summary name the files applied, e.g. `base    release/1.x  config main:.deps.yaml + release/1.x:.deps.yaml`. When the default branch or its config cannot be read, each checkout keeps its own config, with a warning; with `--renovate` it is not read.

**Step summary.** The comparison is appended to `GITHUB_STEP_SUMMARY` (or `GITEA_STEP_SUMMARY`) as markdown, with an update table per new or changed PR.

**Check run (GitHub).** In a `pull_request` or `pull_request_target` job on GitHub, deps also creates the completed check run `deps preview` on the pull request's head commit (from the event payload), with the same markdown as its output, so it shows on the PR's *Checks* tab. The conclusion is `success`, or `neutral` when lookups failed in either checkout: the preview informs and never blocks. Only App installation tokens (`GITHUB_TOKEN` included) can create check runs, with `checks: write`; for a token that can't (a PAT, a fork PR's read-only token), deps prints a `note` and keeps the step summary.

Exit codes: 0 with no lookup errors in `--dir` (errors of `--base-dir` are reported but do not fail; lookups that failed on an exhausted rate limit are `warn` lines in either checkout), 1 with lookup errors, 2 for an invalid config in either checkout.

**Without checkouts.** `--repositories <owner/name>` together with `--pr <number>` (GitLab: the merge request number) makes deps read the pull request from the forge and clone both sides itself: its head ref (`refs/pull/<n>/head`, on GitLab `refs/merge-requests/<n>/head`, so pull requests from forks work) and its base branch, into temporary directories that are removed again. The base branch is the one the forge reports for the pull request, and it is resolved as `--base` resolves it. `--repositories` and `--base-dir` are mutually exclusive, `--pr` needs `--repositories`, and exactly one repository must match.
