package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

type rootOpts struct {
	dir     string
	config  string
	verbose bool
}

func newRoot() *cobra.Command {
	o := &rootOpts{}
	root := &cobra.Command{
		Use:           "deps",
		Short:         "Update GitHub/Gitea/Forgejo Actions and Go module dependencies",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	pf := root.PersistentFlags()
	pf.StringVar(&o.dir, "dir", ".", "repository directory to scan")
	pf.StringVar(&o.config, "config", "", "config file (default <dir>/.deps.yaml or .deps.yml)")
	pf.BoolVarP(&o.verbose, "verbose", "v", false, "print skipped dependencies and diagnostics")
	root.AddCommand(newPlan(o), newApply(o), newRun(o), newVersion())
	return root
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{fmt.Errorf("unexpected arguments: %v", args)}
	}
	return nil
}

// envToken returns the first token set in the environment.
func envToken() string {
	for _, k := range []string{"DEPS_TOKEN", "GITHUB_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// session is the state shared by plan, apply and run.
type session struct {
	root   string
	cfg    *config.Config
	client *http.Client
	src    engine.Sources
	host   string
	stderr io.Writer
}

// newSession loads the config and wires the datasources onto one HTTP client and one
// request semaphore.
func newSession(o *rootOpts, info platform.Info, token string, client *http.Client, stderr io.Writer) (*session, error) {
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(root, o.config)
	if err != nil {
		return nil, err
	}
	sem := make(chan struct{}, 16)
	auth := httpx.NewAuth(info, token, os.Getenv)
	host, source := cfg.Actions.DefaultHost, "config"
	if host == "" {
		host, source = platform.DefaultActionsHost(info, os.Getenv)
	}
	if o.verbose {
		fmt.Fprintf(stderr, "actions default host %s (%s)\n", host, source)
	}
	return &session{
		root:   root,
		cfg:    cfg,
		client: client,
		src:    engine.Sources{Tags: gittags.New(client, auth, sem), Go: goproxy.New(client, sem)},
		host:   host,
		stderr: stderr,
	}, nil
}

func (s *session) resolve(ctx context.Context) (*engine.Result, error) {
	return engine.Resolve(ctx, s.root, s.cfg, s.host, s.src)
}

func (s *session) abs(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }
