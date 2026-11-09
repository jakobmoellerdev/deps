package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerated fails when replacements_gen.go is not what the vendored Renovate data
// generates: run `go generate ./internal/config` after changing either.
func TestGenerated(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", input))
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := generate(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", output))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("internal/config/replacements_gen.go is stale; run go generate ./internal/config")
	}
}

// TestTranslate covers the rules deps leaves out and the template form it translates.
func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		r          rule
		entry, why string
	}{
		{rule{MatchDatasources: []string{"nuget"}, MatchPackageNames: []string{"A"}, ReplacementName: "B"}, "", "datasources nuget not renamed by deps"},
		{rule{MatchDatasources: []string{"maven"}, MatchPackageNames: []string{"a:b"}, MatchCurrentVersion: "[3,4)", ReplacementName: "c:d"},
			`Managers: []string{"maven", "gradle"}, Names: []string{"a:b"}, CurrentVersion: ">=3, <4", To: "c:d"`, ""},
		{rule{MatchDatasources: []string{"maven"}, MatchPackageNames: []string{"a:b"}, MatchCurrentVersion: "[1,2),[3,4)", ReplacementName: "c:d"}, "", `matchCurrentVersion [1,2),[3,4): improper constraint: "[1,2),[3,4)"`},
		{rule{MatchDatasources: []string{"docker"}, MatchPackageNames: []string{"x"}, ReplacementName: "y", ExtractVersion: "^(?<version>.+)-slim$"}, "", "remaps versions"},
		{rule{MatchDatasources: []string{"docker"}, MatchPackageNames: []string{"x"}, ReplacementNameTemplate: "{{{lowercase packageName}}}"}, "", "replacementNameTemplate {{{lowercase packageName}}}"},
		{rule{MatchDatasources: []string{"docker"}, MatchPackageNames: []string{`/^k8s\.gcr\.io/.+$/`}, ReplacementNameTemplate: `{{{replace 'k8s\.gcr\.io/' 'registry.k8s.io/' packageName}}}`},
			`Managers: []string{"docker", "gitlabci", "kustomize", "terraform", "devcontainer", "bitbucket-pipelines", "woodpecker", "droneci", "circleci", "azure-pipelines", "quadlet", "crossplane"}, Patterns: []string{"^k8s\\.gcr\\.io/.+$"}, Prefix: "k8s.gcr.io/", To: "registry.k8s.io/"`, ""},
		{rule{MatchDatasources: []string{"github-releases", "github-tags"}, MatchPackageNames: []string{"a/b"}, MatchCurrentValue: "/^v1/", ReplacementName: "c/b", ReplacementVersion: "v2"},
			`Managers: []string{"github-actions"}, Names: []string{"a/b"}, CurrentVersion: "/^v1/", To: "c/b", Version: "v2"`, ""},
	} {
		entry, why := translate(tc.r)
		if entry != tc.entry || why != tc.why {
			t.Errorf("%+v: %q %q, want %q %q", tc.r, entry, why, tc.entry, tc.why)
		}
	}
}
