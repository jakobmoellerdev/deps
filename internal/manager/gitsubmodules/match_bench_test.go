package gitsubmodules

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"testing"
)

// fileRe is the regexp Match replaced.
var fileRe = regexp.MustCompile(`(^|/)\.gitmodules$`)

// repoPaths returns the slash path of every file of this repository, the mix discovery sees.
func repoPaths(tb testing.TB) []string {
	tb.Helper()
	var paths []string
	root := filepath.Join("..", "..", "..")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return paths
}

// BenchmarkMatch runs Match over every file path of this repository.
func BenchmarkMatch(b *testing.B) {
	paths := repoPaths(b)
	m := New()
	for b.Loop() {
		for _, p := range paths {
			m.Match(p)
		}
	}
}

var matchSeeds = []string{".gitmodules", "a/.gitmodules", "/.gitmodules", "a.gitmodules", ".gitmodulesx", "a/b/.gitmodules", ".gitmodules/x", "", "xgitmodules", "a/.gitmodules\n"}

func TestMatchParity(t *testing.T) {
	m := New()
	for _, p := range append(repoPaths(t), matchSeeds...) {
		if got, want := m.Match(p), fileRe.MatchString(p); got != want {
			t.Errorf("Match(%q) = %v, regexp says %v", p, got, want)
		}
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range matchSeeds {
		f.Add(p)
	}
	m := New()
	f.Fuzz(func(t *testing.T, p string) {
		if got, want := m.Match(p), fileRe.MatchString(p); got != want {
			t.Errorf("Match(%q) = %v, regexp says %v", p, got, want)
		}
	})
}
