package npm

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestEnginesVolta(t *testing.T) {
	in := `{
  "engines": {
    "node": ">=26.1.0",
    "npm": "^11",
    "pnpm": "10.2",
    "yarn": ">= 4 <5",
    "vscode": "^1.90.0",
    "bun": {"x": 1}
  },
  "volta": {"node": "26.1.0", "npm": 11, "yarn": "4.5.0-rc.1", "pnpm": "latest"},
  "engines2": {"node": "1.0.0"}
}`
	b := []byte(in)
	deps, err := New().Extract("package.json", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	wants := []struct {
		depType, name, ds, versioning, ver, skip string
		line                                     int
	}{
		{DepTypeEngines, "node", "node-version", "semver", "26.1.0", "", 3},
		{DepTypeEngines, "npm", "npm", "tag", "11", "", 4},
		{DepTypeEngines, "pnpm", "npm", "tag", "10.2", "", 5},
		{DepTypeEngines, "yarn", "npm", "semver", ">= 4 <5", "unsupported range", 6},
		{DepTypeVolta, "node", "node-version", "semver", "26.1.0", "", 10},
		{DepTypeVolta, "yarn", "npm", "semver", "4.5.0-rc.1", "", 10},
		{DepTypeVolta, "pnpm", "npm", "semver", "latest", "tag", 10},
	}
	if len(deps) != len(wants) {
		t.Fatalf("deps = %+v", deps)
	}
	news := map[string]string{"26.1.0": "27.0.0", "11": "12", "10.2": "10.3", "4.5.0-rc.1": "4.6.0"}
	var edits []patch.Edit
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.DepType != w.depType || d.Name != w.name || d.Datasource != w.ds ||
			d.Versioning != w.versioning || d.Version != w.ver || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
			continue
		}
		if d.Skip != "" {
			continue
		}
		if s := string(b[d.RefSpan.Start:d.RefSpan.End]); s != w.ver {
			t.Errorf("%d: span %q", i, s)
		}
		e, err := New().Edits(dep.Update{Dep: d, NewVersion: news[w.ver]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "engines": {
    "node": ">=27.0.0",
    "npm": "^12",
    "pnpm": "10.3",
    "yarn": ">= 4 <5",
    "vscode": "^1.90.0",
    "bun": {"x": 1}
  },
  "volta": {"node": "27.0.0", "npm": 11, "yarn": "4.6.0", "pnpm": "latest"},
  "engines2": {"node": "1.0.0"}
}`
	if string(out) != want {
		t.Errorf("patched =\n%s", out)
	}
}
