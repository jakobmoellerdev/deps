package docker

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
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

// Renovate's dockerfile managerFilePatterns, which Match implements with name checks;
// TestMatchParity and FuzzMatch pin Match to them.
var (
	dockerfileRe    = regexp.MustCompile(`(^|/|\.)([Dd]ocker|[Cc]ontainer)file$`)
	dockerfileExtRe = regexp.MustCompile(`(^|/)([Dd]ocker|[Cc]ontainer)file[^/]*$`)
	yamlRe          = regexp.MustCompile(`\.ya?ml$`)
)

// regexpMatch is Match without the literal checks that skip the regexps.
func regexpMatch(p string) bool {
	return (dockerfileRe.MatchString(p) || dockerfileExtRe.MatchString(p)) && !strings.HasSuffix(p, ".dockerignore") ||
		yamlRe.MatchString(p)
}

func TestMatchParity(t *testing.T) {
	m := New()
	for _, p := range append(repoPaths(t), "Dockerfile", "a/Dockerfile.dev", "x.Containerfile", "Dockerfile.dockerignore", "a/b.yaml", "c.yml", "NotDockerfileX/y", "Dockerfile/x", "Dockerfile-prod", "dockerfile", "docker/Dockerfile-arm64", "Containerfile-arm") {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	}
}

func FuzzMatch(f *testing.F) {
	for _, p := range []string{"Dockerfile", "a/Dockerfile.dev", "x.Containerfile", "Dockerfile.dockerignore", "a/b.yaml", "c.yml", "NotDockerfileX/y", "Dockerfile/x"} {
		f.Add(p)
	}
	m := New()
	f.Fuzz(func(t *testing.T, p string) {
		if got, want := m.Match(p), regexpMatch(p); got != want {
			t.Errorf("Match(%q) = %v, regexps say %v", p, got, want)
		}
	})
}
