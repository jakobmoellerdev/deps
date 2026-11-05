package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/diff"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
)

func newPlan(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Print the updates as a unified diff without writing files",
		Long:  helpText("plan"),
		Example: "  deps plan > deps.diff && git apply --check deps.diff\n" +
			"  deps plan -v --dir ../other-repo\n" +
			"  deps plan --config ci/deps.yaml --log-level info",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return local(cmd, o, false)
		},
	}
}

// local implements plan (write=false) and apply (write=true).
func local(cmd *cobra.Command, o *rootOpts, write bool) error {
	start := time.Now()
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(o, info, envToken(), newHTTPClient())
	if err != nil {
		return err
	}
	defer s.close()
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	post := planPostProcessors()
	if write {
		post = postProcessors(s)
	}
	// A dep with several PR buckets gets one local update; report marks the others.
	updates, separate := engine.Local(res.Updates)
	changes, buildErrs := engine.Build(ctx, s.root, s.managers, updates, post)
	if write {
		for _, c := range changes {
			var err error
			if c.New == nil {
				err = os.Remove(s.abs(c.Path))
			} else {
				err = os.WriteFile(s.abs(c.Path), c.New, c.Mode)
			}
			if err != nil {
				buildErrs = append(buildErrs, err)
			}
		}
	} else {
		out := cmd.OutOrStdout()
		for _, c := range changes {
			if _, err := io.WriteString(out, diff.Unified("a/"+c.Path, "b/"+c.Path, string(c.Old), string(c.New))); err != nil {
				return fmt.Errorf("write diff: %w", err)
			}
		}
	}
	errs := append(append([]error(nil), res.Errors...), buildErrs...)
	report(stderr, o.term, o.verbose, res, errs, separate)
	if !write && hasManager(res, gomod.Name) {
		fmt.Fprintln(stderr, "note    go.sum changes are computed by apply/run")
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
		fmt.Fprintln(stderr, "note    --hash lines of requirements files are computed by apply/run")
	}
	if !write && hasManager(res, terraform.Name) {
		fmt.Fprintln(stderr, "note    .terraform.lock.hcl changes are computed by apply/run")
	}
	cmdName := "plan"
	if write {
		cmdName = "apply"
	}
	sum := summary{Command: cmdName, Res: res, Groups: engine.GroupUpdates(res.Updates, s.cfg.Prefix()), Errors: errs, Elapsed: time.Since(start), Verbose: o.verbose, Report: o.report, Style: o.term.style}
	sum.write(stderr)
	files := map[string]bool{}
	for _, u := range res.Updates {
		files[u.Dep.File] = true
	}
	fmt.Fprintf(stderr, "%d updates in %d files, %d skipped, %d errors\n", len(res.Updates), len(files), len(res.Skipped), len(errs))
	if len(errs) > 0 {
		return errReported
	}
	return nil
}

// report prints update, skip (verbose) and error lines. separate (nil for run) marks the
// updates plan/apply leave to their own PR: their type gets a ", separate PR" hint. On a
// terminal the update lines are left out: the summary lists the same updates, grouped by PR.
func report(w io.Writer, t terminal, verbose bool, res *engine.Result, errs []error, separate []bool) {
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
	if verbose && len(res.Skipped) > 0 {
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

// reportError prints the error line of e and annotates e.
func reportError(w io.Writer, t terminal, e error) {
	fmt.Fprintf(w, "%s   %v\n", t.errorLabel(), e)
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

// hashedUpdate reports whether an update is in a requirements file with --hash options.
func hashedUpdate(root string, res *engine.Result) bool {
	for _, u := range res.Updates {
		if u.Dep.Manager != piprequirements.Name {
			continue
		}
		if _, ok := (pytool.Compiler{}).Scope(root, u.Dep.File); ok {
			return true
		}
	}
	return false
}
