// Package engine discovers, extracts, resolves and patches dependencies.
package engine

import (
	"cmp"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/beam"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/clojure"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/composer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/depsconfig"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/devcontainer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gitsubmodules"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/jsonnetbundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/kustomize"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/precommit"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/rusttoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Aliases of the leaf datasource package, so implementations need not import the engine.
type (
	Release        = datasource.Release
	Datasource     = datasource.Datasource
	SecuritySource = datasource.SecuritySource
	Vuln           = datasource.Vuln
	ChecksumSource = datasource.ChecksumSource
)

// DepError is the lookup error of one dependency, located at its file and line.
type DepError struct {
	File string
	Line int
	Name string
	Err  error
}

func (e *DepError) Error() string {
	return e.File + ":" + strconv.Itoa(e.Line) + ": " + e.Name + ": " + e.Err.Error()
}

func (e *DepError) Unwrap() error { return e.Err }

// RateLimited reports whether err is a DepError whose lookup failed on an exhausted rate limit
// (httpx.ErrRateLimited). The commands report such a dep as a warning that does not fail them;
// it stays in Result.Errors, so the bot treats the dep as unresolved like any failed lookup
// (it keeps the dep's open PRs and branches).
func RateLimited(err error) bool {
	de, ok := errors.AsType[*DepError](err)
	return ok && errors.Is(de.Err, httpx.ErrRateLimited)
}

// FileError is the extraction error of one dependency file.
type FileError struct {
	File string
	Err  error
}

func (e *FileError) Error() string { return e.File + ": " + e.Err.Error() }

func (e *FileError) Unwrap() error { return e.Err }

// Options configures Resolve.
type Options struct {
	Managers    []manager.Manager
	Datasources map[string]Datasource // by dep.Dep.Datasource
	Security    SecuritySource        // nil = no security lookups
	Checksums   ChecksumSource        // nil = deps with a checksum source cannot be updated
	Filter      func(dep.Dep) bool    // nil = all; deps it rejects are dropped before any lookup
	Trace       func(Trace)           // nil = off; called sequentially from the Resolve goroutine
	Now         time.Time             // reference time of minimumReleaseAge; zero = time.Now()
	Progress    Progress              // nil = off; discover, extract and resolve phases, rate-limit waits
	// Concurrency is the per-upstream request limit of the datasources (cmd/deps
	// --concurrency); it bounds how many deps are resolved at a time (resolveSlots). 0 = 32.
	Concurrency int
}

// Trace explains the decision for one dep.
type Trace struct {
	Dep        dep.Dep
	Rules      []int // indices of matching rules of the pre-policy
	Policy     config.Policy
	Candidates int                 // releases returned by the datasource
	Rejected   map[string][]string // reason -> versions
	Updates    []TraceUpdate       // one per PR bucket, lowest version first; nil = none
	// AgeLookup reports that a currentAge rule made the resolver look up the release time of
	// the current version: Released, zero when the datasource does not know it.
	AgeLookup bool
	Released  time.Time
	Err       error
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
	// Warnings are the deprecated and abandoned deps (Warning), in discovery order.
	Warnings []Warning
}

// Pending is the newest candidate of a dep that minimumReleaseAge held back.
type Pending struct {
	Dep     dep.Dep
	Version string
	Reason  string // "too new (released <date>, minimum <age>)" or "release time unknown (minimum <age>, timestamp-required)"
}

// digestManagers support digest pinning (pin: digest, digest refresh).
var digestManagers = map[string]bool{"github-actions": true, "docker": true, precommit.Name: true, gitsubmodules.Name: true, vcpkg.Name: true}

// digestCapable reports whether d's digest can be refreshed, or pinned when d has none:
// github-actions, docker images (not the packages a Dockerfile RUN installs), pre-commit hook
// repositories and git-submodules deps whatever their versioning (a rule may override it),
// except setup-action version inputs and runner labels,
// which are plain values, and pre-commit additional_dependencies (npm, PyPI, Go); vcpkg registry
// baselines (git-refs); npm git dependencies pinned to a commit (git-refs); images of
// gitlabci, terraform (docker and kubernetes resources), ansible tasks and the CI pipeline managers, which are
// edited like docker refs, the devcontainer image, and kustomize images with a newTag (pinned
// as `newTag: 1.2@sha256:…`); gitlabci includes, remote includes and
// components pinned to a commit, or with room for the version comment pinning writes. Custom, bazel-module, nix,
// deps-edn, deps-config and jsonnet-bundler deps, other kustomize deps and devcontainer features (never pinned, as Renovate's pinDigests: false) can only
// refresh a digest they already carry (regex `digest` group, an annotated `@sha256:`, a
// kustomize `digest` field or commit ref, an `oci.pull` digest, a git override commit, the commit a flake
// input or jsonnet-bundler dependency is locked or pinned to, the commit an extends entry is pinned to, `feature:1@sha256:…`, a git lib's `:git/sha`); they have no place to insert a new one.
// Neither have terraform and terragrunt module sources: a commit-pinned git source
// (`?ref=<sha>`, git-refs) refreshes its commit, an OCI source (`?tag=`) is never pinned.
func digestCapable(d dep.Dep) bool {
	switch d.Manager {
	case "custom", "cargo", bazel.Name, nix.Name, clojure.DepsEdnName, depsconfig.Name, jsonnetbundler.Name:
		return d.Digest != ""
	case kustomize.Name:
		return d.Digest != "" || kustomize.Pinnable(d)
	case "gitlabci":
		return d.Datasource == "docker" || d.Digest != "" || d.InsertAt > 0
	case "terraform", "terragrunt":
		if d.DepType == "module" || d.Manager == "terragrunt" {
			return d.Datasource == "git-refs" && d.Digest != ""
		}
		return d.Datasource == "docker"
	case "docker", "bitbucket-pipelines", "woodpecker", "crow", "droneci", "circleci", "azure-pipelines", "cloudbuild", "velaci", "buildkite", "ansible", "quadlet", "crossplane", "tekton":
		return d.Datasource == "docker"
	case devcontainer.Name:
		return d.DepType == devcontainer.DepTypeImage || d.DepType == devcontainer.DepTypeFeature && d.Digest != ""
	case vcpkg.Name, npmManager:
		return d.Datasource == "git-refs"
	case precommit.Name:
		return d.DepType == precommit.DepTypeRepository
	}
	return digestManagers[d.Manager] && d.DepType != actions.DepTypeSetup && d.DepType != actions.DepTypeRunner
}

// tagOnly reports whether d's version is not a version but a ref whose digest can be
// maintained: a docker tag (latest, bookworm), the branch a git submodule, a vcpkg registry
// baseline or a git-refs dep (a Bazel git override commit) tracks, or the tag a frozen
// pre-commit rev names (`# frozen: nightly`). Such deps get digest-only resolution. A kustomize
// image without newTag is one even without a digest: it can still be replaced.
func tagOnly(d dep.Dep) bool {
	s, ok := versioning.Get(d.Versioning)
	frozenTag := d.Manager == precommit.Name && d.Digest != "" && d.Version != ""
	return ok && (d.Datasource == "docker" || d.Datasource == "git-refs" || d.Manager == gitsubmodules.Name || d.Manager == vcpkg.Name || frozenTag) &&
		(digestCapable(d) || kustomize.Untagged(d)) && !s.Valid(d.Version)
}

type job struct {
	file string
	m    manager.Manager
}

type extracted struct {
	deps []dep.Dep
	err  error
}

// tree is the extraction of a repository: the files per manager and their deps.
type tree struct {
	jobs     []job
	found    [][]string // per manager in Options.Managers order
	ex       []extracted
	lockErrs []error
}

// extractTree discovers the dependency files under root, extracts them, links the deps
// across files, reads locked versions and drops deps a custom dep covers. The extract phase
// of prog is left running: the caller ends it after its own summary record, so a log group
// of the phase holds it.
func extractTree(ctx context.Context, root string, cfg *config.Config, env manager.Env, managers []manager.Manager, prog Progress) (*tree, error) {
	start := time.Now()
	prog.Start(PhaseDiscover, 0)
	found, err := discover(root, cfg.IgnorePaths, cfg.GitIgnoreEnabled(), managers)
	if err != nil {
		prog.End(PhaseDiscover)
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	slog.InfoContext(ctx, "files discovered", discoveryAttrs(managers, found, time.Since(start))...)
	prog.End(PhaseDiscover)
	var jobs []job
	for i, m := range managers {
		for _, f := range found[i] {
			jobs = append(jobs, job{f, m})
		}
	}
	if env.Exists == nil {
		env.Exists = func(p string) bool {
			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
			return err == nil
		}
	}
	if env.ReadFile == nil {
		env.ReadFile = func(p string) ([]byte, error) {
			return os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		}
	}
	if env.Gitlinks == nil {
		env.Gitlinks = gitsubmodules.IndexGitlinks(root)
	}
	if env.Superproject == nil {
		env.Superproject = gitsubmodules.GitSuperproject(root)
	}

	prog.Start(PhaseExtract, len(jobs))
	ex := extractAll(root, jobs, env, prog)
	linkAll(managers, found, ex)
	lockErrs := readLocks(root, managers, found, ex)
	dedupeCovered(ex)
	return &tree{jobs: jobs, found: found, ex: ex, lockErrs: lockErrs}, nil
}

// Extract returns every dependency under root as extracted (Skip set by the managers only;
// no policy, no lookups), in discovery order, with the per-file errors.
func Extract(ctx context.Context, root string, cfg *config.Config, env manager.Env, managers []manager.Manager) ([]dep.Dep, []error, error) {
	prog := noProgress{}
	t, err := extractTree(ctx, root, cfg, env, managers, prog)
	if err != nil {
		return nil, nil, err
	}
	prog.End(PhaseExtract)
	errs := t.lockErrs
	var deps []dep.Dep
	for i, e := range t.ex {
		if e.err != nil {
			errs = append(errs, &FileError{File: t.jobs[i].file, Err: e.err})
			continue
		}
		deps = append(deps, e.deps...)
	}
	return deps, errs, nil
}

// Resolve discovers dependency files under root, extracts dependencies and looks up updates.
// The returned error is non-nil only for walk failures or context cancellation;
// per-file and per-dependency failures are collected in Result.Errors.
func Resolve(ctx context.Context, root string, cfg *config.Config, env manager.Env, o Options) (*Result, error) {
	start := time.Now()
	ctx, prog := WithProgress(ctx, o.Progress)
	t, err := extractTree(ctx, root, cfg, env, o.Managers, prog)
	if err != nil {
		return nil, err
	}
	jobs, found, ex := t.jobs, t.found, t.ex

	res := &Result{Errors: t.lockErrs}
	if cfg.LockFileMaintenance.Enabled && o.Filter == nil {
		res.Updates = maintenanceUpdates(cfg, root, o.Managers, found, ex)
	}
	// The lock file maintenance updates come first; the resolved updates follow them, so
	// res.Updates[maint:] is what upPols below is indexed like.
	maint := len(res.Updates)
	// Dep, Update and Pending are hundreds of bytes and append grows large slices by a
	// quarter: the slices below are sized up front, counting first where the length is only
	// known after a pass.
	n := 0
	for _, e := range ex {
		n += len(e.deps)
	}
	var allPols []*policies // the policies of res.Deps[i]
	if n > 0 {
		res.Deps = make([]dep.Dep, 0, n)
		allPols = make([]*policies, 0, n)
	}
	skipped := 0
	kindRules := cfg.HasUpdateTypeRules()
	lister, _ := prog.(Lister)
	var files []FileReport    // with a lister
	var fileAt map[string]int // index of a file in files
	if lister != nil {
		fileAt = make(map[string]int, len(jobs))
	}
	for i, e := range ex {
		var f *FileReport
		if lister != nil {
			at, ok := fileAt[jobs[i].file]
			if !ok {
				at = len(files)
				fileAt[jobs[i].file] = at
				files = append(files, FileReport{File: jobs[i].file})
			}
			f = &files[at]
			if name := jobs[i].m.Name(); !slices.Contains(f.Managers, name) {
				f.Managers = append(f.Managers, name)
			}
		}
		if e.err != nil {
			res.Errors = append(res.Errors, &FileError{File: jobs[i].file, Err: e.err})
			if f != nil && f.Err == nil {
				f.Err = e.err
			}
			continue
		}
		slog.DebugContext(ctx, "file extracted", "file", jobs[i].file, "manager", jobs[i].m.Name(), "deps", len(e.deps))
		for _, d := range e.deps {
			if o.Filter != nil && !o.Filter(d) {
				continue
			}
			_, timed := o.Datasources[d.Datasource].(datasource.Timer)
			d, pol := classify(cfg, d, kindRules, timed, o.Security != nil)
			res.Deps = append(res.Deps, d)
			allPols = append(allPols, pol)
			if f != nil {
				f.Deps++
			}
			if d.Skip != "" {
				skipped++
				if f != nil {
					f.Skipped++
				}
				if o.Trace != nil {
					t := Trace{Dep: d}
					if pol != nil {
						t.Rules, t.Policy = pol.pre.Rules, pol.pre
					}
					o.Trace(t)
				}
			}
		}
	}
	var todo []dep.Dep
	var pols []*policies
	var depAt []int // index of todo[i] in res.Deps
	if skipped > 0 {
		res.Skipped = make([]dep.Dep, 0, skipped)
	}
	if m := len(res.Deps) - skipped; m > 0 {
		todo, pols, depAt = make([]dep.Dep, 0, m), make([]*policies, 0, m), make([]int, 0, m)
	}
	for i := range res.Deps {
		if d := &res.Deps[i]; d.Skip != "" {
			res.Skipped = append(res.Skipped, *d)
		} else {
			todo = append(todo, *d)
			pols = append(pols, allPols[i])
			depAt = append(depAt, i)
		}
	}
	slog.InfoContext(ctx, "dependencies extracted", "files", len(jobs), "updatable", len(todo), "skipped", len(res.Skipped), "duration", time.Since(start))
	if lister != nil {
		slices.SortFunc(files, func(a, b FileReport) int { return strings.Compare(a.File, b.File) })
		lister.ListFiles(files)
	}
	prog.End(PhaseExtract)
	start = time.Now()
	planAll(o.Datasources, todo)

	// Security updates bypass per-kind rules but not a pre-policy ignore: deps resolved only
	// because an updateTypes rule opted some update types in are not looked up. Deps whose
	// pre-policy waits for currentAge are (applySecurity drops them if it ignores them).
	// Non-version tags have no version to look up, nor do bare commit pins before resolution.
	// Deps ignored by default whose security updates still apply (securityByDefault) are
	// looked up; they are resolved only when affected.
	secIdx := make([]int, len(todo))
	nSec := 0
	for i, d := range todo {
		secIdx[i] = -1
		if (!pols[i].pre.Ignore || pols[i].aged || pols[i].securityOnly) && !tagOnly(d) && !config.UnversionedPin(d) {
			secIdx[i] = nSec
			nSec++
		}
	}
	var secDeps []dep.Dep
	if nSec > 0 {
		secDeps = make([]dep.Dep, 0, nSec)
		for i, d := range todo {
			if secIdx[i] >= 0 {
				secDeps = append(secDeps, d)
			}
		}
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	prog.Start(PhaseResolve, len(todo))
	sec := lookupSecurity(ctx, o.Security, secDeps)
	outs := make([]Trace, len(todo))
	ups := make([][]dep.Update, len(todo))
	pend := make([]*Pending, len(todo)) // nil = none held back
	warns := make([][]Warning, len(todo))
	var wg sync.WaitGroup
	rewriters := map[string]manager.RangeRewriter{}
	rangers := map[string]manager.RangeUpdater{}
	renamers := map[string]manager.Renamer{}
	for _, m := range o.Managers {
		if rw, ok := m.(manager.RangeRewriter); ok {
			rewriters[m.Name()] = rw
		}
		if ru, ok := m.(manager.RangeUpdater); ok {
			rangers[m.Name()] = ru
		}
		if rn, ok := m.(manager.Renamer); ok {
			renamers[m.Name()] = rn
		}
	}
	// A vcpkg port waits for the registry baseline it is read at (dep.Dep.Baseline): when the
	// baseline moves, the port is looked up at the new commit (portAtBaseline).
	baselineOf := vcpkgBaselines(todo)
	var resolved []chan struct{}
	if len(baselineOf) > 0 {
		resolved = make([]chan struct{}, len(todo))
		for _, j := range baselineOf {
			resolved[j] = make(chan struct{})
		}
	}
	names := newReleaseNames()
	defer names.done()
	// resolveOne fills t: Trace is large, and copies of it on every per-dep goroutine stack
	// would grow those stacks. free gives up the dep's slot before it waits for the
	// advisories.
	resolveOne := func(i int, d dep.Dep, t *Trace, free func()) ([]dep.Update, *resolver) {
		r := &resolver{ctx: ctx, root: root, d: d, cur: d.Version, pol: pols[i], ds: o.Datasources[d.Datasource], cs: o.Checksums, trace: o.Trace != nil, now: now,
			rewriter: rewriters[d.Manager], ranger: rangers[d.Manager], renamer: renamers[d.Manager], names: names, skipGomodPaths: env.SkipGomodImportPaths}
		u := r.run(t, func() []Vuln {
			if !sec.ready() {
				free()
			}
			return sec.of(secIdx[i])
		})
		return u, r
	}
	resolveAt := func(i int, free func()) {
		if len(resolved) > 0 && resolved[i] != nil {
			defer close(resolved[i])
		}
		d := todo[i]
		t := &outs[i]
		// Every dep counted in the phase total reports once, the securityOnly deps returning
		// below included: the phase would otherwise never reach its total.
		defer func() { prog.Done(PhaseResolve, t.Err) }()
		if pols[i].securityOnly {
			if !sec.ready() {
				free()
			}
			if len(sec.of(secIdx[i])) == 0 {
				t.Dep, t.Rules, t.Policy = d, pols[i].pre.Rules, pols[i].pre
				t.Dep.Skip = pols[i].pre.IgnoreReason
				return
			}
		}
		var u []dep.Update
		var r *resolver
		if j, ok := baselineOf[d.Baseline]; ok {
			free()
			<-resolved[j]
			u, r = portAtBaseline(d, ups[j], func(d dep.Dep) ([]dep.Update, *resolver) { return resolveOne(i, d, t, free) })
		} else {
			u, r = resolveOne(i, d, t, free)
		}
		if pols[i].securityOnly && t.Err == nil && u == nil {
			t.Dep.Skip = pols[i].pre.IgnoreReason // no fix for its advisories
		}
		var skip *dep.SkipError
		switch {
		case errors.As(t.Err, &skip):
			t.Err, t.Dep.Skip = nil, skip.Reason
		case t.Err != nil:
			t.Err = &DepError{File: d.File, Line: d.Line, Name: d.Name, Err: t.Err}
		case u == nil:
			slog.DebugContext(ctx, "dependency up to date", "file", d.File, "line", d.Line, "name", d.Name, "version", d.Version)
		default:
			for _, up := range u {
				slog.DebugContext(ctx, "update found", "file", d.File, "line", d.Line, "name", d.Name, "from", d.Version, "to", up.NewVersion, "type", up.Type, "bucket", up.Bucket)
			}
		}
		ups[i], warns[i] = u, r.warnings
		if r.pending.Version != "" {
			p := r.pending
			pend[i] = &p
		}
	}
	// Each dep is resolved on a goroutine of its own, which mostly waits for its lookups. At
	// most resolveSlots deps of one datasource hold a slot (and a goroutine) at a time:
	// thousands of waiting goroutines (a JavaScript monorepo) cost tens of MB of stack. Slots
	// are per datasource, so deps waiting for a busy or rate-limited upstream do not hold up
	// the lookups of the others. A dep gives up its slot before it waits for the result of
	// another lookup (the advisories, its registry baseline), which may need the slot, and
	// after slotHold: the deps of a package whose lookup stalls would otherwise take every
	// slot, holding up the lookups of all other packages until it returns.
	nSlots := resolveSlots(o.Concurrency)
	for _, idx := range byDatasource(todo) {
		wg.Go(func() {
			slots := make(chan struct{}, nSlots)
			for _, i := range idx {
				slots <- struct{}{}
				wg.Go(func() {
					free := sync.OnceFunc(func() { <-slots })
					hold := time.AfterFunc(slotHold, free)
					defer func() {
						hold.Stop()
						free()
					}()
					resolveAt(i, free)
				})
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		prog.End(PhaseResolve)
		return nil, err
	}
	if err := sec.wait(); err != nil {
		res.Errors = append(res.Errors, fmt.Errorf("security advisories: %w", err))
	}
	res.Errors = append(res.Errors, holdTogether(todo, outs, ups)...)
	nUp, nSkip, nWarn, nPend := 0, 0, 0, 0
	for i := range outs {
		if outs[i].Err == nil && outs[i].Dep.Skip != "" {
			nSkip++
		}
		nUp += len(ups[i])
		nWarn += len(warns[i])
		if pend[i] != nil {
			nPend++
		}
	}
	res.Skipped = slices.Grow(res.Skipped, nSkip)
	res.Updates = slices.Grow(res.Updates, nUp)
	res.Warnings = slices.Grow(res.Warnings, nWarn)
	res.Pending = slices.Grow(res.Pending, nPend)
	upPols := make([]*policies, 0, nUp) // the policies of res.Updates[maint+i]
	var upAt []int                      // with a lister: the index of ups[i] in res.Updates
	if lister != nil {
		upAt = make([]int, len(outs))
	}
	for i, t := range outs {
		switch {
		case t.Err != nil:
			res.Errors = append(res.Errors, t.Err)
		case t.Dep.Skip != "":
			res.Skipped = append(res.Skipped, t.Dep)
			res.Deps[depAt[i]].Skip = t.Dep.Skip
		}
		if upAt != nil {
			upAt[i] = len(res.Updates)
		}
		res.Updates = append(res.Updates, ups[i]...)
		for range ups[i] {
			upPols = append(upPols, pols[i])
		}
		res.Warnings = append(res.Warnings, warns[i]...)
		if pend[i] != nil {
			res.Pending = append(res.Pending, *pend[i])
		}
		if o.Trace != nil {
			o.Trace(t)
		}
	}
	// Only the resolved updates are handed over: a maintenance update is never a member of a
	// shared definition (sharedMember), and its index would not be one of upPols.
	unifyShared(ctx, res.Updates[maint:], func(i int, kind dep.UpdateType) *config.Policy { return upPols[i].of(kind) })
	var listed []DepReport
	if lister != nil {
		listed = depReports(todo, outs, pend, res.Updates, upAt, ups)
	}
	sortUpdates(res.Updates)
	limited := 0
	for _, err := range res.Errors {
		if RateLimited(err) {
			limited++
		}
	}
	attrs := []any{"updates", len(res.Updates), "errors", len(res.Errors) - limited}
	if limited > 0 {
		attrs = append(attrs, "rateLimited", limited)
	}
	slog.InfoContext(ctx, "dependencies resolved", append(attrs, "duration", time.Since(start))...)
	if lister != nil {
		lister.ListDeps(listed)
	}
	prog.End(PhaseResolve)
	return res, nil
}

// depReports are the outcomes of the looked-up deps todo for a Lister, sorted by file and
// line: outs[i] is the outcome of todo[i], pend[i] its held-back release, and its updates (as
// unifyShared left them) are updates[upAt[i]:][:len(ups[i])].
func depReports(todo []dep.Dep, outs []Trace, pend []*Pending, updates []dep.Update, upAt []int, ups [][]dep.Update) []DepReport {
	out := make([]DepReport, len(todo))
	for i := range todo {
		d, t := &todo[i], &outs[i]
		r := DepReport{File: d.File, Line: d.Line, Name: d.Name}
		switch {
		case t.Err != nil:
			r.Err = t.Err
		case t.Dep.Skip != "":
			r.Skip = t.Dep.Skip
		default:
			r.Updates = slices.Clone(updates[upAt[i]:][:len(ups[i])])
			r.Pending = pend[i]
		}
		out[i] = r
	}
	slices.SortStableFunc(out, func(a, b DepReport) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	return out
}

// holdTogether drops the updates of Together deps (dep.Dep.Together) to a new version that not
// every dep of their group has: the deps with equal Manager, File and Name that are not
// skipped, of which one that failed or is up to date has none. ups[i] are the updates of
// todo[i], outs[i] its outcome. It returns one DepError per group and dropped version, located
// at the group's first dep; the errors of the deps that failed are reported on their own.
func holdTogether(todo []dep.Dep, outs []Trace, ups [][]dep.Update) []error {
	type key struct{ manager, file, name string }
	var keys []key // in order of first appearance
	groups := map[key][]int{}
	for i := range todo {
		d := &todo[i]
		if !d.Together || outs[i].Err == nil && outs[i].Dep.Skip != "" {
			continue
		}
		k := key{d.Manager, d.File, d.Name}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	var errs []error
	for _, k := range keys {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		moved := map[string]int{} // per new version, the deps of g updated to it
		var versions []string     // in order of first appearance
		for _, i := range g {
			for j, u := range ups[i] {
				if slices.ContainsFunc(ups[i][:j], func(p dep.Update) bool { return p.NewVersion == u.NewVersion }) {
					continue
				}
				if moved[u.NewVersion] == 0 {
					versions = append(versions, u.NewVersion)
				}
				moved[u.NewVersion]++
			}
		}
		for _, v := range versions {
			if moved[v] == len(g) {
				continue
			}
			for _, i := range g {
				ups[i] = slices.DeleteFunc(ups[i], func(u dep.Update) bool { return u.NewVersion == v })
			}
			d := &todo[g[0]]
			errs = append(errs, &DepError{File: d.File, Line: d.Line, Name: d.Name,
				Err: fmt.Errorf("%d of %d deps of %s in %s resolved to %s, the others did not; updated none", moved[v], len(g), d.Name, d.File, v)})
		}
	}
	return errs
}

// defaultConcurrency is the per-upstream request limit Options.Concurrency defaults to, that
// of cmd/deps --concurrency.
const defaultConcurrency = 32

// resolveSlots is how many deps of one datasource Resolve looks up at a time under the
// per-upstream request limit n (0 = defaultConcurrency): twice the request pool of the
// busiest upstreams (npm's 2n), so the pool stays full while some deps wait for a lookup of
// the same package another dep makes.
func resolveSlots(n int) int {
	if n < 1 {
		n = defaultConcurrency
	}
	return 4 * n
}

// slotHold is how long a dep keeps its resolve slot: longer than most lookups take, short
// enough that a stalled one delays the others by little.
const slotHold = 2 * time.Second

// byDatasource groups the indices of todo by datasource, in order of first appearance and
// in discovery order within each group.
func byDatasource(todo []dep.Dep) [][]int {
	at := map[string]int{}
	var groups [][]int
	for i := range todo {
		k, ok := at[todo[i].Datasource]
		if !ok {
			k = len(groups)
			at[todo[i].Datasource] = k
			groups = append(groups, nil)
		}
		groups[k] = append(groups[k], i)
	}
	return groups
}

// classify applies the pre-policy to an extracted dep and decides whether it is looked up:
// the returned dep has Skip set when it is not. pol is nil only for deps the manager
// already skipped. timed reports whether the dep's datasource has release times: when a
// currentAge rule can match, the policy decisions wait for the age of the current version
// (resolver.applyAge). With security lookups, a dep whose default ignore security updates
// bypass (securityByDefault) is kept as securityOnly. A rule's registryUrls replace the
// registry of apk, deb and rpm deps first, lifting an unknown registry skip.
func classify(cfg *config.Config, d dep.Dep, kindRules, timed, security bool) (dep.Dep, *policies) {
	if urls := cfg.RegistryURLs(d); urls != "" {
		d.Source = urls
		if d.Skip == dep.SkipUnknownRegistry {
			d.Skip = ""
		}
	}
	if d.Skip != "" {
		return d, nil
	}
	// The pre-policy comes first: a rule's versioning replaces the dep's scheme before
	// the current value is validated.
	pol := newPolicies(cfg, d, kindRules)
	if v := pol.pre.Versioning; v != "" {
		d.Versioning, pol.d.Versioning = v, v
	}
	if x := pol.pre.ExtractVersion; x != "" {
		d.ExtractVersion, pol.d.ExtractVersion = x, x
	}
	pol.aged = timed && cfg.WantsCurrentAge(pol.d)
	switch s, ok := versioning.Get(d.Versioning); {
	case ok && !s.Valid(d.Version) && !tagOnly(d) && !goPseudo(d) && !config.UnversionedPin(d) && !rusttoolchain.Channel(d):
		d.Skip = "non-version value " + strconv.Quote(d.Version)
	case config.UnversionedPin(d) && pol.pre.IgnoredByDefault:
		// Looked up: a tag at the commit versions it, which is not ignored (commitUpdates).
	case !pol.aged && pol.pre.Ignore && !pol.anyKind() && security && securityByDefault(d, pol.pre):
		pol.securityOnly = true
	case !pol.aged:
		d.Skip = pol.skip(d)
	}
	return d, pol
}

// discoveryAttrs summarizes one discovery walk: the total file count, the walk duration and
// the managers that matched files, by count descending ("docker 137, helm 134"). Managers of
// one name (the entries of customManagers) are one entry counting their distinct files.
func discoveryAttrs(managers []manager.Manager, found [][]string, d time.Duration) []any {
	type count struct {
		name string
		n    int
	}
	byName := map[string][]int{} // manager indices per name
	var names []string           // in order of first appearance
	total := 0
	for i, m := range managers {
		if len(found[i]) == 0 {
			continue
		}
		total += len(found[i])
		name := m.Name()
		if _, ok := byName[name]; !ok {
			names = append(names, name)
		}
		byName[name] = append(byName[name], i)
	}
	counts := make([]count, len(names))
	for j, name := range names {
		idx := byName[name]
		n := len(found[idx[0]])
		if len(idx) > 1 {
			files := map[string]struct{}{}
			for _, i := range idx {
				for _, f := range found[i] {
					files[f] = struct{}{}
				}
			}
			n = len(files)
		}
		counts[j] = count{name, n}
	}
	slices.SortStableFunc(counts, func(a, b count) int { return cmp.Compare(b.n, a.n) })
	parts := make([]string, len(counts))
	for i, c := range counts {
		parts[i] = c.name + " " + strconv.Itoa(c.n)
	}
	return []any{"files", total, "duration", d, "managers", strings.Join(parts, ", ")}
}

// planAll hands every datasource implementing datasource.Planner its share of the updatable
// deps, before any lookup.
func planAll(sources map[string]Datasource, todo []dep.Dep) {
	byDS := map[string][]dep.Dep{}
	for _, d := range todo {
		if _, ok := sources[d.Datasource].(datasource.Planner); ok {
			byDS[d.Datasource] = append(byDS[d.Datasource], d)
		}
	}
	for name, deps := range byDS {
		sources[name].(datasource.Planner).Plan(deps)
	}
}

// extractAll reads and parses files on GOMAXPROCS workers. A worker takes one file with all
// its jobs (managers matching it): the file is read once, and its managers share the bytes
// and a single-document YAML parse (manager.Env.YAML). Only the extracted deps are kept; file
// bytes and parse trees are dropped as soon as a file is done.
func extractAll(root string, jobs []job, env manager.Env, prog Progress) []extracted {
	ex := make([]extracted, len(jobs))
	at := make(map[string]int, len(jobs)) // file → index in files
	var files [][]int                     // job indexes per file, in first-job order
	for i, j := range jobs {
		k, ok := at[j.file]
		if !ok {
			k = len(files)
			at[j.file] = k
			files = append(files, nil)
		}
		files[k] = append(files[k], i)
	}
	next := make(chan []int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(files)) {
		wg.Go(func() {
			for idx := range next {
				extractFile(root, jobs, idx, env, ex)
				for _, i := range idx {
					prog.Done(PhaseExtract, ex[i].err)
				}
			}
		})
	}
	for _, idx := range files {
		next <- idx
	}
	close(next)
	wg.Wait()
	return ex
}

// partition yields the index of each manager with its slice of ex. found holds the files per
// manager; their jobs (and ex entries) are contiguous in manager order, so part[k] is the
// extraction of found[i][k].
func partition(found [][]string, ex []extracted) func(yield func(i int, part []extracted) bool) {
	return func(yield func(int, []extracted) bool) {
		off := 0
		for i, files := range found {
			part := ex[off : off+len(files)]
			off += len(files)
			if !yield(i, part) {
				return
			}
		}
	}
}

// linkAll lets each manager.Linker resolve the cross-file references of its deps (found and
// ex as for partition).
func linkAll(managers []manager.Manager, found [][]string, ex []extracted) {
	for i, part := range partition(found, ex) {
		l, ok := managers[i].(manager.Linker)
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

// readLocks lets each manager.LockReader record the locked versions of its deps in place
// (found and ex as for partition) and returns the lockfiles it could not read.
func readLocks(root string, managers []manager.Manager, found [][]string, ex []extracted) []error {
	var errs []error
	for i, part := range partition(found, ex) {
		lr, ok := managers[i].(manager.LockReader)
		if !ok {
			continue
		}
		files := make([][]dep.Dep, 0, len(part))
		for _, e := range part {
			if e.err == nil {
				files = append(files, e.deps)
			}
		}
		errs = append(errs, lr.ReadLocks(root, files)...)
	}
	return errs
}

// maintenanceUpdates returns one lock file maintenance update (lockFileMaintenance) per lock
// root of each manager.LockMaintainer, over the files it extracted without error. It carries
// the lockFileMaintenance schedule and the PR settings of the top level
// (config.DefaultPolicy; the title stays fixed, see GroupUpdates); rules do not apply.
func maintenanceUpdates(cfg *config.Config, root string, managers []manager.Manager, found [][]string, ex []extracted) []dep.Update {
	var out []dep.Update
	p := cfg.DefaultPolicy()
	for i, part := range partition(found, ex) {
		m := managers[i]
		lm, ok := m.(manager.LockMaintainer)
		if !ok {
			continue
		}
		files := make([]string, 0, len(part))
		for k, e := range part {
			if e.err == nil {
				files = append(files, found[i][k])
			}
		}
		for _, lr := range lm.LockRoots(root, files) {
			out = append(out, dep.Update{
				Dep:        dep.Dep{Manager: m.Name(), File: lr.Manifest, Name: lr.Lockfile},
				Type:       dep.LockFileMaintenance,
				Schedule:   cfg.LockFileMaintenanceSchedule(),
				PRSettings: p.PRSettings,
			})
		}
	}
	return out
}

// extractFile extracts the file of jobs[idx[0]] with the manager of every job in idx (all of
// the same file) into ex.
func extractFile(root string, jobs []job, idx []int, env manager.Env, ex []extracted) {
	file := jobs[idx[0]].file
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		for _, i := range idx {
			ex[i] = extracted{err: err}
		}
		return
	}
	env.YAML = &yamlspan.Parsed{}
	for _, i := range idx {
		deps, err := jobs[i].m.Extract(file, src, env)
		ex[i] = extracted{deps: deps, err: err}
	}
}

// dedupeCovered skips deps whose bytes are also claimed by a custom dep in the same file: an
// explicit annotation (or regex match) wins over native extraction, and the first custom
// manager (in config order) over later ones whose match claims the same version (Renovate
// configs often add a generic regex next to a specific one), so the same version is never
// patched twice nor proposed in two PRs. A later custom dep of the same version bytes,
// datasource and name that pins a digest or checksum of its own (one version, a checksum per
// platform) shares the version instead (shareCustom).
func dedupeCovered(ex []extracted) {
	type seen struct {
		d  *dep.Dep
		at int // index in ex
		// own are the digest and checksum spans of the deps sharing d's version.
		own []dep.Span
	}
	first := map[string][]*seen{}
	for i := range ex {
		for j := range ex[i].deps {
			d := &ex[i].deps[j]
			if d.Manager != custom.Name || d.Skip != "" || d.RefSpan.End <= d.RefSpan.Start {
				continue
			}
			dup := false
			for _, s := range first[d.File] {
				if s.at == i || d.RefSpan.Start >= s.d.RefSpan.End || s.d.RefSpan.Start >= d.RefSpan.End {
					continue
				}
				if own := ownSpans(d); shareable(s.d, d, s.own, own) {
					shareCustom(s.d, d)
					s.own = append(s.own, own...)
				} else {
					d.Skip = fmt.Sprintf("covered by custom match on line %d", s.d.Line)
				}
				dup = true
				break
			}
			if !dup {
				first[d.File] = append(first[d.File], &seen{d: d, at: i, own: ownSpans(d)})
			}
		}
	}
	type claim struct {
		spans [3]dep.Span
		line  int
		what  string // the skip reason's name of the custom dep
	}
	claims := map[string][]claim{}
	for _, e := range ex {
		for _, d := range e.deps {
			if d.Manager != custom.Name || d.RefSpan.End <= d.RefSpan.Start {
				continue
			}
			c := claim{spans: [3]dep.Span{d.RefSpan, d.DigestSpan, d.VersionSpan}, line: d.Line, what: "annotation"}
			switch d.DepType {
			case custom.DepTypeRegex:
				c.what = "custom regex match"
			case custom.DepTypeJSONata:
				c.what = "custom jsonata match"
			default:
				if c.line > 1 {
					c.line-- // the annotation comment precedes the annotated line
				}
			}
			claims[d.File] = append(claims[d.File], c)
		}
	}
	if len(claims) == 0 {
		return
	}
	for i := range ex {
		for j := range ex[i].deps {
			d := &ex[i].deps[j]
			if d.Manager == custom.Name || d.Skip != "" && d.Skip != dep.SkipUnknownRegistry {
				continue
			}
			for _, c := range claims[d.File] {
				hit := false
				for _, s := range [3]dep.Span{d.RefSpan, d.DigestSpan, d.VersionSpan} {
					for _, t := range c.spans {
						hit = hit || overlaps(s, t)
					}
				}
				if !hit {
					continue
				}
				d.Skip = fmt.Sprintf("covered by %s on line %d", c.what, c.line)
				break
			}
		}
	}
}

// overlaps reports whether the non-empty spans a and b share a byte.
func overlaps(a, b dep.Span) bool {
	return a.End > a.Start && b.End > b.Start && a.Start < b.End && b.Start < a.End
}

// ownSpans returns the non-empty digest and checksum spans of d: the bytes it writes besides
// its version.
func ownSpans(d *dep.Dep) []dep.Span {
	var own []dep.Span
	for _, s := range [2]dep.Span{d.DigestSpan, d.ChecksumSpan} {
		if s.End > s.Start {
			own = append(own, s)
		}
	}
	return own
}

// shareable reports whether the custom dep b may share the version of the earlier custom dep
// a, whose sharing deps write aOwn besides it: both cover the same version bytes with the same
// datasource and name, and b writes a digest or checksum (bOwn) none of them writes. Anything
// else is a duplicate of a.
func shareable(a, b *dep.Dep, aOwn, bOwn []dep.Span) bool {
	if a.RefSpan != b.RefSpan || a.Datasource != b.Datasource || a.Name != b.Name || len(bOwn) == 0 {
		return false
	}
	for _, s := range bOwn {
		for _, t := range aOwn {
			if overlaps(s, t) {
				return false
			}
		}
	}
	return true
}

// shareCustom makes the custom deps a and b, of one version, share it: a version definition
// named after the dep (dep.Dep.VersionRef, so unifyShared gives their updates one version and
// one PR and Local writes it once) whose deps move only as a whole (dep.Dep.Together, so no
// digest or checksum moves unless all of them can).
func shareCustom(a, b *dep.Dep) {
	a.VersionRef, a.Together = a.Name, true
	b.VersionRef, b.Together = a.Name, true
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

// ready reports whether the lookup finished, so of does not block; nil-safe.
func (s *security) ready() bool {
	if s == nil {
		return true
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
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
	// aged: the policies depend on the age of the current version (config.WantsCurrentAge)
	// and its datasource has release times; age is config.UnknownAge until setAge.
	aged bool
	age  time.Duration
	// securityOnly: the dep is ignored by default but security updates still apply
	// (securityByDefault); it is looked up only when advisories affect it.
	securityOnly bool
}

func newPolicies(cfg *config.Config, d dep.Dep, kindRules bool) *policies {
	return &policies{cfg: cfg, d: d, pre: cfg.Policy(d), kindRules: kindRules, age: config.UnknownAge}
}

// setAge re-evaluates the policies with age, the age of the current version.
func (p *policies) setAge(age time.Duration) {
	p.age, p.kinds = age, nil
	p.pre = p.cfg.PolicyAt(p.d, "", age)
}

// securityByDefault reports whether security updates apply to d although its dep type is
// ignored by default: the transitive pins of compiled requirements files (Renovate's
// pip-compile indirect deps, which vulnerability alerts force-enable). A rule's ignore still
// wins.
func securityByDefault(d dep.Dep, pre config.Policy) bool {
	return pre.IgnoredByDefault && d.Manager == piprequirements.Name && d.DepType == piprequirements.DepTypeIndirect
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
	kp := p.cfg.PolicyAt(p.d, kind, p.age)
	p.kinds[kind] = &kp
	return &kp
}

// skip is why the policies leave d (with a valid or tag-only version) alone, "" when it is
// looked up: ignored for every update type, or a non-version tag (a branch ref of git-refs)
// without a digest that is not pinned nor replaced.
func (p *policies) skip(d dep.Dep) string {
	switch {
	case p.pre.Ignore && !p.anyKind():
		return p.pre.IgnoreReason
	case d.Digest == "" && (p.pre.Pin != config.PinDigest || !digestCapable(d)) && tagOnly(d) && (p.pre.ReplacementName == "" || !d.Renamable()):
		// A non-version tag without a digest can only be pinned (when digestCapable) or replaced.
		if d.Manager == actions.Name && d.Datasource == "git-refs" {
			return "branch ref"
		}
		return "non-version tag"
	}
	return ""
}

// anyKind reports whether an update of some kind is not ignored, a replacement included when
// the pre-policy names one.
func (p *policies) anyKind() bool {
	if p.pre.ReplacementName != "" && !p.of(dep.Replacement).Ignore {
		return true
	}
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
// getBucket): with separate none every candidate shares one bucket; otherwise majors go to
// "major" (or "v<N>" per major with majors) and minors and patches share the non-major bucket
// "", or get "minor" and "patch" with minor-patch.
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
// no new version for allow). Replacements are not in allowedUpdateTypes lists: only ignore
// stops them.
func (p *policies) allows(kind dep.UpdateType) bool {
	kp := p.of(kind)
	return !kp.Ignore && (kind == dep.Replacement || kp.AllowedUpdateTypes[kind])
}

// resolver decides the update of one dep.
type resolver struct {
	ctx   context.Context
	root  string
	d     dep.Dep
	cur   string // version candidates are ranked against: d.Version, or a Go pseudo-version's base (pseudoCur)
	pol   *policies
	ds    Datasource
	cs    ChecksumSource
	trace bool
	now   time.Time
	// skipGomodPaths turns off majorPathUpdates (manager.Env.SkipGomodImportPaths).
	skipGomodPaths bool
	// base is ds before extractVersion wrapping: the head of a commit pin (commitUpdates).
	base Datasource

	rels     []Release
	names    *releaseNames        // shared with the other resolvers of the run
	times    map[string]time.Time // release times asked so far; zero = unknown
	pending  Pending              // newest candidate held back by minimumReleaseAge
	warnings []Warning            // checkLifecycle's findings
	// declared looks up the module path a Go module version declares (nil = no check);
	// renamed is the reason of the first candidate pathChange rejected.
	declared datasource.DeclaredPathSource
	renamed  string

	// rangeStrategy of a range dep (setRange): strategy is the effective rangeStrategy (""
	// for deps without a range); inRange reports whether a version is one the manifest keeps
	// (d.Range admits it; under bump and in-range-only only when the range cannot be raised
	// for it); nil means every candidate rewrites the range. In-range candidates are
	// lockfile-only updates when lockOnly, else they are not proposed. within (in-range-only)
	// reports the versions the range admits: no others are proposed; nil = no limit.
	// rewrites reports whether rewriter writes a new range for a version under strategy (set
	// for range deps with a rewriter, nil otherwise).
	strategy string
	inRange  func(string) bool
	within   func(string) bool
	rewrites func(string) bool
	lockOnly bool
	rewriter manager.RangeRewriter // the dep's manager rewrites its whole range; nil = by version token
	ranger   manager.RangeUpdater  // the rewriter also matches the range (Python specifiers)
	renamer  manager.Renamer       // the dep's manager rewrites names (replacements); nil = none
}

// autoRange is what rangeStrategy auto means per manager (Renovate's defaults): replace for
// terraform, terragrunt, pub and cocoapods (Renovate's pub and cocoapods managers have no
// lockfile-only updates) and the Helm chart ranges of helm and kustomize (Renovate's helm managers have no lockfile-only
// updates either), widen for elm package constraints (Dependabot's; Renovate has no Elm
// manager) and gleam requirements, update-lockfile for npm, cargo and any other manager (widen
// for npm peerDependencies and `||` ranges, for composer constraints with alternatives and the
// constraints of a TYPO3 extension (dep.Dep.AutoWiden), for mix requirements with several
// comparators and for julia compat entries without a locked version, replace for the
// dependencies of a bundler gemspec, rangeStrategy).
var autoRange = map[string]string{"terraform": config.RangeReplace, "terragrunt": config.RangeReplace, "elm": config.RangeWiden, "pub": config.RangeReplace, beam.NameGleam: config.RangeWiden, "helm": config.RangeReplace, "kustomize": config.RangeReplace, bundler.NameCocoaPods: config.RangeReplace}

// rangeStrategy is the effective rangeStrategy of the range dep: auto resolved per manager
// (bump too for composer constraints with alternatives and mix and gleam requirements with
// several comparators, which it widens as Renovate does),
// widen and pin resolved to replace for managers that cannot write them (no
// manager.RangeRewriter: terraform, Gradle) and for ranges that are never rewritten. lockfile
// reports that the versions the range admits stay lockfile-only updates although the range is
// widened for others: auto for a composer constraint with alternatives (`^1.0|^2.0`) is
// update-lockfile that widens instead of replacing.
func (r *resolver) rangeStrategy() (s string, lockfile bool) {
	d := r.d
	s = r.pol.pre.RangeStrategy
	if s == config.RangeAuto {
		s = cmp.Or(autoRange[d.Manager], config.RangeUpdateLockfile)
		switch {
		case d.Manager == npmManager && (d.DepType == "peerDependencies" || strings.Contains(d.Range, "||")):
			s = config.RangeWiden
		case d.Manager == bundler.Name && (d.DepType == bundler.DepTypeRuntime || d.DepType == bundler.DepTypeDevelopment):
			// A gemspec states the versions a gem supports: its users' Gemfile.lock, not the
			// gem's own, picks one, so a new version only matters when the requirements
			// exclude it.
			s = config.RangeReplace
		case d.Manager == composer.Name && d.AutoWiden:
			s = config.RangeWiden
		case d.Manager == composer.Name && strings.Contains(d.Range, "|"):
			s, lockfile = config.RangeWiden, true
		case d.Manager == beam.NameMix && beam.Complex(d.Range):
			s = config.RangeWiden
		case d.Manager == julia.Name && d.Locked == "" && !d.LockUnread:
			// A Julia package commits no manifest: its compat entries are what it supports,
			// and the Julia convention (CompatHelper) adds new majors rather than drop old ones.
			s = config.RangeWiden
		}
	}
	if s == config.RangeBump && d.Manager == composer.Name && strings.Contains(d.Range, "|") {
		s = config.RangeWiden // Renovate's composer getRangeStrategy: bump cannot raise `^1 || ^2`
	}
	if s == config.RangeBump && (d.Manager == beam.NameMix || d.Manager == beam.NameGleam) && beam.Complex(d.Range) {
		s = config.RangeWiden // Renovate's mix and gleam getRangeStrategy
	}
	if (s == config.RangeWiden || s == config.RangePin) && (r.rewriter == nil || d.RangeFixed) {
		s = config.RangeReplace
	}
	return s, lockfile
}

// setRange applies the rangeStrategy of a range dep (dep.Dep.Range, read as Constraint when
// set: npm and cargo dependencies, terraform constraints, Gradle dynamic versions); other deps
// always replace their version. Under update-lockfile, replace and widen candidates are ranked
// against the locked version (dep.Dep.Locked) when there is one; update-lockfile moves only the
// lockfile for in-range candidates, replace and widen leave them alone (except where auto widens
// a composer constraint with alternatives: rangeStrategy). A lock root whose
// lockfile deps does not read (LockUnread) turns update-lockfile into bump, except for
// peerDependencies, which never get lockfile-only updates: their in-range versions are left
// alone. A range without a floor (it admits 0.0.0: `*`, `<2`) has nothing to bump, a range that
// is never rewritten (RangeFixed) only moves in the lock, and a Gradle dynamic version (`1.+`,
// `[1.0,2.0)`) keeps its floor in its edit, so an in-range bump would change nothing: bump is
// update-lockfile for all three. Under bump an in-range version a manager.RangeRewriter cannot
// raise the range for (`1.x` with 1.5.0) is left alone. in-range-only is bump limited to the
// versions the range admits; pin goes to setPin. Deps of a manager.RangeUpdater go to
// setRewritten.
func (r *resolver) setRange(s versioning.Scheme, names []string) {
	d := &r.d // not a copy: the closures below would move it to the heap
	if d.Range == "" {
		return
	}
	var lockfile bool
	r.strategy, lockfile = r.rangeStrategy()
	if r.rewriter != nil {
		r.rewrites = lastOf(func(v string) bool {
			_, ok := r.rewriter.NewRange(*d, v, r.strategy)
			return ok
		})
	}
	if r.ranger != nil {
		r.setRewritten(s, names)
		return
	}
	peer := d.DepType == "peerDependencies"
	strategy := r.strategy
	match, err := rangeMatcher(s, cmp.Or(d.Constraint, d.Range))
	if err != nil {
		slog.DebugContext(r.ctx, "range not understood", "name", d.Name, "range", d.Range, "err", err)
		if d.RangeFixed || strategy == config.RangeInRangeOnly {
			r.inRange = func(string) bool { return false }
		}
		if strategy == config.RangeInRangeOnly {
			r.within = r.inRange
		}
		return
	}
	keeps := strategy == config.RangeReplace || strategy == config.RangeWiden // in-range versions are left alone
	bump := strategy == config.RangeBump || strategy == config.RangeInRangeOnly
	if strategy == config.RangeInRangeOnly {
		match = lastOf(match) // accept asks within, then inRange, for each candidate
		r.within = match
	}
	switch {
	case strategy == config.RangePin:
		r.setPin(s, names, match)
		return
	case !d.RangeFixed && d.LockUnread && !peer && !keeps:
		return
	case bump && !d.RangeFixed && d.Manager != gradleManager && !match("0.0.0"):
		if r.rewrites != nil {
			r.inRange = func(v string) bool { return match(v) && !r.rewrites(v) }
		}
		return
	}
	r.inRange = match
	if d.Locked != "" && s.Valid(d.Locked) {
		r.cur = d.Locked
		r.lockOnly = !peer && (!keeps || lockfile)
	}
}

// setRewritten is setRange for the deps of a manager.RangeUpdater (Python specifiers): under
// update-lockfile (auto), replace and widen every admitted version keeps the range, under bump
// and in-range-only only those the range cannot be raised for (`<2`, `>1`, `==1.*`). Admitted
// versions are lockfile-only updates except under replace and widen. Candidates are ranked
// against the locked version under every strategy; pin goes to setPin.
func (r *resolver) setRewritten(s versioning.Scheme, names []string) {
	d := &r.d
	strategy := r.strategy
	match := func(v string) bool { return r.ranger.InRange(*d, v) }
	switch strategy {
	case config.RangePin:
		r.setPin(s, names, match)
		return
	case config.RangeInRangeOnly:
		match = lastOf(match) // accept asks within, then inRange, for each candidate
		r.within = match
	}
	bump := strategy == config.RangeBump || strategy == config.RangeInRangeOnly
	r.inRange = func(v string) bool {
		return match(v) && (!bump || !r.rewrites(v)) // the ranger is the rewriter
	}
	if d.Locked != "" && s.Valid(d.Locked) {
		r.cur = d.Locked
		r.lockOnly = strategy != config.RangeReplace && strategy != config.RangeWiden
	}
}

// setPin applies rangeStrategy pin: every candidate writes its exact version, ranked against
// the version the range is pinned to (pinUpdate): the locked one, else the newest stable
// release the range admits, else the dep's version.
func (r *resolver) setPin(s versioning.Scheme, names []string, match func(string) bool) {
	if l := r.d.Locked; l != "" && s.Valid(l) {
		r.cur = l
		return
	}
	best := ""
	for _, v := range names {
		if sv := s.Semver(v); sv == nil || sv.Prerelease() != "" || !s.Valid(v) || !match(v) {
			continue
		}
		if best == "" || s.Compare(v, best) > 0 {
			best = v
		}
	}
	if best != "" {
		r.cur = best
	}
}

// pinUpdate adds the pin of a range to the version candidates are ranked against (setPin)
// under rangeStrategy pin, unless the range is already that exact version, pin updates are not
// enabled, or another update shares the pin's PR bucket: that update pins the range itself.
func (r *resolver) pinUpdate(ups []dep.Update) []dep.Update {
	if _, ok := r.rewriter.NewRange(r.d, r.cur, config.RangePin); !ok || !r.pol.allows(dep.Pin) {
		return ups
	}
	b := r.pol.bucket(versioning.Candidate{Kind: dep.Pin}).label()
	if slices.ContainsFunc(ups, func(u dep.Update) bool { return u.Bucket == b }) {
		return ups
	}
	return slices.Insert(ups, 0, dep.Update{Dep: r.d, Type: dep.Pin, NewVersion: r.cur, Bucket: b})
}

// rangeMatcher returns whether a version satisfies rng: the scheme's own range grammar
// (versioning.RangeMatcher, e.g. Terraform constraints), else a Masterminds constraint.
func rangeMatcher(s versioning.Scheme, rng string) (func(string) bool, error) {
	if m, ok := s.(versioning.RangeMatcher); ok {
		return m.MatchRange(rng)
	}
	c, err := semver.NewConstraint(rng)
	if err != nil {
		return nil, err
	}
	return func(v string) bool {
		sv := s.Semver(v)
		return sv != nil && c.Check(sv)
	}, nil
}

// lastOf wraps f, a check that depends only on its version, to answer a repeated ask for the
// last version without calling f again: accept and the range checks it calls ask the same
// candidate in turn (within and inRange share a match; inRange under bump and accept ask
// whether the range can be rewritten).
func lastOf(f func(string) bool) func(string) bool {
	var last string
	var asked, ok bool
	return func(v string) bool {
		if !asked || v != last {
			last, ok, asked = v, f(v), true
		}
		return ok
	}
}

// gradleManager is the name of the gradle manager, whose dynamic versions keep their floor;
// npmManager that of the npm manager, whose `||` ranges auto widens.
const (
	gradleManager = "gradle"
	npmManager    = "npm"
)

// installed is the version an npm range dep's installs get: the locked one when known, else
// the version in the range. It is the baseline of security updates.
func (r *resolver) installed(s versioning.Scheme) string {
	if l := r.d.Locked; l != "" && s.Valid(l) {
		return l
	}
	return r.cur
}

// run resolves in order into t: datasource → candidates ranked under policy, one per PR
// bucket, with the release-time guards → digest pin/refresh → security. vulns blocks until
// the advisories of this dep are known. Trace.Updates is only filled when tracing.
func (r *resolver) run(t *Trace, vulns func() []Vuln) []dep.Update {
	*t = Trace{} // set field by field: a Trace literal is built in a stack temporary first
	t.Dep, t.Rules, t.Policy = r.d, r.pol.pre.Rules, r.pol.pre
	ups, err := r.resolve(t, vulns)
	t.Err = err
	if r.trace && len(ups) > 0 {
		t.Updates = make([]TraceUpdate, len(ups))
		for i, u := range ups {
			t.Updates[i] = TraceUpdate{Update: u, Policy: r.pol.of(u.Type)}
		}
	}
	return ups
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
	// An action in a monorepo subdirectory without an extractVersion of its own is versioned
	// by its per-action tags (`get-vault-secrets/v1.3.1`) when the repository has any.
	r.base = r.ds
	var prefixed *extracting
	var prefixExpr string
	switch {
	case d.ExtractVersion != "":
		x, err := versioning.CompileExtract(d.ExtractVersion)
		if err != nil {
			return nil, fmt.Errorf("extractVersion: %w", err)
		}
		r.ds = withExtract(r.ds, x)
	case d.Manager == actions.Name && d.DepType != actions.DepTypeSetup && d.DepType != actions.DepTypeRunner && d.Datasource != "git-refs":
		// A branch ref (git-refs) names a branch, not a per-action tag.
		if prefixExpr = actions.TagExtract(d.Name); prefixExpr != "" {
			x, err := versioning.CompileExtract(prefixExpr)
			if err != nil {
				return nil, fmt.Errorf("per-action tags: %w", err)
			}
			prefixed = &extracting{ds: r.ds, x: x, fallback: true}
			r.ds = wrapExtract(prefixed)
		}
	}
	if toolversions.Partial(d) {
		r.ds = withLines(r.ds)
	}
	ups, err := r.updates(t, scheme, vulns)
	if err != nil {
		return nil, err
	}
	warnings := warningTexts(r.warnings)
	var decorate func([]dep.Update)
	decorate = func(ups []dep.Update) {
		for i := range ups {
			up := &ups[i]
			if prefixed != nil && len(prefixed.names) > 0 {
				up.Dep.ExtractVersion = prefixExpr // release notes look up the per-action tags
			}
			applyPolicy(up, r.pol.of(up.Type))
			up.Warnings = warnings
			decorate(up.Declined)
		}
	}
	decorate(ups)
	return ups, nil
}

// applyPolicy sets the fields of up that come from kp, the policy of its type: PR, grouping
// and schedule settings. Security updates follow it too, apart from schedule, group and
// dashboard approval: they take the security group instead and never wait for approval.
// unifyShared applies it again to an update it lowers.
func applyPolicy(up *dep.Update, kp *config.Policy) {
	up.PRSettings = kp.PRSettings
	up.BranchTopicPrefix = config.ExpandBranchPrefix(kp.AdditionalBranchPrefix, up.Dep)
	if len(up.Advisories) == 0 {
		up.Group, up.Schedule, up.SecurityGroup = kp.Group, kp.Schedule, ""
	} else {
		up.SecurityGroup, up.DashboardApproval = kp.SecurityGroup, false
	}
}

// usual resolves the dep's updates other than a replacement.
func (r *resolver) usual(t *Trace, scheme versioning.Scheme, vulns func() []Vuln) ([]dep.Update, error) {
	switch {
	case goPseudo(r.d):
		return r.versionUpdates(t, scheme, vulns)
	case config.UnversionedPin(r.d):
		return r.commitUpdates(t, scheme, vulns)
	case rusttoolchain.Channel(r.d):
		// A channel names no release: only rangeStrategy pin writes one, the newest it admits
		// (setPin), so other strategies need no lookup.
		if s, _ := r.rangeStrategy(); s != config.RangePin {
			return nil, nil
		}
		return r.versionUpdates(t, scheme, vulns)
	case !scheme.Valid(r.d.Version):
		// Resolve only lets non-version docker tags through (tagOnly): their digest is kept
		// current; there are no releases to compare and no advisories to look up.
		if reason := r.applyAge(t); reason != "" {
			return nil, &dep.SkipError{Reason: reason}
		}
		return r.digestUpdate()
	}
	return r.versionUpdates(t, scheme, vulns)
}

// versionUpdates resolves a dep with a valid version: the best candidate under policy per PR
// bucket, plus the digest refresh or pin of the current version when no update sits in the
// digest's bucket (Renovate proposes it next to a major under separate: major; for a Go
// pseudo-version, without updates, the newer pseudo-version of the module's head), then
// security, the digest and checksum of every
// resulting version, and Go major module path updates. Updates are ordered by version, lowest
// first. A dep left without updates because candidates declare another module path is
// skipped with the reason of the newest one (*dep.SkipError): it needs a manual move to the
// new path, which the dashboard and the run report show.
func (r *resolver) versionUpdates(t *Trace, scheme versioning.Scheme, vulns func() []Vuln) ([]dep.Update, error) {
	d := &r.d // not a copy: the closures below would move it to the heap
	dir := filepath.Join(r.root, filepath.FromSlash(path.Dir(d.File)))
	rels, err := r.ds.Releases(r.ctx, *d, dir)
	if err != nil {
		return nil, err
	}
	r.rels = rels
	// After Releases: with extractVersion, the current version maps back to its release name.
	if reason := r.applyAge(t); reason != "" {
		return nil, &dep.SkipError{Reason: reason}
	}
	r.checkLifecycle(scheme)
	r.declared, _ = r.ds.(datasource.DeclaredPathSource)
	t.Candidates = len(rels)
	names := r.names.of(rels)
	var reject func(v, reason string)
	if r.trace {
		t.Rejected = map[string][]string{}
		reject = func(v, reason string) { t.Rejected[reason] = append(t.Rejected[reason], v) }
	}
	pseudo := goPseudo(*d)
	if pseudo {
		r.cur, names = pseudoCandidates(d.Version, names, reject)
	}
	r.setRange(scheme, names)
	semverOf := scheme.Semver
	accept := func(v string, kind dep.UpdateType) (bool, string) {
		if r.within != nil && !r.within(v) {
			return false, "outside range " + d.Range + " (in-range-only)"
		}
		in := r.inRange != nil && r.inRange(v)
		switch {
		case in && !r.lockOnly:
			return false, "admitted by range " + d.Range + " (no lockfile update)"
		case !in && d.RangeFixed:
			return false, "outside range " + d.Range + " (not rewritten)"
		case !in && r.inRange != nil && r.rewrites != nil:
			// The policy is cheaper than a range rewrite (npm parses the range it writes):
			// the rewrite is only tried when the policy accepts, or a trace wants its reason,
			// which comes first.
			ok, why := r.pol.check(kind, v, semverOf)
			if (ok || r.trace) && !r.rewrites(v) {
				return false, "range " + d.Range + " cannot be rewritten to admit it"
			}
			return ok, why
		}
		return r.pol.check(kind, v, semverOf)
	}

	digests := digestCapable(*d)
	wantDigest := digests && (d.Digest != "" || r.pol.pre.Pin == config.PinDigest)

	ups := r.picks(scheme, names, accept, reject)
	if r.strategy == config.RangePin {
		ups = r.pinUpdate(ups)
	}
	switch {
	case len(ups) == 0 && pseudo:
		if ups, err = r.headUpdate(dir, reject); err != nil {
			return nil, err
		}
	case digests:
		ups, err = r.digestAlongside(ups)
		if err != nil {
			return nil, err
		}
	}

	var vs []Vuln
	// An ignored dep is looked up only when a currentAge rule could opt it in, or when its
	// default ignore lets security updates through (securityOnly).
	if !r.pol.pre.Ignore || r.pol.securityOnly {
		vs = vulns()
	}
	ups = r.applySecurity(ups, scheme, names, vs)
	if r.lockOnly {
		for i := range ups {
			ups[i].Lockfile = r.inRange(ups[i].NewVersion)
		}
	}
	switch {
	case r.rewrites != nil:
		// Security updates skip accept: drop those neither the lockfile nor the range can take,
		// and under in-range-only those outside the range.
		ups = slices.DeleteFunc(ups, func(u dep.Update) bool {
			if r.within != nil && !r.within(u.NewVersion) {
				return true
			}
			return !u.Lockfile && !r.rewrites(u.NewVersion)
		})
	case d.RangeFixed:
		// Security fixes outside the range cannot be written either.
		ups = slices.DeleteFunc(ups, func(u dep.Update) bool { return !u.Lockfile })
	case r.within != nil:
		ups = slices.DeleteFunc(ups, func(u dep.Update) bool { return !r.within(u.NewVersion) })
	}
	for i := range ups {
		ups[i].RangeStrategy = r.strategy
	}
	if ups, err = r.majorPathUpdates(t, ups, scheme, dir, accept, reject); err != nil {
		return nil, err
	}

	// A checksum failure drops only that update: another bucket's release may carry the asset.
	var csErr error
	kept := ups[:0]
	for i := range ups {
		up := &ups[i]
		if up.Type != dep.Digest && up.Type != dep.Pin && wantDigest {
			if up.NewDigest, err = r.digestOf(up.NewVersion); err != nil {
				return nil, err
			}
		}
		if d.ChecksumSource != nil && up.NewVersion != d.Version {
			if up.NewChecksum, err = r.checksumOf(up.NewVersion); err != nil {
				if csErr == nil {
					csErr = err
				}
				if reject != nil {
					reject(up.NewVersion, "no checksum")
				}
				continue
			}
		}
		kept = append(kept, *up)
	}
	ups = kept
	if len(ups) == 0 && csErr != nil {
		return nil, csErr
	}
	if len(ups) == 0 && r.renamed != "" {
		return nil, &dep.SkipError{Reason: r.renamed}
	}
	return ups, nil
}

// digestAlongside prepends the digest refresh or pin of the current version (digestUpdate)
// to ups unless an update already sits in its PR bucket, the test pinUpdate uses: a version
// update there writes the new digest itself. Its version is the current one, the lowest.
func (r *resolver) digestAlongside(ups []dep.Update) ([]dep.Update, error) {
	b := r.pol.bucket(versioning.Candidate{Kind: dep.Digest}).label()
	if slices.ContainsFunc(ups, func(u dep.Update) bool { return u.Bucket == b }) {
		return ups, nil
	}
	du, err := r.digestUpdate()
	if err != nil || len(du) == 0 {
		return ups, err
	}
	du[0].Bucket = b
	return append(du, ups...), nil
}

// digestUpdate keeps the current version and refreshes its digest when pinned (Type digest,
// none when unchanged), or pins it under pin: digest (Type pin). It has no new version, so
// allow and minimumReleaseAge do not apply. A datasource that resolves no digest (github
// releases) yields no update: there is nothing to write.
func (r *resolver) digestUpdate() ([]dep.Update, error) {
	d := r.d
	switch {
	case d.Digest != "" && r.pol.allows(dep.Digest):
		c, err := r.currentDigest()
		if err != nil {
			return nil, err
		}
		if c != "" && !sameCommit(c, d.Digest) {
			return []dep.Update{{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: c}}, nil
		}
	case d.Digest == "" && r.pol.pre.Pin == config.PinDigest && r.pol.allows(dep.Pin):
		c, err := r.currentDigest()
		if err != nil {
			return nil, err
		}
		if c != "" {
			return []dep.Update{{Dep: d, Type: dep.Pin, NewVersion: d.Version, NewDigest: c}}, nil
		}
	}
	return nil, nil
}

// sameCommit reports whether the digest c is cur, or starts with cur when cur is an
// abbreviated commit (an npm git dependency's `#1a2b3c4`).
func sameCommit(c, cur string) bool {
	if len(cur) >= 7 && len(cur) < len(c) {
		c = c[:len(cur)]
	}
	return strings.EqualFold(c, cur)
}

// currentDigest is digestOf the current version; a version the upstream no longer has
// (deleted tag) skips the dep instead of failing it, as Renovate does.
func (r *resolver) currentDigest() (string, error) {
	c, err := r.digestOf(r.d.Version)
	if errors.Is(err, datasource.ErrVersionNotFound) {
		return "", &dep.SkipError{Reason: "current version " + r.d.Version + " not found upstream (pin an existing version)"}
	}
	return c, err
}

// Bounds of the retries per PR bucket: stale major candidates skipped before giving up,
// candidates held back for an unknown release time (timestamp-required) before giving up, and
// candidates rejected in total (stale, too new or undated).
const (
	maxOlderMajors  = 3
	maxUnknownTimes = 3
	maxRetries      = 50
)

// picks ranks the candidates once and chooses at most one per PR bucket (policies.bucket):
// the best one of the bucket that passes two release-time guards and the module path guard,
// each falling back to the bucket's next best candidate:
//   - a major update whose release predates the current version is a legacy tag
//     (k8s.io/client-go v1.5.2 from 2016 versus v0.37.1), not an upgrade; skipped for docker
//     and git-tags deps, see movingTimes. A datasource naming its latest release
//     (datasource.LatestSource, npm) decides it without release times, see aboveLatest;
//   - a candidate released less than the minimumReleaseAge of its update type ago is too new;
//     under minimumReleaseAgeBehaviour timestamp-required, so is one whose release time is
//     unknown;
//   - a Go module version whose go.mod declares another module path is no version of the
//     dep (see pathChange).
//
// Release times are only asked when a guard applies and the datasource has them, at most
// once per version; unknown times (zero or an error) only reject under timestamp-required.
// Declared module paths are only asked for a candidate that passed the time guards.
// The updates are returned lowest version first.
func (r *resolver) picks(scheme versioning.Scheme, names []string, accept func(string, dep.UpdateType) (bool, string), reject func(v, reason string)) []dep.Update {
	cur := r.cur
	timer, _ := r.ds.(datasource.Timer)
	above := r.aboveLatest(scheme)
	single := r.pol.singleBucket()
	if timer == nil && above == nil && r.declared == nil && single && !(r.pol.pre.MinAge > 0 && r.pol.pre.TimestampRequired) {
		// No guard can apply and there is one bucket: one O(n) pass without ranking.
		best, ok := scheme.Best(cur, names, accept, reject)
		if !ok {
			return nil
		}
		return []dep.Update{{Dep: r.d, Type: scheme.Kind(cur, best), NewVersion: best}}
	}
	ranked := scheme.Rank(cur, names, accept, reject) // parsed and filtered once
	type state struct {
		key                          bucketKey
		tries, older, unknown        int
		prefetched, pathsAsked, done bool
	}
	// next returns the versions of at most limit candidates after i in bucket key.
	next := func(i int, key bucketKey, limit int) []string {
		out := make([]string, 0, limit)
		for n := i + 1; n < ranked.Len() && len(out) < limit; n++ {
			if nc := ranked.At(n); r.pol.bucket(nc) == key {
				out = append(out, nc.Version)
			}
		}
		return out
	}
	states := make([]state, 0, 4) // a handful of buckets: a linear scan beats a map
	var out []dep.Update
	for i := range ranked.Len() {
		c := ranked.At(i)
		if above != nil && c.Kind == dep.Major && above(c.Version) {
			// Not a candidate at all, so it counts against no bucket bound.
			if reject != nil {
				reject(c.Version, reasonAboveLatest)
			}
			continue
		}
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
		reason := r.guard(timer, above == nil, c)
		if reason == "" {
			if why := r.pathChange(r.d.Name, c.Version); why != "" {
				if reject != nil {
					reject(c.Version, why)
				}
				if !st.pathsAsked {
					// A renamed module declares the new path in every later release: ask
					// for the bucket's next candidates concurrently.
					st.pathsAsked = true
					r.prefetchPaths(r.d.Name, next(i, key, min(prefetchWindow, maxRetries-st.tries)))
				}
				continue
			}
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
		if reason != reasonOlder {
			r.holdBack(r.d, c.Version, reason)
		}
		switch {
		case reason == reasonOlder:
			st.older++
			if st.older == maxOlderMajors {
				st.done = true
				continue
			}
		case strings.HasPrefix(reason, reasonUnknown):
			// The datasource may have no times for this dep at all: give up early, and
			// do not prefetch, which serves frequent releases.
			st.unknown++
			if st.unknown == maxUnknownTimes {
				st.done = true
			}
			continue
		}
		if !st.prefetched {
			// One rejection suggests a frequently released dependency: fetch the bucket's next
			// candidates' times concurrently instead of one round trip per candidate.
			st.prefetched = true
			r.prefetchTimes(timer, next(i, key, min(prefetchWindow, maxRetries-st.tries)))
		}
	}
	slices.Reverse(out)
	return out
}

// reasonOlder rejects a major candidate released before the current version.
const reasonOlder = "older than current"

// reasonAboveLatest rejects a major candidate above the major of the datasource's latest
// release (see aboveLatest).
const reasonAboveLatest = "above latest"

// reasonUnknown starts the reason of a candidate held back for its unknown release time.
const reasonUnknown = "release time unknown"

// movingTimes reports whether datasource's release times can be those of the content a
// floating tag points to now rather than of the version's first release, so they say nothing
// about which major is the legacy one and the older-than-current guard would reject real
// upgrades:
//   - docker image `created` stamps are build times: docker-library rebuilds every floating
//     tag within minutes (postgres:14 can be seconds newer than postgres:18);
//   - a floating git tag (actions/checkout v5) is dated by the release of the commit it points
//     to, which moves with every backport: v5's latest release can be minutes newer than v7's.
//
// The minimum release age still applies to them.
func movingTimes(ds string) bool { return ds == "docker" || ds == "git-tags" }

// aboveLatest reports, for a datasource naming its latest release (datasource.LatestSource:
// npm's `dist-tags.latest`), whether a version is in a major above latest's: a legacy or
// abandoned line (@stoplight/types 9999.9999.9999) or one not released as latest yet. Majors
// up to latest's are upgrades, so the older-than-current guard needs no release times, which
// on npm only the full packument has (vite's is 4.6 MB gzipped). nil, leaving the decision to
// the release times, when the datasource names no latest release valid under scheme or the
// current version is above it (a prerelease or `next` line).
func (r *resolver) aboveLatest(scheme versioning.Scheme) func(string) bool {
	ls, ok := r.ds.(datasource.LatestSource)
	if !ok {
		return nil
	}
	latest := ls.Latest(r.d)
	if latest == "" || !scheme.Valid(latest) || !scheme.Valid(r.cur) || scheme.Compare(r.cur, latest) > 0 {
		return nil
	}
	return func(v string) bool {
		if scheme.Compare(v, latest) <= 0 || scheme.Kind(latest, v) != dep.Major {
			return false
		}
		slog.DebugContext(r.ctx, "major candidate above latest", "name", r.d.Name, "current", r.d.Version, "latest", latest, "candidate", v)
		return true
	}
}

// guard returns why candidate c fails a release-time guard, or "". timer is nil when the
// datasource has no release times; byTimes is false when aboveLatest already decided which
// majors are legacy.
func (r *resolver) guard(timer datasource.Timer, byTimes bool, c versioning.Candidate) string {
	if byTimes && timer != nil && c.Kind == dep.Major && !movingTimes(r.d.Datasource) && r.olderThanCurrent(timer, c.Version) {
		slog.DebugContext(r.ctx, "major candidate older than current", "name", r.d.Name, "current", r.d.Version, "candidate", c.Version)
		return reasonOlder
	}
	p := r.pol.of(c.Kind)
	if p.MinAge <= 0 || timer != nil && r.releasedBefore(timer, r.now.Add(-p.MinAge)) {
		return ""
	}
	var tb time.Time
	if timer != nil {
		tb = r.releaseTime(timer, c.Version)
	}
	return r.ageReason(p, c.Version, tb)
}

// ageReason returns why version, released at t (zero = unknown), fails the minimum release
// age of policy p, or "": released less than p.MinAge before now, or, under timestamp-required,
// at an unknown time.
func (r *resolver) ageReason(p *config.Policy, version string, t time.Time) string {
	switch {
	case p.MinAge <= 0:
	case t.IsZero() && p.TimestampRequired:
		slog.DebugContext(r.ctx, "release time unknown, held back (timestamp-required)", "name", r.d.Name, "candidate", version)
		return fmt.Sprintf("%s (minimum %s, %s)", reasonUnknown, config.FormatAge(p.MinAge), config.TimestampRequired)
	case t.IsZero():
		slog.DebugContext(r.ctx, "release time unknown, minimum release age not enforced", "name", r.d.Name, "candidate", version)
	case r.now.Sub(t) < p.MinAge:
		// Every too-new candidate of a frequently released dep lands here (up to maxRetries
		// per bucket): skip boxing the log attributes when debug is off, and build the
		// reason without fmt.
		if slog.Default().Enabled(r.ctx, slog.LevelDebug) {
			slog.DebugContext(r.ctx, "candidate too new", "name", r.d.Name, "candidate", version, "released", t, "minimumReleaseAge", p.MinAge)
		}
		var day [len(time.DateOnly)]byte
		return "too new (released " + string(t.UTC().AppendFormat(day[:0], time.DateOnly)) + ", minimum " + config.FormatAge(p.MinAge) + ")"
	}
	return ""
}

// prefetchWindow is how many release times picks fetches concurrently once a candidate of a
// bucket was rejected; the datasource's request pool still bounds the requests.
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
			times[i] = r.lookupTime(timer, r.d, r.upstream(v))
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

// pathChange returns why version of module path name is no version of it: its go.mod
// declares another module path (datasource.DeclaredPathSource), which the go command refuses
// ("module declares its path as … but was required as …"), so go mod tidy would fail on
// the update. "" when it declares name or the path is unknown; a failed lookup is logged and
// left to go mod tidy. The first reason is kept for the dep's skip (versionUpdates).
func (r *resolver) pathChange(name, version string) string {
	if r.declared == nil {
		return ""
	}
	p, err := r.declared.DeclaredPath(r.ctx, name, version)
	switch {
	case err != nil:
		slog.DebugContext(r.ctx, "declared module path lookup failed", "name", name, "version", version, "err", err)
		return ""
	case p == "" || p == name:
		return ""
	}
	slog.DebugContext(r.ctx, "candidate declares another module path", "name", name, "candidate", version, "declared", p)
	reason := fmt.Sprintf("module path changed to %s (%s@%s)", p, name, version)
	if r.renamed == "" {
		r.renamed = reason
	}
	return reason
}

// prefetchPaths asks for the declared module paths of versions of name concurrently; the
// datasource dedupes them, so pathChange gets the answers without a round trip each.
func (r *resolver) prefetchPaths(name string, versions []string) {
	var wg sync.WaitGroup
	for _, v := range versions {
		wg.Go(func() { _, _ = r.declared.DeclaredPath(r.ctx, name, v) })
	}
	wg.Wait()
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

// releasedBefore reports whether the datasource bounds every release of the dep at or before
// t (see datasource.ReleaseBound), so no candidate can be too new and no time need be asked.
func (r *resolver) releasedBefore(timer datasource.Timer, t time.Time) bool {
	b, ok := timer.(datasource.ReleaseBound)
	if !ok {
		return false
	}
	bound := b.ReleasedBy(r.d)
	return !bound.IsZero() && !bound.After(t)
}

// applyAge evaluates the policies of a dep whose rules depend on the age of its current
// version (policies.aged): it looks up the release time of the version installed (the locked
// one when known) and re-evaluates them with its age, unknown when the datasource has no
// time. The trace gets the new pre-policy. It returns why the new policy skips the dep, "" if
// it does not. Other deps are left alone.
func (r *resolver) applyAge(t *Trace) string {
	if !r.pol.aged {
		return ""
	}
	age := config.UnknownAge
	if timer, ok := r.ds.(datasource.Timer); ok {
		if rt := r.releaseTime(timer, cmp.Or(r.d.Locked, r.d.Version)); !rt.IsZero() {
			age, t.Released = max(r.now.Sub(rt), 0), rt
		}
	}
	r.pol.setAge(age)
	t.AgeLookup, t.Rules, t.Policy = true, r.pol.pre.Rules, r.pol.pre
	return r.pol.skip(r.d)
}

// releaseTime returns when version was released, asking the datasource once per version;
// zero when unknown (errors are logged and treated as unknown).
func (r *resolver) releaseTime(timer datasource.Timer, version string) time.Time {
	if t, ok := r.times[version]; ok {
		return t
	}
	t := r.lookupTime(timer, r.d, r.upstream(version))
	if r.times == nil {
		r.times = make(map[string]time.Time, 2)
	}
	r.times[version] = t
	return t
}

// lookupTime asks timer for the release time of d at version; a failed lookup is logged
// and reported as zero (unknown).
func (r *resolver) lookupTime(timer datasource.Timer, d dep.Dep, version string) time.Time {
	t, err := timer.ReleaseTime(r.ctx, d, version)
	if err != nil {
		slog.DebugContext(r.ctx, "release time lookup failed", "name", d.Name, "version", version, "err", err)
		return time.Time{}
	}
	return t
}

// holdBack records d at version as the held-back candidate unless one is recorded already.
func (r *resolver) holdBack(d dep.Dep, version, reason string) {
	if r.pending.Version == "" {
		r.pending = Pending{Dep: d, Version: version, Reason: reason}
	}
}

// upstream returns the release version that version (as written into the file) stands for:
// a dep written without the release's "v" (a Renovate regex `v?(?<currentValue>…)` over
// go module or image tags `v1.9.0`) is looked up as `v1.9.0`, else as written.
func (r *resolver) upstream(version string) string {
	has := func(v string) bool {
		return slices.ContainsFunc(r.rels, func(rel datasource.Release) bool { return rel.Version == v })
	}
	if has(version) {
		return version
	}
	alt := "v" + version
	if s, ok := strings.CutPrefix(version, "v"); ok {
		alt = s
	}
	if has(alt) {
		return alt
	}
	return version
}

// digestOf returns the digest of version from the releases, asking the datasource when the
// release carries none. version matches a release exactly, else with a leading "v" added or
// removed (a `# 7.0.6` comment on a SHA pin of tag v7.0.6).
func (r *resolver) digestOf(version string) (string, error) {
	toggled, ok := strings.CutPrefix(version, "v")
	if !ok {
		toggled = "v" + version
	}
	for _, v := range [...]string{version, toggled} {
		for _, rel := range r.rels {
			if rel.Version == v && rel.Digest != "" {
				return rel.Digest, nil
			}
		}
	}
	return r.ds.Digest(r.ctx, r.d, r.upstream(version))
}

// checksumOf fetches the checksum of version, in the letter case and length of the current one.
func (r *resolver) checksumOf(version string) (string, error) {
	if r.cs == nil {
		return "", errors.New("no checksum source")
	}
	release := version
	if e, ok := r.ds.(releaseNamer); ok {
		release = e.name(version)
	}
	c, err := r.cs.Checksum(r.ctx, r.d, version, release)
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
	cur := r.installed(s)
	var fix string
	mins := make([]string, len(vulns))
	for i, v := range vulns {
		m := minFix(s, cur, v)
		mins[i] = m
		if m != "" && (fix == "" || s.Compare(m, fix) > 0) {
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
			// Compatible parses cur and n; the cheaper order checks run first.
			if s.Compare(n, fix) >= 0 && (chosen == "" || s.Compare(n, chosen) < 0) && s.Compatible(cur, n) {
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
	for i, v := range vulns {
		if m := mins[i]; m != "" && s.Compare(up.NewVersion, m) >= 0 {
			up.Advisories = append(up.Advisories, v.Advisory)
		}
	}
	return ups
}

// minFix returns v's lowest fixed version that is a compatible upgrade of cur, or "".
func minFix(s versioning.Scheme, cur string, v Vuln) string {
	var m string
	for _, f := range v.Fixed {
		if (m == "" || s.Compare(f, m) < 0) && s.Compatible(cur, f) {
			m = f
		}
	}
	return m
}

// sortUpdates orders updates by file, then line, keeping the input order
// of equal positions.
func sortUpdates(ups []dep.Update) {
	stableSortInPlace(ups, func(a, b *dep.Update) int {
		if c := strings.Compare(a.Dep.File, b.Dep.File); c != 0 {
			return c
		}
		return a.Dep.Line - b.Dep.Line
	})
}
