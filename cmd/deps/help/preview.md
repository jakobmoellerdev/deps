Resolves two checkouts of the repository, `--dir` (the pull request) and `--base-dir` (its base branch), each with its own `.deps.yaml`, and reports how the update PRs `deps run` would open differ: new PRs, changed PRs (other updates or another title) and PRs no longer opened (open ones would be closed as obsolete). A PR without a base counterpart whose dependency file the base checkout failed to extract, or whose dependency it failed to look up, is reported as not compared instead of new. Lookups are shared between the two checkouts, so each release list is fetched once. Nothing is written to the repository or the forge, except the check run below.

Output lines (stderr):

```
new     <branch>  <title>
changed <branch>  <title>
unknown <branch>  <title>             (not compared: the base checkout failed on its dependencies)
removed <branch>  <title>
error   <message>                    (lookup errors of --dir; those of --base-dir prefixed "base checkout:")
check   <url>                        (the check run, GitHub)
<n> new, <c> changed, <r> no longer opened, <s> the same[, <u> not compared] (<duration>)
```

**Step summary.** The comparison is appended to `GITHUB_STEP_SUMMARY` (or `GITEA_STEP_SUMMARY`) as markdown, with an update table per new or changed PR.

**Check run (GitHub).** In a `pull_request` or `pull_request_target` job on GitHub, deps also creates the completed check run `deps preview` on the pull request's head commit (from the event payload), with the same markdown as its output, so it shows on the PR's *Checks* tab. The conclusion is `success`, or `neutral` when lookups failed in either checkout: the preview informs and never blocks. Only App installation tokens (`GITHUB_TOKEN` included) can create check runs, with `checks: write`; for a token that can't (a PAT, a fork PR's read-only token), deps prints a `note` and keeps the step summary.

Exit codes: 0 with no lookup errors in `--dir` (errors of `--base-dir` are reported but do not fail), 1 with lookup errors, 2 for an invalid config in either checkout.
