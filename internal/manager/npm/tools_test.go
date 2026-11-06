package npm

import (
	"cmp"
	"os"
	"path/filepath"
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
		depType, name, ds, versioning, ver, rng, skip string
		line                                          int
	}{
		{DepTypeEngines, "node", "node-version", "semver", "26.1.0", "", "", 3},
		{DepTypeEngines, "npm", "npm", "tag", "11", "", "", 4},
		{DepTypeEngines, "pnpm", "npm", "tag", "10.2", "", "", 5},
		{DepTypeEngines, "yarn", "npm", "semver", "4.0.0", ">= 4 <5", "", 6},
		{DepTypeVolta, "node", "node-version", "semver", "26.1.0", "", "", 10},
		{DepTypeVolta, "yarn", "npm", "semver", "4.5.0-rc.1", "", "", 10},
		{DepTypeVolta, "pnpm", "npm", "semver", "latest", "", "tag", 10},
	}
	if len(deps) != len(wants) {
		t.Fatalf("deps = %+v", deps)
	}
	news := map[string]string{"26.1.0": "27.0.0", "11": "12", "10.2": "10.3", "4.0.0": "5.1.0", "4.5.0-rc.1": "4.6.0"}
	var edits []patch.Edit
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.DepType != w.depType || d.Name != w.name || d.Datasource != w.ds ||
			d.Versioning != w.versioning || d.Version != w.ver || d.Range != w.rng || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
			continue
		}
		if d.Skip != "" {
			continue
		}
		if s := string(b[d.RefSpan.Start:d.RefSpan.End]); s != cmp.Or(w.rng, w.ver) {
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
    "yarn": ">= 5 <6",
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

// TestToolRanges covers the npm ranges of engines and volta beyond the token forms (as
// written in real package.json files: next.js, backstage, grafana, airflow, openclaw): they are
// range deps, rewritten under rangeStrategy; ranges without a version and dist-tags stay skipped.
func TestToolRanges(t *testing.T) {
	for _, tt := range []struct {
		raw, ver, skip string
		news, strategy string // a new version and the rangeStrategy it is written under
		want           string // the rewritten range
	}{
		{">= 22", "22.0.0", "", "26.1.0", "", ">= 26"},
		{">=20.9.0 <25", "20.9.0", "", "26.1.0", "", ">=26.1.0 <27"},
		{">=18.18.0 <19 || >=20", "20.0.0", "", "26.1.0", dep.RangeWiden, ">=18.18.0 <19 || >=26"},
		{"^18.18.0 || ^20.9.0 || >=21.1.0", "21.1.0", "", "26.1.0", "", "^18.18.0 || ^20.9.0 || >=26.1.0"},
		{"^20 || ^22", "22.0.0", "", "26.1.0", dep.RangeWiden, "^20 || ^22 || ^26"},
		{"22.x", "22.0.0", "", "26.1.0", "", "26.x"},
		{">22", "22.0.0", "", "26.1.0", "", ">=26"},
		{"<=22", "22.0.0", "", "26.1.0", "", "<=26"},
		{"18 - 22", "18.0.0", "", "26.1.0", "", "18 - 26"},
		{"v20.11.0", "20.11.0", "", "26.1.0", "", "v26.1.0"},
		{"~>4.1", "4.1.0", "", "4.2.0", "bump", "~>4.2"},
		{"*", "", skipRange, "", "", ""},
		{"x", "", skipRange, "", "", ""},
		{"", "", skipRange, "", "", ""},
		{"latest", "", "tag", "", "", ""},
		{">=20 foo", "", skipRange, "", "", ""},
	} {
		for _, depType := range []string{DepTypeEngines, DepTypeVolta} {
			b := []byte(`{"` + depType + `": {"node": "` + tt.raw + `"}}`)
			deps, err := New().Extract("package.json", b, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			if len(deps) != 1 {
				t.Fatalf("%s %q: deps = %+v", depType, tt.raw, deps)
			}
			d := deps[0]
			if d.Skip != tt.skip || d.Version != tt.ver && tt.skip == "" || d.Versioning != "semver" || d.Datasource != "node-version" {
				t.Errorf("%s %q: got %+v", depType, tt.raw, d)
				continue
			}
			if tt.skip != "" {
				continue
			}
			if d.Range != tt.raw || string(b[d.RefSpan.Start:d.RefSpan.End]) != tt.raw {
				t.Errorf("%s %q: range %q span %q", depType, tt.raw, d.Range, b[d.RefSpan.Start:d.RefSpan.End])
			}
			e, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.news, RangeStrategy: tt.strategy})
			if err != nil {
				t.Fatal(err)
			}
			if len(e) != 1 || e[0].New != tt.want {
				t.Errorf("%s %q → %s (%s): edits %+v, want %q", depType, tt.raw, tt.news, tt.strategy, e, tt.want)
			}
		}
	}
}

// TestReadLocksSkipsTools checks that engines/volta ranges are not looked up in the lockfile:
// they name the tool, not a dependency.
func TestReadLocksSkipsTools(t *testing.T) {
	dir := t.TempDir()
	lock := `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/npm": {"version": "10.0.0"}, "node_modules/a": {"version": "1.2.0"}}}`
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	deps, err := New().Extract("package.json", []byte(`{"dependencies": {"a": "^1.0.0"}, "engines": {"npm": ">=10 <12"}}`), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if errs := New().(manager.LockReader).ReadLocks(dir, [][]dep.Dep{deps}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if deps[0].Locked != "1.2.0" || deps[1].Range != ">=10 <12" || deps[1].Locked != "" || deps[1].LockUnread {
		t.Errorf("deps = %+v", deps)
	}
}
