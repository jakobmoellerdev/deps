package nodetool

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const wsLock = `{
  "name": "ws",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {"name": "ws", "workspaces": ["packages/*"], "dependencies": {"a": "^1.0.0"}},
    "node_modules/a": {"version": "1.2.0", "resolved": "https://r/a-1.2.0.tgz"},
    "node_modules/b": {"version": "2.0.0"},
    "node_modules/m": {"resolved": "packages/m", "link": true},
    "node_modules/@s/c": {"version": "3.1.0", "dev": true},
    "packages/m": {"name": "m", "dependencies": {"a": "^1.0.0", "b": "^1.0.0"}},
    "packages/m/node_modules/b": {"version": "1.5.0"}
  }
}
`

func TestReadLocked(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"package.json":            `{"name": "ws", "workspaces": ["packages/*"]}`,
		"package-lock.json":       wsLock,
		"packages/m/package.json": `{"name": "m"}`,
		"packages/n/package.json": `{"name": "n"}`,
		// lockfile v1: not read
		"old/package.json":      `{}`,
		"old/package-lock.json": `{"lockfileVersion": 1, "dependencies": {"a": {"version": "1.0.0"}}}`,
		// pnpm: not read; yarn named by packageManager wins over package-lock.json
		"pn/package.json":         `{}`,
		"pn/pnpm-lock.yaml":       "lockfileVersion: '9.0'\n",
		"y/package.json":          `{"packageManager": "yarn@4.1.0"}`,
		"y/yarn.lock":             "",
		"y/package-lock.json":     wsLock,
		"bad/package.json":        `{}`,
		"bad/package-lock.json":   `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": 1}}}`,
		"trunc/package.json":      `{}`,
		"trunc/package-lock.json": `{"lockfileVersion": 3, "packages": {`,
		"none/package.json":       `{}`,
	})
	wants := []Want{
		{"package.json", "a"},            // hoisted
		{"package.json", "@s/c"},         // scoped
		{"package.json", "missing"},      // not locked
		{"packages/m/package.json", "b"}, // the member's own copy wins
		{"packages/m/package.json", "a"}, // hoisted
		{"packages/m/package.json", "m"}, // a link has no version
		{"packages/n/package.json", "b"},
		{"old/package.json", "a"},
		{"pn/package.json", "a"},
		{"y/package.json", "a"},
		{"bad/package.json", "a"},
		{"trunc/package.json", "a"},
		{"none/package.json", "a"},
	}
	got, errs := ReadLocked(root, wants)
	want := []LockState{
		{Version: "1.2.0"}, {Version: "3.1.0"}, {}, {Version: "1.5.0"}, {Version: "1.2.0"}, {}, {Version: "2.0.0"},
		{Unread: true}, {Unread: true}, {Unread: true}, {Unread: true}, {Unread: true}, {},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%v = %+v, want %+v", wants[i], got[i], want[i])
		}
	}
	if len(errs) != 2 || !strings.HasPrefix(errs[0].Error(), "bad/package-lock.json: packages[\"node_modules/a\"]: ") ||
		!strings.HasPrefix(errs[1].Error(), "trunc/package-lock.json: ") {
		t.Errorf("errs = %v", errs)
	}
}

func TestLockedVersionsErrors(t *testing.T) {
	for _, src := range []string{"", "[]", `{"lockfileVersion": "x"}`, `{"packages": []}`, `{"packages": {"node_modules/a": []}}`} {
		if _, _, err := lockedVersions(bufio.NewReader(strings.NewReader(src)), map[string]bool{"node_modules/a": true}); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
}

// BenchmarkReadLocked reads the locked versions of an npm workspace with 50 members, three
// wants each.
func BenchmarkReadLocked(b *testing.B) {
	const members = 50
	root := b.TempDir()
	var lock strings.Builder
	lock.WriteString(`{"lockfileVersion": 3, "packages": {"": {"name": "ws", "workspaces": ["packages/*"]}`)
	files := map[string]string{"package.json": `{"name": "ws", "private": true, "workspaces": ["packages/*", "!packages/skip"],` +
		` "devDependencies": {"eslint": "^9.0.0", "typescript": "^5.4.0", "vitest": "^1.6.0"}}`}
	var wants []Want
	for i := range members {
		m := fmt.Sprintf("packages/m%d/package.json", i)
		files[m] = fmt.Sprintf(`{"name": "m%d", "dependencies": {"a": "^1.0.0", "b": "^2.0.0", "c": "^3.0.0"}}`, i)
		fmt.Fprintf(&lock, `, "packages/m%d": {"name": "m%d"}`, i, i)
		wants = append(wants, Want{m, "a"}, Want{m, "b"}, Want{m, "c"})
	}
	lock.WriteString(`, "node_modules/a": {"version": "1.2.0"}, "node_modules/b": {"version": "2.1.0"}, "node_modules/c": {"version": "3.0.1"}}}`)
	files["package-lock.json"] = lock.String()
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if got, errs := ReadLocked(root, wants); len(errs) != 0 || got[len(got)-1].Version != "3.0.1" {
			b.Fatalf("ReadLocked = %v, %v", got, errs)
		}
	}
}

func lockUp(depType, name, rng, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{DepType: depType, Name: name, Range: rng}, NewVersion: version, Lockfile: true}
}

func TestLockCommands(t *testing.T) {
	got := lockCommands(map[string][]dep.Update{
		"packages/z/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.1.0")},
		"package.json":            {lockUp("dependencies", "z", "^1.0.0", "1.2.0"), lockUp("devDependencies", "@s/a", "~2.0.0", "2.0.3")},
		"packages/m/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.1.0")},
	})
	want := [][]string{{"@s/a@2.0.3", "z@1.2.0"}, {"--workspace=packages/m", "b@1.1.0"}, {"--workspace=packages/z", "b@1.1.0"}}
	if len(got) != len(want) {
		t.Fatalf("lockCommands = %q", got)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("command %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRestoreRanges(t *testing.T) {
	saved := strings.NewReplacer(`"dependencies": {"a": "^1.0.0"}}`, `"dependencies": {"a": "^1.3.0"}}`,
		`"a": "^1.0.0", "b": "^1.0.0"`, `"a": "^1.0.0", "b": "^1.6.0"`).Replace(wsLock)
	locks := map[string][]dep.Update{
		"package.json":            {lockUp("dependencies", "a", "^1.0.0", "1.3.0")},
		"packages/m/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.6.0")},
	}
	got, err := restoreRanges([]byte(saved), locks)
	if err != nil || string(got) != wsLock {
		t.Fatalf("restoreRanges = %s, %v", got, err)
	}

	locks["package.json"] = append(locks["package.json"], lockUp("devDependencies", "a", "^1.0.0", "1.3.0"))
	if _, err := restoreRanges([]byte(saved), locks); err == nil || err.Error() != `package-lock.json has no devDependencies entry for a in package "" to restore its range` {
		t.Errorf("missing entry: err = %v", err)
	}
	if _, err := restoreRanges([]byte(`{"packages": [`), locks); err == nil {
		t.Error("broken lock: no error")
	}
}

func TestProcessLockedCommands(t *testing.T) {
	log := stubs(t, "npm")
	dir := t.TempDir()
	pkg := `{"name": "ws", "workspaces": ["packages/*"], "dependencies": {"a": "^1.0.0"}}`
	member := `{"name": "m", "dependencies": {"a": "^1.0.0", "b": "^1.0.0"}}`
	write(t, dir, map[string]string{"package.json": pkg, "packages/m/package.json": member})
	locks := map[string][]dep.Update{
		"package.json":            {lockUp("dependencies", "a", "^1.0.0", "1.3.0")},
		"packages/m/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.6.0")},
	}
	// Unchanged manifests: only the lockfile-only installs run. The stub appends "modified"
	// after the JSON document, which the range restore leaves alone.
	out, err := Locker{}.ProcessLocked(t.Context(), dir, map[string][]byte{"package.json": []byte(pkg)}, map[string][]byte{PackageLock: []byte(wsLock)}, locks)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[PackageLock]); got != wsLock+"modified\nmodified\n" {
		t.Errorf("lock = %q", got)
	}
	var argv []string
	for l := range strings.Lines(readLog(t, log)) {
		if strings.HasPrefix(l, "argv:") {
			argv = append(argv, strings.TrimSpace(l))
		}
	}
	base := "argv: npm install --package-lock-only --ignore-scripts --no-audit --no-fund"
	if want := []string{base + " a@1.3.0", base + " --workspace=packages/m b@1.6.0"}; strings.Join(argv, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands =\n%s\nwant\n%s", strings.Join(argv, "\n"), strings.Join(want, "\n"))
	}

	// A patched manifest: the plain install runs first.
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(member, `"a": "^1.0.0"`, `"a": "^1.3.0"`, 1)
	if _, err := (Locker{}).ProcessLocked(t.Context(), dir, map[string][]byte{"packages/m/package.json": []byte(patched)}, map[string][]byte{PackageLock: []byte(wsLock)},
		map[string][]dep.Update{"package.json": locks["package.json"]}); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, log); strings.Count(got, "argv:") != 2 || !strings.Contains(got, "argv: npm install --package-lock-only --ignore-scripts --no-audit --no-fund\n") {
		t.Errorf("log = %s", got)
	}

	// Other lockfiles cannot take lockfile-only updates.
	if _, err := (Locker{}).ProcessLocked(t.Context(), dir, nil, map[string][]byte{YarnLock: []byte("")}, locks); err == nil ||
		err.Error() != "lockfile-only updates need package-lock.json, the lock root uses yarn.lock" {
		t.Errorf("yarn: err = %v", err)
	}
}

// TestProcessLockedRealNpm moves a locked version within the range with the real npm: the lock
// gets the new version and keeps the manifest's range.
func TestProcessLockedRealNpm(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH")
	}
	isolate(t)
	srv := registry(t)
	rc := []byte("registry=" + srv.URL + "/\n")
	for _, tt := range []struct {
		name, rootPkg, manifest string
		disk                    map[string]string
	}{
		{name: "root", rootPkg: string(manifest("^1.0.0")), manifest: "package.json"},
		// npm names an unnamed root after its directory: the lock keeps the lock root's name.
		{name: "unnamed root", rootPkg: `{"dependencies":{"tiny":"^1.0.0"}}` + "\n", manifest: "package.json"},
		{
			name: "workspace member", rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`, manifest: "packages/a/package.json",
			disk: map[string]string{"packages/a/package.json": `{"name":"a","version":"1.0.0","dependencies":{"tiny":"^1.0.0"}}`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			disk := map[string]string{"package.json": tt.rootPkg}
			maps.Copy(disk, tt.disk)
			write(t, dir, disk)
			// Lock tiny at 1.0.0 with npm itself, then move it to 1.1.0 lockfile-only.
			pin := strings.ReplaceAll(disk[tt.manifest], `"^1.0.0"`, `"1.0.0"`)
			first := `{"name":"x","lockfileVersion":3,"requires":true,"packages":{}}`
			out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{tt.manifest: []byte(pin)}, map[string][]byte{PackageLock: []byte(first), npmrc: rc})
			if err != nil {
				t.Fatal(err)
			}
			lock, err := restoreRanges(out[PackageLock], map[string][]dep.Update{tt.manifest: {lockUp("dependencies", "tiny", "^1.0.0", "1.0.0")}})
			if err != nil || !strings.Contains(string(lock), "tiny-1.0.0.tgz") {
				t.Fatalf("initial lock = %s, %v", lock, err)
			}
			locks := map[string][]dep.Update{tt.manifest: {lockUp("dependencies", "tiny", "^1.0.0", "1.1.0")}}
			out, err = Locker{}.ProcessLocked(t.Context(), dir, nil, map[string][]byte{PackageLock: lock, npmrc: rc}, locks)
			if err != nil {
				t.Fatal(err)
			}
			got := string(out[PackageLock])
			if !strings.Contains(got, "tiny-1.1.0.tgz") || strings.Contains(got, "tiny-1.0.0.tgz") || !strings.Contains(got, `"tiny": "^1.0.0"`) || strings.Contains(got, `"tiny": "^1.1.0"`) {
				t.Errorf("lock = %s", got)
			}
			if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tt.manifest))); err != nil || string(b) != disk[tt.manifest] {
				t.Errorf("manifest on disk changed: %q, %v", b, err)
			}
			if !strings.Contains(tt.rootPkg, `"name"`) && !strings.Contains(got, `"name": "`+filepath.Base(dir)+`"`) {
				t.Errorf("lock name is not the directory's (%s):\n%s", filepath.Base(dir), got)
			}
		})
	}
}
