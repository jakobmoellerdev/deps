package beam

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Real-world gleam.toml files (fetched 2026-11-06): gleam-lang/packages (with its
// manifest.toml) and gleam-wisp/wisp.
func TestExtractGleamFixtures(t *testing.T) {
	for file, want := range map[string]map[string]string{
		"testdata/gleam-packages/gleam.toml": {
			"gleam_stdlib": `15 dependencies hex "~> 0.23" "0.23.0" "" `,
			"wisp":         `25 dependencies hex ">= 2.0.0 and < 3.0.0" "2.0.0" "" `,
			"gleeunit":     `37 dev-dependencies hex "~> 1.0" "1.0.0" "" `,
		},
		"testdata/wisp/gleam.toml": {
			"gleam_json": `15 dependencies hex ">= 3.0.0 and < 4.0.0" "3.0.0" "" `,
			"gleeunit":   `26 dev-dependencies hex ">= 1.0.0 and < 2.0.0" "1.0.0" "" `,
		},
	} {
		got := summary(extract(t, NewGleam(), file, []byte(read(t, file))))
		for name, w := range want {
			if got[name] != w {
				t.Errorf("%s: %s = %s, want %s", file, name, got[name], w)
			}
		}
	}
}

func TestExtractGleam(t *testing.T) {
	src := `name = "app"

[dependencies]
plain = ">= 0.44.0 and < 2.0.0"
table = { version = "~> 1.0" }
local = { path = "../local" }
remote = { git = "https://github.com/o/r", ref = "main" }
escaped = "~> 1.\u0030"
bad = "^1.0"
number = 1

[dev_dependencies]
under = "~> 2.0"
`
	ds := extract(t, NewGleam(), "gleam.toml", []byte(src))
	got := summary(ds)
	for name, want := range map[string]string{
		"plain":   `4 dependencies hex ">= 0.44.0 and < 2.0.0" "0.44.0" "" `,
		"table":   `5 dependencies hex "~> 1.0" "1.0.0" "" `,
		"local":   `6 dependencies hex "" "" "path dependency" `,
		"remote":  `7 dependencies hex "" "" "git dependency" `,
		"escaped": `8 dependencies hex "" "~> 1.0" "cannot locate value" `,
		"bad":     `9 dependencies hex "" "^1.0" "unsupported requirement" `,
		"number":  `10 dependencies hex "" "" "no requirement" `,
		"under":   `13 dev-dependencies hex "~> 2.0" "2.0.0" "" `,
	} {
		if got[name] != want {
			t.Errorf("%s = %s, want %s", name, got[name], want)
		}
	}
	es, err := NewGleam().Edits(dep.Update{Dep: ds[1], NewVersion: "2.1.0", RangeStrategy: "widen"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), es)
	if err != nil || !strings.Contains(string(out), `table = { version = "~> 1.0 or ~> 2.1" }`) {
		t.Errorf("patched = %s, %v", out, err)
	}
	if r, ok := NewGleam().(manager.RangeRewriter).NewRange(ds[0], "2.1.0", "widen"); !ok || r != ">= 0.44.0 and < 3.0.0" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
	if _, err := NewGleam().Extract("gleam.toml", []byte("[dependencies\n"), manager.Env{}); err == nil {
		t.Error("invalid TOML")
	}
}

func TestGleamReadLocks(t *testing.T) {
	root := copyFixture(t, "gleam-packages")
	write(t, root, map[string]string{"broken/gleam.toml": "[dependencies]\na = \"~> 1.0\"\n", "broken/manifest.toml": "x = 1\n"})
	files := [][]dep.Dep{
		extract(t, NewGleam(), "gleam.toml", []byte(read(t, filepath.Join(root, "gleam.toml")))),
		extract(t, NewGleam(), "broken/gleam.toml", []byte(read(t, filepath.Join(root, "broken", "gleam.toml")))),
	}
	errs := NewGleam().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "broken/manifest.toml: not a Gleam manifest.toml") {
		t.Errorf("errs = %v", errs)
	}
	locked := map[string]string{}
	for _, f := range files {
		for _, d := range f {
			locked[d.Name] = d.Locked
		}
	}
	if locked["gleam_stdlib"] != "0.71.0" || locked["wisp"] != "2.2.2" || locked["gleeunit"] != "1.9.0" || !files[1][0].LockUnread {
		t.Errorf("locked = %v", locked)
	}
	roots := NewGleam().(manager.LockMaintainer).LockRoots(root, []string{"gleam.toml", "broken/gleam.toml", "none/gleam.toml"})
	if want := []manager.LockRoot{{Manifest: "broken/gleam.toml", Lockfile: "broken/manifest.toml"}, {Manifest: "gleam.toml", Lockfile: "manifest.toml"}}; !slices.Equal(roots, want) {
		t.Errorf("roots = %+v", roots)
	}
	if _, err := gleamLocked([]byte("packages = [")); err == nil {
		t.Error("invalid manifest")
	}
}

func TestGleamProcessUpdates(t *testing.T) {
	log := stub(t, "gleam", "manifest.toml")
	dir := copyFixture(t, "gleam-packages")
	write(t, dir, map[string]string{"build/packages/x": ""})
	before := read(t, filepath.Join(dir, "gleam.toml"))
	lock, _, _ := strings.Cut(read(t, filepath.Join(dir, "manifest.toml")), "[requirements]") // the stub writes its own
	write(t, dir, map[string]string{"manifest.toml": lock})
	patched := strings.Replace(before, `gleam_json = ">= 3.0.0 and < 4.0.0"`, `gleam_json = ">= 3.0.0 and < 5.0.0"`, 1)
	ups := map[string][]dep.Update{"gleam.toml": {update(NameGleam, "gleam.toml", "gleam_json", 16, Datasource, "4.0.0")}}
	locks := map[string][]dep.Update{"gleam.toml": {update(NameGleam, "gleam.toml", "wisp", 25, Datasource, "2.2.3"), update(NameGleam, "gleam.toml", "wisp", 25, Datasource, "2.2.3")}}
	t.Setenv("STUB_PIN", "1")
	out, err := GleamLocker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"gleam.toml": []byte(patched)}, map[string][]byte{"manifest.toml": []byte(lock)}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out["manifest.toml"])
	// The stub appends its args and a [requirements] table with the pins; the pins are set back.
	if !strings.HasSuffix(got, "# deps update gleam_json wisp\n[requirements]\ngleam_json = { version = \">= 3.0.0 and < 5.0.0\" }\nwisp = { version = \">= 2.0.0 and < 3.0.0\" }\n") {
		t.Errorf("manifest.toml ends with %q", got[max(0, len(got)-200):])
	}
	l := read(t, log)
	if !strings.HasPrefix(l, "args: deps update gleam_json wisp\n") || !strings.Contains(l, `gleam.toml: gleam_json = "== 4.0.0"`) || !strings.Contains(l, `gleam.toml: wisp = "== 2.2.3"`) || strings.Contains(l, "build visible") || strings.Contains(l, "lock is a symlink") {
		t.Errorf("log:\n%s", l)
	}
	if read(t, filepath.Join(dir, "gleam.toml")) != before || read(t, filepath.Join(dir, "manifest.toml")) != lock {
		t.Error("the working tree was modified")
	}
}

func TestGleamProcessOutcomes(t *testing.T) {
	stub(t, "gleam", "manifest.toml")
	dir := t.TempDir()
	write(t, dir, map[string]string{"gleam.toml": "[dependencies]\na = \"~> 1.0\"\n", "manifest.toml": "packages = []\n"})
	files := map[string][]byte{"manifest.toml": []byte("packages = []\n")}
	if out, err := (GleamLocker{}).Process(t.Context(), dir, nil, files); err != nil || string(out["manifest.toml"]) != "packages = []\n# deps download\n" {
		t.Errorf("Process = %q, %v", out["manifest.toml"], err)
	}
	if out, err := (GleamLocker{}).Maintain(t.Context(), dir, files); err != nil || string(out["manifest.toml"]) != "packages = []\n# deps update\n" {
		t.Errorf("Maintain = %q, %v", out["manifest.toml"], err)
	}
	ups := map[string][]dep.Update{"gleam.toml": {update(NameGleam, "gleam.toml", "a", 2, Datasource, "1.1.0")}}
	// A pin that does not end up in [requirements] leaves the lock as the tool wrote it.
	if out, err := (GleamLocker{}).ProcessUpdates(t.Context(), dir, nil, files, ups, nil); err != nil || string(out["manifest.toml"]) != "packages = []\n# deps update a\n" {
		t.Errorf("ProcessUpdates = %q, %v", out["manifest.toml"], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (GleamLocker{}).ProcessUpdates(t.Context(), dir, nil, files, ups, nil); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	if _, err := (GleamLocker{}).Process(t.Context(), dir, nil, files); err == nil || !strings.HasPrefix(err.Error(), "gleam deps in "+dir+": ") {
		t.Errorf("failure = %v", err)
	}
	for name, u := range map[string]dep.Update{
		"unknown package": update(NameGleam, "gleam.toml", "missing", 2, Datasource, "1.1.0"),
		"invalid version": update(NameGleam, "gleam.toml", "a", 2, Datasource, "v1"),
	} {
		if _, err := (GleamLocker{}).ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{"gleam.toml": {u}}, nil); err == nil || !strings.Contains(err.Error(), "no requirement of") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if out, err := (GleamLocker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if out, err := (GleamLocker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock maintenance = %v, %v", out, err)
	}
	if _, err := (GleamLocker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, ups); err == nil {
		t.Error("lockfile-only update without manifest.toml")
	}
	if _, err := (GleamLocker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing gleam.toml")
	}
	if _, err := (GleamLocker{}).Maintain(t.Context(), t.TempDir(), files); err == nil {
		t.Error("missing gleam.toml for maintenance")
	}
	if _, err := (GleamLocker{}).Process(t.Context(), dir, map[string][]byte{"gleam.toml": []byte("[x")}, files); err == nil {
		t.Error("invalid gleam.toml")
	}
	if _, err := restoreRequirements([]byte("[x"), map[string]string{"a": "~> 1.0"}); err == nil {
		t.Error("invalid manifest.toml")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (GleamLocker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "gleam not found (needed to update manifest.toml)" {
		t.Errorf("no gleam: %v", err)
	}
}
