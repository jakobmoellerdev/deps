package gitlabci

import (
	"io/fs"
	"path/filepath"
	"testing"
)

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

// regexpMatch is Match without the literal checks that skip the regexps.
func regexpMatch(p string) bool {
	return ciFileRe.MatchString(p) || ciDirRe.MatchString(p)
}

func TestMatchParity(t *testing.T) {
	m := New()
	for _, p := range append(repoPaths(t), ".gitlab-ci.yml", "a/b.gitlab-ci.yaml", "gitlab-ci.yml", ".gitlab/ci/x/y.yml", "a/.gitlab/ci/z.yaml", ".gitlab/ci/", "gitlab-ci.txt") {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range []string{".gitlab-ci.yml", "a/b.gitlab-ci.yaml", "gitlab-ci.yml", ".gitlab/ci/x/y.yml", "a/.gitlab/ci/z.yaml", ".gitlab/ci/", "gitlab-ci.txt"} {
		f.Add(p)
	}
	m := New()
	f.Fuzz(func(t *testing.T, p string) {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	})
}
