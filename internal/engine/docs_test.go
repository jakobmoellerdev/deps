package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	helmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

func TestManagerDocsDepTypesMatchConfig(t *testing.T) {
	got := map[string]bool{}
	for _, m := range ManagerDocs() {
		seen := map[string]bool{}
		for _, dt := range m.DepTypes {
			if seen[dt.Name] {
				t.Errorf("%s: dep type %q documented twice", m.Name, dt.Name)
			}
			seen[dt.Name] = true
			got[dt.Name] = true
		}
	}
	for _, dt := range config.DepTypes {
		if !got[dt] {
			t.Errorf("config dep type %q is not documented by any manager", dt)
		}
		delete(got, dt)
	}
	for dt := range got {
		t.Errorf("documented dep type %q is not in config.DepTypes", dt)
	}
}

// codeOf concatenates the non-test Go files of dirs, except the doc.go files holding the docs.
func codeOf(t *testing.T, dirs ...string) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "doc.go" {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(src)
		}
	}
	return b.String()
}

// literalPrefix is the part of a documented reason before its first placeholder or quote.
func literalPrefix(reason string) string {
	if i := strings.IndexAny(reason, "<\""); i >= 0 {
		reason = reason[:i]
	}
	return strings.TrimSpace(reason)
}

func assertSkipsInCode(t *testing.T, owner string, skips []refdoc.Skip, code string) {
	t.Helper()
	for _, s := range skips {
		p := literalPrefix(s.Reason)
		if p == "" {
			t.Errorf("%s: skip %q has no literal prefix", owner, s.Reason)
			continue
		}
		if !strings.Contains(code, p) {
			t.Errorf("%s: documented skip %q (%q) is not produced by the code", owner, s.Reason, p)
		}
		if s.When == "" {
			t.Errorf("%s: skip %q has no When", owner, s.Reason)
		}
	}
}

func TestSkipDocsAreProducedByCode(t *testing.T) {
	assertSkipsInCode(t, "engine", SkipDocs, codeOf(t, ".", "../config"))
	pkgDir := map[string]string{actions.Name: "actions"}
	for _, m := range ManagerDocs() {
		dir := m.Name
		if d, ok := pkgDir[m.Name]; ok {
			dir = d
		}
		assertSkipsInCode(t, m.Name, m.Skips, codeOf(t, filepath.Join("..", "manager", dir)))
	}
}

func TestManagerDocsComplete(t *testing.T) {
	for _, m := range ManagerDocs() {
		if m.Name == "" || m.Summary == "" || len(m.Files) == 0 || len(m.DepTypes) == 0 {
			t.Errorf("manager doc %q lacks Name, Summary, Files or DepTypes", m.Name)
		}
		for _, dt := range m.DepTypes {
			if dt.What == "" || dt.Datasource == "" || dt.Versioning == "" {
				t.Errorf("%s: dep type %q lacks What, Datasource or Versioning", m.Name, dt.Name)
			}
		}
		for _, s := range m.Sections {
			if s.Title == "" || s.Body == "" && len(s.Items) == 0 {
				t.Errorf("%s: empty section %q", m.Name, s.Title)
			}
		}
	}
}

func TestDatasourceDocsComplete(t *testing.T) {
	docs := []refdoc.Datasource{gittags.Doc, githubreleases.Doc, goproxy.Doc, gotoolchain.Doc, dockerds.Doc, npmds.Doc, nodeversion.Doc, helmds.Doc, customds.Doc}
	var names []string
	for _, d := range docs {
		if d.Name == "" || d.NameForm == "" || d.Endpoints == "" || d.Auth == "" || d.ReleaseTimes == "" {
			t.Errorf("datasource doc %q has an empty table field: %+v", d.Name, d)
		}
		names = append(names, d.Name)
	}
	want := append(slices.Clone(config.Datasources), config.CustomDatasourcePrefix+"<name>")
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("datasource doc names = %v, want %v", names, want)
	}
}
