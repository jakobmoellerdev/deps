package pyreq

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in                      string
		ok                      bool
		name, extras, spec, url string
		marker                  string
	}{
		{"requests>=2.31.0", true, "requests", "", ">=2.31.0", "", ""},
		{"  uvicorn[standard] >=0.12.0", true, "uvicorn", "standard", ">=0.12.0", "", ""},
		{"pkg [a, b] (>=1.0, <2) ; python_version < '3.11'", true, "pkg", "a, b", ">=1.0, <2", "", "python_version < '3.11'"},
		{"poetry-core (==2.5.0)", true, "poetry-core", "", "==2.5.0", "", ""},
		{"tzdata; platform_system == \"Windows\"", true, "tzdata", "", "", "", `platform_system == "Windows"`},
		{"zope.interface", true, "zope.interface", "", "", "", ""},
		{"pip @ https://example.com/pip.whl ; os_name == 'nt'", true, "pip", "", "", "https://example.com/pip.whl", "os_name == 'nt'"},
		{"x @ ", false, "", "", "", "", ""},
		{"-r other.txt", false, "", "", "", "", ""},
		{"", false, "", "", "", "", ""},
		{"bad_[x", false, "", "", "", "", ""},
		{"pkg[x", false, "", "", "", "", ""},
		{"pkg (>=1", false, "", "", "", "", ""},
	} {
		r, ok := Parse(c.in)
		if ok != c.ok || r.Name != c.name || r.Extras != c.extras || r.Spec != c.spec || r.URL != c.url || r.Marker != c.marker {
			t.Errorf("Parse(%q) = %+v, %v", c.in, r, ok)
			continue
		}
		if ok && c.spec != "" && c.in[r.SpecStart:r.SpecEnd] != c.spec {
			t.Errorf("Parse(%q): spec span %q", c.in, c.in[r.SpecStart:r.SpecEnd])
		}
		if ok && c.in[r.NameStart:r.NameStart+len(r.Name)] != r.Name {
			t.Errorf("Parse(%q): name offset %d", c.in, r.NameStart)
		}
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"a": true, "A1": true, "zope.interface": true, "typing_extensions": true, "-a": false, "a-": false, "": false, "a b": false, "a+b": false} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v", name, got)
		}
	}
}

func TestSetVersionAndEdits(t *testing.T) {
	src := `x = " ^1.2.3"`
	d := NewDep("poetry", "dependencies", "pyproject.toml", "x", 1)
	SetVersion(&d, " ^1.2.3", 5, true, Poetry)
	if d.Version != "1.2.3" || d.Range != "^1.2.3" || src[d.RefSpan.Start:d.RefSpan.End] != "^1.2.3" || d.Datasource != Datasource || d.Versioning != Versioning {
		t.Fatalf("dep = %+v", d)
	}
	edits, err := Edits(dep.Update{Dep: d, NewVersion: "2.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply([]byte(src), edits)
	if err != nil || string(got) != `x = " ^2.4.0"` {
		t.Errorf("patched = %q, %v", got, err)
	}
	if _, err := Edits(dep.Update{Dep: d, NewVersion: "1.2.3"}); err == nil {
		t.Error("unchanged range: no error")
	}
	SetVersion(&d, "^1.2.3", 5, false, Poetry)
	if d.Skip != SkipLocate {
		t.Errorf("inexact: skip = %q", d.Skip)
	}
	d = NewDep("pep621", "x", "f", "x", 1)
	SetVersion(&d, " <2 || >3 ", 0, true, PEP440)
	if d.Skip != SkipRange || d.Version != "<2 || >3" {
		t.Errorf("range: %+v", d)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Django": "django", "zope.interface": "zope-interface", "typing_extensions": "typing-extensions",
		"Foo__Bar..baz-_-qux": "foo-bar-baz-qux", "a": "a", "-a_": "a",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLSkip(t *testing.T) {
	for u, want := range map[string]string{
		"git+https://github.com/o/r": SkipGit, "hg+https://x": SkipGit, "file:///tmp/x": SkipLocal,
		"./pkg": SkipLocal, "../pkg": SkipLocal, "/abs": SkipLocal, "https://x/y.whl": SkipURL,
	} {
		if got := URLSkip(u); got != want {
			t.Errorf("URLSkip(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestTableSpec(t *testing.T) {
	root, err := tomlspan.Parse([]byte(`a = { git = "x" }
b = { path = "." }
c = { url = "x" }
d = { file = "x" }
e = { extras = ["x"] }
f = { version = 1 }
g = { version = "^1", extras = ["x"] }
`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"a": SkipGit, "b": SkipLocal, "c": SkipURL, "d": SkipURL, "e": SkipUnconstrained, "f": SkipUnconstrained, "g": ""} {
		spec, skip := TableSpec(root.Get(key))
		if skip != want || (skip == "") != (spec != nil) {
			t.Errorf("%s: %v, %q; want %q", key, spec, skip, want)
		}
	}
}

func TestTOMLRequirement(t *testing.T) {
	src := `d = ["requests>=2.31.0", "x @ git+https://x", "-bad", "esc\u0061pe==1.0"]`
	root, err := tomlspan.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	items := root.Get("d").Strings()
	d, ok := TOMLRequirement("pep621", "t", "f", items[0])
	if !ok || d.Version != "2.31.0" || src[d.RefSpan.Start:d.RefSpan.End] != ">=2.31.0" {
		t.Errorf("requests = %+v", d)
	}
	if d, ok := TOMLRequirement("pep621", "t", "f", items[1]); !ok || d.Skip != SkipGit {
		t.Errorf("git = %+v", d)
	}
	if _, ok := TOMLRequirement("pep621", "t", "f", items[2]); ok {
		t.Error("invalid requirement accepted")
	}
	if d, ok := TOMLRequirement("pep621", "t", "f", items[3]); !ok || d.Skip != SkipLocate {
		t.Errorf("escaped = %+v", d)
	}
}
