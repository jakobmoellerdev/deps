package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/checksum"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/osv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

type rootOpts struct {
	dir     string
	config  string
	verbose bool
	log     logOpts
}

func newRoot() *cobra.Command {
	o := &rootOpts{}
	root := &cobra.Command{
		Use:           "deps",
		Short:         "Update GitHub/Gitea/Forgejo Actions, Go module, container image and npm dependencies",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return setupLogger(o.log, cmd.ErrOrStderr())
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	pf := root.PersistentFlags()
	pf.StringVar(&o.dir, "dir", ".", "repository directory to scan")
	pf.StringVar(&o.config, "config", "", "config file (default <dir>/.deps.yaml or .deps.yml)")
	pf.BoolVarP(&o.verbose, "verbose", "v", false, "also print skipped dependencies")
	pf.StringVar(&o.log.level, "log-level", envOr("DEPS_LOG_LEVEL", "warn"), "log level: debug, info, warn, error (env DEPS_LOG_LEVEL)")
	pf.StringVar(&o.log.format, "log-format", envOr("DEPS_LOG_FORMAT", "text"), "log format: text, json (env DEPS_LOG_FORMAT)")
	root.AddCommand(newPlan(o), newApply(o), newRun(o), newVersion(), newExplain(o), newSchema())
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
	root      string
	cfg       *config.Config
	client    *http.Client
	managers  []manager.Manager
	sources   map[string]engine.Datasource
	security  engine.SecuritySource // nil when security: false
	checksums engine.ChecksumSource
	env       manager.Env
	stderr    io.Writer
	auth      *httpx.Auth
	sem       chan struct{}
}

// newSession loads the config and wires the managers and datasources onto one HTTP client
// and one request semaphore.
func newSession(o *rootOpts, info platform.Info, token string, client *http.Client, stderr io.Writer) (*session, error) {
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(root, o.config)
	if err != nil {
		return nil, err
	}
	managers, err := engine.Managers(cfg)
	if err != nil {
		return nil, err
	}
	sem := make(chan struct{}, 16)
	auth := httpx.NewAuth(info, token, os.Getenv)
	host, source := cfg.Actions.DefaultHost, "config"
	if host == "" {
		host, source = platform.DefaultActionsHost(info, os.Getenv)
	}
	slog.Info("actions default host", "host", host, "source", source)
	slog.Debug("config loaded", "root", root, "rules", len(cfg.Rules), "ignorePaths", len(cfg.IgnorePaths), "pin", cfg.Pin)
	var security engine.SecuritySource
	if cfg.SecurityEnabled() {
		security = osv.New(client, sem)
	}
	releases := githubreleases.New(client, auth, sem)
	return &session{
		root:     root,
		cfg:      cfg,
		client:   client,
		managers: managers,
		sources: map[string]engine.Datasource{
			"git-tags":          gittags.New(client, auth, sem),
			"go":                goproxy.New(client, sem),
			gotoolchain.Name:    gotoolchain.New(client, sem),
			"docker":            dockerds.New(client, sem),
			githubreleases.Name: releases,
			npmds.Name:          npmds.New(client, sem, os.Getenv),
		},
		security:  security,
		checksums: checksum.New(client, sem, releases),
		env:       manager.Env{DefaultHost: host},
		stderr:    stderr,
		auth:      auth,
		sem:       sem,
	}, nil
}

// options returns the engine options of this session; callers add Filter/Trace.
func (s *session) options() engine.Options {
	return engine.Options{Managers: s.managers, Datasources: s.sources, Security: s.security, Checksums: s.checksums}
}

func (s *session) resolve(ctx context.Context) (*engine.Result, error) {
	return engine.Resolve(ctx, s.root, s.cfg, s.env, s.options())
}

func (s *session) abs(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }
