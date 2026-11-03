package gomod

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
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
		{"go", "1.27", "", "go", 3},
		{"github.com/spf13/cobra", "v1.9.1", "", "direct", 5},
		{"golang.org/x/mod", "v0.42.0", "", "direct", 8},
		{"github.com/a/ind", "v1.0.0", "", "indirect", 9},
		{"github.com/a/rep", "v1.1.1", "replaced", "direct", 10},
		{"github.com/a/pseudo", "v0.0.0-20240101000000-abcdefabcdef", "pseudo-version", "direct", 11},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps", len(deps))
	}
	for i, w := range want {
		d := deps[i]
		ds, vs := "go", "go"
		if i == 0 {
			ds, vs = "go-toolchain", "tag"
		}
		if d.Manager != Name || d.Name != w.name || d.Version != w.ver || d.Skip != w.skip || d.Line != w.line ||
			d.DepType != w.depType || d.Datasource != ds || d.Versioning != vs {
			t.Errorf("%d: %+v", i, d)
		}
		if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ver {
			t.Errorf("%d: span %q", i, got)
		}
	}

	var asked string
	deps, _ = m.Extract("sub/go.mod", b, manager.Env{Vendored: func(dir string) bool { asked = dir; return true }})
	if deps[1].Skip != "vendored module (not supported)" || deps[3].Skip != "vendored module (not supported)" || asked != "sub" {
		t.Errorf("vendored (asked %q): %+v", asked, deps)
	}
	if _, err := m.Extract("go.mod", []byte("modul x\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if !m.Match("go.mod") || !m.Match("a/go.mod") || m.Match("a/xgo.mod") || m.Match("go.sum") || m.Name() != Name {
		t.Error("Match/Name")
	}
}

func TestDirectives(t *testing.T) {
	tests := []struct {
		name, src            string
		goVer, goSkip        string
		tcVer, tcSkip, tcRaw string
	}{
		{name: "2-part go, no toolchain", src: "module x\n\ngo 1.24\n", goVer: "1.24"},
		{name: "3-part go with toolchain", src: "module x\n\ngo 1.24.0\n\ntoolchain go1.27.1\n", goVer: "1.24.0", tcVer: "1.27.1"},
		{name: "crlf", src: "module x\r\ngo 1.24.2\r\ntoolchain go1.25.0\r\n", goVer: "1.24.2", tcVer: "1.25.0"},
		{name: "prerelease", src: "module x\ngo 1.26rc1\ntoolchain go1.26rc2\n", goVer: "1.26rc1", goSkip: "prerelease toolchain", tcVer: "1.26rc2", tcSkip: "prerelease toolchain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := []byte(tt.src)
			deps, err := New().Extract("go.mod", b, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			n := 1
			if tt.tcVer != "" {
				n = 2
			}
			if len(deps) != n {
				t.Fatalf("got %+v", deps)
			}
			check := func(d dep.Dep, name, ver, skip string) {
				t.Helper()
				if d.Name != name || d.DepType != name || d.Version != ver || d.Skip != skip ||
					d.Datasource != "go-toolchain" || d.Versioning != "tag" {
					t.Errorf("%+v", d)
				}
				if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != ver {
					t.Errorf("span %q", got)
				}
			}
			check(deps[0], "go", tt.goVer, tt.goSkip)
			if n == 2 {
				check(deps[1], "toolchain", tt.tcVer, tt.tcSkip)
				if string(b[deps[1].RefSpan.Start-2:deps[1].RefSpan.Start]) != "go" {
					t.Error("toolchain span must follow the go prefix")
				}
				out, err := applyUpdate(b, deps[1], "1.27.2")
				if err != nil || string(out) != tt.src[:deps[1].RefSpan.Start]+"1.27.2"+tt.src[deps[1].RefSpan.End:] {
					t.Errorf("edit: %q %v", out, err)
				}
			}
		})
	}
}

func TestPolicyDefaults(t *testing.T) {
	deps, err := New().Extract("go.mod", []byte(src+"toolchain go1.27.1\n"), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"go": true, "indirect": true, "toolchain": false, "direct": false}
	def := config.Default()
	f := false
	optIn := &config.Config{Version: 1, Pin: config.PinKeep, Rules: []config.Rule{{DepTypes: []string{"go", "indirect"}, Ignore: &f}}}
	for _, d := range deps {
		if d.Skip != "" {
			continue
		}
		if got := def.Policy(d).Ignore; got != want[d.DepType] {
			t.Errorf("%s (%s): default Ignore = %v", d.Name, d.DepType, got)
		}
		if optIn.Policy(d).Ignore {
			t.Errorf("%s: opt-in rule should un-ignore", d.Name)
		}
	}
}

func applyUpdate(b []byte, d dep.Dep, v string) ([]byte, error) {
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: v})
	if err != nil {
		return nil, err
	}
	return patch.Apply(b, edits)
}

func TestEdits(t *testing.T) {
	b := []byte(src)
	deps, err := New().Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 2, 3} {
		out, err := applyUpdate(b, deps[i], "v9.9.9")
		if err != nil {
			t.Fatal(err)
		}
		if want := string(b[:deps[i].RefSpan.Start]) + "v9.9.9" + string(b[deps[i].RefSpan.End:]); string(out) != want {
			t.Errorf("%d: got %q", i, out)
		}
	}
}
