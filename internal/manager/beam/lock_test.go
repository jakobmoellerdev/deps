package beam

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func copyFixture(t *testing.T, dir string) string {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir(filepath.Join("testdata", dir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("testdata", dir), p)
		write(t, root, map[string]string{filepath.ToSlash(rel): read(t, p)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMixLocked(t *testing.T) {
	locked, err := mixLocked([]byte(read(t, "testdata/plausible/mix.lock")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"jason": "1.4.5", "phoenix": "1.8.9", "decimal": "3.2.0", "plug_cowboy": "2.9.0"} {
		if locked[name].version != want || locked[name].app != name {
			t.Errorf("%s = %+v, want %s", name, locked[name], want)
		}
	}
	if _, ok := locked["con_cache"]; ok {
		t.Error("git entries are not Hex packages")
	}
	org, err := mixLocked([]byte(`%{
  "renamed": {:hex, :real, "1.0.0", "abc", [:mix], [], "hexpm", "def"},
  "secret": {:hex, :secret, "0.2.0", "abc", [:mix], [{:dep, "~> 1.0", [hex: :dep, repo: "hexpm:acme", optional: false]}], "hexpm:acme", "def"},
  "old": {:hex, :old, "0.1.0", "abc"},
  "local": {:path, "../local"},
}
`))
	if err != nil || org["real"].app != "renamed" || org["secret:acme"].version != "0.2.0" || org["old"].version != "0.1.0" {
		t.Errorf("locked = %+v, %v", org, err)
	}
	for _, bad := range []string{"[]", "%{", `%{"a": 1}`, `%{a: {:hex, :a, "1"}}`, `%{"a": {:hex, :a, :v}}`} {
		if _, err := mixLocked([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestMixReadLocks(t *testing.T) {
	root := copyFixture(t, "elixir-ls")
	write(t, root, map[string]string{
		"plain/mix.exs":  `defp deps, do: [{:jason, "~> 1.0"}, {:any, ">= 0.0.0"}, {:gone, ">= 0.0.0"}]`,
		"plain/mix.lock": `%{"jason": {:hex, :jason, "1.4.4", "x", [:mix], [], "hexpm", "y"}, "any": {:hex, :any, "2.0.0", "x", [:mix], [], "hexpm", "y"}}`,
		"broken/mix.exs": `defp deps, do: [{:a, "~> 1.0"}, {:b, "~> 2.0"}]`, "broken/mix.lock": "not a lock",
		"nolock/mix.exs": `defp deps, do: [{:a, "~> 1.0"}, {:b, "< 2.0.0"}]`,
		"escape/mix.exs": `lockfile: "../../../mix.lock"` + "\n" + `defp deps, do: [{:a, "~> 1.0"}]`,
	})
	var files [][]dep.Dep
	for _, f := range []string{"mix.exs", "apps/language_server/mix.exs", "plain/mix.exs", "broken/mix.exs", "nolock/mix.exs", "escape/mix.exs"} {
		files = append(files, extract(t, NewMix(), f, []byte(read(t, filepath.Join(root, f)))))
	}
	errs := NewMix().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "broken/mix.lock: ") {
		t.Errorf("errs = %v", errs)
	}
	got := map[string]string{}
	for _, f := range files {
		for _, d := range f {
			got[d.File+" "+d.Name] = d.Version + " " + d.Locked + " " + d.Skip
			if d.LockUnread {
				got[d.File+" "+d.Name] += "unread"
			}
		}
	}
	for k, want := range map[string]string{
		"mix.exs spec_lint":                         "0.1.0 0.1.1 ",
		"apps/language_server/mix.exs typed_struct": "0.3.0 0.3.0 ",
		"apps/language_server/mix.exs stream_data":  "0.5.0 0.5.0 ",
		"plain/mix.exs jason":                       "1.0.0 1.4.4 ",
		"plain/mix.exs any":                         "2.0.0 2.0.0 ",
		"plain/mix.exs gone":                        "  no locked version",
		"broken/mix.exs a":                          "1.0.0  unread",
		"nolock/mix.exs a":                          "1.0.0  ",
		"nolock/mix.exs b":                          "  no locked version",
		"escape/mix.exs a":                          "1.0.0  ",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	roots := NewMix().(manager.LockMaintainer).LockRoots(root, []string{"apps/language_server/mix.exs", "mix.exs", "plain/mix.exs", "nolock/mix.exs", "escape/mix.exs"})
	want := []manager.LockRoot{{Manifest: "mix.exs", Lockfile: "mix.lock"}, {Manifest: "plain/mix.exs", Lockfile: "plain/mix.lock"}}
	if !slices.Equal(roots, want) {
		t.Errorf("roots = %+v", roots)
	}
	if dir, ok := (MixLocker{}).Scope(t.Context(), root, "apps/language_server/mix.exs"); !ok || dir != "." {
		t.Errorf("umbrella app scope = %q, %v", dir, ok)
	}
}

// stub installs a fake tool on PATH (only it: it uses shell builtins) that logs its
// arguments, the mix.exs/gleam.toml files it sees and what of the mirror is visible, and
// appends its arguments to the lockfile; with STUB_FAIL set it fails, with STUB_NOOP it
// changes nothing, with STUB_PIN (gleam) it records the pins in [requirements].
func stub(t *testing.T, name, lock string) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  echo "args: $*"
  for f in mix.exs apps/web/mix.exs gleam.toml; do
    [ -f "$f" ] && [ ! -L "$f" ] && while IFS= read -r l; do echo "  $f: $l"; done < "$f"
  done
  [ -L "` + lock + `" ] && echo "lock is a symlink"
  [ -e deps ] && echo "deps visible"
  [ -e build ] && echo "build visible"
  [ -L config/config.exs ] && echo "config linked"
  [ -e ../shared/mix.exs ] && echo "sibling visible"
  [ -n "$MIX_ENV" ] && echo "MIX_ENV=$MIX_ENV"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "** (Mix) Hex dependency resolution failed in $PWD"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "# $*" >> "` + lock + `"
if [ -n "$STUB_PIN" ]; then
  printf '[requirements]\ngleam_json = { version = "== 3.1.0" }\nwisp = { version = "== 2.2.2" }\n' >> "` + lock + `"
fi
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("STUB_PIN", "")
	return log
}

func update(m, file, name string, line int, datasource, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: m, File: file, Name: name, Line: line, Datasource: datasource}, NewVersion: version}
}

func TestMixProcessUpdates(t *testing.T) {
	log := stub(t, "mix", "mix.lock")
	repo := t.TempDir()
	root := `defp deps, do: []` + "\n"
	before := "defp deps do\n  [{:jason, \"~> 1.3\"},\n   {:real, \"~> 0.1\", hex: :real_pkg},\n   {:tagged, github: \"o/t\", tag: \"v1\"}]\nend\n"
	write(t, repo, map[string]string{
		".git/HEAD": "", "shared/mix.exs": "",
		"app/mix.exs": root, "app/mix.lock": "lock\n", "app/config/config.exs": "", "app/deps/jason/mix.exs": "", "app/_build/x": "",
		"app/apps/web/mix.exs": before,
	})
	dir := filepath.Join(repo, "app")
	patched := strings.Replace(strings.Replace(before, `"~> 1.3"`, `"~> 2.0"`, 1), `"v1"`, `"v2"`, 1)
	file := "app/apps/web/mix.exs"
	ups := map[string][]dep.Update{"apps/web/mix.exs": {update(NameMix, file, "jason", 2, Datasource, "2.0.1"), update(NameMix, file, "tagged", 4, GitDatasource, "v2")}}
	locks := map[string][]dep.Update{"apps/web/mix.exs": {update(NameMix, file, "real_pkg", 3, Datasource, "0.1.5")}}
	out, err := MixLocker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"apps/web/mix.exs": []byte(patched)}, map[string][]byte{"mix.lock": []byte("lock\n")}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["mix.lock"]); got != "lock\n# deps.update jason real tagged\n" {
		t.Errorf("mix.lock = %q", got)
	}
	want := "args: deps.update jason real tagged\n" +
		"  apps/web/mix.exs: defp deps do\n  apps/web/mix.exs:   [{:jason, \"== 2.0.1\"},\n  apps/web/mix.exs:    {:real, \"== 0.1.5\", hex: :real_pkg},\n" +
		"  apps/web/mix.exs:    {:tagged, github: \"o/t\", tag: \"v2\"}]\n  apps/web/mix.exs: end\n" +
		"config linked\nsibling visible\nMIX_ENV=dev\n"
	if got := read(t, log); got != want {
		t.Errorf("log:\n%s\nwant:\n%s", got, want)
	}
	if read(t, filepath.Join(dir, "apps", "web", "mix.exs")) != before || read(t, filepath.Join(dir, "mix.lock")) != "lock\n" {
		t.Error("the working tree was modified")
	}
	// An update whose dependency is not on its line fails.
	bad := map[string][]dep.Update{"apps/web/mix.exs": {update(NameMix, file, "jason", 3, Datasource, "2.0.1")}}
	if _, err := (MixLocker{}).ProcessUpdates(t.Context(), dir, nil, map[string][]byte{"mix.lock": []byte("lock\n")}, bad, nil); err == nil || !strings.Contains(err.Error(), "dependency jason not found on line 3") {
		t.Errorf("misplaced update: %v", err)
	}
}

func TestMixProcessOutcomes(t *testing.T) {
	log := stub(t, "mix", "mix.lock")
	dir := t.TempDir()
	write(t, dir, map[string]string{"mix.exs": "defp deps, do: [{:a, \"~> 1.0\"}]\n", "mix.lock": "lock\n"})
	files := map[string][]byte{"mix.lock": []byte("lock\n")}
	if out, err := (MixLocker{}).Process(t.Context(), dir, nil, files); err != nil || string(out["mix.lock"]) != "lock\n# deps.get\n" {
		t.Errorf("Process = %q, %v", out["mix.lock"], err)
	}
	if out, err := (MixLocker{}).Maintain(t.Context(), dir, files); err != nil || string(out["mix.lock"]) != "lock\n# deps.update --all\n" {
		t.Errorf("Maintain = %q, %v", out["mix.lock"], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (MixLocker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	ups := map[string][]dep.Update{"mix.exs": {update(NameMix, "mix.exs", "a", 1, Datasource, "1.1.0")}}
	if _, err := (MixLocker{}).ProcessUpdates(t.Context(), dir, nil, files, ups, nil); err == nil || err.Error() != "mix deps.update in "+dir+": ** (Mix) Hex dependency resolution failed in "+dir {
		t.Errorf("failure = %v", err)
	}
	if out, err := (MixLocker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if out, err := (MixLocker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock maintenance = %v, %v", out, err)
	}
	if _, err := (MixLocker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, ups); err == nil {
		t.Error("lockfile-only update without mix.lock")
	}
	if _, err := (MixLocker{}).ProcessUpdates(t.Context(), t.TempDir(), nil, files, ups, nil); err == nil {
		t.Error("missing mix.exs")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (MixLocker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "mix not found (needed to update mix.lock)" {
		t.Errorf("no mix: %v", err)
	}
	if !strings.Contains(read(t, log), "args: deps.update a\n  mix.exs: defp deps, do: [{:a, \"== 1.1.0\"}]") {
		t.Errorf("log:\n%s", read(t, log))
	}
	if (MixLocker{}).Siblings()[0] != "mix.lock" || (GleamLocker{}).Siblings()[0] != "manifest.toml" {
		t.Error("siblings")
	}
	if _, _, err := mirror(filepath.Join(t.TempDir(), "missing"), "x-*", nil, nil); err == nil {
		t.Error("mirror of a missing dir")
	}
}

func TestMirrorSkipsOnlyAtRoot(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"mix.exs", "deps/a/mix.exs", "lib/x/deps/file", "src/build/file"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmp, base, err := mirror(dir, "x-*", nil, []string{"deps", "build"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	for _, f := range []string{"mix.exs", "lib/x/deps/file", "src/build/file"} {
		if _, err := os.Stat(filepath.Join(base, f)); err != nil {
			t.Errorf("%s missing from mirror: %v", f, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "deps")); !os.IsNotExist(err) {
		t.Errorf("root deps mirrored: %v", err)
	}
}
