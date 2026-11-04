// Package engine discovers, extracts, resolves and patches dependencies.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Aliases of the leaf datasource package, so implementations need not import the engine.
type (
	Release        = datasource.Release
	Datasource     = datasource.Datasource
	SecuritySource = datasource.SecuritySource
	Vuln           = datasource.Vuln
	ChecksumSource = datasource.ChecksumSource
)

// Options configures Resolve.
type Options struct {
	Managers    []manager.Manager
	Datasources map[string]Datasource // by dep.Dep.Datasource
	Security    SecuritySource        // nil = no security lookups
	Checksums   ChecksumSource        // nil = deps with a checksum source cannot be updated
	Filter      func(dep.Dep) bool    // nil = all; deps it rejects are dropped before any lookup
	Trace       func(Trace)           // nil = off; called sequentially from the Resolve goroutine
	Now         time.Time             // reference time of minimumReleaseAge; zero = time.Now()
}

// Trace explains the decision for one dep.
type Trace struct {
	Dep        dep.Dep
	Rules      []int // indices of matching rules of the pre-policy
	Policy     config.Policy
	Candidates int                 // releases returned by the datasource
	Rejected   map[string][]string // reason -> versions
	Updates    []TraceUpdate       // one per PR bucket, lowest version first; nil = none
	Err        error
}

// TraceUpdate is one update of a Trace with the policy of its type.
type TraceUpdate struct {
	Update dep.Update
	Policy *config.Policy // per-kind policy of Update.Type (updateTypes rules included)
}

// Result is the outcome of Resolve.
type Result struct {
	Updates []dep.Update
	Skipped []dep.Dep
	Errors  []error
	// Deps are all extracted deps (Skipped included, with Skip set) in discovery order: by
	// manager, then file and position; the dependency dashboard lists them.
	Deps []dep.Dep
	// Pending lists the deps with a candidate held back by minimumReleaseAge (the newest
	// one), in discovery order; the update chosen instead, if any, is in Updates.
	Pending []Pending
}

// Pending is the newest candidate of a dep that minimumReleaseAge held back.
type Pending struct {
	Dep     dep.Dep
	Version string
	Reason  string // "too new (released <date>, minimum <age>)"
}

// digestManagers support digest pinning (pin: digest, digest refresh).
var digestManagers = map[string]bool{"github-actions": true, "docker": true}

// digestCapable reports whether d's digest can be refreshed, or pinned when d has none:
// github-actions and docker deps whatever their versioning (a rule may override it), except
// setup-action version inputs, which are plain values. Custom deps can only refresh a digest
// they already carry (regex `digest` group or an annotated `@sha256:`); they have no place to
// insert a new one.
func digestCapable(d dep.Dep) bool {
	return digestManagers[d.Manager] && d.DepType != actions.DepTypeSetup || d.Manager == "custom" && d.Digest != ""
}

// tagOnly reports whether d is a docker image whose tag is not a version (latest, bookworm)
// but whose digest can be maintained: such deps get digest-only resolution.
func tagOnly(d dep.Dep) bool {
	s, ok := versioning.Get(d.Versioning)
	return ok && d.Datasource == "docker" && digestCapable(d) && !s.Valid(d.Version)
}

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
	linkAll(o.Managers, found, ex)

	res := &Result{}
	var todo []dep.Dep
	var pols []*policies
	kindRules := cfg.HasUpdateTypeRules()
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
			// The pre-policy comes first: a rule's versioning replaces the dep's scheme before
			// the current value is validated.
			var pol *policies
			if d.Skip == "" {
				pol = newPolicies(cfg, d, kindRules)
				if v := pol.pre.Versioning; v != "" {
					d.Versioning, pol.d.Versioning = v, v
				}
			}
			if s, ok := versioning.Get(d.Versioning); d.Skip == "" && ok && !s.Valid(d.Version) && !tagOnly(d) {
				d.Skip = "non-version value " + strconv.Quote(d.Version)
			}
			skip := func() {
				res.Skipped = append(res.Skipped, d)
				res.Deps = append(res.Deps, d)
				if o.Trace != nil {
					t := Trace{Dep: d}
					if pol != nil {
						t.Rules, t.Policy = pol.pre.Rules, pol.pre
					}
					o.Trace(t)
				}
			}
			if d.Skip != "" {
				skip()
				continue
			}
			if pol.pre.Ignore && !pol.anyKind() {
				d.Skip = pol.pre.IgnoreReason
				skip()
				continue
			}
			// A non-version tag without a digest can only be pinned.
			if d.Digest == "" && pol.pre.Pin != config.PinDigest && tagOnly(d) {
				d.Skip = "non-version tag"
				skip()
				continue
			}
			todo = append(todo, d)
			res.Deps = append(res.Deps, d)
			pols = append(pols, pol)
		}
	}
	slog.InfoContext(ctx, "dependencies extracted", "files", len(jobs), "updatable", len(todo), "skipped", len(res.Skipped), "duration", time.Since(start))
	start = time.Now()

	// Security updates bypass per-kind rules but not a pre-policy ignore: deps resolved only
	// because an updateTypes rule opted some update types in are not looked up. Non-version
	// tags have no version to look up.
	var secDeps []dep.Dep
	secIdx := make([]int, len(todo))
	for i, d := range todo {
		secIdx[i] = -1
		if !pols[i].pre.Ignore && !tagOnly(d) {
			secIdx[i] = len(secDeps)
			secDeps = append(secDeps, d)
		}
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	sec := lookupSecurity(ctx, o.Security, secDeps)
	outs := make([]Trace, len(todo))
	ups := make([][]dep.Update, len(todo))
	pend := make([]Pending, len(todo))
	var wg sync.WaitGroup
	for i := range todo {
		wg.Go(func() {
			d := todo[i]
			r := resolver{ctx: ctx, root: root, d: d, pol: pols[i], ds: o.Datasources[d.Datasource], cs: o.Checksums, trace: o.Trace != nil, now: now}
			t, u := r.run(func() []Vuln { return sec.of(secIdx[i]) })
			switch {
			case t.Err != nil:
				t.Err = fmt.Errorf("%s:%d: %s: %w", d.File, d.Line, d.Name, t.Err)
			case u == nil:
				slog.DebugContext(ctx, "dependency up to date", "file", d.File, "line", d.Line, "name", d.Name, "version", d.Version)
			default:
				for _, up := range u {
					slog.DebugContext(ctx, "update found", "file", d.File, "line", d.Line, "name", d.Name, "from", d.Version, "to", up.NewVersion, "type", up.Type, "bucket", up.Bucket)
				}
			}
			outs[i], ups[i], pend[i] = t, u, r.pending
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sec.wait(); err != nil {
		res.Errors = append(res.Errors, fmt.Errorf("security advisories: %w", err))
	}
	for i, t := range outs {
		if t.Err != nil {
			res.Errors = append(res.Errors, t.Err)
		}
		res.Updates = append(res.Updates, ups[i]...)
		if pend[i].Version != "" {
			res.Pending = append(res.Pending, pend[i])
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

// linkAll lets each manager.Linker resolve the cross-file references of its deps. found holds
// the files per manager; their jobs (and ex entries) are contiguous in manager order.
func linkAll(managers []manager.Manager, found [][]string, ex []extracted) {
	off := 0
	for i, m := range managers {
		part := ex[off : off+len(found[i])]
		off += len(found[i])
		l, ok := m.(manager.Linker)
		if !ok {
			continue
		}
		files := make([][]dep.Dep, 0, len(part))
		idx := make([]int, 0, len(part))
		for k, e := range part {
			if e.err == nil {
				files = append(files, e.deps)
				idx = append(idx, k)
			}
		}
		l.Link(files)
		for k, j := range idx {
			part[j].deps = files[k]
		}
	}
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

// of returns the vulnerabilities of dep i, or nil when the lookup failed or is off, or i < 0
// (the dep was not looked up).
func (s *security) of(i int) []Vuln {
	if s == nil || i < 0 || s.wait() != nil {
		return nil
	}
	return s.vulns[i]
}

// policies holds the pre-policy of one dep and its per-kind policies, evaluated lazily. It is
// used by one goroutine at a time.
type policies struct {
	cfg       *config.Config
	d         dep.Dep
	pre       config.Policy
	kindRules bool // cfg has updateTypes rules; without them every kind policy is pre
	kinds     map[dep.UpdateType]*config.Policy
}

func newPolicies(cfg *config.Config, d dep.Dep, kindRules bool) *policies {
	return &policies{cfg: cfg, d: d, pre: cfg.Policy(d), kindRules: kindRules}
}

// of returns the policy of an update of kind.
func (p *policies) of(kind dep.UpdateType) *config.Policy {
	if !p.kindRules {
		return &p.pre
	}
	if kp, ok := p.kinds[kind]; ok {
		return kp
	}
	if p.kinds == nil {
		p.kinds = make(map[dep.UpdateType]*config.Policy, len(dep.UpdateTypes))
	}
	kp := p.cfg.PolicyFor(p.d, kind)
	p.kinds[kind] = &kp
	return &kp
}

// anyKind reports whether an update of some kind is not ignored.
func (p *policies) anyKind() bool {
	return slices.ContainsFunc(dep.UpdateTypes, func(k dep.UpdateType) bool { return !p.of(k).Ignore })
}

// check is the acceptance of a candidate of kind to version v under the kind's policy: an
// ignoring per-kind policy rejects with its ignore reason, otherwise Policy.Check decides.
// semverOf (the scheme's Semver) is only called when an allow constraint needs it: the
// conversion can be regexp-based and candidates number in the hundreds per dependency.
func (p *policies) check(kind dep.UpdateType, v string, semverOf func(string) *semver.Version) (bool, string) {
	kp := p.of(kind)
	if kp.Ignore {
		return false, kp.IgnoreReason
	}
	return kp.Check(kind, v, semverOf)
}

// bucketKey identifies a PR bucket without allocating: name is "", "major", "minor", "patch"
// or "v" (one bucket per major, with major set).
type bucketKey struct{ name, major string }

// label renders the bucket as dep.Update.Bucket.
func (k bucketKey) label() string {
	if k.name == "v" {
		return "v" + k.major
	}
	return k.name
}

// bucket returns the PR bucket of candidate c under the policy of its kind (Renovate's
// getBucket): without separateMajorMinor every candidate shares one bucket; otherwise majors
// go to "major" (or "v<N>" per major with separateMultipleMajor) and minors and patches share
// the non-major bucket "", or get "minor" and "patch" with separateMinorPatch.
func (p *policies) bucket(c versioning.Candidate) bucketKey {
	kp := p.of(c.Kind)
	switch {
	case !kp.SeparateMajorMinor:
		return bucketKey{}
	case c.Kind == dep.Major && kp.SeparateMultipleMajor:
		return bucketKey{name: "v", major: c.Major}
	case c.Kind == dep.Major:
		return bucketKey{name: "major"}
	case kp.SeparateMinorPatch:
		return bucketKey{name: string(c.Kind)}
	}
	return bucketKey{}
}

// singleBucket reports whether every candidate lands in the same bucket, so the first
// accepted candidate is the only update.
func (p *policies) singleBucket() bool { return !p.kindRules && !p.pre.SeparateMajorMinor }

// allows reports whether updates of kind are enabled (digest refresh and pin, which have
// no new version for allow).
func (p *policies) allows(kind dep.UpdateType) bool {
	kp := p.of(kind)
	return !kp.Ignore && kp.Updates[kind]
}

// resolver decides the update of one dep.
type resolver struct {
	ctx   context.Context
	root  string
	d     dep.Dep
	pol   *policies
	ds    Datasource
	cs    ChecksumSource
	trace bool
	now   time.Time

	rels    []Release
	times   map[string]time.Time // release times asked so far; zero = unknown
	pending Pending              // newest candidate held back by minimumReleaseAge
}

// run resolves in order: datasource → candidates ranked under policy, one per PR bucket, with
// the release-time guards → digest pin/refresh → security. vulns blocks until the advisories
// of this dep are known. Trace.Updates is only filled when tracing.
func (r *resolver) run(vulns func() []Vuln) (Trace, []dep.Update) {
	t := Trace{Dep: r.d, Rules: r.pol.pre.Rules, Policy: r.pol.pre}
	ups, err := r.resolve(&t, vulns)
	t.Err = err
	if r.trace && len(ups) > 0 {
		t.Updates = make([]TraceUpdate, len(ups))
		for i, u := range ups {
			t.Updates[i] = TraceUpdate{Update: u, Policy: r.pol.of(u.Type)}
		}
	}
	return t, ups
}

func (r *resolver) resolve(t *Trace, vulns func() []Vuln) ([]dep.Update, error) {
	d := r.d
	scheme, ok := versioning.Get(d.Versioning)
	if !ok {
		return nil, fmt.Errorf("unknown versioning %q", d.Versioning)
	}
	if r.ds == nil {
		return nil, fmt.Errorf("no datasource %q", d.Datasource)
	}
	var ups []dep.Update
	var err error
	if !scheme.Valid(d.Version) {
		// Resolve only lets non-version docker tags through (tagOnly): their digest is kept
		// current; there are no releases to compare and no advisories to look up.
		ups, err = r.digestUpdate()
	} else {
		ups, err = r.versionUpdates(t, scheme, vulns)
	}
	if err != nil {
		return nil, err
	}
	for i := range ups {
		// Commit, grouping and automerge settings come from the policy of the update's type;
		// security updates follow it too, apart from group and schedule.
		up := &ups[i]
		kp := r.pol.of(up.Type)
		up.CommitType, up.CommitScope = kp.CommitType, kp.CommitScope
		up.Automerge, up.AutomergeStrategy = kp.Automerge, kp.AutomergeStrategy
		if len(up.Advisories) == 0 {
			up.Group, up.Schedule = kp.Group, kp.Schedule
		}
	}
	return ups, nil
}

// versionUpdates resolves a dep with a valid version: the best candidate under policy per PR
// bucket, else the digest refresh or pin of the current version, then security, and the
// digest and checksum of every resulting version. Updates are ordered by version, lowest
// first.
func (r *resolver) versionUpdates(t *Trace, scheme versioning.Scheme, vulns func() []Vuln) ([]dep.Update, error) {
	d := r.d
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
	semverOf := scheme.Semver
	accept := func(v string, kind dep.UpdateType) (bool, string) { return r.pol.check(kind, v, semverOf) }

	digests := digestCapable(d)
	wantDigest := digests && (d.Digest != "" || r.pol.pre.Pin == config.PinDigest)

	ups := r.picks(scheme, names, accept, reject)
	if len(ups) == 0 && digests {
		if ups, err = r.digestUpdate(); err != nil {
			return nil, err
		}
	}

	ups = r.applySecurity(ups, scheme, names, vulns())

	for i := range ups {
		up := &ups[i]
		if up.Type != dep.Digest && up.Type != dep.Pin && wantDigest {
			if up.NewDigest, err = r.digestOf(up.NewVersion); err != nil {
				return nil, err
			}
		}
		if d.ChecksumSource != nil && up.NewVersion != d.Version {
			if up.NewChecksum, err = r.checksumOf(up.NewVersion); err != nil {
				return nil, err
			}
		}
	}
	return ups, nil
}

// digestUpdate keeps the current version and refreshes its digest when pinned (Type digest,
// none when unchanged), or pins it under pin: digest (Type pin). It has no new version, so
// allow and minimumReleaseAge do not apply.
func (r *resolver) digestUpdate() ([]dep.Update, error) {
	d := r.d
	switch {
	case d.Digest != "" && r.pol.allows(dep.Digest):
		c, err := r.digestOf(d.Version)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(c, d.Digest) {
			return []dep.Update{{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: c}}, nil
		}
	case d.Digest == "" && r.pol.pre.Pin == config.PinDigest && r.pol.allows(dep.Pin):
		c, err := r.digestOf(d.Version)
		if err != nil {
			return nil, err
		}
		return []dep.Update{{Dep: d, Type: dep.Pin, NewVersion: d.Version, NewDigest: c}}, nil
	}
	return nil, nil
}

// Bounds of the retries per PR bucket: stale major candidates skipped before giving up, and
// candidates rejected in total (stale or too new).
const (
	maxOlderMajors = 3
	maxRetries     = 50
)

// picks ranks the candidates once and chooses at most one per PR bucket (policies.bucket):
// the best one of the bucket that passes two release-time guards, each falling back to the
// bucket's next best candidate:
//   - a major update whose release predates the current version is a legacy tag
//     (k8s.io/client-go v1.5.2 from 2016 versus v0.37.1), not an upgrade;
//   - a candidate released less than the minimumReleaseAge of its update type ago is too new.
//
// Release times are only asked when a guard applies and the datasource has them, at most
// once per version; unknown times (zero or an error) never reject. The updates are returned
// lowest version first.
func (r *resolver) picks(scheme versioning.Scheme, names []string, accept func(string, dep.UpdateType) (bool, string), reject func(v, reason string)) []dep.Update {
	cur := r.d.Version
	timer, _ := r.ds.(datasource.Timer)
	single := r.pol.singleBucket()
	if timer == nil && single {
		// No guard can apply and there is one bucket: one O(n) pass without ranking.
		best, ok := scheme.Best(cur, names, accept, reject)
		if !ok {
			return nil
		}
		return []dep.Update{{Dep: r.d, Type: scheme.Kind(cur, best), NewVersion: best}}
	}
	ranked := scheme.Rank(cur, names, accept, reject) // parsed and filtered once
	type state struct {
		key              bucketKey
		tries, older     int
		prefetched, done bool
	}
	states := make([]state, 0, 4) // a handful of buckets: a linear scan beats a map
	var out []dep.Update
	for i := range ranked.Len() {
		c := ranked.At(i)
		key := r.pol.bucket(c)
		j := slices.IndexFunc(states, func(s state) bool { return s.key == key })
		if j < 0 {
			j = len(states)
			states = append(states, state{key: key})
		}
		st := &states[j]
		if st.done {
			continue
		}
		if st.tries == maxRetries {
			st.done = true
			continue
		}
		st.tries++
		var reason string
		if timer != nil {
			reason = r.guard(timer, c)
		}
		if reason == "" {
			out = append(out, dep.Update{Dep: r.d, Type: c.Kind, NewVersion: c.Version, Bucket: key.label()})
			st.done = true
			if single {
				break
			}
			continue
		}
		if reject != nil {
			reject(c.Version, reason)
		}
		if reason != reasonOlder && r.pending.Version == "" {
			r.pending = Pending{Dep: r.d, Version: c.Version, Reason: reason}
		}
		if reason == reasonOlder {
			st.older++
			if st.older == maxOlderMajors {
				st.done = true
				continue
			}
		}
		if !st.prefetched {
			// One rejection suggests a frequently released dependency: fetch the bucket's next
			// candidates' times concurrently instead of one round trip per candidate.
			st.prefetched = true
			limit := min(prefetchWindow, maxRetries-st.tries)
			next := make([]string, 0, limit)
			for n := i + 1; n < ranked.Len() && len(next) < limit; n++ {
				if nc := ranked.At(n); r.pol.bucket(nc) == key {
					next = append(next, nc.Version)
				}
			}
			r.prefetchTimes(timer, next)
		}
	}
	slices.Reverse(out)
	return out
}

// reasonOlder rejects a major candidate released before the current version.
const reasonOlder = "older than current"

// guard returns why candidate c fails a release-time guard, or "".
func (r *resolver) guard(timer datasource.Timer, c versioning.Candidate) string {
	if c.Kind == dep.Major && r.olderThanCurrent(timer, c.Version) {
		slog.DebugContext(r.ctx, "major candidate older than current", "name", r.d.Name, "current", r.d.Version, "candidate", c.Version)
		return reasonOlder
	}
	age := r.pol.of(c.Kind).MinAge
	if age <= 0 {
		return ""
	}
	tb := r.releaseTime(timer, c.Version)
	switch {
	case tb.IsZero():
		slog.DebugContext(r.ctx, "release time unknown, minimum release age not enforced", "name", r.d.Name, "candidate", c.Version)
	case r.now.Sub(tb) < age:
		slog.DebugContext(r.ctx, "candidate too new", "name", r.d.Name, "candidate", c.Version, "released", tb, "minimumReleaseAge", age)
		return fmt.Sprintf("too new (released %s, minimum %s)", tb.UTC().Format(time.DateOnly), config.FormatAge(age))
	}
	return ""
}

// prefetchWindow is how many release times picks fetches concurrently once a candidate of a
// bucket was rejected; the shared semaphore still bounds the requests.
const prefetchWindow = 8

// prefetchTimes fills the release-time cache for versions concurrently.
func (r *resolver) prefetchTimes(timer datasource.Timer, versions []string) {
	times := make([]time.Time, len(versions))
	var wg sync.WaitGroup
	for i, v := range versions {
		if _, ok := r.times[v]; ok {
			continue
		}
		wg.Go(func() {
			t, err := timer.ReleaseTime(r.ctx, r.d, v)
			if err != nil {
				slog.DebugContext(r.ctx, "release time lookup failed", "name", r.d.Name, "version", v, "err", err)
				t = time.Time{}
			}
			times[i] = t
		})
	}
	wg.Wait()
	if r.times == nil {
		r.times = make(map[string]time.Time, len(versions))
	}
	for i, v := range versions {
		if _, ok := r.times[v]; !ok {
			r.times[v] = times[i]
		}
	}
}

// olderThanCurrent reports whether candidate was released before the current version; false
// when either time is unknown.
func (r *resolver) olderThanCurrent(timer datasource.Timer, candidate string) bool {
	tb := r.releaseTime(timer, candidate)
	if tb.IsZero() {
		return false
	}
	tc := r.releaseTime(timer, r.d.Version)
	return !tc.IsZero() && tb.Before(tc)
}

// releaseTime returns when version was released, asking the datasource once per version;
// zero when unknown (errors are logged and treated as unknown).
func (r *resolver) releaseTime(timer datasource.Timer, version string) time.Time {
	if t, ok := r.times[version]; ok {
		return t
	}
	t, err := timer.ReleaseTime(r.ctx, r.d, version)
	if err != nil {
		slog.DebugContext(r.ctx, "release time lookup failed", "name", r.d.Name, "version", version, "err", err)
		t = time.Time{}
	}
	if r.times == nil {
		r.times = make(map[string]time.Time, 2)
	}
	r.times[version] = t
	return t
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

// checksumOf fetches the checksum of version, in the letter case and length of the current one.
func (r *resolver) checksumOf(version string) (string, error) {
	if r.cs == nil {
		return "", errors.New("no checksum source")
	}
	c, err := r.cs.Checksum(r.ctx, r.d, version)
	if err != nil {
		return "", fmt.Errorf("checksum of %s: %w", version, err)
	}
	if len(c) != len(r.d.Checksum) {
		return "", fmt.Errorf("checksum of %s: checksum length mismatch: have %d hex digits, got %d", version, len(r.d.Checksum), len(c))
	}
	if strings.ToUpper(r.d.Checksum) == r.d.Checksum && strings.ToLower(r.d.Checksum) != r.d.Checksum {
		return strings.ToUpper(c), nil
	}
	return strings.ToLower(c), nil
}

// applySecurity raises the updates of d to a fixing version when d has advisories. The
// minimum fix F is the highest, over all vulns, of each vuln's lowest compatible fixed
// version. The lowest update at or above F becomes the security update. If there is none, the
// smallest compatible release ≥ F is chosen regardless of policy: it replaces the update of
// its PR bucket (which is below F) and any digest or pin update. The advisories whose fix the
// resulting version includes are attached to that one update. Vulns without a compatible fix,
// or a missing fixing release, leave ups unchanged. ups and the result are ordered by version.
func (r *resolver) applySecurity(ups []dep.Update, s versioning.Scheme, names []string, vulns []Vuln) []dep.Update {
	d := r.d
	cur := d.Version
	var fix string
	for _, v := range vulns {
		if m := minFix(s, cur, v); m != "" && (fix == "" || s.Compare(m, fix) > 0) {
			fix = m
		}
	}
	if fix == "" {
		return ups
	}
	sec := slices.IndexFunc(ups, func(u dep.Update) bool {
		return u.Type != dep.Digest && u.Type != dep.Pin && s.Compare(u.NewVersion, fix) >= 0
	})
	if sec < 0 {
		var chosen string
		for _, n := range names {
			if s.Compatible(cur, n) && s.Compare(n, fix) >= 0 && (chosen == "" || s.Compare(n, chosen) < 0) {
				chosen = n
			}
		}
		if chosen == "" {
			return ups
		}
		// Ranking the single compatible candidate gives its kind and major and renders it in
		// cur's style (semver "v" prefix).
		c := s.Rank(cur, []string{chosen}, nil, nil).At(0)
		bucket := r.pol.bucket(c).label()
		ups = slices.DeleteFunc(ups, func(u dep.Update) bool {
			return u.Bucket == bucket || u.Type == dep.Digest || u.Type == dep.Pin
		})
		sec, _ = slices.BinarySearchFunc(ups, c.Version, func(u dep.Update, v string) int { return s.Compare(u.NewVersion, v) })
		ups = slices.Insert(ups, sec, dep.Update{Dep: d, Type: c.Kind, NewVersion: c.Version, Bucket: bucket})
	}
	up := &ups[sec]
	for _, v := range vulns {
		if m := minFix(s, cur, v); m != "" && s.Compare(up.NewVersion, m) >= 0 {
			up.Advisories = append(up.Advisories, v.Advisory)
		}
	}
	return ups
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
