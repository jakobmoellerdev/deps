package tomlspan

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtures returns the TOML manifests and lockfiles in the testdata of the packages built on
// tomlspan.
func fixtures(tb testing.TB) [][]byte {
	tb.Helper()
	var srcs [][]byte
	for _, pat := range []string{
		"../manager/cargo/testdata/*.toml",
		"../manager/cargo/testdata/*.lock",
		"../manager/gradle/testdata/*.toml",
		"../manager/pep621/testdata/*.toml",
		"../manager/poetry/testdata/*.toml",
		"../pytool/testdata/*/*.toml",
		"../pytool/testdata/*/*.lock",
	} {
		files, err := filepath.Glob(pat)
		if err != nil {
			tb.Fatal(err)
		}
		for _, f := range files {
			if filepath.Base(f) == "Pipfile.lock" {
				continue // JSON
			}
			src, err := os.ReadFile(f)
			if err != nil {
				tb.Fatal(err)
			}
			srcs = append(srcs, src)
		}
	}
	if len(srcs) == 0 {
		tb.Fatal("no fixtures")
	}
	return srcs
}

// BenchmarkParseFixtures parses every TOML fixture of the managers and lock readers.
func BenchmarkParseFixtures(b *testing.B) {
	srcs := fixtures(b)
	b.ReportAllocs()
	for b.Loop() {
		for _, src := range srcs {
			if _, err := Parse(src); err != nil {
				b.Fatal(err)
			}
		}
	}
}
