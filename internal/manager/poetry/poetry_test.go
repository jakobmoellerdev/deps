package poetry

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
	deps, err := New().Extract("pyproject.toml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Datasource == pyreq.GitTags {
			if d.Manager != Name || d.Versioning != "tag" || src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
				t.Errorf("%s: %+v", d.Name, d)
			}
			continue
		}
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

const src = `[tool.poetry]
name = "demo"

[tool.poetry.dependencies]
python = "^3.9"
requests = "^2.31.0"
click = "~8.1"
exact = "1.2.3"
star = "*"
range = ">=1.0,<2.0"
table = { version = "^1.0", extras = ["x"] }
private = { version = "==2.0", source = "corp" }
unknown = { version = "==2.0", source = "nope" }
gitdep = { git = "https://github.com/o/r.git", tag = "v1" }
pathdep = { path = "../lib", develop = true }
urldep = { url = "https://example.com/x.whl" }
multi = [
  { version = "<2", python = "<3.10" },
  { version = "^2", python = ">=3.10" },
]
number = 1
escaped = "\u005e1.0"
wild = "2.*"

[tool.poetry.dev-dependencies]
pytest = "^7.0"

[tool.poetry.group.lint.dependencies]
ruff = "0.6.0"

[tool.poetry.group.empty]
optional = true

[tool.poetry.dependencies.longform]
version = "^3.1"
extras = ["a"]

[[tool.poetry.source]]
name = "corp"
url = "https://pypi.corp.example/simple"
priority = "explicit"

[[tool.poetry.source]]
name = "mirror"
url = "https://mirror.example/simple"
priority = "primary"

[[tool.poetry.source]]
name = "second"
url = "https://second.example/simple"
default = true

[[tool.poetry.source]]
name = "nourl"
`

func TestExtract(t *testing.T) {
	const mirror = "https://mirror.example/simple"
	check(t, extract(t, src), []want{
		{6, DepTypeDependencies, "requests", "2.31.0", "", mirror},
		{7, DepTypeDependencies, "click", "8.1", "", mirror},
		{8, DepTypeDependencies, "exact", "1.2.3", "", mirror},
		{9, DepTypeDependencies, "star", "*", pyreq.SkipUnconstrained, mirror},
		{10, DepTypeDependencies, "range", "1.0", "", mirror},
		{11, DepTypeDependencies, "table", "1.0", "", mirror},
		{12, DepTypeDependencies, "private", "2.0", "", "https://pypi.corp.example/simple"},
		{13, DepTypeDependencies, "unknown", "2.0", SkipUnknownSource + " nope", mirror},
		{14, DepTypeDependencies, "gitdep", "v1", "", "https://github.com/o/r"},
		{15, DepTypeDependencies, "pathdep", "", pyreq.SkipLocal, mirror},
		{16, DepTypeDependencies, "urldep", "", pyreq.SkipURL, mirror},
		{18, DepTypeDependencies, "multi", "2", "", mirror},
		{19, DepTypeDependencies, "multi", "2", "", mirror},
		{21, DepTypeDependencies, "number", "", pyreq.SkipRange, mirror},
		{22, DepTypeDependencies, "escaped", "", pyreq.SkipLocate, mirror},
		{23, DepTypeDependencies, "wild", "2", "", mirror},
		{35, DepTypeDependencies, "longform", "3.1", "", mirror},
		{26, DepTypeDevDependencies, "pytest", "7.0", "", mirror},
		{29, DepTypeGroup, "ruff", "0.6.0", "", mirror},
	})
}

// TestMultipleConstraints extracts one dep per constraint of an array, limited to the
// constraint's Python versions (numpy's shape from Poetry's docs), and git tags of tables.
func TestMultipleConstraints(t *testing.T) {
	const in = `[tool.poetry.dependencies]
python = "^3.8"
numpy = [
    { version = "<1.25", python = "<3.9" },
    { version = "^1.26", python = ">=3.9", source = "corp" },
    { git = "https://github.com/numpy/numpy.git", tag = "v2.0.0", markers = "sys_platform == 'linux'" },
    { git = "https://github.com/numpy/numpy.git", branch = "main" },
    "1.24.4",
]

[[tool.poetry.source]]
name = "corp"
url = "https://corp.example/simple"
`
	deps := extract(t, in)
	check(t, deps, []want{
		{4, DepTypeDependencies, "numpy", "1.25", "", ""},
		{5, DepTypeDependencies, "numpy", "1.26", "", "https://corp.example/simple"},
		{6, DepTypeDependencies, "numpy", "v2.0.0", "", "https://github.com/numpy/numpy"},
		{7, DepTypeDependencies, "numpy", "", pyreq.SkipGit, ""},
		{8, DepTypeDependencies, "numpy", "1.24.4", "", ""},
	})
	for i, py := range []string{"<3.9", ">=3.9", ">=3.9", "^3.8", "^3.8", "^3.8"} {
		if i < len(deps) && i != 2 && deps[i].PythonConstraint != py {
			t.Errorf("dep %d python = %q, want %q", i, deps[i].PythonConstraint, py)
		}
	}
	edits, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "v2.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(in), edits)
	if err != nil || !strings.Contains(string(out), `tag = "v2.1.0", markers`) {
		t.Errorf("out = %s, %v", out, err)
	}
}

func TestNoPoetry(t *testing.T) {
	if deps := extract(t, "[project]\nname = \"x\"\n"); deps != nil {
		t.Errorf("deps = %+v", deps)
	}
	if _, err := New().Extract("pyproject.toml", []byte("x = \n"), manager.Env{}); err == nil {
		t.Error("invalid TOML accepted")
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
		// Poetry 2 itself: [project] metadata (pep621's), no [tool.poetry.*dependencies].
		{"poetry.toml", 0, 0, 0, want{}},
		// rich: Poetry 1 caret constraints, two-part versions and an optional table.
		{"rich.toml", 10, 0, 4, want{41, DepTypeDevDependencies, "black", "22.6", "", ""}},
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
			if c.deps > 0 {
				check(t, deps[c.pick:c.pick+1], []want{c.want})
			}
		})
	}
}

func TestMatchAndEdits(t *testing.T) {
	m := New()
	if m.Name() != Name || !m.Match("sub/pyproject.toml") || m.Match("Pipfile") {
		t.Error("Match")
	}
	in := "[tool.poetry.dependencies]\nrequests = { version = \"^2.31.0\", extras = [\"socks\"] }\n"
	d := extract(t, in)[0]
	e, err := m.Edits(dep.Update{Dep: d, NewVersion: "2.32.3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply([]byte(in), e)
	if want := "[tool.poetry.dependencies]\nrequests = { version = \"^2.32.3\", extras = [\"socks\"] }\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestSourcesAndPython(t *testing.T) {
	src := `[project]
requires-python = ">=3.8"

[tool.poetry.dependencies]
python = "^3.9"
requests = "2.31.0"
private = { version = "1.0", source = "internal" }

[[tool.poetry.source]]
name = "mirror"
url = "https://mirror.example/simple"
priority = "primary"

[[tool.poetry.source]]
name = "torch"
url = "https://download.pytorch.org/whl/cpu"
priority = "supplemental"

[[tool.poetry.source]]
name = "internal"
url = "https://internal.example/simple"
priority = "explicit"
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
		if d.Source != w.source || !slices.Equal(d.ExtraSources, w.extra) || d.PythonConstraint != "^3.9" {
			t.Errorf("%s: source %q, extra %q, python %q", d.Name, d.Source, d.ExtraSources, d.PythonConstraint)
		}
	}
	// Without a `python` dependency, [project] requires-python (Poetry 2).
	deps, err = New().Extract("pyproject.toml", []byte("[project]\nrequires-python = \">=3.10\"\n[tool.poetry.dependencies]\nrequests = \"2.31.0\"\n"), manager.Env{})
	if err != nil || len(deps) != 1 || deps[0].PythonConstraint != ">=3.10" {
		t.Errorf("requires-python fallback: %+v, %v", deps, err)
	}
}
