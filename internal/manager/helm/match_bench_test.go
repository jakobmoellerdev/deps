package helm

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
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

// BenchmarkExtract runs Extract over every file of this repository that Match accepts.
func BenchmarkExtract(b *testing.B) {
	type file struct {
		path string
		src  []byte
	}
	var files []file
	for _, p := range repoPaths(b) {
		if !New().Match(p) {
			continue
		}
		src, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(p)))
		if err != nil {
			b.Fatal(err)
		}
		files = append(files, file{p, src})
	}
	for b.Loop() {
		m := New()
		for _, f := range files {
			_, _ = m.Extract(f.path, f.src, manager.Env{})
		}
	}
}

// yamlRe is the YAML file regexp Match replaced with suffix checks.
var yamlRe = regexp.MustCompile(`\.ya?ml$`)

// regexpMatch is Match without the literal checks that skip the regexps.
func regexpMatch(p string) bool {
	return helmfileRe.MatchString(p) || yamlRe.MatchString(p)
}

var matchSeeds = []string{"Chart.yaml", "helmfile.yaml", "a/helmfile.yaml.gotmpl", "helmfile.d/a.yml.gotmpl", "x.gotmpl", "a/values.yml", "b.txt"}

func checkMatch(t *testing.T, m manager.Manager, p string) {
	t.Helper()
	if got, want := m.Match(p), regexpMatch(p); got != want {
		t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
	}
	if got, want := isHelmfile(p), helmfileRe.MatchString(p); got != want {
		t.Errorf("isHelmfile(%q) = %v, helmfileRe says %v", p, got, want)
	}
}

func TestMatchParity(t *testing.T) {
	m := New()
	for _, p := range append(repoPaths(t), matchSeeds...) {
		checkMatch(t, m, p)
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range matchSeeds {
		f.Add(p)
	}
	m := New()
	f.Fuzz(func(t *testing.T, p string) { checkMatch(t, m, p) })
}
