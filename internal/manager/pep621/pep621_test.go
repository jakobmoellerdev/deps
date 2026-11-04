package pep621

import (
	"os"
	"slices"
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
	deps, err := New().Extract("pyproject.toml", []byte(src), manager.Env{})
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

const src = `[project]
name = "demo"
requires-python = ">=3.10"
dependencies = [
    "requests>=2.31.0",
    "pydantic[email] (==2.9.0)",
    "httpx>=0.27,<1",
    "local @ file:///tmp/local",
    "member",
    "torch==2.5.0",
    "gitdep>=1.0",
    "multi>=1.0",
    "missing==1.0",
    "-invalid",
    'lit~=1.4',
    "esc\u0061ped==1.0",
]

[project.optional-dependencies]
docs = ["sphinx>=7.0"]
broken = "not an array"

[dependency-groups]
dev = ["pytest>=8.0", { include-group = "docs" }]

[tool.uv]
dev-dependencies = ["ruff==0.6.0"]

[tool.uv.sources]
Member = { workspace = true }
torch = { index = "pytorch" }
gitdep = { git = "https://github.com/o/r" }
multi = [{ index = "a", marker = "sys_platform == 'linux'" }, { index = "b" }]
missing = { index = "nope" }
ruff = { editable = true }
sphinx = "not a table"

[[tool.uv.index]]
name = "pytorch"
url = "https://download.pytorch.org/whl/cpu"
explicit = true

[[tool.uv.index]]
name = "corp"
url = "https://pypi.corp.example/simple"
default = true

[[tool.uv.index]]
name = "nourl"
`

func TestExtract(t *testing.T) {
	const corp = "https://pypi.corp.example/simple"
	check(t, extract(t, src), []want{
		{5, DepTypeDependencies, "requests", "2.31.0", "", corp},
		{6, DepTypeDependencies, "pydantic", "2.9.0", "", corp},
		{7, DepTypeDependencies, "httpx", "0.27", "", corp},
		{8, DepTypeDependencies, "local", "", pyreq.SkipLocal, ""},
		{9, DepTypeDependencies, "member", "", pyreq.SkipUnconstrained, ""},
		{10, DepTypeDependencies, "torch", "2.5.0", "", "https://download.pytorch.org/whl/cpu"},
		{11, DepTypeDependencies, "gitdep", "1.0", pyreq.SkipGit, ""},
		{12, DepTypeDependencies, "multi", "1.0", SkipMultipleSources, ""},
		{13, DepTypeDependencies, "missing", "1.0", SkipUnknownIndex + " nope", ""},
		{15, DepTypeDependencies, "lit", "1.4", "", corp},
		{16, DepTypeDependencies, "escaped", "", pyreq.SkipLocate, ""},
		{20, DepTypeOptional, "sphinx", "7.0", "", ""},
		{24, DepTypeGroups, "pytest", "8.0", "", corp},
		{27, DepTypeUVDev, "ruff", "0.6.0", "", corp},
	})
}

func TestWorkspaceMember(t *testing.T) {
	deps := extract(t, "[project]\ndependencies = [\"Member>=1.0\"]\n[tool.uv.sources]\nmember = { workspace = true }\n")
	check(t, deps, []want{{2, DepTypeDependencies, "Member", "1.0", SkipWorkspace, ""}})
}

const pdmSrc = `[project]
dependencies = ["requests>=2.31.0", "foo-core==1.0", "bar-x==2.0", "uvpinned==3.0"]

[tool.pdm.dev-dependencies]
lint = ["ruff==0.6.0"]
test = ["pytest>=8.0", "-e file:///${PROJECT_ROOT}/sub"]

[tool.uv.sources]
uvpinned = { index = "uvidx" }

[[tool.uv.index]]
name = "uvidx"
url = "https://uv.example/simple"

[[tool.pdm.source]]
name = "private"
url = "https://private.example/simple"
include_packages = ["foo-*"]
exclude_packages = ["bar-*"]

[[tool.pdm.source]]
name = "pypi"
url = "https://mirror.example/simple"

[[tool.pdm.source]]
name = "links"
url = "https://links.example/"
type = "find_links"

[[tool.pdm.source]]
name = "secret"
url = "https://${TOKEN}@secret.example/simple"

[[tool.pdm.source]]
name = "extra"
url = "https://extra.example/simple"
type = "index"
`

func TestPDM(t *testing.T) {
	const mirror, private, extra = "https://mirror.example/simple", "https://private.example/simple", "https://extra.example/simple"
	deps := extract(t, pdmSrc)
	check(t, deps, []want{
		{2, DepTypeDependencies, "requests", "2.31.0", "", mirror},
		{2, DepTypeDependencies, "foo-core", "1.0", "", private},
		{2, DepTypeDependencies, "bar-x", "2.0", "", mirror},
		{2, DepTypeDependencies, "uvpinned", "3.0", "", "https://uv.example/simple"},
		{5, DepTypePDMDev, "ruff", "0.6.0", "", mirror},
		{6, DepTypePDMDev, "pytest", "8.0", "", mirror},
	})
	for i, w := range [][]string{{private, extra}, nil, {extra}, nil, {private, extra}, {private, extra}} {
		if i < len(deps) && !slices.Equal(deps[i].ExtraSources, w) {
			t.Errorf("%s: extra sources %q, want %q", deps[i].Name, deps[i].ExtraSources, w)
		}
	}
}

// TestFixtures extracts real-world pyproject.toml files (sources in their first line).
func TestFixtures(t *testing.T) {
	for _, c := range []struct {
		file          string
		deps, skipped int
		pick          int
		want          want
	}{
		// pydantic: uv workspace member pydantic-core, unconstrained dependency groups.
		{"pydantic.toml", 54, 44, 2, want{53, DepTypeDependencies, "pydantic-core", "2.50.0", SkipWorkspace, ""}},
		// fastapi: extras with a space before the specifier, ranges with upper bounds.
		{"fastapi.toml", 97, 0, 10, want{69, DepTypeOptional, "fastapi-cli", "0.0.32", "", ""}},
		// chaostoolkit: pdm dev-dependencies, PyPI only.
		{"chaostoolkit.toml", 10, 0, 9, want{59, DepTypePDMDev, "semver", "3.0.2", "", ""}},
		// greybook: pdm dev-dependencies and a `pypi` source overriding PyPI.
		{"greybook.toml", 18, 0, 17, want{36, DepTypePDMDev, "pytest", "8.2.2", "", "https://pypi.org/simple"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + c.file)
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
			if len(deps) != c.deps || skipped != c.skipped {
				t.Errorf("%d deps, %d skipped; want %d, %d", len(deps), skipped, c.deps, c.skipped)
			}
			check(t, deps[c.pick:c.pick+1], []want{c.want})
		})
	}
}

func TestExtractError(t *testing.T) {
	if _, err := New().Extract("pyproject.toml", []byte("[project\n"), manager.Env{}); err == nil {
		t.Error("invalid TOML accepted")
	}
}

func TestMatchAndEdits(t *testing.T) {
	m := New()
	if m.Name() != Name || !m.Match("pyproject.toml") || !m.Match("a/b/pyproject.toml") || m.Match("pyproject.toml.bak") {
		t.Error("Match")
	}
	in := "[project]\ndependencies = [\"requests >= 2.31.0 ; python_version > '3'\"]\n"
	d := extract(t, in)[0]
	e, err := m.Edits(dep.Update{Dep: d, NewVersion: "2.32.3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply([]byte(in), e)
	if want := "[project]\ndependencies = [\"requests >= 2.32.3 ; python_version > '3'\"]\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestUVIndexesAndPython(t *testing.T) {
	src := `[project]
requires-python = ">=3.9"
dependencies = ["requests==2.31.0", "pinned==1.0"]

[[tool.uv.index]]
name = "pytorch"
url = "https://download.pytorch.org/whl/cpu"

[[tool.uv.index]]
name = "internal"
url = "https://internal.example/simple"
explicit = true

[[tool.uv.index]]
name = "mirror"
url = "https://mirror.example/simple"
default = true

[tool.uv.sources]
pinned = { index = "internal" }
`
	deps, err := New().Extract("pyproject.toml", []byte(src), manager.Env{})
	if err != nil || len(deps) != 2 {
		t.Fatalf("Extract = %d deps, %v", len(deps), err)
	}
	for i, w := range []struct {
		source string
		extra  []string
	}{
		{"https://mirror.example/simple", []string{"https://download.pytorch.org/whl/cpu"}},
		{"https://internal.example/simple", nil},
	} {
		d := deps[i]
		if d.Source != w.source || !slices.Equal(d.ExtraSources, w.extra) || d.PythonConstraint != ">=3.9" {
			t.Errorf("%s: source %q, extra %q, python %q", d.Name, d.Source, d.ExtraSources, d.PythonConstraint)
		}
	}
}
