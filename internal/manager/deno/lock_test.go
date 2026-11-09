package deno

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// extractAll extracts the configuration files (slash paths relative to root).
func extractAll(t *testing.T, root string, files ...string) [][]dep.Dep {
	t.Helper()
	out := make([][]dep.Dep, len(files))
	for i, f := range files {
		out[i] = extract(t, f, read(t, filepath.Join(root, filepath.FromSlash(f))))
	}
	return out
}

func lockedOf(ds []dep.Dep, name string) []string {
	var out []string
	for _, d := range ds {
		if d.Name == name {
			out = append(out, d.Locked)
		}
	}
	return out
}

// TestReadLocks reads the lockfiles of denoland/dnt (version 5) and denoland/deployctl
// (version 4); saaskit has `lock: false`.
func TestReadLocks(t *testing.T) {
	files := extractAll(t, "testdata", "dnt/deno.jsonc", "deployctl/deno.jsonc", "saaskit/deno.json")
	if errs := (denoManager{}).ReadLocks("testdata", files); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, tt := range []struct {
		file int
		name string
		want []string
	}{
		{0, "@std/assert", []string{"1.0.19"}}, // `1`, as deno writes it
		{0, "@david/publish-on-tag", []string{"0.2.0"}},
		{0, "@deno/wasmbuild", []string{"0.19.2"}},
		{1, "@std/fmt", []string{"0.217.0", "0.217.0"}},
		{1, "@deno/emit", []string{"0.46.0"}},
		{2, "@std/assert", []string{""}},
	} {
		if got := lockedOf(files[tt.file], tt.name); !slices.Equal(got, tt.want) {
			t.Errorf("%s %s locked = %q, want %q", files[tt.file][0].File, tt.name, got, tt.want)
		}
	}
}

func TestReadLocksStates(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		// A workspace whose root locks its members; ^1.0.0 is written `1` in the lock.
		"ws/deno.json":            `{"workspace": ["./packages/*"], "imports": {"a": "jsr:@std/assert@^1.0.0", "n": "npm:chalk@^5.0.0"}, "tasks": {"t": "deno run npm:chalk@5.0.0"}}`,
		"ws/deno.lock":            `{"version": "5", "specifiers": {"jsr:@std/assert@1": "1.0.13", "jsr:@std/assert@^1.0.17": "1.0.19", "npm:chalk@5": "5.3.0_supports-color@8.1.1", "npm:chalk@^5.1.0": "5.6.2", "jsr:@std/fmt@^1.0.0": "not-a-version"}, "workspace": {"dependencies": ["jsr:@std/assert@1", "npm:chalk@5"], "members": {"packages/a": {"dependencies": ["jsr:@std/fmt@^1.0.0"]}}}}`,
		"ws/packages/a/deno.json": `{"imports": {"f": "jsr:@std/fmt@^1.0.0", "p": "jsr:@std/path@^1.0.0"}}`,
		// Version 3 keeps the specifiers under packages.
		"v3/deno.json":           `{"imports": {"a": "jsr:@std/assert@^0.221.0", "n": "npm:@types/node@^18"}}`,
		"v3/deno.lock":           `{"version": "3", "packages": {"specifiers": {"jsr:@std/assert@^0.221.0": "jsr:@std/assert@0.221.0", "npm:@types/node@^18": "npm:@types/node@18.16.19"}}}`,
		"bad/deno.json":          `{"imports": {"a": "jsr:@std/assert@1", "b": "jsr:@std/fmt@1"}}`,
		"bad/deno.lock":          `{"version": 5`,
		"nover/deno.json":        `{"imports": {"a": "jsr:@std/assert@1"}}`,
		"nover/deno.lock":        `{"specifiers": {}}`,
		"custom/deno.jsonc":      `{"lock": {"path": "locks/deno.lock", "frozen": true}, "imports": {"a": "npm:chalk@^5.0.0"}}`,
		"custom/locks/deno.lock": `{"version": "5", "specifiers": {"npm:chalk@5": "5.6.2"}}`,
		"off/deno.json":          `{"lock": false, "imports": {"a": "npm:chalk@^5.0.0"}}`,
		"off/deno.lock":          `{"version": "5", "specifiers": {"npm:chalk@5": "5.6.2"}}`,
		"none/deno.json":         `{"imports": {"a": "npm:chalk@^5.0.0"}}`,
	})
	files := extractAll(t, root, "ws/deno.json", "ws/packages/a/deno.json", "v3/deno.json", "bad/deno.json", "nover/deno.json", "custom/deno.jsonc", "off/deno.json", "none/deno.json")
	errs := (denoManager{}).ReadLocks(root, files)
	if len(errs) != 2 || !strings.HasPrefix(errs[0].Error(), "bad/deno.lock: ") || !strings.HasPrefix(errs[1].Error(), "nover/deno.lock: not a deno.lock") {
		t.Errorf("errs = %v", errs)
	}
	for _, tt := range []struct {
		file   int
		name   string
		want   []string
		unread bool
	}{
		{0, "@std/assert", []string{"1.0.13"}, false},
		{0, "chalk", []string{"5.3.0", ""}, false}, // the task is not locked
		{1, "@std/fmt", []string{""}, false},       // locked to no version
		{1, "@std/path", []string{""}, false},
		{2, "@std/assert", []string{"0.221.0"}, false},
		{2, "@types/node", []string{"18.16.19"}, false},
		{3, "@std/assert", []string{""}, true},
		{3, "@std/fmt", []string{""}, true},
		{4, "@std/assert", []string{""}, true},
		{5, "chalk", []string{"5.6.2"}, false},
		{6, "chalk", []string{""}, false},
		{7, "chalk", []string{""}, false},
	} {
		ds := files[tt.file]
		if got := lockedOf(ds, tt.name); !slices.Equal(got, tt.want) {
			t.Errorf("%s %s locked = %q, want %q", ds[0].File, tt.name, got, tt.want)
		}
		for _, d := range ds {
			if d.Name == tt.name && d.LockUnread != tt.unread {
				t.Errorf("%s %s LockUnread = %v", d.File, d.Name, d.LockUnread)
			}
		}
	}

	roots := (denoManager{}).LockRoots(root, []string{"ws/packages/a/deno.json", "ws/deno.json", "v3/deno.json", "custom/deno.jsonc", "off/deno.json", "none/deno.json", "missing/deno.json"})
	want := []manager.LockRoot{
		{Manifest: "custom/deno.jsonc", Lockfile: "custom/locks/deno.lock"},
		{Manifest: "v3/deno.json", Lockfile: "v3/deno.lock"},
		{Manifest: "ws/deno.json", Lockfile: "ws/deno.lock"},
	}
	if !slices.Equal(roots, want) {
		t.Errorf("LockRoots = %v, want %v", roots, want)
	}
	l := Locker{}
	if dir, ok := l.Scope(t.Context(), root, "ws/packages/a/deno.json"); !ok || dir != "ws" {
		t.Errorf("Scope(member) = %q, %v", dir, ok)
	}
	if _, ok := l.Scope(t.Context(), root, "off/deno.json"); ok {
		t.Error("Scope with lock: false")
	}
	if got := l.ScopeSiblings(filepath.Join(root, "custom")); !slices.Equal(got, []string{"locks/deno.lock"}) {
		t.Errorf("ScopeSiblings(custom) = %v", got)
	}
	if got := l.ScopeSiblings(filepath.Join(root, "missing")); !slices.Equal(got, l.Siblings()) || got[0] != "deno.lock" {
		t.Errorf("ScopeSiblings(missing) = %v", got)
	}
}

func TestScopeOf(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"deno.json":           `{"workspace": {"members": ["apps/web"]}}`,
		"deno.lock":           `{"version": "5"}`,
		"apps/web/deno.json":  `{}`,
		"apps/api/deno.jsonc": `{"lock": "../../deno.lock"}`,
		"lib/deno.json":       `{"lock": "x.lock"}`,
		"lib/x.lock":          `{"version": "5"}`,
		"broken/deno.json":    `{`,
		"broken/deno.lock":    `{}`,
		"str/deno.json":       `{"lock": true}`,
	})
	for _, tt := range []struct {
		m    string
		want scope
		ok   bool
	}{
		{"deno.json", scope{".", "deno.json", "deno.lock"}, true},
		{"apps/web/deno.json", scope{".", "deno.json", "deno.lock"}, true},
		{"apps/api/deno.jsonc", scope{"apps/api", "apps/api/deno.jsonc", ""}, false}, // a lock outside its directory
		{"lib/deno.json", scope{"lib", "lib/deno.json", "lib/x.lock"}, true},
		{"broken/deno.json", scope{"broken", "broken/deno.json", ""}, false},
		{"str/deno.json", scope{"str", "str/deno.json", "str/deno.lock"}, false},
	} {
		if got, ok := scopeOf(root, tt.m); got != tt.want || ok != tt.ok {
			t.Errorf("scopeOf(%s) = %+v, %v; want %+v, %v", tt.m, got, ok, tt.want, tt.ok)
		}
	}
}

// stub installs a fake deno on PATH (shell builtins only) that logs its arguments, the
// configuration files and lockfile it sees, and then writes $STUB_DIR/lock<n> (n counting its
// runs) as the lockfile when there is one; with STUB_FAIL set it fails.
func stub(t *testing.T) (log, dir string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin, dir := t.TempDir(), t.TempDir()
	log = filepath.Join(dir, "log")
	script := `#!/bin/sh
n=0
[ -f "$STUB_DIR/n" ] && read -r n < "$STUB_DIR/n"
n=$((n+1))
echo "$n" > "$STUB_DIR/n"
lock=${STUB_LOCK:-deno.lock}
{
  echo "run $n: ${0##*/} $* DENO_NO_UPDATE_CHECK=$DENO_NO_UPDATE_CHECK"
  for f in deno.json deno.jsonc */*/deno.json; do
    [ -f "$f" ] || continue
    [ -L "$f" ] && echo "$f is a symlink"
    while IFS= read -r l || [ -n "$l" ]; do echo "  $f: $l"; done < "$f"
  done
  while IFS= read -r l || [ -n "$l" ]; do echo "  lock: $l"; done < "$lock"
  [ -e node_modules ] && echo "node_modules visible"
  [ -e main.ts ] && echo "main.ts visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "error: JSR package not found: @acme/x ($PWD)"
  exit 1
fi
if [ -f "$STUB_DIR/lock$n" ]; then
  while IFS= read -r l || [ -n "$l" ]; do echo "$l"; done < "$STUB_DIR/lock$n" > "$lock"
fi
`
	write(t, bin, map[string]string{"deno": script})
	if err := os.Chmod(filepath.Join(bin, "deno"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_DIR", dir)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_LOCK", "")
	return log, dir
}

func update(depType, datasource, name, rng, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, DepType: depType, Datasource: datasource, Name: name, Range: rng}, NewVersion: version}
}

const denoArgs = "install --lockfile-only --frozen=false DENO_NO_UPDATE_CHECK=1"

func TestProcessUpdates(t *testing.T) {
	log, stubDir := stub(t)
	repo := t.TempDir()
	before := `{"imports": {"p": "jsr:@std/path@^1.0.0", "c": "npm:chalk@^5.0.0", "oak": "https://deno.land/x/oak@v12.6.1/mod.ts"}, "tasks": {"x": "deno run npm:cowsay@1.5.0"}}`
	patched := `{"imports": {"p": "jsr:@std/path@^2.0.0", "c": "npm:chalk@^5.0.0", "oak": "https://deno.land/x/oak@v17.2.0/mod.ts"}, "tasks": {"x": "deno run npm:cowsay@1.6.0"}}`
	orig := `{"version": "5", "specifiers": {"jsr:@std/path@1": "1.1.0", "npm:chalk@5": "5.3.0"}, "workspace": {"dependencies": ["jsr:@std/path@1", "npm:chalk@5"]}}` + "\n"
	write(t, repo, map[string]string{".git/HEAD": "", "app/deno.json": before, "app/deno.lock": orig, "app/main.ts": "", "app/node_modules/x": "", "app/vendor/x": ""})
	dir := filepath.Join(repo, "app")
	// deno's first run locks the pinned requirements as written; another member still requires
	// `npm:chalk@5` (dropped: it admits the new version) and a package requires @std/path ^1.0.0.
	lock1 := `{"version": "5", "specifiers": {"jsr:@std/path@2.0.0": "2.0.0", "jsr:@std/path@^1.0.0": "1.1.0", "npm:chalk@5": "5.3.0", "npm:chalk@5.4.0": "5.4.0"}, "workspace": {"dependencies": ["jsr:@std/path@2.0.0", "npm:chalk@5", "npm:chalk@5.4.0"]}}`
	write(t, stubDir, map[string]string{"lock1": lock1, "lock2": "final\n"})
	ups := map[string][]dep.Update{"deno.json": {
		update(DepTypeImports, "jsr", "@std/path", "^1.0.0", "2.0.0"),
		{Dep: dep.Dep{Manager: Name, DepType: DepTypeImports, Datasource: "deno", Name: "https://deno.land/x/oak", Version: "v12.6.1"}, NewVersion: "v17.2.0"},
		update(DepTypeTasks, "npm", "cowsay", "1.5.0", "1.6.0"),
	}}
	locks := map[string][]dep.Update{"deno.json": {update(DepTypeImports, "npm", "chalk", "^5.0.0", "5.4.0")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"deno.json": []byte(patched)}, map[string][]byte{"deno.lock": []byte(orig)}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["deno.lock"]); got != "final\n" {
		t.Errorf("deno.lock = %q", got)
	}
	pinned := `{"imports": {"p": "jsr:@std/path@2.0.0", "c": "npm:chalk@5.4.0", "oak": "https://deno.land/x/oak@v17.2.0/mod.ts"}, "tasks": {"x": "deno run npm:cowsay@1.6.0"}}`
	renamed := `{"specifiers":{"jsr:@std/path@^1.0.0":"1.1.0","jsr:@std/path@^2.0.0":"2.0.0","npm:chalk@^5.0.0":"5.4.0"},"version":"5"}`
	want := "run 1: deno " + denoArgs + "\n  deno.json: " + pinned + "\n  lock: " + strings.TrimSuffix(orig, "\n") + "\nmain.ts visible\n" +
		"run 2: deno " + denoArgs + "\n  deno.json: " + patched + "\n  lock: " + renamed + "\nmain.ts visible\n"
	if got := string(read(t, log)); got != want {
		t.Errorf("log:\n%s\nwant:\n%s", got, want)
	}
	if string(read(t, filepath.Join(dir, "deno.json"))) != before || string(read(t, filepath.Join(dir, "deno.lock"))) != orig {
		t.Error("the working tree was modified")
	}
}

func TestProcessWorkspace(t *testing.T) {
	log, stubDir := stub(t)
	dir := t.TempDir()
	root := `{"workspace": ["./packages/*"], "lock": {"path": "locks/deno.lock"}}`
	member := `{"imports": {"c": "npm:chalk@^5.0.0"}}`
	write(t, dir, map[string]string{"deno.json": root, "locks/deno.lock": "lock\n", "packages/a/deno.json": member, "packages/b/deno.jsonc": "{}", "packages/c/x": ""})
	t.Setenv("STUB_LOCK", "locks/deno.lock")
	write(t, stubDir, map[string]string{"lock1": "locked\n"})
	// Only a task changes: one run on the final configuration.
	ups := map[string][]dep.Update{"packages/a/deno.json": {update(DepTypeTasks, "npm", "cowsay", "1.5.0", "1.6.0")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, nil, map[string][]byte{"locks/deno.lock": []byte("lock\n")}, ups, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["locks/deno.lock"]); got != "locked\n" {
		t.Errorf("lock = %q", got)
	}
	got := string(read(t, log))
	if !strings.HasPrefix(got, "run 1: deno "+denoArgs+"\n  deno.json: "+root+"\n  packages/a/deno.json: "+member+"\n  lock: lock\n") || strings.Contains(got, "symlink") || strings.Contains(got, "run 2") {
		t.Errorf("log:\n%s", got)
	}
}

func TestProcessOutcomes(t *testing.T) {
	log, stubDir := stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"deno.json": `{"imports": {"c": "npm:chalk@^5.0.0"}}`, "deno.lock": "lock\n"})
	files := map[string][]byte{"deno.lock": []byte("lock\n")}
	write(t, stubDir, map[string]string{"lock1": "lock\n"})
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err == nil || !strings.HasPrefix(err.Error(), "deno install in "+dir+": error: JSR package not found: @acme/x ("+dir+")") {
		t.Errorf("failure = %v", err)
	}
	t.Setenv("STUB_FAIL", "")
	write(t, stubDir, map[string]string{"n": "0", "lock1": "not json\n"})
	lockOnly := map[string][]dep.Update{"deno.json": {update(DepTypeImports, "npm", "chalk", "^5.0.0", "5.4.0")}}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, files, nil, lockOnly); err == nil || !strings.HasPrefix(err.Error(), "deno.lock: ") {
		t.Errorf("unparsable lock after the pinned run = %v", err)
	}
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, lockOnly); err == nil {
		t.Error("lockfile-only update without deno.lock")
	}
	if _, err := (Locker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil || !strings.HasPrefix(err.Error(), "no deno.json in ") {
		t.Errorf("missing deno.json = %v", err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, map[string][]byte{"deno.json": []byte(`{"imports": [`)}, files, lockOnly, nil); err == nil {
		t.Error("unparsable deno.json")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "deno not found (needed to update deno.lock)" {
		t.Errorf("no deno: %v", err)
	}
	if b := read(t, log); !strings.Contains(string(b), "run 1: deno "+denoArgs) {
		t.Errorf("log:\n%s", b)
	}
}

func TestMaintain(t *testing.T) {
	log, stubDir := stub(t)
	dir := t.TempDir()
	orig := `{"version": "4", "specifiers": {"npm:chalk@5": "5.3.0"}, "npm": {}, "redirects": {"a": "b"}, "remote": {"https://deno.land/x/oak@v12.6.1/mod.ts": "abc"}}`
	write(t, dir, map[string]string{"deno.jsonc": `{"imports": {"c": "npm:chalk@^5.0.0"}}`, "deno.lock": orig})
	write(t, stubDir, map[string]string{"lock1": "fresh\n"})
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{"deno.lock": []byte(orig)})
	if err != nil || string(out["deno.lock"]) != "fresh\n" {
		t.Errorf("Maintain = %q, %v", out["deno.lock"], err)
	}
	if got := string(read(t, log)); !strings.Contains(got, `  lock: {"version":"4","remote":{"https://deno.land/x/oak@v12.6.1/mod.ts":"abc"}}`) {
		t.Errorf("log:\n%s", got)
	}
	if out, err := (Locker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), dir, map[string][]byte{"deno.lock": []byte("{")}); err == nil {
		t.Error("unparsable lock")
	}
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), map[string][]byte{"deno.lock": nil}); err == nil {
		t.Error("missing deno.json")
	}
}

// TestRename renames the pinned requirements of a lockfile deno 2.9.8 wrote
// (`deno install --lockfile-only` with `jsr:@std/assert@1.0.14` and `npm:chalk@5.4.0` pinned).
func TestRename(t *testing.T) {
	lock := `{
  "version": "5",
  "specifiers": {
    "jsr:@std/assert@1.0.14": "1.0.14",
    "jsr:@std/internal@^1.0.10": "1.0.14",
    "jsr:@std/path@1.0.0": "1.0.0",
    "npm:chalk@5.4.0": "5.4.0"
  },
  "jsr": {
    "@std/assert@1.0.14": {
      "integrity": "68d0d4a43b365abc927f45a9b85c639ea18a9fab96ad92281e493e4ed84abaa4",
      "dependencies": [
        "jsr:@std/internal"
      ]
    }
  },
  "workspace": {
    "dependencies": [
      "jsr:@std/assert@1.0.14",
      "jsr:@std/path@1.0.0",
      "npm:chalk@5.4.0"
    ]
  }
}
`
	out, err := rename([]byte(lock), map[string][]string{"jsr:@std/assert@1.0.14": {"jsr:@std/assert@^1.0.0"}, "npm:chalk@5.4.0": {"npm:chalk@^5.0.0", "npm:chalk@5"}, "npm:gone@1.0.0": {"npm:gone@1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jsr":{"@std/assert@1.0.14":{"integrity":"68d0d4a43b365abc927f45a9b85c639ea18a9fab96ad92281e493e4ed84abaa4","dependencies":["jsr:@std/internal"]}},"specifiers":{"jsr:@std/assert@^1.0.0":"1.0.14","jsr:@std/internal@^1.0.10":"1.0.14","jsr:@std/path@1.0.0":"1.0.0","npm:chalk@5":"5.4.0","npm:chalk@^5.0.0":"5.4.0"},"version":"5"}`
	if string(out) != want {
		t.Errorf("rename =\n%s\nwant\n%s", out, want)
	}
	if _, err := rename([]byte(`{`), nil); err == nil {
		t.Error("unparsable lock")
	}
	if _, err := rename([]byte(`{"specifiers": []}`), nil); err == nil {
		t.Error("specifiers not an object")
	}
}
