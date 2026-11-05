package maven

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

type runFixture struct {
	file string
	src  []byte
}

func runFixtures(tb testing.TB) []runFixture {
	tb.Helper()
	m := New()
	var out []runFixture
	err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := filepath.ToSlash(p)
		if !m.Match(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		out = append(out, runFixture{file: rel, src: b})
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

// BenchmarkExtractLink extracts every matched fixture and links them, as one run does.
func BenchmarkExtractLink(b *testing.B) {
	fixtures := runFixtures(b)
	b.ReportAllocs()
	for b.Loop() {
		m := New()
		all := make([][]dep.Dep, len(fixtures))
		for i, f := range fixtures {
			ds, err := m.Extract(f.file, f.src, manager.Env{})
			if err != nil {
				b.Fatal(err)
			}
			all[i] = ds
		}
		m.(manager.Linker).Link(all)
	}
}
