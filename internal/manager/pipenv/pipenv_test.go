package pipenv

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

type want struct {
	line                             int
	depType, name, ver, skip, source string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("Pipfile", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.Datasource != "pypi" || d.Versioning != "pep440" {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Range {
			t.Errorf("%s: span %q != range %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Range)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Errorf("got %d deps, want %d", len(deps), len(wants))
	}
	for i := range min(len(deps), len(wants)) {
		d, w := deps[i], wants[i]
		if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Skip, d.Source}); got != w {
			t.Errorf("dep %d = %+v, want %+v", i, got, w)
		}
	}
}

const src = `[[source]]
url = "https://pypi.org/simple"
verify_ssl = true
name = "pypi"

[[source]]
url = "https://${USER}:${PASS}@corp.example/simple"
name = "corp"

[[source]]
name = "nourl"

[packages]
requests = "==2.31.0"
flask = "*"
django = {version = "~=4.2", extras = ["argon2"]}
private = {version = ">=1.0", index = "corp"}
unknown = {version = ">=1.0", index = "nope"}
gitdep = {git = "https://github.com/o/r.git", ref = "main"}
editable = {path = ".", editable = true}
wheel = {file = "https://example.com/x.whl"}
bare = "1.0"
number = 1

[dev-packages]
pytest = ">=8.0"
black = "==24.*"

[docs]
sphinx = "==7.0"

[pipenv]
allow_prereleases = false

[scripts]
test = "pytest"

[requires]
python_version = "3.12"
`

func TestExtract(t *testing.T) {
	const pypi = "https://pypi.org/simple"
	check(t, extract(t, src), []want{
		{14, DepTypePackages, "requests", "2.31.0", "", pypi},
		{15, DepTypePackages, "flask", "*", pyreq.SkipUnconstrained, pypi},
		{16, DepTypePackages, "django", "4.2", "", pypi},
		{17, DepTypePackages, "private", "1.0", "", ""},
		{18, DepTypePackages, "unknown", "1.0", SkipUnknownIndex + " nope", pypi},
		{19, DepTypePackages, "gitdep", "", pyreq.SkipGit, pypi},
		{20, DepTypePackages, "editable", "", pyreq.SkipLocal, pypi},
		{21, DepTypePackages, "wheel", "", pyreq.SkipURL, pypi},
		{22, DepTypePackages, "bare", "1.0", "", pypi},
		{23, DepTypePackages, "number", "", pyreq.SkipRange, pypi},
		{26, DepTypeDevPackages, "pytest", "8.0", "", pypi},
		{27, DepTypeDevPackages, "black", "24", "", pypi},
		{30, DepTypeCategory, "sphinx", "7.0", "", pypi},
	})
}

func TestExtractError(t *testing.T) {
	if _, err := New().Extract("Pipfile", []byte("[packages\n"), manager.Env{}); err == nil {
		t.Error("invalid TOML accepted")
	}
	if deps := extract(t, "[scripts]\nx = \"y\"\n"); deps != nil {
		t.Errorf("deps = %+v", deps)
	}
}

// TestFixture extracts pipenv's own Pipfile (source in its first line).
func TestFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/pipenv.Pipfile")
	if err != nil {
		t.Fatal(err)
	}
	deps := extract(t, string(b))
	skipped := 0
	for _, d := range deps {
		if d.Skip != "" {
			skipped++
		}
	}
	// 14 `*` entries; `==4.*`-style wildcards are updatable at their precision.
	if len(deps) != 28 || skipped != 14 {
		t.Errorf("%d deps, %d skipped", len(deps), skipped)
	}
	check(t, deps[:1], []want{{34, DepTypePackages, "filelock", "4.2.1", "", "https://pypi.org/simple"}})
	check(t, deps[7:8], []want{{11, DepTypeDevPackages, "sphinx-click", "4", "", "https://pypi.org/simple"}})
}

// TestCategoryFixture extracts ohcnetwork/care's Pipfile with a custom `[docs]` category and
// a second source (source in its first line).
func TestCategoryFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/care.Pipfile")
	if err != nil {
		t.Fatal(err)
	}
	deps := extract(t, string(b))
	if len(deps) != 63 {
		t.Errorf("%d deps", len(deps))
	}
	const pypi = "https://pypi.org/simple"
	check(t, deps[37:38], []want{{50, DepTypePackages, "python-magic", "0.4.28", "", "https://github.com/ohcnetwork/python-magic-bin/releases/expanded_assets/v0.1"}})
	check(t, deps[60:61], []want{{77, DepTypeCategory, "sphinx", "8.2.3", "", pypi}})
}

func TestMatchAndEdits(t *testing.T) {
	m := New()
	if m.Name() != Name || !m.Match("Pipfile") || !m.Match("app/Pipfile") || m.Match("Pipfile.lock") {
		t.Error("Match")
	}
	in := "[packages]\nblack = {version = \"==24.*\", markers = \"os_name == 'nt'\"}\n"
	d := extract(t, in)[0]
	e, err := m.Edits(dep.Update{Dep: d, NewVersion: "26"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply([]byte(in), e)
	if want := "[packages]\nblack = {version = \"==26.*\", markers = \"os_name == 'nt'\"}\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestSourcesAndPython(t *testing.T) {
	const sources = `[[source]]
name = "pypi"
url = "https://pypi.org/simple"

[[source]]
name = "torch"
url = "https://download.pytorch.org/whl/cpu"

[packages]
requests = "==2.31.0"
torch = {version = "==2.4.0", index = "torch"}
`
	for _, c := range []struct {
		name, extra, python string
		extras              []string
	}{
		{"first source only", "[requires]\npython_version = \"3.11\"\n", "==3.11.*", nil},
		{"search all sources", "[pipenv]\ninstall_search_all_sources = true\n[requires]\npython_full_version = \"3.11.4\"\npython_version = \"3.11\"\n",
			"==3.11.4", []string{"https://download.pytorch.org/whl/cpu"}},
		{"no requires", "", "", nil},
	} {
		deps, err := New().Extract("Pipfile", []byte(sources+c.extra), manager.Env{})
		if err != nil || len(deps) != 2 {
			t.Fatalf("%s: Extract = %d deps, %v", c.name, len(deps), err)
		}
		if d := deps[0]; d.Source != "https://pypi.org/simple" || !slices.Equal(d.ExtraSources, c.extras) || d.PythonConstraint != c.python {
			t.Errorf("%s: %s: source %q, extra %q, python %q", c.name, d.Name, d.Source, d.ExtraSources, d.PythonConstraint)
		}
		if d := deps[1]; d.Source != "https://download.pytorch.org/whl/cpu" || d.ExtraSources != nil || d.PythonConstraint != c.python {
			t.Errorf("%s: %s: source %q, extra %q, python %q", c.name, d.Name, d.Source, d.ExtraSources, d.PythonConstraint)
		}
	}
}

// TestGitRef makes a `ref` that is a version tag a git-tags dep (pipenv's documented shape).
func TestGitRef(t *testing.T) {
	const in = "[packages]\nrequests = {git = \"https://github.com/psf/requests.git\", ref = \"v2.32.3\", editable = true}\n"
	deps, err := New().Extract("Pipfile", []byte(in), manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatal(deps, err)
	}
	d := deps[0]
	if d.Datasource != pyreq.GitTags || d.Versioning != "tag" || d.Source != "https://github.com/psf/requests" || d.Version != "v2.32.3" || d.Skip != "" {
		t.Fatalf("dep = %+v", d)
	}
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "v2.33.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := patch.Apply([]byte(in), edits); err != nil || string(out) != strings.Replace(in, "v2.32.3", "v2.33.0", 1) {
		t.Errorf("out = %s, %v", out, err)
	}
}
