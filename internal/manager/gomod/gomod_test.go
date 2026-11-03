package gomod

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const src = `module example.com/x

go 1.27

require github.com/spf13/cobra v1.9.1

require (
	golang.org/x/mod v0.42.0
	github.com/a/ind v1.0.0 // indirect
	github.com/a/rep v1.1.1
	github.com/a/pseudo v0.0.0-20240101000000-abcdefabcdef
)

replace github.com/a/rep => ../rep
`

func TestExtract(t *testing.T) {
	b := []byte(src)
	m := New()
	deps, err := m.Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, ver, skip, depType string
		line                     int
	}{
		{"github.com/spf13/cobra", "v1.9.1", "", "direct", 5},
		{"golang.org/x/mod", "v0.42.0", "", "direct", 8},
		{"github.com/a/ind", "v1.0.0", "indirect", "indirect", 9},
		{"github.com/a/rep", "v1.1.1", "replaced", "direct", 10},
		{"github.com/a/pseudo", "v0.0.0-20240101000000-abcdefabcdef", "pseudo-version", "direct", 11},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps", len(deps))
	}
	for i, w := range want {
		d := deps[i]
		if d.Manager != Name || d.Name != w.name || d.Version != w.ver || d.Skip != w.skip || d.Line != w.line ||
			d.DepType != w.depType || d.Datasource != "go" || d.Versioning != "go" {
			t.Errorf("%d: %+v", i, d)
		}
		if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ver {
			t.Errorf("%d: span %q", i, got)
		}
	}

	var asked string
	deps, _ = m.Extract("sub/go.mod", b, manager.Env{Vendored: func(dir string) bool { asked = dir; return true }})
	if deps[0].Skip != "vendored module (not supported)" || deps[2].Skip != "indirect" || asked != "sub" {
		t.Errorf("vendored (asked %q): %+v", asked, deps)
	}
	if _, err := m.Extract("go.mod", []byte("modul x\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if !m.Match("go.mod") || !m.Match("a/go.mod") || m.Match("a/xgo.mod") || m.Match("go.sum") || m.Name() != Name {
		t.Error("Match/Name")
	}
}

func TestEdits(t *testing.T) {
	b := []byte(src)
	deps, err := New().Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	edits, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "v0.43.0"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(b[:deps[1].RefSpan.Start]) + "v0.43.0" + string(b[deps[1].RefSpan.End:]); string(out) != want {
		t.Errorf("got %q", out)
	}
}
