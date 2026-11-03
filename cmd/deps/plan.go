package main

import (
	"fmt"
	"io"
	"os"

	"github.com/aymanbagabas/go-udiff"
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func newPlan(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Print the updates as a unified diff without writing files",
		Args:  noArgs,
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
	s, err := newSession(o, info, envToken(), httpx.NewClient(), stderr)
	if err != nil {
		return err
	}
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	var tidy engine.Tidier
	if write {
		tidy = goTidier{}
	}
	changes, buildErrs := engine.Build(ctx, s.root, res.Updates, tidy)
	if write {
		for _, c := range changes {
			if err := os.WriteFile(s.abs(c.Path), c.New, c.Mode); err != nil {
				buildErrs = append(buildErrs, err)
			}
		}
	} else {
		out := cmd.OutOrStdout()
		for _, c := range changes {
			io.WriteString(out, udiff.Unified("a/"+c.Path, "b/"+c.Path, string(c.Old), string(c.New)))
		}
	}
	errs := append(append([]error(nil), res.Errors...), buildErrs...)
	report(stderr, o.verbose, res, errs)
	if !write && hasGomod(res) {
		fmt.Fprintln(stderr, "note    go.sum changes are computed by apply/run")
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

// report prints update, skip (verbose) and error lines.
func report(w io.Writer, verbose bool, res *engine.Result, errs []error) {
	for _, u := range res.Updates {
		from, to := u.FromTo()
		fmt.Fprintf(w, "update  %s:%d  %s  %s -> %s  (%s)\n", u.Dep.File, u.Dep.Line, u.Dep.Name, from, to, u.Type)
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

func hasGomod(res *engine.Result) bool {
	for _, u := range res.Updates {
		if u.Dep.Manager == gomod.Name {
			return true
		}
	}
	return false
}
