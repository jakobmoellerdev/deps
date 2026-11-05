package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/azuretasks"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/crate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gradleversion"
	helmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/helm"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/orb"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubygems"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubyversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/tfregistry"
	vcpkgds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
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
	// pkgDirs maps managers to their package directories when the name differs; kustomize,
	// gitlabci and terraform report docker.ParseRef's skips, cargo the crate datasource's
	// registry skip, the Python managers internal/pyreq's, gomod the engine's and
	// internal/gotool's module path skip, pip_requirements internal/pytool's conflict skip.
	pkgDirs := map[string][]string{
		actions.Name: {"actions"}, "pre-commit": {"precommit"},
		"nvm": {"toolversions"}, "nodenv": {"toolversions"}, "ruby-version": {"toolversions"}, "asdf": {"toolversions"}, "mise": {"toolversions"},
		"kustomize": {"kustomize", "docker"}, "gitlabci": {"gitlabci", "docker"},
		"cargo":              {"cargo", filepath.Join("..", "datasource", "crate")},
		"gomod":              {"gomod", filepath.Join("..", "engine"), filepath.Join("..", "gotool")},
		piprequirements.Name: {"piprequirements", "../pyreq", "../pytool"}, pep621.Name: {"pep621", "../pyreq"},
		poetry.Name: {"poetry", "../pyreq"}, pipenv.Name: {"pipenv", "../pyreq"},
		terraform.Name: {"terraform", "docker"}, terraform.VersionName: {"terraform"}, terraform.TFLintName: {"terraform"},
		terraform.TerragruntName: {"terraform"},
		gradlewrapper.Name:       {"gradlewrapper"}, mavenwrapper.Name: {"mavenwrapper"}, "git-submodules": {"gitsubmodules"},
		"rust-toolchain":      {"rusttoolchain"},
		"devcontainer":        {"devcontainer", "docker", "toolversions"},
		"bitbucket-pipelines": {"bitbucketpipelines", "docker"}, "woodpecker": {"woodpecker", "docker"}, "droneci": {"droneci", "docker"},
		"circleci": {"circleci", "docker"}, "azure-pipelines": {"azurepipelines", "docker"},
	}
	for _, m := range ManagerDocs() {
		dirs, ok := pkgDirs[m.Name]
		if !ok {
			dirs = []string{m.Name}
		}
		for i, d := range dirs {
			dirs[i] = filepath.Join("..", "manager", d)
		}
		assertSkipsInCode(t, m.Name, m.Skips, codeOf(t, dirs...))
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
	docs := []refdoc.Datasource{gittags.Doc, gittags.RefsDoc, githubreleases.Doc, goproxy.Doc, gotoolchain.Doc, dockerds.Doc, npmds.Doc, nodeversion.Doc, helmds.Doc, crate.Doc, pypi.Doc, tfregistry.ProviderDoc, tfregistry.ModuleDoc, mavends.Doc, gradleversion.Doc, rubygems.Doc, rubyversion.Doc, rustversion.Doc, orb.Doc, azuretasks.Doc, vcpkgds.Doc, customds.Doc}
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
