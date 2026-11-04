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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/osv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

type rootOpts struct {
	dir         string
	config      string
	verbose     bool
	log         logOpts
	prof        profiler
	concurrency int
}

func newRoot() *cobra.Command {
	o := &rootOpts{}
	root := &cobra.Command{
		Use:           "deps",
		Short:         "Update GitHub/Gitea/Forgejo Actions, Go module, container image and npm dependencies",
		Long:          helpText("root"),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Environment defaults apply here, not as flag defaults, so help and the generated
			// reference show the same defaults everywhere.
			envDefault(cmd, "log-level", "DEPS_LOG_LEVEL", &o.log.level)
			envDefault(cmd, "log-format", "DEPS_LOG_FORMAT", &o.log.format)
			if err := setupLogger(o.log, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if o.concurrency < 1 {
				return &usageError{fmt.Errorf("--concurrency: must be at least 1, got %d", o.concurrency)}
			}
			activeProfiler = &o.prof
			return o.prof.start()
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	pf := root.PersistentFlags()
	pf.StringVar(&o.dir, "dir", ".", "repository directory to scan")
	pf.StringVar(&o.config, "config", "", "config file, which must exist (default: <dir>/.deps.yaml, then .deps.yml, else built-in defaults)")
	pf.BoolVarP(&o.verbose, "verbose", "v", false, "also print skipped dependencies (skip lines)")
	pf.StringVar(&o.log.level, "log-level", "warn", "log level: debug, info, warn, error; $DEPS_LOG_LEVEL replaces the default")
	pf.StringVar(&o.log.format, "log-format", "text", "log format: text, json; $DEPS_LOG_FORMAT replaces the default")
	pf.IntVar(&o.concurrency, "concurrency", 32, "maximum concurrent network requests and lookups; at least 1")
	pf.StringVar(&o.prof.cpu, "cpuprofile", "", "write a CPU profile to this file")
	pf.StringVar(&o.prof.mem, "memprofile", "", "write an allocation profile to this file on exit")
	_ = pf.MarkHidden("cpuprofile")
	_ = pf.MarkHidden("memprofile")
	root.AddCommand(newPlan(o), newApply(o), newRun(o), newVersion(), newExplain(o), newSchema(), newDocs())
	root.AddCommand(newHelpTopics()...)
	return root
}

// envDefault sets *dst from the environment variable env unless the flag was given.
func envDefault(cmd *cobra.Command, flag, env string, dst *string) {
	if v := os.Getenv(env); v != "" && !cmd.Flags().Changed(flag) {
		*dst = v
	}
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
	gh        *githubapi.Client // shared GitHub release lists (datasources and release notes)
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
	sem := make(chan struct{}, o.concurrency)
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
	gh := githubapi.New(client, auth, sem)
	releases := githubreleases.New(gh)
	sources := map[string]engine.Datasource{
		"git-tags":          gittags.New(client, auth, sem, gh),
		"go":                goproxy.New(client, sem),
		gotoolchain.Name:    gotoolchain.New(client, sem),
		"docker":            dockerds.New(client, sem),
		githubreleases.Name: releases,
		npmds.Name:          npmds.New(client, sem, os.Getenv),
		nodeversion.Name:    nodeversion.New(client, sem, os.Getenv),
	}
	for name, cd := range cfg.CustomDatasources {
		ds, err := customds.New(client, sem, name, cd.Spec())
		if err != nil {
			return nil, &config.Error{Err: err}
		}
		sources[customds.Prefix+name] = ds
	}
	return &session{
		root:      root,
		cfg:       cfg,
		client:    client,
		managers:  managers,
		sources:   sources,
		security:  security,
		checksums: checksum.New(client, sem, releases),
		env:       manager.Env{DefaultHost: host},
		stderr:    stderr,
		auth:      auth,
		gh:        gh,
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
