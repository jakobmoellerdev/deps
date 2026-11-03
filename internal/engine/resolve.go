// Package engine discovers, extracts, resolves and patches dependencies.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Aliases of the leaf datasource package, so implementations need not import the engine.
type (
	Release        = datasource.Release
	Datasource     = datasource.Datasource
	SecuritySource = datasource.SecuritySource
	Vuln           = datasource.Vuln
)

// Options configures Resolve.
type Options struct {
	Managers    []manager.Manager
	Datasources map[string]Datasource // by dep.Dep.Datasource
	Security    SecuritySource        // nil = no security lookups
	Filter      func(dep.Dep) bool    // nil = all; deps it rejects are dropped before any lookup
	Trace       func(Trace)           // nil = off; called sequentially from the Resolve goroutine
}

// Trace explains the decision for one dep.
type Trace struct {
	Dep        dep.Dep
	Rules      []int // indices of matching rules
	Policy     config.Policy
	Candidates int                 // releases returned by the datasource
	Rejected   map[string][]string // reason -> versions
	Update     *dep.Update         // nil = none
	Err        error
}

// Result is the outcome of Resolve.
type Result struct {
	Updates []dep.Update
	Skipped []dep.Dep
	Errors  []error
}

// digestManagers support digest pinning (pin: digest, digest refresh).
var digestManagers = map[string]bool{"github-actions": true, "docker": true}

type job struct {
	file string
	m    manager.Manager
}

type extracted struct {
	deps []dep.Dep
	err  error
}

// Resolve discovers dependency files under root, extracts dependencies and looks up updates.
// The returned error is non-nil only for walk failures or context cancellation;
// per-file and per-dependency failures are collected in Result.Errors.
func Resolve(ctx context.Context, root string, cfg *config.Config, env manager.Env, o Options) (*Result, error) {
	start := time.Now()
	found, err := discover(root, cfg.IgnorePaths, o.Managers)
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	var jobs []job
	for i, m := range o.Managers {
		slog.InfoContext(ctx, "files discovered", "manager", m.Name(), "files", len(found[i]), "duration", time.Since(start))
		for _, f := range found[i] {
			jobs = append(jobs, job{f, m})
		}
	}
	if env.Vendored == nil {
		env.Vendored = func(dir string) bool {
			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "vendor", "modules.txt"))
			return err == nil
		}
	}

	start = time.Now()
	ex := extractAll(root, jobs, env)

	res := &Result{}
	var todo []dep.Dep
	var policies []config.Policy
	for i, e := range ex {
		if e.err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", jobs[i].file, e.err))
			continue
		}
		slog.DebugContext(ctx, "file extracted", "file", jobs[i].file, "manager", jobs[i].m.Name(), "deps", len(e.deps))
		for _, d := range e.deps {
			if o.Filter != nil && !o.Filter(d) {
				continue
			}
			if d.Skip != "" {
				res.Skipped = append(res.Skipped, d)
				if o.Trace != nil {
					o.Trace(Trace{Dep: d})
				}
				continue
			}
			p := cfg.Policy(d)
			if p.Ignore {
				d.Skip = p.IgnoreReason
				res.Skipped = append(res.Skipped, d)
				if o.Trace != nil {
					o.Trace(Trace{Dep: d, Rules: p.Rules, Policy: p})
				}
				continue
			}
			todo = append(todo, d)
			policies = append(policies, p)
		}
	}
	slog.InfoContext(ctx, "dependencies extracted", "files", len(jobs), "updatable", len(todo), "skipped", len(res.Skipped), "duration", time.Since(start))
	start = time.Now()

	sec := lookupSecurity(ctx, o.Security, todo)
	outs := make([]Trace, len(todo))
	var wg sync.WaitGroup
	for i := range todo {
		wg.Go(func() {
			d := todo[i]
			r := resolver{ctx: ctx, root: root, d: d, p: policies[i], ds: o.Datasources[d.Datasource], trace: o.Trace != nil}
			t := r.run(func() []Vuln { return sec.of(i) })
			switch {
			case t.Err != nil:
				t.Err = fmt.Errorf("%s:%d: %s: %w", d.File, d.Line, d.Name, t.Err)
			case t.Update == nil:
				slog.DebugContext(ctx, "dependency up to date", "file", d.File, "line", d.Line, "name", d.Name, "version", d.Version)
			default:
				slog.DebugContext(ctx, "update found", "file", d.File, "line", d.Line, "name", d.Name, "from", d.Version, "to", t.Update.NewVersion, "type", t.Update.Type)
			}
			outs[i] = t
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sec.wait(); err != nil {
		res.Errors = append(res.Errors, fmt.Errorf("security advisories: %w", err))
	}
	for _, t := range outs {
		if t.Err != nil {
			res.Errors = append(res.Errors, t.Err)
		} else if t.Update != nil {
			res.Updates = append(res.Updates, *t.Update)
		}
		if o.Trace != nil {
			o.Trace(t)
		}
	}
	slices.SortStableFunc(res.Updates, func(a, b dep.Update) int {
		if c := strings.Compare(a.Dep.File, b.Dep.File); c != 0 {
			return c
		}
		return a.Dep.Line - b.Dep.Line
	})
	slog.InfoContext(ctx, "dependencies resolved", "updates", len(res.Updates), "errors", len(res.Errors), "duration", time.Since(start))
	return res, nil
}

// extractAll reads and parses files on GOMAXPROCS workers. Only the extracted deps are kept;
// file bytes and parse trees are dropped as soon as a file is done.
func extractAll(root string, jobs []job, env manager.Env) []extracted {
	ex := make([]extracted, len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(jobs)) {
		wg.Go(func() {
			for i := range next {
				ex[i] = extractOne(root, jobs[i], env)
			}
		})
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()
	return ex
}

func extractOne(root string, j job, env manager.Env) extracted {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(j.file)))
	if err != nil {
		return extracted{err: err}
	}
	deps, err := j.m.Extract(j.file, src, env)
	return extracted{deps: deps, err: err}
}

// security is the in-flight result of the single SecuritySource call.
type security struct {
	done  chan struct{}
	vulns [][]Vuln
	err   error
}

// lookupSecurity starts the one advisories lookup concurrently with the release lookups.
// It returns nil when there is no source or nothing to look up.
func lookupSecurity(ctx context.Context, src SecuritySource, deps []dep.Dep) *security {
	if src == nil || len(deps) == 0 {
		return nil
	}
	s := &security{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		start := time.Now()
		s.vulns, s.err = src.Advisories(ctx, deps)
		if s.err == nil && len(s.vulns) != len(deps) {
			s.vulns, s.err = nil, fmt.Errorf("got %d results for %d deps", len(s.vulns), len(deps))
		}
		slog.DebugContext(ctx, "security advisories", "deps", len(deps), "duration", time.Since(start), "err", s.err)
	}()
	return s
}

// wait blocks until the lookup finished; nil-safe.
func (s *security) wait() error {
	if s == nil {
		return nil
	}
	<-s.done
	return s.err
}

// of returns the vulnerabilities of dep i, or nil when the lookup failed or is off.
func (s *security) of(i int) []Vuln {
	if s == nil || s.wait() != nil {
		return nil
	}
	return s.vulns[i]
}

// resolver decides the update of one dep.
type resolver struct {
	ctx   context.Context
	root  string
	d     dep.Dep
	p     config.Policy
	ds    Datasource
	trace bool

	rels []Release
}

// run resolves in order: datasource → Best under policy → digest pin/refresh → security.
// vulns blocks until the advisories of this dep are known.
func (r *resolver) run(vulns func() []Vuln) Trace {
	t := Trace{Dep: r.d, Rules: r.p.Rules, Policy: r.p}
	t.Update, t.Err = r.resolve(&t, vulns)
	return t
}

func (r *resolver) resolve(t *Trace, vulns func() []Vuln) (*dep.Update, error) {
	d := r.d
	scheme, ok := versioning.Get(d.Versioning)
	if !ok {
		return nil, fmt.Errorf("unknown versioning %q", d.Versioning)
	}
	if r.ds == nil {
		return nil, fmt.Errorf("no datasource %q", d.Datasource)
	}
	if !scheme.Valid(d.Version) {
		return nil, fmt.Errorf("unparsable version %q", d.Version)
	}
	rels, err := r.ds.Releases(r.ctx, d, filepath.Join(r.root, filepath.FromSlash(path.Dir(d.File))))
	if err != nil {
		return nil, err
	}
	r.rels = rels
	t.Candidates = len(rels)
	names := make([]string, len(rels))
	for i, rel := range rels {
		names[i] = rel.Version
	}
	var reject func(v, reason string)
	if r.trace {
		t.Rejected = map[string][]string{}
		reject = func(v, reason string) { t.Rejected[reason] = append(t.Rejected[reason], v) }
	}
	accept := func(v string, kind dep.UpdateType) (bool, string) { return r.p.Check(kind, scheme.Semver(v)) }

	// Custom deps can only refresh a digest they already carry (regex `digest` group); they
	// have no place to insert a new one.
	digests := digestManagers[d.Manager] && (d.Versioning == "tag" || d.Versioning == "docker") ||
		d.Manager == "custom" && d.Digest != ""
	pinned := d.Digest != ""
	wantDigest := digests && (pinned || r.p.Pin == config.PinDigest)

	var up *dep.Update
	if best, ok := scheme.Best(d.Version, names, accept, reject); ok {
		up = &dep.Update{Dep: d, Type: scheme.Kind(d.Version, best), NewVersion: best}
	} else if digests {
		switch {
		case pinned && r.p.Updates[dep.Digest]:
			c, err := r.digestOf(d.Version)
			if err != nil {
				return nil, err
			}
			if !strings.EqualFold(c, d.Digest) {
				up = &dep.Update{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: c}
			}
		case !pinned && r.p.Pin == config.PinDigest && r.p.Updates[dep.Pin]:
			c, err := r.digestOf(d.Version)
			if err != nil {
				return nil, err
			}
			up = &dep.Update{Dep: d, Type: dep.Pin, NewVersion: d.Version, NewDigest: c}
		}
	}

	up = applySecurity(up, d, scheme, names, vulns())

	if up != nil && up.Type != dep.Digest && up.Type != dep.Pin && wantDigest {
		if up.NewDigest, err = r.digestOf(up.NewVersion); err != nil {
			return nil, err
		}
	}
	if up != nil && len(up.Advisories) == 0 {
		up.Group, up.Schedule = r.p.Group, r.p.Schedule
	}
	return up, nil
}

// digestOf returns the digest of version from the releases, asking the datasource when the
// release carries none.
func (r *resolver) digestOf(version string) (string, error) {
	for _, rel := range r.rels {
		if rel.Version == version && rel.Digest != "" {
			return rel.Digest, nil
		}
	}
	return r.ds.Digest(r.ctx, r.d, version)
}

// applySecurity raises up to a fixing version when d has advisories. The minimum fix F is the
// highest, over all vulns, of each vuln's lowest compatible fixed version. If there is no
// update or it is below F, the smallest compatible release ≥ F is chosen regardless of policy.
// The advisories whose fix the resulting version includes are attached. Vulns without a
// compatible fix, or a missing fixing release, leave up unchanged.
func applySecurity(up *dep.Update, d dep.Dep, s versioning.Scheme, names []string, vulns []Vuln) *dep.Update {
	cur := d.Version
	var fix string
	for _, v := range vulns {
		if m := minFix(s, cur, v); m != "" && (fix == "" || s.Compare(m, fix) > 0) {
			fix = m
		}
	}
	if fix == "" {
		return up
	}
	if up == nil || s.Compare(up.NewVersion, fix) < 0 {
		var chosen string
		for _, n := range names {
			if s.Compatible(cur, n) && s.Compare(n, fix) >= 0 && (chosen == "" || s.Compare(n, chosen) < 0) {
				chosen = n
			}
		}
		if chosen == "" {
			return up
		}
		// Best on the single compatible candidate renders it in cur's style (semver "v" prefix).
		chosen, _ = s.Best(cur, []string{chosen}, nil, nil)
		up = &dep.Update{Dep: d, Type: s.Kind(cur, chosen), NewVersion: chosen}
	}
	for _, v := range vulns {
		if m := minFix(s, cur, v); m != "" && s.Compare(up.NewVersion, m) >= 0 {
			up.Advisories = append(up.Advisories, v.Advisory)
		}
	}
	return up
}

// minFix returns v's lowest fixed version that is a compatible upgrade of cur, or "".
func minFix(s versioning.Scheme, cur string, v Vuln) string {
	var m string
	for _, f := range v.Fixed {
		if s.Compatible(cur, f) && (m == "" || s.Compare(f, m) < 0) {
			m = f
		}
	}
	return m
}
