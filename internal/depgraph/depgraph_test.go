package depgraph

import (
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestPURL(t *testing.T) {
	for _, tc := range []struct {
		d    dep.Dep
		want string // "" = none
	}{
		{dep.Dep{Datasource: "go", Name: "github.com/spf13/cobra", Version: "v1.10.2"}, "pkg:golang/github.com/spf13/cobra@v1.10.2"},
		{dep.Dep{Datasource: "npm", Name: "@types/node", Version: "1.0.0", Range: "^1.0.0", Locked: "1.2.0"}, "pkg:npm/%40types/node@1.2.0"},
		{dep.Dep{Datasource: "npm", Name: "left-pad", Version: "1.0.0", Range: "^1.0.0"}, ""},
		{dep.Dep{Datasource: "pypi", Name: "Django", Version: "5.1"}, "pkg:pypi/django@5.1"},
		{dep.Dep{Datasource: "crate", Name: "serde", Version: "1.0.200"}, "pkg:cargo/serde@1.0.200"},
		{dep.Dep{Datasource: "maven", Name: "com.google.guava:guava", Version: "33.0.0-jre"}, "pkg:maven/com.google.guava/guava@33.0.0-jre"},
		{dep.Dep{Datasource: "maven", Name: "nocolon", Version: "1"}, ""},
		{dep.Dep{Datasource: "github-releases", Name: "cli/cli", Version: "v2.60.0"}, "pkg:github/cli/cli@v2.60.0"},
		{dep.Dep{Datasource: "github-releases", Name: "https://ghes.example/o/r", Version: "v1"}, ""},
		{dep.Dep{Datasource: "docker", Name: "alpine", Version: "3.20"}, "pkg:docker/library/alpine@3.20"},
		{dep.Dep{Datasource: "docker", Name: "ghcr.io/org/img", Version: "v1"}, "pkg:docker/org/img@v1?repository_url=ghcr.io"},
		{dep.Dep{Manager: "github-actions", DepType: "action", Datasource: "git-tags", Name: "actions/cache/save", Version: "v4.2.0"}, "pkg:githubactions/actions/cache@v4.2.0"},
		{dep.Dep{Datasource: "helm", Name: "cert-manager", Version: "1.0.0"}, ""},
		{dep.Dep{Datasource: "docker", Name: "x", Version: "${TAG}"}, ""},
		{dep.Dep{Datasource: "terraform-provider", Name: "hashicorp/aws", Version: "5.0.0"}, ""},
	} {
		got, ok := PURL(tc.d)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("PURL(%s %s %s) = %q %v, want %q", tc.d.Datasource, tc.d.Name, tc.d.Version, got, ok, tc.want)
		}
	}
}

func TestBuild(t *testing.T) {
	deps := []dep.Dep{
		{Manager: "gomod", File: "go.mod", Datasource: "go", Name: "a.b/c", Version: "v1.0.0"},
		{Manager: "docker", File: "Dockerfile", Datasource: "docker", Name: "alpine", Version: "3.20"},
		{Manager: "docker", File: "Dockerfile", Datasource: "docker", Name: "alpine", Version: "3.20"},
		{Manager: "docker", File: "Dockerfile", Datasource: "docker", Name: "golang", Version: "1.25", Skip: "ignored by rule #0"},
		{Manager: "docker", File: "Dockerfile", Datasource: "docker", Name: "$BASE", Skip: "variable reference"},
		{Manager: "gradle", File: "build.gradle", DepType: "test", Datasource: "maven", Name: "junit:junit", Version: "4.13.2"},
	}
	o := Options{Job: Job{ID: "1", Correlator: "c"}, SHA: "s", Ref: "refs/heads/main", Detector: Detector{Name: "deps"}, Scanned: time.Date(2026, 11, 5, 9, 0, 0, 0, time.FixedZone("x", 3600))}
	s, n := Build(deps, o)
	if n != 3 || s.Scanned != "2026-11-05T08:00:00Z" {
		t.Fatalf("n = %d, scanned %s, manifests %+v", n, s.Scanned, s.Manifests)
	}
	if got := s.Files(); len(got) != 2 || got[0] != "Dockerfile" || got[1] != "build.gradle" {
		t.Errorf("files = %v (go.mod must be left out)", got)
	}
	if p := s.Manifests["build.gradle"].Resolved["pkg:maven/junit/junit@4.13.2"]; p.Scope != "development" || p.Relationship != "direct" {
		t.Errorf("junit = %+v", p)
	}
	if _, ok := s.Manifests["Dockerfile"].Resolved["pkg:docker/library/golang@1.25"]; !ok {
		t.Error("a dep ignored by policy must be submitted")
	}
	o.AllManagers = true
	if _, n := Build(deps, o); n != 4 {
		t.Errorf("all managers: n = %d, want 4", n)
	}
}
