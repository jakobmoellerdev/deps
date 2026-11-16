package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

// serveOpts are the flags of `deps serve`; everything else is in the serve configuration.
type serveOpts struct {
	config   string
	listen   string
	valkey   string
	cacheDir string
	workerID string
	workers  int
	once     bool
}

func newServe(o *rootOpts) *cli.Command {
	s := &serveOpts{}
	return &cli.Command{
		Name:  "serve",
		Usage: "Run as a long-running bot: schedule and execute runs for many repositories",
		Metadata: map[string]any{docgen.MetaExample: "  deps serve --config /etc/deps/serve.yaml\n" +
			"  # run every repository that is due once and exit (cron-style deployments and tests)\n" +
			"  deps serve --config serve.yaml --once"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runServe(ctx, cmd, o, s)
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Value: "/etc/deps/serve.yaml", Destination: &s.config, Usage: "serve configuration file (see deps help serve)"},
			&cli.StringFlag{Name: "listen", Destination: &s.listen, Usage: "address of the HTTP API, metrics and webhooks (default: the config's listen, else :8080)"},
			&cli.StringFlag{Name: "valkey", Destination: &s.valkey, Usage: "Valkey URL, overriding the config (redis://host:6379/0)"},
			&cli.StringFlag{Name: "cache-dir", Destination: &s.cacheDir, Usage: "directory of the git mirrors and the installed toolchains, overriding the config"},
			&cli.StringFlag{Name: "worker-id", Destination: &s.workerID, Usage: "identity of this replica in the worker set (default: <hostname>-<pid>)"},
			&cli.IntFlag{Name: "workers", Destination: &s.workers, Usage: "cycles this replica runs at once, overriding the config (default: max(4, GOMAXPROCS))"},
			&cli.BoolFlag{Name: "once", Destination: &s.once, Usage: "refresh the inventory, run every repository that is due, then exit"},
		},
	}
}

func runServe(ctx context.Context, _ *cli.Command, o *rootOpts, s *serveOpts) error {
	cfg, err := serve.Load(s.config)
	if err != nil {
		return err
	}
	if s.listen != "" {
		cfg.Listen = s.listen
	}
	if s.valkey != "" {
		cfg.Valkey.URL = s.valkey
	}
	if s.cacheDir != "" {
		cfg.CacheDir = s.cacheDir
	}
	if s.workers > 0 {
		cfg.Workers = s.workers
	}
	worker := s.workerID
	if worker == "" {
		host, err := os.Hostname()
		if err != nil {
			return err
		}
		worker = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	// A daemon wants a global cap on child processes that matches its own concurrency; the
	// CLI keeps the default. It must be set before the first Build.
	engine.SetPostLimit(max(engine.DefaultPostLimit, runtime.GOMAXPROCS(0)))

	tlsConf, err := cfg.Valkey.TLSConfig()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.Valkey.URL, tlsConf)
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer st.Close()

	runner, err := newServeRunner(ctx, cfg, o, st)
	if err != nil {
		return err
	}
	defer runner.close()
	d, err := serve.New(cfg, st, runner, worker, s.once)
	if err != nil {
		return err
	}
	runner.metrics = d.Metrics()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.InfoContext(ctx, "serve", "worker", worker, "workers", cfg.Workers, "forges", len(cfg.Forges),
		"cacheDir", cfg.CacheDir, "once", s.once)
	return d.Run(ctx)
}

// serveRunner executes one cycle with the same code paths as `deps run` and `deps plan`.
type serveRunner struct {
	cfg     *serve.Config
	root    *rootOpts
	run     *runOpts
	shared  *runShared
	client  *http.Client
	metrics *serve.Metrics

	mu       sync.Mutex
	sessions map[string]*credSession
}

// credSession is the base session of one credential: its datasources, HTTP client and
// request pools are shared by every cycle of that credential, which is where the cross-repo
// reuse of release lists comes from.
type credSession struct {
	s     *session
	token string
	built time.Time
}

func newServeRunner(ctx context.Context, cfg *serve.Config, o *rootOpts, st *store.Store) (*serveRunner, error) {
	rn := &serveRunner{cfg: cfg, root: o, run: &runOpts{maxNewPRs: cfg.MaxNewPRs}, sessions: map[string]*credSession{}}
	// One HTTP client for the process, with the shared metadata cache in front of it: without
	// it a fleet of this size would be refused by the registries long before CPU mattered.
	// The metrics are attached by the caller right after, so the closure reads them lazily.
	client := httpx.NewCachingClient(valkeyCache{st}, "serve", func(host string, outcome httpx.CacheOutcome) {
		if m := rn.metrics; m != nil {
			m.Inc("deps_http_requests_total", map[string]string{"host": host, "outcome": string(outcome)})
		}
	})
	sh := &runShared{
		trigger:    schedule.Trigger{Time: time.Now()},
		allowTasks: cfg.AllowTasks,
		client:     client,
	}
	var err error
	if cfg.SigningKeyEnv != "" {
		if sh.signer, err = newSigner(ctx, os.Getenv(cfg.SigningKeyEnv), cfg.SigningKeyEnv); err != nil {
			return nil, err
		}
	}
	rn.shared, rn.client = sh, client
	return rn, nil
}

func (rn *serveRunner) close() {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	for _, c := range rn.sessions {
		c.s.close()
	}
	rn.sessions = nil
	if err := rn.shared.signer.Close(); err != nil {
		slog.Warn("removing commit signing key material", "err", err)
	}
}

// Cycle runs one repository: plan mode resolves and reports without touching the forge, run
// mode is exactly what `deps run` does on a checkout.
func (rn *serveRunner) Cycle(ctx context.Context, in serve.CycleInput) (serve.CycleResult, error) {
	base, err := rn.baseSession(ctx, in)
	if err != nil {
		return serve.CycleResult{}, err
	}
	s, err := base.forCheckout(ctx, in.Dir, "", in.Info)
	if err != nil {
		return serve.CycleResult{}, err
	}
	// A cycle renders into its own buffer and never into a terminal: no progress overlay, no
	// colors, no Actions annotations, and no --report file, since the daemon stores the
	// report itself.
	o := *rn.root
	o.dir, o.config, o.report, o.showSkipped = in.Dir, "", "", false
	o.concurrency = rn.cfg.Concurrency
	o.term = terminal{}
	o.stdout = io.Discard
	var result serve.CycleResult
	o.collect = func(sum *summary) {
		c := sum.counts()
		result.Created, result.Updated, result.Closed = c["created"], c["updated"], c["closed"]
		result.UpToDate, result.Skipped, result.Errors = c["upToDate"], c["skipped"], c["errors"]
		result.RateLimited = c["rateLimited"]
		if body, err := json.Marshal(sum.report()); err == nil {
			result.Report = body
		}
	}
	sh := *rn.shared
	sh.token = in.Token
	sh.trigger = schedule.Trigger{Time: time.Now()}
	start := time.Now()
	if in.Mode == store.ModePlan {
		err = planSession(ctx, in.Out, &o, s, false, nil, "", start)
	} else {
		r := *rn.run
		r.base = in.Base
		author := gitAuthor{Name: rn.cfg.Author.Name, Email: rn.cfg.Author.Email}
		err = runRepo(ctx, in.Out, &o, &r, &sh, s, author, start, nil)
	}
	// errReported means the cycle ran and reported its failures itself; the counts carry them.
	if errors.Is(err, errReported) {
		err = nil
	}
	return result, err
}

// baseSession returns the shared session of a credential, building it on the first cycle of
// that credential and rebuilding it when the token rotated or it grew old: the per-directory
// memos of the datasources must not accumulate forever.
func (rn *serveRunner) baseSession(ctx context.Context, in serve.CycleInput) (*session, error) {
	key := in.Info.Host + "|" + string(in.Info.Kind)
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if c, ok := rn.sessions[key]; ok {
		if c.token == in.Token && time.Since(c.built) < rn.cfg.InventoryInterval.D() {
			return c.s, nil
		}
		c.s.close()
		delete(rn.sessions, key)
	}
	o := *rn.root
	o.dir, o.config, o.report = in.Dir, "", ""
	o.concurrency = rn.cfg.Concurrency
	o.term = terminal{}
	s, err := newSession(ctx, &o, in.Info, in.Token, rn.client, in.Out)
	if err != nil {
		return nil, err
	}
	rn.sessions[key] = &credSession{s: s, token: in.Token, built: time.Now()}
	return s, nil
}

// valkeyCache backs the shared metadata cache with the control plane's Valkey. A failure is
// a miss, never an error: a Valkey outage must slow the fleet down, not stop it.
type valkeyCache struct{ st *store.Store }

func (c valkeyCache) Get(ctx context.Context, key string) ([]byte, bool) {
	b, err := c.st.CacheGet(ctx, key)
	if err != nil {
		httpx.LogCacheError(ctx, "get", err)
		return nil, false
	}
	return b, b != nil
}

func (c valkeyCache) Put(ctx context.Context, key string, entry []byte, ttl time.Duration) {
	if err := c.st.CachePut(ctx, key, entry, ttl); err != nil {
		httpx.LogCacheError(ctx, "put", err)
	}
}
