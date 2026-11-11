package engine

import (
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// corpus returns a large, varied set of slash paths: every path of this repository (testdata
// trees included, so the managers' own fixtures are covered) plus names that exercise case,
// extensions and dot-files.
func corpus(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "Dockerfile", "a/Dockerfile.dev", "dev.Containerfile", "DOCKERFILE",
		"go.mod", "a/b/go.sum", "Chart.yaml", "chart.YAML", "values.yml", "deno.jsonc",
		".vela.yml", "cloudbuild.yaml", "vcpkg-configuration.json", "Gemfile", "Gemfile.lock",
		"Cargo.toml", "pyproject.toml", "requirements.txt", "x.tf", "y.tofu", "build.gradle.kts",
		".gitlab-ci.yml", ".pre-commit-config.yaml", "a/.tool-versions", "flake.nix", "noext",
		".hidden", "a.b.c.yaml", "sub/dir/package.json", "project.clj", "Mintfile", "x.py",
		// Base names that are only an extension, and spellings the hints must not miss.
		".yaml", ".yml", ".json", ".config", ".py", ".txt", ".tf", ".sbt", ".scala", ".gemspec",
		"-pipelines.yml", "MODULE.bazel", "x.MODULE.bazel", ".bazelversion", "tasks/main.yml",
		".circleci/x.yml", ".buildkite/x.yaml", ".github/workflows/ci.yaml", "nuget.config",
		"NuGet.Config", "a/.copier-answers.x.yml", "helmfile.yaml.gotmpl", "a/kustomization.yaml",
		"azure-pipelines.yml", "a.main.kts", "gradle/libs.versions.toml", "x.container")
	if len(paths) < 500 {
		t.Fatalf("corpus too small: %d", len(paths))
	}
	return paths
}

// TestDispatchParity: for every manager and path, the dispatch index offers the manager every
// path its Match accepts. A manager whose MatchHints are narrower than its Match fails here.
func TestDispatchParity(t *testing.T) {
	managers, err := Managers(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	disp := newDispatch(managers)
	paths := corpus(t)
	for _, p := range paths {
		got := map[int]bool{}
		for _, i := range disp.candidates(p) {
			if managers[i].Match(p) {
				got[i] = true
			}
		}
		for i, m := range managers {
			if m.Match(p) && !got[i] {
				t.Errorf("%s: Match(%q) is true but its MatchHints exclude the path", m.Name(), p)
			}
		}
	}
}

// TestDispatchNarrowsCandidates: the index must actually filter, or it buys nothing. Source
// files no manager claims should reach only a handful of managers.
func TestDispatchNarrowsCandidates(t *testing.T) {
	managers, err := Managers(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	disp := newDispatch(managers)
	for _, p := range []string{"internal/engine/discover.go", "docs/design.md", "a/b/logo.svg"} {
		if n := len(disp.candidates(p)); n > len(managers)/3 {
			t.Errorf("%q reaches %d of %d managers", p, n, len(managers))
		}
	}
	// Candidates come in manager order and without duplicates.
	cands := slices.Clone(disp.candidates("values.yaml"))
	if !slices.IsSorted(cands) || len(slices.Compact(slices.Clone(cands))) != len(cands) {
		t.Errorf("candidates = %v", cands)
	}
}
