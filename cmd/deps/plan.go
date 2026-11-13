package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/diff"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/beam"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/composer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/conda"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/copier"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/deno"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/jsonnetbundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nuget"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pub"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
)

func newPlan(o *rootOpts) *cli.Command {
	return &cli.Command{
		Name:  "plan",
		Usage: "Print the updates as a unified diff without writing files",
		Description: "On a terminal plan prints the summary only and names the changed files; --diff prints the diff there too. " +
			"Piped or redirected stdout always gets the diff.",
		Metadata: map[string]any{docgen.MetaExample: "  deps plan > deps.diff && git apply --check deps.diff\n" +
			"  deps plan --diff\n" +
			"  deps plan --show-skipped --dir ../other-repo\n" +
			"  deps plan --config ci/deps.yaml --log-level info"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return local(ctx, cmd, o, false)
		},
		Flags: append(scanFlags(o, scanResults), &cli.BoolFlag{Name: "diff", Destination: &o.showDiff,
			Usage: "print the diff when stdout is a terminal too (piped stdout always gets it)"},
			repositoriesFlag(&o.repositories, "plan")),
	}
}

// colorDiff colors a unified diff as git does: file headers bold, hunk headers cyan, added
// lines green, removed lines red. Without colors it is d unchanged.
func colorDiff(st logx.Style, d string) string {
	if !st.Color() || d == "" {
		return d
	}
	var b strings.Builder
	b.Grow(len(d) + len(d)/4)
	for line := range strings.SplitAfterSeq(d, "\n") {
		text, nl := strings.CutSuffix(line, "\n")
		sgr := ""
		switch {
		case strings.HasPrefix(text, "--- "), strings.HasPrefix(text, "+++ "):
			sgr = logx.Bold
		case strings.HasPrefix(text, "@@"):
			sgr = logx.Cyan
		case strings.HasPrefix(text, "+"):
			sgr = logx.Green
		case strings.HasPrefix(text, "-"):
			sgr = logx.Red
		}
		b.WriteString(st.Paint(sgr, text))
		if nl {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// local implements plan (write=false) and apply (write=true): on the checkout of --dir, or,
// for plan with --repositories, on each remote repository in turn (planRepositories).
func local(ctx context.Context, cmd *cli.Command, o *rootOpts, write bool) error {
	start := time.Now()
	stderr := cmd.Root().ErrWriter
	if !write {
		if entries := repositoryEntries(cmd, o.repositories); len(entries) > 0 {
			return planRepositories(ctx, cmd, o, entries)
		}
	}
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(ctx, o, info, envToken(), newHTTPClient(), stderr)
	if err != nil {
		return err
	}
	defer s.close()
	return planSession(ctx, cmd, o, s, write, nil, "", start)
}

// planRepositories plans each repository of --repositories in turn, sharing the datasources
// between them, and writes one report over all of them.
func planRepositories(ctx context.Context, cmd *cli.Command, o *rootOpts, entries []string) error {
	return repositoriesCommand(ctx, cmd, o, entries, false, "")
}

// applyRepositories applies to a kept clone of each repository of --repositories under into
// (<into>/<owner>/<name>), so the changed files, their lockfiles and the output of the
// post-upgrade tasks stay for inspection.
func applyRepositories(ctx context.Context, cmd *cli.Command, o *rootOpts, entries []string, into string) error {
	abs, err := filepath.Abs(into)
	if err != nil {
		return err
	}
	return repositoriesCommand(ctx, cmd, o, entries, true, abs)
}

// repositoriesCommand plans (write=false) or applies (write=true) every repository of
// entries in turn, sharing the datasources between them, and writes one report over all of
// them. A failing repository does not stop the others.
func repositoriesCommand(ctx context.Context, cmd *cli.Command, o *rootOpts, entries []string, write bool, into string) error {
	start := time.Now()
	stderr := cmd.Root().ErrWriter
	client := newHTTPClient()
	src, err := newRemoteSource(ctx, o, entries, envToken(), client)
	if err != nil {
		return err
	}
	src.into = into
	command := "plan"
	if write {
		command = "apply"
	}
	multi := newMultiRun(command)
	var sessions repoSessions
	defer sessions.close()
	total, failed, err := src.eachRepository(ctx, o, stderr, multi, func(dir, name string, info platform.Info) error {
		s, err := sessions.open(ctx, o, dir, info, src.tokenFor(info), client, stderr)
		if err != nil {
			return err
		}
		return planSession(ctx, cmd, o, s, write, multi, name, time.Now())
	})
	if err != nil {
		return err
	}
	reportErr := multi.write(stderr, o.report, time.Since(start))
	fmt.Fprintf(stderr, "%d repositories, %d failed\n", total, failed)
	if failed > 0 {
		return errReported
	}
	return reportErr
}

// planSession plans (write=false) or applies (write=true) the updates of session s. In a
// multi-repository plan (multi non-nil) repo names the repository: its summary carries the
// name, its result is added to multi instead of written, and the diff paths are prefixed
// with it, so one stream tells the repositories apart.
func planSession(ctx context.Context, cmd *cli.Command, o *rootOpts, s *session, write bool, multi *multiRun, repo string, start time.Time) error {
	stderr := cmd.Root().ErrWriter
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	post := planPostProcessors(s)
	if write {
		post = postProcessors(s)
	}
	// A dep with several PR buckets gets one local update; report marks the others.
	updates, separate := engine.Local(res.Updates)
	changes, inapplicable, buildErrs := engine.BuildApplicable(ctx, s.root, s.managers, updates, post)
	separate = skipInapplicable(res, separate, inapplicable)
	for _, sk := range inapplicable {
		from, to := sk.Update.FromTo()
		fmt.Fprintf(stderr, "skip    %s:%d  %s  %s -> %s  (cannot be applied: %s)\n", sk.Update.Dep.File, sk.Update.Dep.Line, sk.Update.Dep.Name, from, to, sk.Reason)
	}
	cmds := engine.TaskRuns(updates)
	var denied error
	if write {
		if denied = taskAllowlist(o).Check(cmds); denied != nil {
			buildErrs = append(buildErrs, denied) // no update is written without its tasks
		}
	}
	diffHidden := !write && o.term.outTTY && !o.showDiff && len(changes) > 0
	switch {
	case write && denied == nil:
		for _, c := range changes {
			var err error
			switch {
			case c.Gitlink:
				err = gitx.Repo{Dir: s.root}.StageGitlink(ctx, c.Path, string(c.New))
			case c.New == nil:
				err = os.Remove(s.abs(c.Path))
			default:
				err = os.WriteFile(s.abs(c.Path), c.New, c.Mode)
			}
			if err != nil {
				buildErrs = append(buildErrs, err)
			}
		}
		if err := engine.ApplyTasks(ctx, s.root, updates); err != nil {
			buildErrs = append(buildErrs, err)
		}
	case !write && !diffHidden:
		out := cmd.Root().Writer
		prefix := ""
		if repo != "" {
			prefix = repo + "/"
		}
		for _, c := range changes {
			from, to := string(c.Old), string(c.New)
			if c.Gitlink {
				from, to = "Subproject commit "+from+"\n", "Subproject commit "+to+"\n"
			}
			if _, err := io.WriteString(out, colorDiff(o.term.diff, diff.Unified("a/"+prefix+c.Path, "b/"+prefix+c.Path, from, to))); err != nil {
				return fmt.Errorf("write diff: %w", err)
			}
		}
	}
	errs := append(append([]error(nil), res.Errors...), buildErrs...)
	report(stderr, o.term, o.showSkipped, res, errs, separate)
	if diffHidden {
		fmt.Fprintf(stderr, "note    diff of %s left out on a terminal (--diff prints it)\n", plural(len(changes), "changed file", "changed files"))
	}
	if !write && hasManager(res, gomod.Name) {
		fmt.Fprintln(stderr, "note    go.sum changes are computed by apply/run")
	}
	if !write && cmds != nil {
		fmt.Fprintln(stderr, "note    postUpgradeTasks run in apply/run")
	}
	if !write && hasManager(res, npm.Name) {
		fmt.Fprintln(stderr, "note    package-lock.json changes are computed by apply/run")
	}
	if !write && hasManager(res, cargo.Name) {
		fmt.Fprintln(stderr, "note    Cargo.lock changes are computed by apply/run")
	}
	if !write && (hasManager(res, pep621.Name) || hasManager(res, poetry.Name) || hasManager(res, pipenv.Name)) {
		fmt.Fprintln(stderr, "note    uv.lock, poetry.lock, pdm.lock and Pipfile.lock changes are computed by apply/run")
	}
	if !write && hashedUpdate(s.root, res) {
		fmt.Fprintln(stderr, "note    pip-compile output and --hash lines of requirements files are computed by apply/run")
	}
	if !write && hasManager(res, terraform.Name) {
		fmt.Fprintln(stderr, "note    .terraform.lock.hcl changes are computed by apply/run")
	}
	if !write && hasManager(res, bundler.Name) {
		fmt.Fprintln(stderr, "note    Gemfile.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, composer.Name) {
		fmt.Fprintln(stderr, "note    composer.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, julia.Name) {
		fmt.Fprintln(stderr, "note    Manifest.toml changes are computed by apply/run")
	}
	if !write && hasManager(res, pub.Name) {
		fmt.Fprintln(stderr, "note    pubspec.lock changes are computed by apply/run")
	}
	if !write && (hasManager(res, bazel.Name) || hasManager(res, bazel.BazeliskName)) {
		fmt.Fprintln(stderr, "note    MODULE.bazel.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, beam.NameMix) {
		fmt.Fprintln(stderr, "note    mix.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, beam.NameGleam) {
		fmt.Fprintln(stderr, "note    manifest.toml changes are computed by apply/run")
	}
	if !write && slices.ContainsFunc(res.Updates, func(u dep.Update) bool { return u.Dep.Manager == nix.Name && u.Dep.DepType == nix.DepTypeInput }) {
		fmt.Fprintln(stderr, "note    flake.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, nuget.Name) && len(nuget.Locker{}.ScopeSiblings(s.root)) > 0 {
		fmt.Fprintln(stderr, "note    packages.lock.json changes are computed by apply/run")
	}
	if !write && slices.ContainsFunc(res.Updates, func(u dep.Update) bool {
		_, ok := conda.Locker{}.Scope(ctx, s.root, u.Dep.File)
		return u.Dep.Manager == conda.Name && ok
	}) {
		fmt.Fprintln(stderr, "note    conda-lock.yml changes are computed by apply/run")
	}
	if !write && hasManager(res, bundler.NameCocoaPods) {
		fmt.Fprintln(stderr, "note    Podfile.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, deno.Name) {
		fmt.Fprintln(stderr, "note    deno.lock changes are computed by apply/run")
	}
	if !write && hasManager(res, jsonnetbundler.Name) {
		fmt.Fprintln(stderr, "note    jsonnetfile.lock.json changes are computed by apply/run")
	}
	if !write && hasManager(res, copier.Name) {
		fmt.Fprintln(stderr, "note    copier template changes are computed by apply/run")
	}
	if write && slices.ContainsFunc(changes, func(c engine.FileChange) bool { return c.Gitlink }) {
		fmt.Fprintln(stderr, "note    submodule commits are staged in the git index; run git submodule update to check them out")
	}
	cmdName := "plan"
	if write {
		cmdName = "apply"
	}
	errs, limited := splitRateLimited(errs)
	sum := summary{Command: cmdName, Res: res, Groups: engine.GroupUpdates(res.Updates, s.cfg.Prefix()), Errors: errs, RateLimited: limited, Elapsed: time.Since(start), ShowSkipped: o.showSkipped,
		Unused: s.cfg.UnusedRules(res.Deps), Config: s.cfg, Report: o.report, Style: o.term.style}
	var reportErr error
	if multi != nil {
		sum.Repository = repo
		multi.add(&sum)
	}
	sum.write(stderr)
	if multi == nil {
		reportErr = sum.writeMachine(stderr, o.report)
	}
	// The counts line is for scripts and logs; on a terminal the summary headline says the same.
	if !o.term.tty {
		files := map[string]bool{}
		for _, u := range res.Updates {
			files[u.Dep.File] = true
		}
		fmt.Fprintf(stderr, "%d updates in %d files, %d skipped, %s%d errors\n", len(res.Updates), len(files), len(res.Skipped), rateLimitedCount(limited), len(errs))
	}
	if len(errs) > 0 {
		return errReported
	}
	return reportErr
}

// splitRateLimited splits errs into the errors and the lookups that failed on an exhausted
// rate limit (engine.RateLimited): those are warnings, retried by the next run, and do not
// fail the command.
func splitRateLimited(errs []error) (failed, limited []error) {
	for _, e := range errs {
		if engine.RateLimited(e) {
			limited = append(limited, e)
		} else {
			failed = append(failed, e)
		}
	}
	return failed, limited
}

// rateLimitedCount is "<n> rate limited, " for the counts lines, "" without rate-limited
// lookups.
func rateLimitedCount(limited []error) string {
	if len(limited) == 0 {
		return ""
	}
	return strconv.Itoa(len(limited)) + " rate limited, "
}

// report prints update, skip (--show-skipped) and error lines. separate (nil for run) marks the
// updates plan/apply leave to their own PR: their type gets a ", separate PR" hint. On a
// terminal the update lines are left out: the summary lists the same updates, grouped by PR.
func report(w io.Writer, t terminal, showSkipped bool, res *engine.Result, errs []error, separate []bool) {
	for _, u := range res.Updates {
		actionsLog.security(u)
	}
	updates := res.Updates
	if t.tty {
		updates = nil
	}
	for i, u := range updates {
		from, to := u.FromTo()
		sec := ""
		if len(u.Advisories) > 0 {
			ids := make([]string, len(u.Advisories))
			for i, a := range u.Advisories {
				ids[i] = a.ID
			}
			sec = "  security: " + strings.Join(ids, ",")
			if risk := dep.Worst(u.Advisories); risk != "" {
				sec += " (" + risk + ")"
			}
		}
		hint := ""
		if u.Lockfile {
			hint = ", lockfile"
		}
		if i < len(separate) && separate[i] {
			hint += ", separate PR"
		}
		if u.Maintenance() {
			fmt.Fprintf(w, "update  %s  lock file maintenance  (%s%s)\n", u.Dep.Name, u.Type, hint)
			continue
		}
		fmt.Fprintf(w, "update  %s:%d  %s  %s -> %s  (%s%s)%s\n", u.Dep.File, u.Dep.Line, u.Dep.Name, from, to, u.Type, hint, sec)
	}
	if showSkipped && len(res.Skipped) > 0 {
		actionsLog.group(plural(len(res.Skipped), "skipped dependency", "skipped dependencies"))
		for _, d := range res.Skipped {
			fmt.Fprintf(w, "skip    %s:%d  %s  %s\n", d.File, d.Line, d.Name, d.Skip)
		}
		actionsLog.endGroup()
	}
	for _, e := range errs {
		reportError(w, t, e)
	}
}

// reportError prints the error line of e and annotates e; a rate-limited lookup
// (engine.RateLimited) gets a warning line and annotation instead.
func reportError(w io.Writer, t terminal, e error) {
	if engine.RateLimited(e) {
		fmt.Fprintf(w, "%s    %v\n", t.warnLabel(), e)
	} else {
		fmt.Fprintf(w, "%s   %v\n", t.errorLabel(), e)
	}
	actionsLog.error(e)
}

func hasManager(res *engine.Result, name string) bool {
	for _, u := range res.Updates {
		if u.Dep.Manager == name {
			return true
		}
	}
	return false
}

// hashedUpdate reports whether an update is in a requirements file with --hash options or
// compiled by pip-compile, or in an input of one.
func hashedUpdate(root string, res *engine.Result) bool {
	for _, u := range res.Updates {
		if u.Dep.Manager != piprequirements.Name {
			continue
		}
		if _, ok := (pytool.Compiler{}).Scope(context.Background(), root, u.Dep.File); ok {
			return true
		}
	}
	return false
}

// skipInapplicable records the updates Build could not apply as `run` records their groups:
// they leave res.Updates (with their separate marks), and their deps are skipped with the
// reason in res.Skipped and res.Deps. It returns the separate marks of the remaining updates.
func skipInapplicable(res *engine.Result, separate []bool, skipped []engine.SkippedUpdate) []bool {
	if len(skipped) == 0 {
		return separate
	}
	type key struct {
		file, name, version string
		line                int
		kind                dep.UpdateType
	}
	keyOf := func(u dep.Update) key { return key{u.Dep.File, u.Dep.Name, u.NewVersion, u.Dep.Line, u.Type} }
	reasons := map[key]string{}
	for _, s := range skipped {
		reasons[keyOf(s.Update)] = "cannot be applied: " + s.Reason
	}
	var ups []dep.Update
	var sep []bool
	for i, u := range res.Updates {
		reason, ok := reasons[keyOf(u)]
		if !ok {
			ups = append(ups, u)
			sep = append(sep, i < len(separate) && separate[i])
			continue
		}
		res.SkipDep(u.Dep, reason)
	}
	res.Updates = ups
	return sep
}
