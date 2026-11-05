package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/azuretasks"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/checksum"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/crate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/elmpackage"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gradleversion"
	helmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/helm"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/orb"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/osv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubygems"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubyversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/tfregistry"
	vcpkgds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

type rootOpts struct {
	dir         string
	config      string
	verbose     bool
	log         logOpts
	term        terminal     // set up with the logger
	stderr      *logx.Writer // stderr of the command, set up with the logger
	rawStderr   io.Writer    // the writer behind stderr (the terminal's file)
	prof        profiler
	concurrency int
	report      string   // --report: JSON result file of plan, apply and run
	allowTasks  []string // --allow-tasks of apply and run
}

// allowTasksFlag is --allow-tasks of apply and run.
func allowTasksFlag(o *rootOpts) cli.Flag {
	return &cli.StringSliceFlag{Name: "allow-tasks", Destination: &o.allowTasks, Usage: "postUpgradeTasks command allowed to run, '*' matching any text but ; & | ` $( and line breaks, e.g. 'go generate ./...', 'make *' or '*' (repeatable); they run repository code on this machine (default: $DEPS_ALLOW_TASKS, one per line)"}
}

// taskAllowlist returns the --allow-tasks patterns, else those of DEPS_ALLOW_TASKS (one per line).
func taskAllowlist(o *rootOpts) engine.TaskAllowlist {
	if len(o.allowTasks) > 0 {
		return o.allowTasks
	}
	var a engine.TaskAllowlist
	for l := range strings.Lines(os.Getenv("DEPS_ALLOW_TASKS")) {
		if l = strings.TrimSpace(l); l != "" {
			a = append(a, l)
		}
	}
	return a
}

func newRoot() *cli.Command {
	o := &rootOpts{}
	root := &cli.Command{
		Name:                            "deps",
		Usage:                           "Update GitHub/Gitea/Forgejo Actions, Go module, container image, npm and Cargo dependencies",
		Description:                     helpText("root"),
		HideVersion:                     true,
		EnableShellCompletion:           true,
		ConfigureShellCompletionCommand: configureCompletion,
		// Errors are printed and mapped to exit codes by main (exitCode), never by urfave/cli.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			// Environment defaults apply here, not as flag defaults, so help and the generated
			// reference show the same defaults everywhere.
			envDefault(cmd, "log-level", "DEPS_LOG_LEVEL", &o.log.level)
			// A re-run with debug logging (RUNNER_DEBUG=1) gets deps' debug records too.
			if os.Getenv("RUNNER_DEBUG") == "1" && os.Getenv("DEPS_LOG_LEVEL") == "" && !cmd.IsSet("log-level") {
				o.log.level = "debug"
			}
			envDefault(cmd, "log-format", "DEPS_LOG_FORMAT", &o.log.format)
			envDefault(cmd, "report", "DEPS_REPORT", &o.report)
			// All of stderr goes through one Writer, so records and result lines never
			// interleave and a status line can redraw around them. Commands write to the
			// root's ErrWriter.
			stderr := cmd.ErrWriter
			tty := logx.IsTerminal(stderr)
			w := logx.NewWriter(stderr)
			cmd.ErrWriter = w
			o.stderr, o.rawStderr = w, stderr
			actionsLog = newAnnotator(os.Getenv, w, o.dir)
			t, err := setupLogger(o.log, w, tty, os.Getenv)
			if err != nil {
				return ctx, err
			}
			o.term = t
			if o.concurrency < 1 {
				return ctx, &usageError{fmt.Errorf("--concurrency: must be at least 1, got %d", o.concurrency)}
			}
			activeProfiler = &o.prof
			return ctx, o.prof.start()
		},
		// Without a command: help, as `deps help`; an unknown command is an error.
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown command %q for %q", cmd.Args().First(), cmd.Name)
			}
			return cli.ShowRootCommandHelp(cmd)
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: ".", Destination: &o.dir, Usage: "repository directory to scan"},
			&cli.StringFlag{Name: "config", Destination: &o.config, Usage: "config file, which must exist (default: <dir>/.deps.yaml, then .deps.yml, else built-in defaults)"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Destination: &o.verbose, Usage: "also print skipped dependencies (skip lines)"},
			&cli.StringFlag{Name: "log-level", Value: "warn", Destination: &o.log.level, Usage: "log level: debug, info, warn, error; $DEPS_LOG_LEVEL, then $RUNNER_DEBUG=1 (debug) replace the default"},
			&cli.StringFlag{Name: "log-format", Value: "auto", Destination: &o.log.format, Usage: "log format: auto (actions on a GitHub/Gitea Actions runner, pretty on a terminal, else text), pretty, text, json, actions (pretty records without elapsed time, colored, plus workflow commands: annotations, log groups); $DEPS_LOG_FORMAT replaces the default"},
			&cli.StringFlag{Name: "color", Value: "auto", Destination: &o.log.color, Usage: "color stderr: auto (on a terminal, unless $NO_COLOR is set or $TERM is dumb; $CLICOLOR_FORCE or $FORCE_COLOR force it), always, never"},
			&cli.IntFlag{Name: "concurrency", Value: 32, Destination: &o.concurrency, Usage: "maximum concurrent network requests and lookups per upstream; npm and OSV get twice as many, container registries half; at least 1"},
			&cli.StringFlag{Name: "report", Destination: &o.report, Usage: "plan, apply, run: also write the result as JSON to this file (see deps help output; default: $DEPS_REPORT)"},
			&cli.StringFlag{Name: "cpuprofile", Hidden: true, Destination: &o.prof.cpu, Usage: "write a CPU profile to this file"},
			&cli.StringFlag{Name: "memprofile", Hidden: true, Destination: &o.prof.mem, Usage: "write an allocation profile to this file on exit"},
		},
		Commands: append([]*cli.Command{newPlan(o), newApply(o), newRun(o), newVersion(), newExplain(o), newPreview(o), newSubmit(o), newSchema(), newDocs(), newHelp()},
			newHelpTopics()...),
	}
	_ = root.Walk(func(c *cli.Command) error {
		c.OnUsageError = flagError
		if len(c.Commands) == 0 {
			// `deps plan help` is not help for plan; leaf commands take no subcommands.
			c.HideHelpCommand = true
		}
		return nil
	})
	return root
}

// flagError makes flag parsing errors usage errors (exit code 2), naming unknown flags as
// they were given.
func flagError(_ context.Context, _ *cli.Command, err error, _ bool) error {
	if name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: -"); ok {
		dashes := "--"
		if len(name) == 1 {
			dashes = "-"
		}
		err = fmt.Errorf("unknown flag: %s%s", dashes, name)
	}
	return &usageError{err}
}

// envDefault sets *dst from the environment variable env unless the flag was given.
func envDefault(cmd *cli.Command, flag, env string, dst *string) {
	if v := os.Getenv(env); v != "" && !cmd.IsSet(flag) {
		*dst = v
	}
}

// noArgs is the cli.ArgValidatorFunc of commands without positional arguments.
func noArgs(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Present() {
		return &usageError{fmt.Errorf("unexpected arguments: %v", cmd.Args().Slice())}
	}
	return nil
}

// envToken returns the first token set in the environment.
func envToken() string {
	for _, k := range []string{"DEPS_TOKEN", "GITHUB_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN", "GITLAB_TOKEN"} {
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
	managers  []manager.Manager
	sources   map[string]engine.Datasource
	security  engine.SecuritySource // nil when security: false
	checksums engine.ChecksumSource
	env       manager.Env
	auth      *httpx.Auth
	gh        *githubapi.Client // shared GitHub release lists (datasources and release notes)
	sem       chan struct{}     // the default request pool (httpx.Limits.Default)
	progress  engine.Progress   // the live status line on a terminal; nil = none
	info      platform.Info
	client    *http.Client          // sibling sessions build their custom datasources on it
	fetcher   *config.GitFetcher    // reads extended configs; shared by sibling sessions
	osv       engine.SecuritySource // the OSV source security uses when the config enables it
}

// extendsFetcher reads the configs of extends with git, sending the platform token (and
// DEPS_GITHUB_COM_TOKEN) to their hosts. owner/name entries are on the forge of info, else
// on the one of dir's origin remote.
func extendsFetcher(info platform.Info, token, dir string) *config.GitFetcher {
	return &config.GitFetcher{
		Auth: httpx.NewAuth(info, token, os.Getenv),
		Server: func(ctx context.Context) (string, error) {
			if info.ServerURL != "" {
				return info.ServerURL, nil
			}
			remote, err := gitx.Repo{Dir: dir}.RemoteURL(ctx)
			if err != nil {
				return "", err
			}
			return platform.RemoteServerURL(remote)
		},
	}
}

// newSession loads the config and wires the managers and datasources onto one HTTP client
// and the request pools of --concurrency (httpx.NewLimits).
func newSession(ctx context.Context, o *rootOpts, info platform.Info, token string, client *http.Client) (*session, error) {
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return nil, err
	}
	fetcher := extendsFetcher(info, token, root)
	cfg, err := config.Load(ctx, root, o.config, fetcher)
	if err != nil {
		return nil, err
	}
	logExtends(ctx, cfg)
	managers, err := engine.Managers(cfg)
	if err != nil {
		return nil, err
	}
	limits := httpx.NewLimits(o.concurrency)
	sem := limits.Default
	gitlabURL, gitlabSource := platform.GitLabURL(cfg.GitLabCI.URL, info, os.Getenv)
	slog.DebugContext(ctx, "gitlab url", "url", gitlabURL, "source", gitlabSource)
	auth := httpx.NewAuth(info, token, os.Getenv, append(gitLabAuth(gitlabURL, os.Getenv), httpx.WithSecretHook(actionsLog.mask))...)
	env := configEnv(cfg, info, gitlabURL)
	slog.DebugContext(ctx, "config loaded", "root", root, "rules", len(cfg.Rules), "ignorePaths", len(cfg.IgnorePaths), "pin", cfg.Pin)
	osvSource := osv.New(client, limits.OSV)
	var security engine.SecuritySource
	if cfg.SecurityEnabled() {
		security = osvSource
	}
	gh := githubapi.New(client, auth, sem)
	releases := githubreleases.New(gh)
	docker := dockerds.New(client, limits.Docker)
	registry := tfregistry.New(client, sem, os.Getenv)
	maven := mavends.New(client, limits.Maven, os.Getenv)
	tags := gittags.New(client, auth, sem, gh)
	sources := map[string]engine.Datasource{
		"git-tags":              tags,
		gittags.RefsName:        tags.Refs(),
		"go":                    goproxy.New(client, sem),
		gotoolchain.Name:        gotoolchain.New(client, sem),
		"docker":                docker,
		githubreleases.Name:     releases,
		npmds.Name:              npmds.New(client, limits.NPM, os.Getenv),
		nodeversion.Name:        nodeversion.New(client, sem, os.Getenv),
		helmds.Name:             helmds.New(client, sem, auth, docker),
		crate.Name:              crate.New(client, limits.Crates, auth, os.Getenv),
		pypi.Name:               pypi.New(client, sem, os.Getenv),
		tfregistry.ProviderName: registry.Providers(),
		tfregistry.ModuleName:   registry.Modules(),
		mavends.Name:            maven,
		gradleversion.Name:      gradleversion.New(client, sem, os.Getenv),
		rubygems.Name:           rubygems.New(client, sem, os.Getenv),
		rubyversion.Name:        rubyversion.New(client, sem, os.Getenv),
		rustversion.Name:        rustversion.New(client, sem, os.Getenv),
		orb.Name:                orb.New(client, sem, os.Getenv),
		azuretasks.Name:         azuretasks.New(client, sem, os.Getenv),
		vcpkgds.Name:            vcpkgds.New(client, sem),
		elmpackage.Name:         elmpackage.New(client, sem, os.Getenv),
	}
	if err := addCustomSources(sources, client, sem, cfg); err != nil {
		return nil, err
	}
	return &session{
		root:      root,
		cfg:       cfg,
		managers:  managers,
		sources:   sources,
		security:  security,
		checksums: checksum.New(client, sem, releases, map[string]checksum.ArtifactOpener{mavends.Name: maven}),
		env:       env,
		client:    client,
		auth:      auth,
		gh:        gh,
		sem:       sem,
		progress:  o.progress(limits),
		info:      info,
		fetcher:   fetcher,
		osv:       osvSource,
	}, nil
}

// logExtends logs the configs cfg extends.
func logExtends(ctx context.Context, cfg *config.Config) {
	if len(cfg.Extends) > 0 {
		slog.InfoContext(ctx, "config extends", "configs", cfg.Extends)
	}
}

// configEnv is the manager environment of cfg: the actions default host (logged with its
// source), the GitLab URL and the Terraform registry.
func configEnv(cfg *config.Config, info platform.Info, gitlabURL string) manager.Env {
	host, source := cfg.Actions.DefaultHost, "config"
	if host == "" {
		host, source = platform.DefaultActionsHost(info, os.Getenv)
	}
	slog.Info("actions default host", "host", host, "source", source)
	return manager.Env{DefaultHost: host, GitLabURL: gitlabURL, TerraformRegistry: cfg.Terraform.DefaultRegistry}
}

// addCustomSources adds the custom datasources of cfg as custom.<name>.
func addCustomSources(sources map[string]engine.Datasource, client *http.Client, sem chan struct{}, cfg *config.Config) error {
	for name, cd := range cfg.CustomDatasources {
		ds, err := customds.New(client, sem, name, cd.Spec())
		if err != nil {
			return &config.Error{Err: err}
		}
		sources[customds.Prefix+name] = ds
	}
	return nil
}

// sibling is a session for another checkout of the repository (preview's base branch) with
// the config found there; see forCheckout.
func (s *session) sibling(ctx context.Context, dir string) (*session, error) {
	return s.forCheckout(ctx, dir, "", s.info)
}

// forCheckout is a session for the checkout dir of the repository of info with the config at
// configPath, else the one found in dir. It shares the datasources, so a release list fetched
// for one checkout is reused for the other; only custom datasources are the config's own.
func (s *session) forCheckout(ctx context.Context, dir, configPath string, info platform.Info) (*session, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(ctx, root, configPath, s.fetcher)
	if err != nil {
		return nil, err
	}
	logExtends(ctx, cfg)
	managers, err := engine.Managers(cfg)
	if err != nil {
		return nil, err
	}
	sources := maps.Clone(s.sources)
	for name := range sources {
		if strings.HasPrefix(name, customds.Prefix) {
			delete(sources, name)
		}
	}
	if err := addCustomSources(sources, s.client, s.sem, cfg); err != nil {
		return nil, err
	}
	gitlabURL, _ := platform.GitLabURL(cfg.GitLabCI.URL, info, os.Getenv)
	sib := *s
	sib.root, sib.cfg, sib.managers, sib.sources, sib.env, sib.info = root, cfg, managers, sources, configEnv(cfg, info, gitlabURL), info
	sib.security = nil
	if cfg.SecurityEnabled() {
		sib.security = s.osv
	}
	return &sib, nil
}

// forBase is the session resolving the checkout root of base branch base: the same config,
// its rules with baseBranches matching base, and the same datasources.
func (s *session) forBase(root, base string) *session {
	b := *s
	b.root, b.cfg = root, s.cfg.ForBaseBranch(base)
	return &b
}

// gitLabAuth returns the credentials for the host of the GitLab instance at gitlabURL, in
// precedence order: DEPS_GITLAB_TOKEN (user oauth2), then CI_JOB_TOKEN (user gitlab-ci-token)
// when the GitLab CI job runs on that instance (CI_SERVER_HOST, else CI_SERVER_URL's host).
// httpx.NewAuth keeps the platform token over both when the platform shares the host: it is
// the token explicitly given for the host deps runs against.
func gitLabAuth(gitlabURL string, getenv func(string) string) []httpx.AuthOption {
	u, err := url.Parse(gitlabURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := u.Hostname()
	opts := []httpx.AuthOption{httpx.WithToken(host, "oauth2", getenv("DEPS_GITLAB_TOKEN"))}
	ciHost := getenv("CI_SERVER_HOST")
	if ciHost == "" {
		if cu, err := url.Parse(getenv("CI_SERVER_URL")); err == nil {
			ciHost = cu.Hostname()
		}
	}
	if ciHost != "" && strings.EqualFold(ciHost, host) {
		opts = append(opts, httpx.WithToken(host, "gitlab-ci-token", getenv("CI_JOB_TOKEN")))
	}
	return opts
}

// options returns the engine options of this session; callers add Filter/Trace.
func (s *session) options() engine.Options {
	return engine.Options{Managers: s.managers, Datasources: s.sources, Security: s.security, Checksums: s.checksums, Progress: s.progress}
}

// progress returns the progress of the command (newProgress): the live status line when
// stderr is a terminal that can redraw a line (TERM is not dumb); with the actions format a
// log group per phase, plus a heartbeat line every 30s when stderr is no terminal. nil when
// neither applies, so plain piped output is unchanged.
func (o *rootOpts) progress(limits httpx.Limits) engine.Progress {
	line := o.term.tty && os.Getenv("TERM") != "dumb"
	if o.stderr == nil || !line && !actionsLog.on {
		return nil
	}
	p := newProgress(o.stderr, o.rawStderr, o.term.style, limits)
	if line {
		p.line, p.tick = true, statusTick
	}
	if actionsLog.on {
		p.groups = actionsLog
		if !o.term.tty {
			p.heartbeat = heartbeatEvery
		}
	}
	return p
}

func (s *session) resolve(ctx context.Context) (*engine.Result, error) {
	return engine.Resolve(ctx, s.root, s.cfg, s.env, s.options())
}

func (s *session) abs(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }

// close releases what the datasources keep for the run (the crate datasource's git index
// clones); failures only matter for temp space, so they are logged.
func (s *session) close() {
	if p, ok := s.progress.(*progress); ok {
		p.endGroup()
	}
	for name, ds := range s.sources {
		if c, ok := ds.(io.Closer); ok {
			if err := c.Close(); err != nil {
				slog.Warn("datasource cleanup", "datasource", name, "err", err)
			}
		}
	}
}
