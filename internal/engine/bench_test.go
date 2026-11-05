package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
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
		got, err := discover(root, config.DefaultIgnorePaths, managers)
		if err != nil || len(got[0]) != 200 {
			b.Fatalf("discovered %d files, err %v", len(got[0]), err)
		}
	}
}
