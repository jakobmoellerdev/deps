package pep723

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

type want struct {
	line            int
	name, ver, skip string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("script.py", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.Datasource != "pypi" || d.Versioning != "pep440" || d.DepType != DepType {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Range {
			t.Errorf("%s: span %q != range %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Range)
		}
	}
	return deps
}

func TestExtract(t *testing.T) {
	src, err := os.ReadFile("testdata/script.py")
	if err != nil {
		t.Fatal(err)
	}
	deps := extract(t, string(src))
	wants := []want{
		{6, "requests", "3", ""},
		{7, "rich", "", pyreq.SkipUnconstrained},
		{8, "httpx", "0.27.0", ""},
		{9, "pkg", "", pyreq.SkipGit},
		{10, "click", "8.1.7", ""},
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if got := (want{d.Line, d.Name, d.Version, d.Skip}); got != w {
			t.Errorf("dep %d = %+v, want %+v", i, got, w)
		}
		if d.PythonConstraint != ">=3.11" {
			t.Errorf("%s: python %q", d.Name, d.PythonConstraint)
		}
	}

	d := deps[2]
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "1.0.0", RangeStrategy: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got := extract(t, string(out))[2].Range; got == d.Range {
		t.Errorf("range not rewritten: %q", got)
	}
}

func TestExtractNoBlock(t *testing.T) {
	for _, src := range []string{
		"import os\n",
		"# /// script\n# dependencies = [\"a>=1\"]\n",                   // not closed
		"x = 1  # /// script\n# dependencies = [\"a>=1\"]\n# ///\n",     // not at line start
		"# /// script\n# dependencies = [\"a>=1\"]\nimport os\n# ///\n", // closed after code
		"# /// pyproject\n# dependencies = [\"a>=1\"]\n# ///\n",
	} {
		if deps := extract(t, src); len(deps) != 0 {
			t.Errorf("%q: %+v", src, deps)
		}
	}
}

func TestExtractCRLFFirstBlock(t *testing.T) {
	src := "# /// script\r\n# dependencies = [\r\n#   \"a>=1.0\",\r\n# ]\r\n# ///\r\n\r\n# /// script\r\n# dependencies = [\"b>=1\"]\r\n# ///\r\n"
	deps := extract(t, src)
	if len(deps) != 1 || deps[0].Name != "a" || deps[0].Line != 3 {
		t.Fatalf("%+v", deps)
	}
}

func TestExtractSkipsUnclosedOpener(t *testing.T) {
	src := "# /// script\n# not closed\nimport os\n# /// script\n# dependencies = [\"a>=1\"]\n# ///\n"
	deps := extract(t, src)
	if len(deps) != 1 || deps[0].Name != "a" || deps[0].Line != 5 {
		t.Fatalf("%+v", deps)
	}
}

func TestExtractInvalidTOML(t *testing.T) {
	if _, err := New().Extract("x.py", []byte("# /// script\n# dependencies = [\n# ///\n"), manager.Env{}); err == nil {
		t.Error("want error")
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if !m.Match("scripts/tool.py") || m.Match("tool.pyc") || m.Name() != Name {
		t.Error("match")
	}
	d := dep.Dep{Range: ">=1.0", Version: "1.0", Versioning: "pep440"}
	if !m.(manager.RangeUpdater).InRange(d, "1.5") {
		t.Error("InRange")
	}
	if r, ok := m.(manager.RangeUpdater).NewRange(d, "2.0.0", "bump"); !ok || r != ">=2.0.0" {
		t.Errorf("NewRange = %q %v", r, ok)
	}
}
