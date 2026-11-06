package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	apkds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/apk"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/azuretasks"
	bazelds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/bazel"
	condads "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/conda"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/crate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/dotnetversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/elmpackage"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gitlabpackages"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gradleversion"
	helmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/juliaregistry"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	nugetds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nuget"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/orb"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/packagist"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	rpmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rpm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubygems"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rubyversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/tfregistry"
	vcpkgds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/registry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// TestManagerDocsDepTypes checks the dep type facts config derives its lists from: each dep type
// once per manager, and a Datasource column text naming its datasources; only the custom
// manager's dep types take any datasource (nil Datasources). TestDatasourceDocsComplete checks
// that the datasources (config.Datasources) are the documented ones.
func TestManagerDocsDepTypes(t *testing.T) {
	for _, m := range registry.Docs() {
		seen := map[string]bool{}
		for _, dt := range m.DepTypes {
			if seen[dt.Name] {
				t.Errorf("%s: dep type %q documented twice", m.Name, dt.Name)
			}
			seen[dt.Name] = true
			if (dt.Datasources == nil) != (m.Name == custom.Name) {
				t.Errorf("%s: dep type %q: Datasources = %v", m.Name, dt.Name, dt.Datasources)
			}
			for _, ds := range dt.Datasources {
				if dt.Datasource != "" && !strings.Contains(dt.Datasource, "`"+ds+"`") && !strings.HasPrefix(dt.Datasource, "per ") {
					t.Errorf("%s: dep type %q: the Datasource column %q does not name %s", m.Name, dt.Name, dt.Datasource, ds)
				}
			}
		}
	}
}

// TestTestdataDepsMatchDocs extracts the testdata of every manager package with the built-in
// managers of that package and checks each extracted dep against its manager's
// documentation: its dep type is documented, and its datasource is one of that dep type's
// Datasources. A file goes to the package's managers that match its path, or to all of them
// when none does (fixtures are named after their origin, e.g. ripgrep.Cargo.toml).
func TestTestdataDepsMatchDocs(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "manager", "*", "testdata"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("testdata dirs = %v, %v", dirs, err)
	}
	env := manager.Env{Exists: func(string) bool { return false }}
	for _, dir := range dirs {
		pkg := "/internal/manager/" + filepath.Base(filepath.Dir(dir))
		var ms []manager.Manager
		var docs []refdoc.Manager
		for _, e := range registry.Builtin {
			m := e.New()
			rt := reflect.TypeOf(m)
			if rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
			if strings.HasSuffix(rt.PkgPath(), pkg) {
				ms, docs = append(ms, m), append(docs, e.Doc)
			}
		}
		if len(ms) == 0 {
			t.Errorf("%s: no built-in manager in %s", dir, pkg)
			continue
		}
		files := make([][][]dep.Dep, len(ms)) // per manager, the deps of each file it extracted
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			matched := slices.ContainsFunc(ms, func(m manager.Manager) bool { return m.Match(rel) })
			for i, m := range ms {
				if matched && !m.Match(rel) {
					continue
				}
				// Testdata holds files some or all managers of the package reject (locks, malformed files).
				if deps, err := m.Extract(rel, src, env); err == nil {
					files[i] = append(files[i], deps)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for i, m := range ms {
			if l, ok := m.(manager.Linker); ok {
				l.Link(files[i])
			}
			for _, d := range slices.Concat(files[i]...) {
				if d.Skip != "" {
					continue
				}
				checked++
				j := slices.IndexFunc(docs[i].DepTypes, func(dt refdoc.DepType) bool { return dt.Name == d.DepType })
				switch {
				case j < 0:
					t.Errorf("%s/%s:%d: %s dep %s has undocumented dep type %q", dir, d.File, d.Line, m.Name(), d.Name, d.DepType)
				case !slices.Contains(docs[i].DepTypes[j].Datasources, d.Datasource):
					t.Errorf("%s/%s:%d: %s dep %s (%s) has datasource %q, documented %v", dir, d.File, d.Line, m.Name(), d.Name, d.DepType, d.Datasource, docs[i].DepTypes[j].Datasources)
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: no dep extracted", dir)
		}
		t.Logf("%s: %d deps", dir, checked)
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
		poetry.Name: {"poetry", "../pyreq"}, pipenv.Name: {"pipenv", "../pyreq"}, "conda": {"conda", "../pyreq"},
		terraform.Name: {"terraform", "docker"}, terraform.VersionName: {"terraform"}, terraform.TFLintName: {"terraform"},
		terraform.TerragruntName: {"terraform"},
		gradlewrapper.Name:       {"gradlewrapper"}, mavenwrapper.Name: {"mavenwrapper"}, "git-submodules": {"gitsubmodules"},
		"rust-toolchain":      {"rusttoolchain"},
		"devcontainer":        {"devcontainer", "docker", "toolversions"},
		"bitbucket-pipelines": {"bitbucketpipelines", "docker"}, "woodpecker": {"woodpecker", "docker"}, "droneci": {"droneci", "docker"},
		"circleci": {"circleci", "docker"}, "azure-pipelines": {"azurepipelines", "docker"},
		"bazel-module": {"bazel"}, "bazelisk": {"bazel"},
	}
	for _, m := range registry.Docs() {
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
	for _, m := range registry.Docs() {
		if m.Name == "" || m.Summary == "" || len(m.Files) == 0 || len(m.DepTypes) == 0 {
			t.Errorf("manager doc %q lacks Name, Summary, Files or DepTypes", m.Name)
		}
		for _, dt := range m.DepTypes {
			if dt.What == "" || dt.Datasource == "" && len(dt.Datasources) == 0 || dt.Versioning == "" {
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
	docs := []refdoc.Datasource{gittags.Doc, gittags.RefsDoc, githubreleases.Doc, goproxy.Doc, gotoolchain.Doc, dockerds.Doc, npmds.Doc, nodeversion.Doc, helmds.Doc, crate.Doc, pypi.Doc, condads.Doc, tfregistry.ProviderDoc, tfregistry.ModuleDoc, mavends.Doc, gradleversion.Doc, rubygems.Doc, rubyversion.Doc, rustversion.Doc, orb.Doc, azuretasks.Doc, vcpkgds.Doc, elmpackage.Doc, gitlabpackages.Doc, packagist.Doc, juliaregistry.Doc, bazelds.Doc, nugetds.Doc, dotnetversion.Doc, rpmds.Doc, apkds.Doc, customds.Doc}
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
