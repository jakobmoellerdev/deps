package gomod

import "testing"

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
	deps, err := Extract("go.mod", b, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, ver, skip string
		line            int
	}{
		{"github.com/spf13/cobra", "v1.9.1", "", 5},
		{"golang.org/x/mod", "v0.42.0", "", 8},
		{"github.com/a/ind", "v1.0.0", "indirect", 9},
		{"github.com/a/rep", "v1.1.1", "replaced", 10},
		{"github.com/a/pseudo", "v0.0.0-20240101000000-abcdefabcdef", "pseudo-version", 11},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps", len(deps))
	}
	for i, w := range want {
		d := deps[i]
		if d.Manager != Name || d.Name != w.name || d.Version != w.ver || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: %+v", i, d)
		}
		if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ver {
			t.Errorf("%d: span %q", i, got)
		}
	}

	deps, _ = Extract("go.mod", b, true)
	if deps[0].Skip != "vendored module (not supported)" || deps[2].Skip != "indirect" {
		t.Errorf("vendored: %+v", deps)
	}
	if _, err := Extract("go.mod", []byte("modul x\n"), false); err == nil {
		t.Error("want parse error")
	}
	if !Match("go.mod") || !Match("a/go.mod") || Match("a/xgo.mod") || Match("go.sum") {
		t.Error("Match")
	}
}
