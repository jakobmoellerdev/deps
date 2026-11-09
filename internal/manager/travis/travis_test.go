package travis

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                      int
	version, versioning, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".travis.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.DepType != DepTypeNode || d.Name != "node" || d.Datasource != "node-version" {
			t.Errorf("%d: %s/%s/%s/%s", i, d.Manager, d.DepType, d.Name, d.Datasource)
		}
		if d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Line != w.line || d.Skip != w.skip || w.skip == "" && (d.Version != w.version || d.Versioning != w.versioning) {
			t.Errorf("%d: got line=%d version=%q versioning=%q skip=%q\nwant %+v", i, d.Line, d.Version, d.Versioning, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		".travis.yml":     true,
		".travis.yaml":    true,
		"sub/.travis.yml": false,
		"travis.yml":      false,
		".travis.yml.bak": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`language: node_js
node_js:
  - node
  - 22
  - "20.11"
  - 'v18.20.4'
  - lts/*
  - ">=20 <22"
jobs:
  include:
    - node_js: '24'
    - node_js: [26, "\x32\x34"]
    - script: skip
matrix:
  include:
    - node_js: 20.x
`)
	check(t, extract(t, src), []want{
		{line: 3, skip: "non-version value"},
		{line: 4, version: "22", versioning: "tag"},
		{line: 5, version: "20.11", versioning: "tag"},
		{line: 6, version: "v18.20.4", versioning: "semver"},
		{line: 7, skip: "non-version value"},
		{line: 8, skip: "unsupported range"},
		{line: 11, version: "24", versioning: "tag"},
		{line: 12, version: "26", versioning: "tag"},
		{line: 12, skip: "cannot locate value"},
		{line: 16, version: "20", versioning: "tag"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "node_js: {a: 1}\njobs: [a]\nmatrix: {include: x}\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if _, err := New().Extract(".travis.yml", []byte("node_js: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are real .travis.yml files: thomasdondorf/puppeteer-cluster (a quoted list
// and jobs.include), petkaantonov/bluebird (matrix.include only, 0.x versions) and
// cisco/node-jose (lts aliases).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		wants []want
	}{
		{"testdata/puppeteer-cluster.yml", []want{
			{line: 13, version: "10", versioning: "tag"},
			{line: 14, version: "11", versioning: "tag"},
			{line: 15, version: "12", versioning: "tag"},
			{line: 16, version: "13", versioning: "tag"},
			{line: 26, version: "14", versioning: "tag"},
			{line: 30, version: "14", versioning: "tag"},
		}},
		{"testdata/bluebird.yml", []want{
			{line: 5, version: "0.10", versioning: "tag"},
			{line: 6, version: "0.12", versioning: "tag"},
			{line: 7, version: "4", versioning: "tag"},
			{line: 8, version: "5", versioning: "tag"},
			{line: 9, version: "6", versioning: "tag"},
			{line: 10, version: "7", versioning: "tag"},
			{line: 11, version: "8", versioning: "tag"},
			{line: 12, version: "10", versioning: "tag"},
		}},
		{"testdata/node-jose.yml", []want{
			{line: 4, skip: "non-version value"},
			{line: 5, skip: "non-version value"},
			{line: 6, skip: "non-version value"},
			{line: 11, skip: "non-version value"},
			{line: 16, skip: "non-version value"},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src), tt.wants)
		})
	}
}

func TestEdits(t *testing.T) {
	src := []byte("node_js:\n  - '20.11'\n")
	deps := extract(t, src)
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "20.19"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "node_js:\n  - '20.19'\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}
