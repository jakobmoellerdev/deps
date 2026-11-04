package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
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
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(o, info, envToken(), newHTTPClient(), stderr)
	if err != nil {
		return err
	}
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	var post map[string]engine.PostProcessor
	if write {
		post = postProcessors()
	}
	// A dep with several PR buckets gets one local update; report marks the others.
	updates, separate := engine.Local(res.Updates)
	changes, buildErrs := engine.Build(ctx, s.root, s.managers, updates, post)
	if write {
		for _, c := range changes {
			if err := os.WriteFile(s.abs(c.Path), c.New, c.Mode); err != nil {
				buildErrs = append(buildErrs, err)
			}
		}
	} else {
		out := cmd.OutOrStdout()
		for _, c := range changes {
			if _, err := io.WriteString(out, udiff.Unified("a/"+c.Path, "b/"+c.Path, string(c.Old), string(c.New))); err != nil {
				return fmt.Errorf("write diff: %w", err)
			}
		}
	}
	errs := append(append([]error(nil), res.Errors...), buildErrs...)
	report(stderr, o.verbose, res, errs, separate)
	if !write && hasManager(res, gomod.Name) {
		fmt.Fprintln(stderr, "note    go.sum changes are computed by apply/run")
	}
	if !write && hasManager(res, npm.Name) {
		fmt.Fprintln(stderr, "note    package-lock.json changes are computed by apply/run")
	}
	files := map[string]bool{}
	for _, u := range res.Updates {
		files[u.Dep.File] = true
	}
	fmt.Fprintf(stderr, "%d updates in %d files, %d skipped, %d errors\n", len(res.Updates), len(files), len(res.Skipped), len(errs))
	writeUpdateSummary(res)
	if len(errs) > 0 {
		return errReported
	}
	return nil
}

// report prints update, skip (verbose) and error lines. separate (nil for run) marks the
// updates plan/apply leave to their own PR: their type gets a ", separate PR" hint.
func report(w io.Writer, verbose bool, res *engine.Result, errs []error, separate []bool) {
	for i, u := range res.Updates {
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
		if i < len(separate) && separate[i] {
			hint = ", separate PR"
		}
		fmt.Fprintf(w, "update  %s:%d  %s  %s -> %s  (%s%s)%s\n", u.Dep.File, u.Dep.Line, u.Dep.Name, from, to, u.Type, hint, sec)
	}
	if verbose {
		for _, d := range res.Skipped {
			fmt.Fprintf(w, "skip    %s:%d  %s  %s\n", d.File, d.Line, d.Name, d.Skip)
		}
	}
	for _, e := range errs {
		fmt.Fprintf(w, "error   %v\n", e)
	}
}

func hasManager(res *engine.Result, name string) bool {
	for _, u := range res.Updates {
		if u.Dep.Manager == name {
			return true
		}
	}
	return false
}
