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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// TagSource lists the tags of a git repository.
type TagSource interface {
	Tags(ctx context.Context, repoURL string) ([]gittags.Tag, error)
}

// GoSource lists the published versions of a Go module.
type GoSource interface {
	Versions(ctx context.Context, modPath, dir string) ([]string, error)
}

// Tidier runs `go mod tidy` on a copy of go.mod/go.sum.
type Tidier interface {
	Tidy(ctx context.Context, dir string, mod, sum []byte) ([]byte, []byte, error)
}

// Sources bundles the datasources used by Resolve.
type Sources struct {
	Tags TagSource
	Go   GoSource
}

// Result is the outcome of Resolve.
type Result struct {
	Updates []dep.Update
	Skipped []dep.Dep
	Errors  []error
}

type job struct {
	file    string
	manager string
}

type extracted struct {
	deps []dep.Dep
	err  error
}

// Resolve discovers dependency files under root, extracts dependencies and looks up updates.
// The returned error is non-nil only for walk failures or context cancellation;
// per-file and per-dependency failures are collected in Result.Errors.
func Resolve(ctx context.Context, root string, cfg *config.Config, defaultHost string, src Sources) (*Result, error) {
	start := time.Now()
	found, err := discover(root, cfg.IgnorePaths)
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	slog.InfoContext(ctx, "files discovered", "actions", len(found.actions), "gomod", len(found.gomod), "duration", time.Since(start))
	jobs := make([]job, 0, len(found.actions)+len(found.gomod))
	for _, f := range found.actions {
		jobs = append(jobs, job{f, actions.Name})
	}
	for _, f := range found.gomod {
		jobs = append(jobs, job{f, gomod.Name})
	}

	start = time.Now()
	ex := extractAll(root, jobs, defaultHost)

	res := &Result{}
	var todo []dep.Dep
	var policies []config.Policy
	for i, e := range ex {
		if e.err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", jobs[i].file, e.err))
			continue
		}
		slog.DebugContext(ctx, "file extracted", "file", jobs[i].file, "manager", jobs[i].manager, "deps", len(e.deps))
		for _, d := range e.deps {
			if d.Skip != "" {
				res.Skipped = append(res.Skipped, d)
				continue
			}
			p := cfg.Policy(d)
			if p.Ignore {
				d.Skip = "ignored by rule"
				res.Skipped = append(res.Skipped, d)
				continue
			}
			todo = append(todo, d)
			policies = append(policies, p)
		}
	}
	slog.InfoContext(ctx, "dependencies extracted", "files", len(jobs), "updatable", len(todo), "skipped", len(res.Skipped), "duration", time.Since(start))
	start = time.Now()

	type out struct {
		up  *dep.Update
		err error
	}
	outs := make([]out, len(todo))
	var wg sync.WaitGroup
	for i := range todo {
		wg.Go(func() {
			d := todo[i]
			var up *dep.Update
			var err error
			switch d.Manager {
			case actions.Name:
				up, err = resolveAction(ctx, d, policies[i], src.Tags)
			case gomod.Name:
				up, err = resolveGo(ctx, root, d, policies[i], src.Go)
			}
			if err != nil {
				err = fmt.Errorf("%s:%d: %s: %w", d.File, d.Line, d.Name, err)
			} else if up == nil {
				slog.DebugContext(ctx, "dependency up to date", "file", d.File, "line", d.Line, "name", d.Name, "version", d.Version)
			} else {
				slog.DebugContext(ctx, "update found", "file", d.File, "line", d.Line, "name", d.Name, "from", d.Version, "to", up.NewVersion, "type", up.Type)
			}
			outs[i] = out{up, err}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, o := range outs {
		if o.err != nil {
			res.Errors = append(res.Errors, o.err)
		} else if o.up != nil {
			res.Updates = append(res.Updates, *o.up)
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
func extractAll(root string, jobs []job, defaultHost string) []extracted {
	ex := make([]extracted, len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(jobs)) {
		wg.Go(func() {
			for i := range next {
				ex[i] = extractOne(root, jobs[i], defaultHost)
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

func extractOne(root string, j job, defaultHost string) extracted {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(j.file)))
	if err != nil {
		return extracted{err: err}
	}
	var deps []dep.Dep
	switch j.manager {
	case actions.Name:
		deps, err = actions.Extract(j.file, src, defaultHost)
	case gomod.Name:
		dir := filepath.Join(root, filepath.FromSlash(path.Dir(j.file)))
		_, statErr := os.Stat(filepath.Join(dir, "vendor", "modules.txt"))
		deps, err = gomod.Extract(j.file, src, statErr == nil)
	}
	return extracted{deps: deps, err: err}
}

func resolveAction(ctx context.Context, d dep.Dep, p config.Policy, ts TagSource) (*dep.Update, error) {
	cur, ok := versioning.ParseTag(d.Version)
	if !ok {
		return nil, fmt.Errorf("unparsable version %q", d.Version)
	}
	tags, err := ts.Tags(ctx, d.Source)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	commitOf := func(name string) (string, bool) {
		for _, t := range tags {
			if t.Name == name {
				return t.Commit, true
			}
		}
		return "", false
	}
	pinned := d.Digest != ""
	best, found := versioning.BestTag(cur, names, func(t versioning.Tag) bool {
		return p.Accepts(versioning.TagKind(cur, t), t.Semver())
	})
	if found {
		next, _ := versioning.ParseTag(best)
		up := &dep.Update{Dep: d, Type: versioning.TagKind(cur, next), NewVersion: best}
		if pinned || p.Pin == "digest" {
			if up.NewDigest, ok = commitOf(best); !ok {
				return nil, fmt.Errorf("tag %s not found upstream", best)
			}
		}
		return up, nil
	}
	switch {
	case pinned && p.Updates[dep.Digest]:
		c, ok := commitOf(d.Version)
		if !ok {
			return nil, fmt.Errorf("tag %s not found upstream", d.Version)
		}
		if strings.EqualFold(c, d.Digest) {
			return nil, nil
		}
		return &dep.Update{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: c}, nil
	case !pinned && p.Pin == "digest" && p.Updates[dep.Pin]:
		c, ok := commitOf(d.Version)
		if !ok {
			return nil, fmt.Errorf("tag %s not found upstream", d.Version)
		}
		return &dep.Update{Dep: d, Type: dep.Pin, NewVersion: d.Version, NewDigest: c}, nil
	}
	return nil, nil
}

func resolveGo(ctx context.Context, root string, d dep.Dep, p config.Policy, gs GoSource) (*dep.Update, error) {
	vs, err := gs.Versions(ctx, d.Name, filepath.Join(root, filepath.FromSlash(path.Dir(d.File))))
	if err != nil {
		return nil, err
	}
	best, ok := versioning.BestGo(d.Version, vs, func(v string, kind dep.UpdateType) bool {
		return p.Accepts(kind, versioning.GoSemver(v))
	})
	if !ok {
		return nil, nil
	}
	return &dep.Update{Dep: d, Type: versioning.GoKind(d.Version, best), NewVersion: best}, nil
}
