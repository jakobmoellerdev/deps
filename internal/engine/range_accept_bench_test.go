package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// rangeSource serves the same releases for every dependency and has no release times, so
// candidate acceptance dominates the profile.
type rangeSource struct{ rels []Release }

func (s rangeSource) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return s.rels, nil
}
func (rangeSource) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// BenchmarkRangeAccept resolves 50 npm `^1.0.0` dependencies locked at 1.0.0 against 500
// versions each (0.0.0 to 4.9.9) under several rangeStrategy settings, one with majors not
// allowed, so every newer candidate goes through the range and policy checks of accept.
func BenchmarkRangeAccept(b *testing.B) {
	var pkg, lock strings.Builder
	pkg.WriteString("{\n  \"dependencies\": {\n")
	lock.WriteString(`{"lockfileVersion": 3, "packages": {`)
	for i := range 50 {
		if i > 0 {
			pkg.WriteString(",\n")
			lock.WriteString(", ")
		}
		fmt.Fprintf(&pkg, "    \"dep%d\": \"^1.0.0\"", i)
		fmt.Fprintf(&lock, `"node_modules/dep%d": {"version": "1.0.0"}`, i)
	}
	pkg.WriteString("\n  }\n}\n")
	lock.WriteString("}}\n")
	src := newBenchSource(500, "", time.Now())
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(prev) })
	for _, bc := range []struct{ name, cfg string }{
		{config.RangeWiden, "rangeStrategy: widen\n"},
		{config.RangeUpdateLockfile, "rangeStrategy: update-lockfile\n"},
		{"update-lockfile-no-major", "rangeStrategy: update-lockfile\nrules: [{allowedUpdateTypes: [minor, patch]}]\n"},
		{config.RangeBump, "rangeStrategy: bump\n"},
		{config.RangeInRangeOnly, "rangeStrategy: in-range-only\n"},
	} {
		b.Run(bc.name, func(b *testing.B) {
			root, cfg := setup(b, bc.cfg, map[string]string{"package.json": pkg.String(), "package-lock.json": lock.String()})
			o := Options{
				Managers:    []manager.Manager{npm.New()},
				Datasources: map[string]Datasource{"npm": rangeSource{rels: src.rels}},
			}
			b.ReportAllocs()
			for b.Loop() {
				res, err := Resolve(b.Context(), root, cfg, env, o)
				if err != nil || len(res.Errors) > 0 || len(res.Updates) == 0 {
					b.Fatalf("err = %v, errors = %v, updates = %d", err, res.Errors, len(res.Updates))
				}
			}
		})
	}
}
