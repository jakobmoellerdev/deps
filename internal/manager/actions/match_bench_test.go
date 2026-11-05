package actions

import (
	"io/fs"
	"path/filepath"
	"regexp"
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

// actionRe is the action file regexp Match replaced with a name check.
var actionRe = regexp.MustCompile(`(^|/)action\.ya?ml$`)

// regexpMatch is Match without the literal checks that skip the regexps.
func regexpMatch(p string) bool {
	return workflowRe.MatchString(p) || actionRe.MatchString(p)
}

var matchSeeds = []string{".github/workflows/ci.yml", ".gitea/actions/a/action.yaml", "workflow-templates/t.yml",
	"a/.forgejo/workflows/x.yaml", ".github/workflows/.yml", "action.yml", "x/action.yaml", "myaction.yml",
	"action.yml/x", ".github/dependabot.yml", "workflows/ci.yml", "actions/a.yml", ".github/actions/"}

func TestMatchParity(t *testing.T) {
	m := New()
	for _, p := range append(repoPaths(t), matchSeeds...) {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range matchSeeds {
		f.Add(p)
	}
	m := New()
	f.Fuzz(func(t *testing.T, p string) {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	})
}
