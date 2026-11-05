package main

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/depgraph"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func newSubmit(o *rootOpts) *cobra.Command {
	var dryRun, all bool
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit the dependencies to GitHub's dependency graph",
		Long:  helpText("submit"),
		Example: "  # in a GitHub Actions job with contents: write\n" +
			"  deps submit\n" +
			"  # print the snapshot instead of submitting it\n" +
			"  deps submit --dry-run > snapshot.json",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return submit(cmd, o, dryRun, all)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the snapshot JSON to stdout instead of submitting it; works outside GitHub Actions")
	cmd.Flags().BoolVar(&all, "all-managers", false, "also submit the manifests GitHub reads by itself (go.mod, package.json, Python, Cargo, pom.xml, workflows); a submitted manifest replaces GitHub's own view of that file")
	return cmd
}

func submit(cmd *cobra.Command, o *rootOpts, dryRun, all bool) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	info, ok := platform.FromEnv(os.Getenv)
	if !dryRun && (!ok || info.Kind != platform.GitHub) {
		return &usageError{errors.New("submit runs in GitHub Actions (GITHUB_ACTIONS=true on a GitHub runner); use --dry-run elsewhere")}
	}
	token := envToken()
	client := newHTTPClient()
	s, err := newSession(o, info, token, client)
	if err != nil {
		return err
	}
	defer s.close()
	deps, errs, err := engine.Extract(ctx, s.root, s.cfg, s.env, s.managers)
	if err != nil {
		return err
	}
	snap, n := depgraph.Build(deps, depgraph.Options{
		Job: depgraph.Job{
			ID:         cmp.Or(os.Getenv("GITHUB_RUN_ID"), "local"),
			Correlator: strings.Join(slices.DeleteFunc([]string{os.Getenv("GITHUB_WORKFLOW"), os.Getenv("GITHUB_JOB"), "deps"}, func(s string) bool { return s == "" }), " "),
		},
		SHA: os.Getenv("GITHUB_SHA"), Ref: os.Getenv("GITHUB_REF"),
		Detector:    depgraph.Detector{Name: "deps", Version: version, URL: "https://github.com/jakobmoellerdev/deps"},
		Scanned:     time.Now(),
		AllManagers: all,
	})
	for _, e := range errs {
		fmt.Fprintf(stderr, "error   %v\n", e)
	}
	files := snap.Files()
	for _, f := range files {
		fmt.Fprintf(stderr, "manifest  %s  %d\n", f, len(snap.Manifests[f].Resolved))
	}
	if dryRun {
		b, err := json.Marshal(snap, json.Deterministic(true))
		if err != nil {
			return err
		}
		if _, err := cmd.OutOrStdout().Write(append(b, '\n')); err != nil {
			return err
		}
	} else {
		if snap.SHA == "" || snap.Ref == "" {
			return &usageError{errors.New("GITHUB_SHA and GITHUB_REF must be set")}
		}
		result, err := platform.SubmitSnapshot(ctx, info, token, client, snap)
		if err != nil {
			return fmt.Errorf("submit snapshot: %w", err)
		}
		fmt.Fprintf(stderr, "submitted %s\n", result)
	}
	fmt.Fprintf(stderr, "%d dependencies in %d manifests, %d errors\n", n, len(files), len(errs))
	if len(errs) > 0 {
		return errReported
	}
	return nil
}
