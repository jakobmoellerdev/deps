package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// benchSource serves n versions per dependency from memory, with release times one day apart
// (newest = now), so the engine's selection, policy and age paths dominate the profile.
type benchSource struct {
	rels []Release
	now  time.Time
	pfx  string
}

func newBenchSource(n int, prefix string, now time.Time) benchSource {
	rels := make([]Release, 0, n)
	for i := n - 1; i >= 0; i-- {
		rels = append(rels, Release{Version: fmt.Sprintf("%s%d.%d.%d", prefix, i/100, (i/10)%10, i%10), Digest: strings.Repeat("a", 40)})
	}
	return benchSource{rels: rels, now: now, pfx: prefix}
}

func (s benchSource) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return s.rels, nil
}
func (benchSource) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }
func (s benchSource) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	for i, r := range s.rels {
		if r.Version == v {
			return s.now.Add(-time.Duration(i) * 24 * time.Hour), nil
		}
	}
	return time.Time{}, nil
}

// BenchmarkResolve resolves a synthetic repository shaped like open-component-model (four
// go.mod files with 150 requirements each, 30 workflows with 10 actions each) against
// in-memory datasources with 600 Go and 80 action versions, including a 28-day release age.
func BenchmarkResolve(b *testing.B) {
	files := map[string]string{}
	for m := range 4 {
		var mod strings.Builder
		fmt.Fprintf(&mod, "module example.com/m%d\n\ngo 1.24\n\nrequire (\n", m)
		for i := range 150 {
			fmt.Fprintf(&mod, "\texample.com/dep%d v1.0.%d\n", i, i%10)
		}
		mod.WriteString(")\n")
		files[fmt.Sprintf("m%d/go.mod", m)] = mod.String()
	}
	for w := range 30 {
		var wf strings.Builder
		wf.WriteString("on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n")
		for i := range 10 {
			fmt.Fprintf(&wf, "      - uses: org/action%d@v0.%d.0\n", i, i)
		}
		files[fmt.Sprintf(".github/workflows/w%d.yml", w)] = wf.String()
	}
	root, cfg := setup(b, "minimumReleaseAge: 28 days\nrules:\n  - names: ['example.com/dep1*']\n    allow: '< 5'\n", files)
	now := time.Now()
	o := Options{
		Managers: []manager.Manager{actions.New(), gomod.New()},
		Datasources: map[string]Datasource{
			"go":       newBenchSource(600, "v", now),
			"git-tags": newBenchSource(80, "v", now),
		},
		Now: now,
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(prev) })
	b.ReportAllocs()
	for b.Loop() {
		res, err := Resolve(b.Context(), root, cfg, env, o)
		if err != nil || len(res.Errors) > 0 || len(res.Updates) == 0 {
			b.Fatalf("err = %v, errors = %v, updates = %d", err, res.Errors, len(res.Updates))
		}
	}
}

// BenchmarkDiscover walks a synthetic tree of 20,000 files (200 directories, four levels deep)
// with the default ignore globs, so the per-entry path handling dominates the profile.
func BenchmarkDiscover(b *testing.B) {
	root := b.TempDir()
	for d := range 200 {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", d%10), fmt.Sprintf("sub%d", d/10%5), fmt.Sprintf("leaf%d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for f := range 100 {
			name := fmt.Sprintf("file%d.go", f)
			if f == 0 {
				name = "go.mod"
			}
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	managers := []manager.Manager{suffixManager{}}
	b.ReportAllocs()
	for b.Loop() {
		got, err := discover(root, config.DefaultIgnorePaths, true, managers)
		if err != nil || len(got[0]) != 200 {
			b.Fatalf("discovered %d files, err %v", len(got[0]), err)
		}
	}
}

// BenchmarkDiscoverManagers walks a synthetic monorepo of 12,000 files, mostly sources and
// docs with some manifests, workflows and YAML, and matches every path against all built-in
// managers with the default ignore globs, as `deps plan` does on a large JavaScript repository.
func BenchmarkDiscoverManagers(b *testing.B) {
	root := b.TempDir()
	names := []string{"index.ts", "index.test.tsx", "README.md", "CHANGELOG.md", "util.js", "styles.css",
		"config.yaml", "api-report.md", "schema.json", "logo.svg"}
	for d := range 400 {
		dir := filepath.Join(root, "packages", fmt.Sprintf("pkg%d", d%40), "src", fmt.Sprintf("dir%d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for f := range 30 {
			name := fmt.Sprintf("%d-%s", f, names[f%len(names)])
			switch f {
			case 0:
				name = "package.json"
			case 1:
				name = "Dockerfile"
			}
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), nil, 0o644); err != nil {
		b.Fatal(err)
	}
	managers, err := Managers(config.Default())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		got, err := discover(root, config.DefaultIgnorePaths, true, managers)
		if err != nil || len(got[0]) != 1 {
			b.Fatalf("discovered %d workflows, err %v", len(got[0]), err)
		}
	}
}

// BenchmarkExtractReleases maps the 600 release names of one package to versions for the 100
// deps of a monorepo that pin it with the same extractVersion.
func BenchmarkExtractReleases(b *testing.B) {
	src := newBenchSource(600, "jq-", time.Now())
	x, err := versioning.CompileExtract(`^jq-(?<version>.+)$`)
	if err != nil {
		b.Fatal(err)
	}
	d := dep.Dep{Name: "jq", Version: "1.7.1"}
	b.ReportAllocs()
	for b.Loop() {
		// A fresh list per iteration: a run looks up the datasource once, not once per b.Loop.
		shared := benchSource{rels: slices.Clone(src.rels), now: src.now, pfx: src.pfx}
		for range 100 {
			rels, err := (&extracting{ds: shared, x: x}).Releases(b.Context(), d, "")
			if err != nil || len(rels) != len(src.rels) {
				b.Fatalf("releases = %d, err = %v", len(rels), err)
			}
		}
	}
}

// BenchmarkResultSkipDep marks 200 of the 3,000 deps of a resolved monorepo as skipped, as
// `deps plan` does for the updates a group leaves out.
func BenchmarkResultSkipDep(b *testing.B) {
	res := Result{Deps: make([]dep.Dep, 3000)}
	for i := range res.Deps {
		res.Deps[i] = dep.Dep{
			Manager: gomod.Name, Datasource: "go", File: fmt.Sprintf("m%d/go.mod", i/150),
			Line: i%150 + 5, Name: fmt.Sprintf("example.com/dep%d", i), Version: "v1.0.0",
		}
	}
	skip := make([]dep.Dep, 0, 200)
	for i := 0; i < len(res.Deps); i += 15 {
		skip = append(skip, res.Deps[i])
	}
	b.ReportAllocs()
	for b.Loop() {
		res.Skipped = res.Skipped[:0]
		for _, d := range skip {
			res.SkipDep(d, "superseded by another update")
		}
	}
}
