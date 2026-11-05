package gradle

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// scriptFixture is a Groovy or Kotlin build script or source of testdata.
type scriptFixture struct {
	file string
	src  []byte
}

// scriptFixtures returns the build scripts, settings files and Kotlin/Groovy sources of
// testdata by slash path.
func scriptFixtures(tb testing.TB) []scriptFixture {
	tb.Helper()
	var out []scriptFixture
	err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".gradle", ".kts", ".kt", ".groovy":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, scriptFixture{file: filepath.ToSlash(p), src: b})
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	if len(out) == 0 {
		tb.Fatal("no script fixtures")
	}
	return out
}

// BenchmarkExtractScripts extracts every Groovy/Kotlin script fixture once per op.
func BenchmarkExtractScripts(b *testing.B) {
	fixtures := scriptFixtures(b)
	g := New()
	b.ReportAllocs()
	for b.Loop() {
		for _, f := range fixtures {
			if _, err := g.Extract(f.file, f.src, manager.Env{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
