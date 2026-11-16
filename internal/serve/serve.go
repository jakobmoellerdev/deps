package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/ghapp"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitcache"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// CycleInput is one repository cycle, handed to the Runner.
type CycleInput struct {
	RunID string
	// RepoID is "<host>/<fullName>", as the store keys it.
	RepoID string
	// Dir is the work tree to read; it is new for every cycle.
	Dir      string
	Base     string
	Token    string
	Info     platform.Info
	Mode     store.Mode
	Out      io.Writer
	Provider toolrun.Provider
}

// CycleResult is what one cycle did.
type CycleResult struct {
	Created, Updated, Closed, UpToDate, Skipped int
	Errors, RateLimited                         int
	// Report is the machine-readable result, the same document --report writes.
	Report []byte
}

// Runner executes one repository cycle. The implementation lives in the command, where the
// bot and the session are; the daemon owns everything around it.
type Runner interface {
	Cycle(ctx context.Context, in CycleInput) (CycleResult, error)
}

// maxVisibility bounds how long a claimed repository stays leased without an Extend.
const maxVisibility = 15 * time.Minute

// stealAfter is how long a due repository may sit untouched before another worker takes it.
const stealAfter = 10 * time.Minute

// workerTTL is how long a worker counts as alive after its last heartbeat.
const workerTTL = 30 * time.Second

// schedulerLock is the leader lock of the inventory and retention duties.
const schedulerLock = "scheduler"

// Daemon is one replica.
type Daemon struct {
	cfg    *Config
	st     *store.Store
	runner Runner
	worker string
	once   bool

	metrics *Metrics
	cache   *gitcache.Cache
	tools   *toolchain.Manager

	tokens   map[string]string     // forge name -> static token
	apps     map[string]*ghapp.App // forge name -> App
	secrets  map[string]string     // forge name -> webhook secret
	apiToken string

	active   Gauge
	live     Gauge
	dueOwned Gauge
	dueAll   Gauge
	repos    Gauge
	leader   atomic.Bool
	beat     atomic.Int64 // last heartbeat, unix milli
	buckets  atomic.Pointer[[]uint16]
	gcAt     atomic.Int64

	stopOnce sync.Once
}

// New wires a daemon from the configuration; it reads the credentials out of the
// environment, so a missing one fails at startup rather than in the first cycle.
func New(cfg *Config, st *store.Store, runner Runner, workerID string, once bool) (*Daemon, error) {
	d := &Daemon{
		cfg: cfg, st: st, runner: runner, worker: workerID, once: once,
		metrics: NewMetrics(),
		tokens:  map[string]string{},
		apps:    map[string]*ghapp.App{},
		secrets: map[string]string{},
		cache:   &gitcache.Cache{Root: filepath.Join(cfg.CacheDir, "git"), MaxBytes: cfg.CloneCacheMaxBytes},
		tools: &toolchain.Manager{
			Root:  filepath.Join(cfg.CacheDir, "tools"),
			State: filepath.Join(cfg.CacheDir, "state"),
			Pins:  pins(cfg.ToolPins),
		},
	}
	d.tools.OnInstall = func(tool toolchain.Tool, err error) {
		result := "ok"
		if err != nil {
			result = "failed"
		}
		d.metrics.Inc("deps_toolchain_installs_total", map[string]string{"tool": string(tool), "result": result})
	}
	if cfg.APITokenEnv != "" {
		d.apiToken = os.Getenv(cfg.APITokenEnv)
	}
	for i := range cfg.Forges {
		f := &cfg.Forges[i]
		if f.WebhookSecretEnv != "" {
			d.secrets[f.Name] = os.Getenv(f.WebhookSecretEnv)
		}
		if f.TokenEnv != "" {
			token := os.Getenv(f.TokenEnv)
			if token == "" {
				return nil, fmt.Errorf("forge %s: %s is empty", f.Name, f.TokenEnv)
			}
			d.tokens[f.Name] = token
			continue
		}
		pem, err := os.ReadFile(f.App.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("forge %s: %w", f.Name, err)
		}
		key, err := ghapp.LoadKey(pem)
		if err != nil {
			return nil, fmt.Errorf("forge %s: %s: %w", f.Name, f.App.PrivateKeyFile, err)
		}
		d.apps[f.Name] = &ghapp.App{
			ID: f.App.ID, PrivateKey: key, APIURL: f.APIURL,
			Cache: store.TokenCache{S: st},
			// A token must outlive a whole cycle, or a long one loses its credential
			// halfway and fails its pushes.
			MinLifetime: cfg.CycleTimeout.D() + 5*time.Minute,
		}
	}
	d.registerGauges()
	return d, nil
}

// Metrics exposes the exposition for the HTTP surface.
func (d *Daemon) Metrics() *Metrics { return d.metrics }

// Tools exposes the toolchain manager, so the command can share it with its HTTP client.
func (d *Daemon) Tools() *toolchain.Manager { return d.tools }

func pins(m map[string]string) map[toolchain.Tool]string {
	out := make(map[toolchain.Tool]string, len(m))
	for k, v := range m {
		out[toolchain.Tool(k)] = v
	}
	return out
}

func (d *Daemon) registerGauges() {
	d.metrics.SetGauge("deps_worker_active_cycles", d.active.Get)
	d.metrics.SetGauge("deps_workers_live", d.live.Get)
	d.metrics.SetGauge("deps_repositories", d.repos.Get)
	d.metrics.SetGauge("deps_clone_cache_bytes", func() float64 { return float64(d.cache.Size()) })
	d.metrics.SetGauge("deps_tool_cache_bytes", func() float64 { return float64(d.tools.Size()) })
	d.metrics.SetGauge("deps_scheduler_leader", func() float64 {
		if d.leader.Load() {
			return 1
		}
		return 0
	})
}

// TLSConfig builds the Valkey TLS configuration of a serve config; nil when it configures
// none.
func (v Valkey) TLSConfig() (*tls.Config, error) {
	if v.TLS.CAFile == "" && v.TLS.CertFile == "" {
		return nil, nil
	}
	conf := &tls.Config{MinVersion: tls.VersionTLS12}
	if v.TLS.CAFile != "" {
		pem, err := os.ReadFile(v.TLS.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificate found", v.TLS.CAFile)
		}
		conf.RootCAs = pool
	}
	if v.TLS.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(v.TLS.CertFile, v.TLS.KeyFile)
		if err != nil {
			return nil, err
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	return conf, nil
}

// makeCacheDirs creates every directory the daemon and its tools write to. They all live
// under the cache volume, which starts empty, so creating them here is what lets the
// image's root filesystem be mounted read-only: the only other path written at runtime is
// $TMPDIR, where the post-processors mirror a scope before running the ecosystem tool.
func (d *Daemon) makeCacheDirs() error {
	dirs := []string{d.cfg.CacheDir, d.cache.Root, d.tools.Root, d.tools.State}
	// A HOME inside the cache volume keeps the tools' dotfiles off the root filesystem.
	if home := os.Getenv("HOME"); home != "" && underCache(d.cfg.CacheDir, home) {
		dirs = append(dirs, home)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// underCache reports whether p is the cache directory or below it.
func underCache(cacheDir, p string) bool {
	rel, err := filepath.Rel(cacheDir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Run starts the HTTP surface, the worker loop and the scheduler duties and returns when ctx
// ends. With Once it refreshes the inventory, runs every repository that is due and returns.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.makeCacheDirs(); err != nil {
		return err
	}
	if err := d.cache.PruneWork(ctx); err != nil {
		slog.WarnContext(ctx, "pruning leftover work trees", "err", err)
	}
	if d.once {
		return d.runOnce(ctx)
	}
	srv := &http.Server{
		Addr:              d.cfg.Listen,
		Handler:           d.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "listening", "addr", d.cfg.Listen, "worker", d.worker)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	var wg sync.WaitGroup
	loops := context.AfterFunc(ctx, func() {})
	defer loops()
	wg.Go(func() { d.workerLoop(ctx) })
	wg.Go(func() { d.schedulerLoop(ctx) })

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	wg.Wait()
	d.stop(shutdown)
	return runErr
}

// stop hands this replica's work back: its buckets become claimable at once instead of after
// the heartbeat timeout, and the scheduler lock is released.
func (d *Daemon) stop(ctx context.Context) {
	d.stopOnce.Do(func() {
		if err := d.st.Leave(ctx, d.worker); err != nil {
			slog.WarnContext(ctx, "leaving the worker set", "err", err)
		}
		if d.leader.Load() {
			if err := d.st.Unlock(ctx, schedulerLock, d.worker); err != nil {
				slog.WarnContext(ctx, "releasing the scheduler lock", "err", err)
			}
		}
	})
}

// runOnce refreshes the inventory and runs every repository that is due once, then returns.
// It is what the end-to-end tests drive and what a cron-style deployment without a
// long-running process would use.
func (d *Daemon) runOnce(ctx context.Context) error {
	if err := d.refreshInventory(ctx); err != nil {
		return err
	}
	_ = d.st.Heartbeat(ctx, d.worker)
	all := make([]uint16, store.Buckets)
	for i := range all {
		all[i] = uint16(i)
	}
	sem := make(chan struct{}, d.cfg.Workers)
	var wg sync.WaitGroup
	// Once per repository, whatever its cadence says afterwards: a short interval makes a
	// repository due again the moment its cycle completes, which must not turn this into a
	// loop that never ends.
	done := map[string]bool{}
	for ctx.Err() == nil {
		repos, err := d.st.Claim(ctx, d.worker, all, d.cfg.Workers, d.visibility())
		if err != nil {
			return err
		}
		fresh := repos[:0]
		for _, r := range repos {
			if !done[r.ID] {
				done[r.ID] = true
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			break
		}
		for _, r := range fresh {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
			wg.Go(func() {
				defer func() { <-sem }()
				d.runCycle(ctx, r)
			})
		}
		wg.Wait()
	}
	wg.Wait()
	slog.InfoContext(ctx, "once complete", "repositories", len(done))
	d.stop(ctx)
	return nil
}

// workerLoop claims and runs cycles for as long as the process lives.
func (d *Daemon) workerLoop(ctx context.Context) {
	sem := make(chan struct{}, d.cfg.Workers)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	steal := time.NewTicker(30 * time.Second)
	defer steal.Stop()
	for {
		d.heartbeat(ctx)
		free := d.cfg.Workers - int(d.active.Get())
		if free > 0 {
			var repos []store.Repo
			var err error
			select {
			case <-steal.C:
				repos, err = d.st.ClaimAny(ctx, d.worker, free, stealAfter, d.visibility())
				d.metrics.Add("deps_queue_claims_total", map[string]string{"kind": "stolen"}, float64(len(repos)))
			default:
				repos, err = d.st.Claim(ctx, d.worker, d.ownedBuckets(), free, d.visibility())
				d.metrics.Add("deps_queue_claims_total", map[string]string{"kind": "owned"}, float64(len(repos)))
			}
			if err != nil {
				slog.WarnContext(ctx, "claiming work", "err", err)
			}
			for _, r := range repos {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Go(func() {
					defer func() { <-sem }()
					d.runCycle(ctx, r)
				})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// heartbeat keeps this worker in the live set and recomputes which buckets it owns.
func (d *Daemon) heartbeat(ctx context.Context) {
	if err := d.st.Heartbeat(ctx, d.worker); err != nil {
		slog.WarnContext(ctx, "heartbeat", "err", err)
		return
	}
	d.beat.Store(time.Now().UnixMilli())
	workers, err := d.st.Workers(ctx, workerTTL)
	if err != nil {
		slog.WarnContext(ctx, "reading the worker set", "err", err)
		return
	}
	d.live.Set(int64(len(workers)))
	owned := store.OwnedBuckets(d.worker, workers)
	d.buckets.Store(&owned)
	if n, err := d.st.Due(ctx, time.Now(), owned); err == nil {
		d.dueOwned.Set(n)
	}
	if n, err := d.st.Due(ctx, time.Now(), nil); err == nil {
		d.dueAll.Set(n)
	}
	if n, err := d.st.Count(ctx); err == nil {
		d.repos.Set(n)
	}
}

// Ready reports whether the replica is serving: Valkey answers and the worker loop
// heartbeated recently.
func (d *Daemon) Ready(ctx context.Context) error {
	if err := d.st.Ping(ctx); err != nil {
		return err
	}
	last := d.beat.Load()
	if last == 0 || time.Since(time.UnixMilli(last)) > workerTTL {
		return errors.New("worker loop has not heartbeated")
	}
	return nil
}

func (d *Daemon) ownedBuckets() []uint16 {
	if b := d.buckets.Load(); b != nil {
		return *b
	}
	return nil
}

// NewRunID mints the id of a cycle: sortable by time, unique without coordination.
func NewRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + hex.EncodeToString(b[:])
}

// forgeOf returns the configured forge of a repository.
func (d *Daemon) forgeOf(name string) *Forge {
	for i := range d.cfg.Forges {
		if d.cfg.Forges[i].Name == name {
			return &d.cfg.Forges[i]
		}
	}
	return nil
}

// token resolves the credential of a repository to a concrete token: the static one, or a
// GitHub App installation token minted (or read from the shared cache) right now.
func (d *Daemon) token(ctx context.Context, r store.Repo) (string, error) {
	if t, ok := d.tokens[r.Forge]; ok {
		return t, nil
	}
	app, ok := d.apps[r.Forge]
	if !ok {
		return "", fmt.Errorf("forge %s has no credential", r.Forge)
	}
	if r.Installation == 0 {
		return "", fmt.Errorf("%s: no GitHub App installation recorded", r.ID)
	}
	token, _, err := app.InstallationToken(ctx, r.Installation)
	return token, err
}

// runCycle is one repository cycle, end to end: credential, budget, checkout, toolchains,
// the bot, the record, the next due time.
func (d *Daemon) runCycle(parent context.Context, r store.Repo) {
	d.active.Add(1)
	defer d.active.Add(-1)

	runID := NewRunID()
	ctx, cancel := context.WithTimeout(parent, d.cfg.CycleTimeout.D())
	defer cancel()
	ctx = logx.WithAttrs(ctx, slog.String("repo", r.ID), slog.String("run", runID))
	start := time.Now()
	if err := d.st.MarkRunning(ctx, r.ID, start); err != nil {
		slog.DebugContext(ctx, "marking the cycle as running", "err", err)
	}

	// The lease is renewed while the cycle runs; letting it lapse is how a crashed worker
	// hands the repository back.
	stopExtend := make(chan struct{})
	defer close(stopExtend)
	go d.extendLease(ctx, r.ID, stopExtend)

	status, res, err := d.cycle(ctx, r, runID)
	elapsed := time.Since(start)
	forge := r.Forge
	d.metrics.Inc("deps_cycles_total", map[string]string{"forge": forge, "mode": string(r.Mode), "status": status})
	d.metrics.Observe("deps_cycle_duration_seconds", map[string]string{"forge": forge, "mode": string(r.Mode)}, elapsed.Seconds())
	for action, n := range map[string]int{
		"created": res.Created, "updated": res.Updated, "closed": res.Closed, "up-to-date": res.UpToDate,
	} {
		if n > 0 {
			d.metrics.Add("deps_prs_total", map[string]string{"action": action}, float64(n))
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	run := store.Run{
		ID: runID, RepoID: r.ID, Worker: d.worker, Status: status, StartedAt: start, Duration: elapsed,
		Created: res.Created, Updated: res.Updated, Closed: res.Closed, UpToDate: res.UpToDate,
		Errors: res.Errors, Message: msg,
	}
	record := context.WithoutCancel(ctx)
	if err := d.st.AddRun(record, run); err != nil {
		slog.WarnContext(ctx, "recording the run", "err", err)
	}
	if len(res.Report) > 0 {
		if err := d.st.PutReport(record, runID, res.Report, d.cfg.ReportTTL.D()); err != nil {
			slog.WarnContext(ctx, "storing the report", "err", err)
		}
	}
	if status == "ok" {
		r.FailCount = 0
	} else {
		r.FailCount++
	}
	next := store.NextBackoff(r.ID, time.Now(), d.interval(r), r.FailCount)
	if status == "rate-limited" && !res.resetAt.IsZero() {
		next = res.resetAt
	}
	if d.once {
		// A cadence shorter than the run makes a finished repository due again immediately, and
		// since the claim takes the earliest due ones it would then hand back the same handful
		// forever while the rest of the fleet waits. One pass means one cycle per repository, so
		// hold the finished one back until after this process is gone.
		next = time.Now().Add(d.visibility())
	}
	ok, err := d.st.Complete(record, d.worker, r, status, next)
	switch {
	case err != nil:
		slog.ErrorContext(ctx, "recording the result", "err", err)
	case !ok:
		d.metrics.Inc("deps_cycle_lease_lost_total", nil)
		slog.WarnContext(ctx, "lease lost, result not recorded")
	}
	attrs := []any{"status", status, "duration", elapsed, "created", res.Created,
		"updated", res.Updated, "closed", res.Closed, "upToDate", res.UpToDate, "errors", res.Errors,
		"next", next.Format(time.RFC3339)}
	if err != nil {
		// A cycle that failed before it had a result (a clone, config or credential error)
		// has no error line of its own: say why here.
		slog.ErrorContext(ctx, "cycle", append(attrs, "err", err)...)
	} else {
		slog.InfoContext(ctx, "cycle", attrs...)
	}
	d.maybeGC(ctx)
}

// extendLease renews the lease of a running cycle until it is done.
func (d *Daemon) extendLease(ctx context.Context, id string, done <-chan struct{}) {
	t := time.NewTicker(d.visibility() / 3)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.st.Extend(context.WithoutCancel(ctx), d.worker, id, d.visibility()); err != nil {
				slog.DebugContext(ctx, "extending the lease", "err", err)
			}
		}
	}
}

// cycleOutcome carries the few fields runCycle needs beyond the Runner's result.
type cycleOutcome struct {
	CycleResult
	resetAt time.Time
}

// cycle does the work of one repository and reports its status: "ok", "failed" or
// "rate-limited".
func (d *Daemon) cycle(ctx context.Context, r store.Repo, runID string) (string, cycleOutcome, error) {
	var out cycleOutcome
	f := d.forgeOf(r.Forge)
	if f == nil {
		return "failed", out, fmt.Errorf("forge %s is no longer configured", r.Forge)
	}
	token, err := d.token(ctx, r)
	if err != nil {
		return "failed", out, err
	}
	credential := r.Forge
	if r.Installation != 0 {
		credential = r.Forge + ":" + strconv.FormatInt(r.Installation, 10)
	}
	// A credential's hourly API budget is per account and does not grow with the fleet, so a
	// cycle that cannot fit is rescheduled rather than spending the rest on errors.
	if ok, reset, err := d.st.Budget(ctx, credential, 0, d.cfg.CredentialLimit); err == nil && !ok {
		out.resetAt = reset
		d.metrics.Inc("deps_rate_limited_total", map[string]string{"host": r.Host})
		return "rate-limited", out, nil
	}

	base := r.Base
	if base == "" {
		base = r.DefaultBranch
	}
	checkout, cleanup, err := d.cache.Checkout(ctx, runID, r.CloneURL, r.Host, r.FullName, base,
		f.ServerURL+"/", authHeader(token))
	if err != nil {
		return "failed", out, fmt.Errorf("checkout: %w", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			slog.WarnContext(ctx, "removing the work tree", "err", err)
		}
	}()
	if checkout.Base != r.DefaultBranch && r.Base == "" {
		if err := d.st.SetDefaultBranch(context.WithoutCancel(ctx), r.ID, checkout.Base); err != nil {
			slog.DebugContext(ctx, "recording the default branch", "err", err)
		}
	}

	// The forge comes from the configuration, so a cycle makes no detection request at all.
	info := platform.Info{
		Kind:      platform.Kind(f.Kind),
		ServerURL: f.ServerURL,
		APIURL:    f.APIURL,
		Host:      r.Host,
		Repo:      r.FullName,
	}
	provider := d.tools.For(toolchain.Detect(checkout.Work))
	ctx = toolrun.WithProvider(ctx, provider)

	var buf bytes.Buffer
	res, err := d.runner.Cycle(ctx, CycleInput{
		RunID: runID, RepoID: r.ID, Dir: checkout.Work, Base: checkout.Base,
		Token: token, Info: info, Mode: r.Mode, Out: &buf, Provider: provider,
	})
	out.CycleResult = res
	if buf.Len() > 0 {
		slog.DebugContext(ctx, "cycle output", "lines", strings.TrimRight(buf.String(), "\n"))
	}
	if tools := provider.Installed(); len(tools) > 0 {
		slog.DebugContext(ctx, "toolchains used", "tools", tools)
	}
	switch {
	case err != nil:
		return "failed", out, err
	case res.Errors > 0:
		return "failed", out, nil
	}
	return "ok", out, nil
}

// visibility is how long a claimed repository stays leased without an Extend: a cycle
// renews it while it runs, and a worker that dies lets it lapse, which is what hands the
// repository back. It never needs to outlive the cycle timeout, so a deployment with short
// cycles also retries a dead worker's repositories sooner.
func (d *Daemon) visibility() time.Duration {
	return min(d.cfg.CycleTimeout.D(), maxVisibility)
}

// interval is the cadence of a repository: its own, else the configuration's default.
func (d *Daemon) interval(r store.Repo) time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return d.cfg.Interval.D()
}

// gcEvery is how often the caches are swept; a sweep walks the cache directories, which is
// too slow to do after every cycle.
const gcEvery = 15 * time.Minute

// maybeGC sweeps the git mirrors and the toolchains when the last sweep is old enough.
func (d *Daemon) maybeGC(ctx context.Context) {
	now := time.Now().UnixMilli()
	last := d.gcAt.Load()
	if now-last < gcEvery.Milliseconds() || !d.gcAt.CompareAndSwap(last, now) {
		return
	}
	if n, freed, err := d.cache.GC(ctx); err != nil {
		slog.WarnContext(ctx, "git cache gc", "err", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "git cache gc", "mirrors", n, "freed", freed)
	}
	if n, freed := d.tools.GC(d.cfg.ToolCacheMaxBytes); n > 0 {
		slog.InfoContext(ctx, "tool cache gc", "versions", n, "freed", freed)
	}
}

// authHeader is the git http.extraheader that authenticates with token, the way
// actions/checkout builds it.
func authHeader(token string) string {
	if token == "" {
		return ""
	}
	return "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}
