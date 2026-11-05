package nodetool

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		// lockfile v1: the top-level dependencies
		"old/package.json":      `{}`,
		"old/package-lock.json": `{"lockfileVersion": 1, "dependencies": {"a": {"version": "1.0.0"}, "b": {"version": "2.0.0"}}}`,
		// no lockfileVersion: not read
		"nov/package.json":      `{}`,
		"nov/package-lock.json": `{"dependencies": {"a": {"version": "1.0.0"}}}`,
		// pnpm without importers; yarn named by packageManager wins over package-lock.json
		"pn/package.json":         `{}`,
		"pn/pnpm-lock.yaml":       "lockfileVersion: '9.0'\n",
		"y/package.json":          `{"packageManager": "yarn@4.1.0"}`,
		"y/yarn.lock":             "",
		"y/package-lock.json":     wsLock,
		"bad/package.json":        `{}`,
		"bad/package-lock.json":   `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": 1}}}`,
		"trunc/package.json":      `{}`,
		"trunc/package-lock.json": `{"lockfileVersion": 3, "packages": {`,
		"badpn/package.json":      `{}`,
		"badpn/pnpm-lock.yaml":    "importers: [\n",
		"badbun/package.json":     `{}`,
		"badbun/bun.lock":         `{"packages": {"a": "a@1.0.0"}}`,
		"none/package.json":       `{}`,
	})
	wants := []Want{
		{"package.json", "a", "^1.0.0"},            // hoisted
		{"package.json", "@s/c", "^3.0.0"},         // scoped
		{"package.json", "missing", "^1.0.0"},      // not locked
		{"packages/m/package.json", "b", "^1.0.0"}, // the member's own copy wins
		{"packages/m/package.json", "a", "^1.0.0"}, // hoisted
		{"packages/m/package.json", "m", "^1.0.0"}, // a link has no version
		{"packages/n/package.json", "b", "^2.0.0"},
		{"old/package.json", "a", "^1.0.0"},
		{"nov/package.json", "a", "^1.0.0"},
		{"pn/package.json", "a", "^1.0.0"},
		{"y/package.json", "a", "^1.0.0"},
		{"bad/package.json", "a", "^1.0.0"},
		{"trunc/package.json", "a", "^1.0.0"},
		{"badpn/package.json", "a", "^1.0.0"},
		{"badbun/package.json", "a", "^1.0.0"},
		{"none/package.json", "a", "^1.0.0"},
	}
	got, errs := ReadLocked(root, wants)
	want := []LockState{
		{Version: "1.2.0"}, {Version: "3.1.0"}, {}, {Version: "1.5.0"}, {Version: "1.2.0"}, {}, {Version: "2.0.0"},
		{Version: "1.0.0"}, {Unread: true}, {}, {}, {Unread: true}, {Unread: true}, {Unread: true}, {Unread: true}, {},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%v = %+v, want %+v", wants[i], got[i], want[i])
		}
	}
	var msgs []string
	for _, err := range errs {
		msgs = append(msgs, err.Error())
	}
	if len(msgs) != 4 || !strings.HasPrefix(msgs[0], "bad/package-lock.json: packages[\"node_modules/a\"]: ") ||
		!strings.HasPrefix(msgs[1], "trunc/package-lock.json: ") || !strings.HasPrefix(msgs[2], "badpn/pnpm-lock.yaml: ") ||
		!strings.HasPrefix(msgs[3], `badbun/bun.lock: packages["a"]: `) {
		t.Errorf("errs = %q", msgs)
	}
}

// TestReadLockedFixtures reads the lockfiles npm 12, pnpm 7, 8 and 12 (with packageManager: two
// YAML documents), yarn 1.22 and 4 and bun 1.4 wrote for the same workspace (testdata/locks;
// npm-shrinkwrap is the npm lock under that name, npm-v1 a lock as npm 6 wrote it): ms is hoisted
// for the root's ^2.1.0 and the member a has its own 2.1.1; react-dom carries a peer suffix in
// pnpm. pnpm-v5 and pnpm-v6 are single projects without importers.
func TestReadLockedFixtures(t *testing.T) {
	type lookup struct {
		want Want
		v    string
	}
	const a = "packages/a/package.json"
	workspace := []lookup{
		{Want{"package.json", "ms", "^2.1.0"}, "2.1.3"},
		{Want{"package.json", "@types/ms", "^0.7.31"}, "0.7.34"},
		{Want{a, "ms", "2.1.1"}, "2.1.1"},
		{Want{a, "debug", "^4.3.1"}, "4.4.3"},
		{Want{a, "react", "^18.2.0"}, "18.3.1"},
		{Want{a, "react-dom", "^18.2.0"}, "18.3.1"},
		{Want{a, "left-pad", "^1.0.0"}, ""},
	}
	single := []lookup{
		{Want{"package.json", "ms", "^2.1.0"}, "2.1.3"},
		{Want{"package.json", "react-dom", "^18.2.0"}, "18.3.1"},
		{Want{"package.json", "@types/ms", "^0.7.31"}, "0.7.34"},
		{Want{"package.json", "left-pad", "^1.0.0"}, ""},
	}
	// yarn looks up the descriptor, the others the member's or the hoisted entry by name.
	byName := append(slices.Clone(workspace), lookup{Want{a, "ms", "^2.0.0"}, "2.1.1"}, lookup{Want{a, "ms", ""}, "2.1.1"})
	byDescriptor := append(slices.Clone(workspace), lookup{Want{a, "ms", "^2.0.0"}, ""}, lookup{Want{a, "ms", ""}, ""})
	for dir, cases := range map[string][]lookup{
		"npm": byName, "npm-shrinkwrap": byName, "pnpm": byName, "bun": byName,
		"yarn-classic": byDescriptor, "yarn-berry": byDescriptor,
		"pnpm-v5": single, "pnpm-v6": single,
		"npm-v1": {
			{Want{"package.json", "ms", "~2.1.1"}, "2.1.1"},
			{Want{"package.json", "debug", "^4.3.1"}, "4.4.3"},
			{Want{"package.json", "@types/ms", "^0.7.31"}, "0.7.34"},
			{Want{"package.json", "left-pad", "^1.0.0"}, ""},
		},
	} {
		t.Run(dir, func(t *testing.T) {
			wants := make([]Want, len(cases))
			for i, c := range cases {
				wants[i] = c.want
			}
			got, errs := ReadLocked(filepath.Join("testdata", "locks", dir), wants)
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			for i, c := range cases {
				if got[i] != (LockState{Version: c.v}) {
					t.Errorf("%v = %+v, want %q", c.want, got[i], c.v)
				}
			}
		})
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
		wants = append(wants, Want{m, "a", "^1.0.0"}, Want{m, "b", "^2.0.0"}, Want{m, "c", "^3.0.0"})
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
	locks := map[string][]dep.Update{
		"packages/z/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.1.0")},
		"package.json":            {lockUp("dependencies", "z", "^1.0.0", "1.2.0"), lockUp("devDependencies", "@s/a", "~2.0.0", "2.0.3")},
		"packages/m/package.json": {lockUp("dependencies", "b", "^1.0.0", "1.1.0")},
	}
	for _, tt := range []struct {
		lock  string
		berry bool
		want  []string
	}{
		{lock: PackageLock, want: []string{
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund @s/a@2.0.3 z@1.2.0",
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund --workspace=packages/m b@1.1.0",
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund --workspace=packages/z b@1.1.0",
		}},
		{lock: NpmShrinkwrap, want: []string{
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund @s/a@2.0.3 z@1.2.0",
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund --workspace=packages/m b@1.1.0",
			"npm install --package-lock-only --ignore-scripts --no-audit --no-fund --workspace=packages/z b@1.1.0",
		}},
		{lock: PnpmLock, want: []string{
			"pnpm update --no-save --lockfile-only --ignore-scripts --filter . @s/a@2.0.3 z@1.2.0",
			"pnpm update --no-save --lockfile-only --ignore-scripts --filter ./packages/m b@1.1.0",
			"pnpm update --no-save --lockfile-only --ignore-scripts --filter ./packages/z b@1.1.0",
		}},
		// One run per descriptor: the members' b@npm:^1.0.0 is the same.
		{lock: YarnLock, berry: true, want: []string{
			"yarn set resolution @s/a@npm:~2.0.0 npm:2.0.3",
			"yarn set resolution b@npm:^1.0.0 npm:1.1.0",
			"yarn set resolution z@npm:^1.0.0 npm:1.2.0",
		}},
		{lock: YarnLock},
		{lock: BunLock},
	} {
		c := command(tt.lock, tt.berry)
		var got []string
		for _, cc := range lockCommands(c, tt.lock, tt.berry, locks) {
			if cc.tool != c.tool || cc.npxPackage != c.npxPackage || !slices.Equal(cc.env, c.env) {
				t.Errorf("%s: command %+v does not keep %+v's tool and env", tt.lock, cc, c)
			}
			got = append(got, strings.Join(append([]string{cc.tool}, cc.args...), " "))
		}
		if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
			t.Errorf("%s (berry %v): commands =\n%s\nwant\n%s", tt.lock, tt.berry, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
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
	got, err := restoreRanges([]byte(saved), locks, PackageLock)
	if err != nil || string(got) != wsLock {
		t.Fatalf("restoreRanges = %s, %v", got, err)
	}

	// bun.lock: workspaces, with trailing commas.
	const bunLock = "{\n  \"workspaces\": {\n    \"\": {\n      \"name\": \"ws\",\n      \"dependencies\": {\n        \"a\": \"%s\",\n      },\n    },\n" +
		"    \"packages/m\": {\n      \"name\": \"m\",\n      \"dependencies\": {\n        \"b\": \"%s\",\n      },\n    },\n  },\n  \"packages\": {\n    \"a\": [\"a@1.3.0\"],\n  },\n}\n"
	got, err = restoreRanges(fmt.Appendf(nil, bunLock, "1.3.0", "1.6.0"), locks, BunLock)
	if want := fmt.Sprintf(bunLock, "^1.0.0", "^1.0.0"); err != nil || string(got) != want {
		t.Errorf("bun: restoreRanges = %s, %v; want %s", got, err, want)
	}

	locks["package.json"] = append(locks["package.json"], lockUp("devDependencies", "a", "^1.0.0", "1.3.0"))
	if _, err := restoreRanges([]byte(saved), locks, NpmShrinkwrap); err == nil || err.Error() != `npm-shrinkwrap.json has no devDependencies entry for a in package "" to restore its range` {
		t.Errorf("missing entry: err = %v", err)
	}
	if _, err := restoreRanges([]byte(`{"packages": [`), locks, PackageLock); err == nil {
		t.Error("broken lock: no error")
	}
}

func TestPinManifests(t *testing.T) {
	manifests := map[string][]byte{
		"package.json":            []byte(`{"dependencies": {"a": "^1.0.0", "b": "^2.0.0"}, "devDependencies": {"a": "^1.0.0"}}`),
		"packages/m/package.json": []byte(`{"name": "m", "devDependencies": {"@s/c": "~3.0.0"}}`),
	}
	got, err := pinManifests(manifests, map[string][]dep.Update{
		"package.json":            {lockUp("dependencies", "a", "^1.0.0", "1.3.0")},
		"packages/m/package.json": {lockUp("devDependencies", "@s/c", "~3.0.0", "3.0.4")},
	})
	if err != nil || len(got) != 2 ||
		string(got["package.json"]) != `{"dependencies": {"a": "1.3.0", "b": "^2.0.0"}, "devDependencies": {"a": "^1.0.0"}}` ||
		string(got["packages/m/package.json"]) != `{"name": "m", "devDependencies": {"@s/c": "3.0.4"}}` {
		t.Errorf("pinManifests = %q, %v", got, err)
	}
	if _, err := pinManifests(manifests, map[string][]dep.Update{"package.json": {lockUp("optionalDependencies", "a", "^1.0.0", "1.3.0")}}); err == nil ||
		err.Error() != "package.json has no optionalDependencies entry for a" {
		t.Errorf("missing entry: err = %v", err)
	}
	if _, err := pinManifests(map[string][]byte{"package.json": []byte(`{"dependencies": [`)}, map[string][]dep.Update{"package.json": {lockUp("dependencies", "a", "^1.0.0", "1.3.0")}}); err == nil ||
		!strings.HasPrefix(err.Error(), "package.json: ") {
		t.Errorf("broken manifest: err = %v", err)
	}
}

// argvs returns the argv lines of the stub log.
func argvs(t *testing.T, log string) []string {
	t.Helper()
	var argv []string
	for l := range strings.Lines(readLog(t, log)) {
		if strings.HasPrefix(l, "argv:") {
			argv = append(argv, strings.TrimSpace(l))
		}
	}
	return argv
}

func TestProcessLockedCommands(t *testing.T) {
	log := stubs(t, "npm", "pnpm", "yarn", "bun")
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
	base := "argv: npm install --package-lock-only --ignore-scripts --no-audit --no-fund"
	if got, want := argvs(t, log), []string{base + " a@1.3.0", base + " --workspace=packages/m b@1.6.0"}; !slices.Equal(got, want) {
		t.Errorf("commands =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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

	// npm-shrinkwrap.json is installed as package-lock.json and returned under its own name.
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = Locker{}.ProcessLocked(t.Context(), dir, nil, map[string][]byte{NpmShrinkwrap: []byte(wsLock)}, locks)
	if err != nil || len(out) != 1 || string(out[NpmShrinkwrap]) != wsLock+"modified\nmodified\n" {
		t.Errorf("shrinkwrap: out = %q, %v", out, err)
	}
	if got := readLog(t, log); !strings.Contains(got, "file: ./package-lock.json\n") || strings.Contains(got, NpmShrinkwrap) {
		t.Errorf("shrinkwrap log = %s", got)
	}

	// pnpm and yarn berry: their lockfile-only commands; a failure names the subcommand.
	for _, tt := range []struct {
		files map[string][]byte
		want  []string
	}{
		{
			files: map[string][]byte{PnpmLock: []byte("lockfileVersion: '9.0'\n")},
			want: []string{
				"argv: pnpm update --no-save --lockfile-only --ignore-scripts --filter . a@1.3.0",
				"argv: pnpm update --no-save --lockfile-only --ignore-scripts --filter ./packages/m b@1.6.0",
			},
		},
		{
			files: map[string][]byte{YarnLock: []byte("__metadata:\n  version: 8\n")},
			want:  []string{"argv: yarn set resolution a@npm:^1.0.0 npm:1.3.0", "argv: yarn set resolution b@npm:^1.0.0 npm:1.6.0"},
		},
	} {
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (Locker{}).ProcessLocked(t.Context(), dir, nil, tt.files, locks); err != nil {
			t.Fatal(err)
		}
		if got := argvs(t, log); !slices.Equal(got, tt.want) {
			t.Errorf("commands =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
		t.Setenv("STUB_FAIL", "1")
		tool, sub, _ := strings.Cut(strings.TrimPrefix(tt.want[0], "argv: "), " ")
		sub, _, _ = strings.Cut(sub, " ")
		if _, err := (Locker{}).ProcessLocked(t.Context(), dir, nil, tt.files, locks); err == nil || !strings.HasPrefix(err.Error(), tool+" "+sub+" in "+dir+": ") {
			t.Errorf("failure: err = %v", err)
		}
		t.Setenv("STUB_FAIL", "")
	}
}

// TestProcessLockedPinned runs the lockfile-only updates of yarn classic and bun with stubs: they
// install from manifests pinned to the new versions; yarn classic moves the descriptor and
// installs again, bun gets the ranges back into bun.lock.
func TestProcessLockedPinned(t *testing.T) {
	log := stubs(t, "yarn", "bun")
	t.Setenv("STUB_KEEP", "1")
	dir := t.TempDir()
	pkg := `{"name": "ws", "workspaces": ["packages/*"], "dependencies": {"a": "^1.0.0"}}`
	write(t, dir, map[string]string{"package.json": pkg, "packages/m/package.json": `{"name": "m"}`})
	locks := map[string][]dep.Update{"package.json": {lockUp("dependencies", "a", "^1.0.0", "1.3.0")}}

	// The stub leaves yarn.lock as if the pinned install had added a@1.3.0.
	const head = "# THIS IS AN AUTOGENERATED FILE. DO NOT EDIT THIS FILE DIRECTLY.\n# yarn lockfile v1\n\n\n"
	before := head + "a@1.3.0:\n  version \"1.3.0\"\n\na@^1.0.0:\n  version \"1.0.0\"\n"
	out, err := Locker{}.ProcessLocked(t.Context(), dir, nil, map[string][]byte{YarnLock: []byte(before)}, locks)
	if want := head + "a@1.3.0, a@^1.0.0:\n  version \"1.3.0\"\n"; err != nil || string(out[YarnLock]) != want {
		t.Errorf("yarn classic: out = %q, %v; want %q", out, err, want)
	}
	yarnArgv := "argv: yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress"
	if got := argvs(t, log); !slices.Equal(got, []string{yarnArgv, yarnArgv}) {
		t.Errorf("yarn classic commands = %q", got)
	}
	if _, err := (Locker{}).ProcessLocked(t.Context(), dir, nil, map[string][]byte{YarnLock: []byte(head)}, locks); err == nil ||
		err.Error() != "yarn.lock has no entry for a@1.3.0 after installing it" {
		t.Errorf("yarn classic without the pinned entry: err = %v", err)
	}

	// The stub leaves bun.lock with the pinned range, which goes back to the manifest's.
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bunLock := "{\n  \"workspaces\": {\n    \"\": {\n      \"dependencies\": {\n        \"a\": \"1.3.0\",\n      },\n    },\n  },\n}\n"
	out, err = Locker{}.ProcessLocked(t.Context(), dir, nil, map[string][]byte{BunLock: []byte(bunLock)}, locks)
	if want := strings.Replace(bunLock, `"1.3.0"`, `"^1.0.0"`, 1); err != nil || string(out[BunLock]) != want {
		t.Errorf("bun: out = %q, %v; want %q", out, err, want)
	}
	if got := argvs(t, log); !slices.Equal(got, []string{"argv: bun install --lockfile-only --ignore-scripts"}) {
		t.Errorf("bun commands = %q", got)
	}
	// A manifest without the dependency cannot be pinned.
	if _, err := (Locker{}).ProcessLocked(t.Context(), dir, nil, map[string][]byte{BunLock: []byte(bunLock)},
		map[string][]dep.Update{"package.json": {lockUp("devDependencies", "a", "^1.0.0", "1.3.0")}}); err == nil ||
		err.Error() != "package.json has no devDependencies entry for a" {
		t.Errorf("bun, missing entry: err = %v", err)
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
			lock, err := restoreRanges(out[PackageLock], map[string][]dep.Update{tt.manifest: {lockUp("dependencies", "tiny", "^1.0.0", "1.0.0")}}, PackageLock)
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

// TestProcessLockedRealPnpmYarnBun moves tiny, locked at 1.0.0 under a workspace member's
// ^1.0.0, to 1.1.0 lockfile-only with the real pnpm, yarn (classic and berry) and bun against a
// local registry whose latest is 1.2.0: the lock gets exactly 1.1.0 and keeps the range,
// ReadLocked reads the versions before and after, and the manifests on disk stay unchanged.
func TestProcessLockedRealPnpmYarnBun(t *testing.T) {
	const member = `{"name":"a","version":"1.0.0","dependencies":{"tiny":"^1.0.0"}}` + "\n"
	for _, tt := range []struct {
		name, bin, pkg, lock string
		files                func(url string) map[string][]byte // siblings besides the lockfile
		rootPkg              string
		pins                 [][2]string // the lock of `"tiny": "1.0.0"` -> that of ^1.0.0
		want                 string      // tiny locked at 1.1.0
	}{
		{
			name: "pnpm", bin: "pnpm", pkg: "pnpm@10", lock: PnpmLock,
			files: func(url string) map[string][]byte {
				return map[string][]byte{npmrc: []byte("registry=" + url + "/\n"), pnpmWorkspace: []byte("packages:\n  - packages/*\n")}
			},
			rootPkg: `{"name":"ws","private":true}`,
			pins:    [][2]string{{"specifier: 1.0.0", "specifier: ^1.0.0"}},
			want:    "tiny@1.1.0",
		},
		{
			name: "yarn classic", bin: "yarn", pkg: "yarn@1", lock: YarnLock,
			files:   func(url string) map[string][]byte { return map[string][]byte{npmrc: []byte("registry=" + url + "/\n")} },
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			pins:    [][2]string{{"tiny@1.0.0:", "tiny@^1.0.0:"}},
			want:    `version "1.1.0"`,
		},
		{
			name: "yarn berry", bin: "yarn", pkg: "@yarnpkg/cli-dist@4", lock: YarnLock,
			files: func(url string) map[string][]byte {
				return map[string][]byte{yarnrc: []byte("npmRegistryServer: \"" + url + "\"\nunsafeHttpWhitelist: [\"127.0.0.1\"]\nenableTelemetry: false\nnpmMinimalAgeGate: 0\n")}
			},
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			pins:    [][2]string{{`"tiny@npm:1.0.0":`, `"tiny@npm:^1.0.0":`}, {`tiny: "npm:1.0.0"`, `tiny: "npm:^1.0.0"`}},
			want:    `resolution: "tiny@npm:1.1.0"`,
		},
		{
			name: "bun", bin: "bun", lock: BunLock,
			files: func(url string) map[string][]byte {
				return map[string][]byte{bunfig: []byte("[install]\nregistry = \"" + url + "/\"\n")}
			},
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			pins:    [][2]string{{`"tiny": "1.0.0"`, `"tiny": "^1.0.0"`}},
			want:    `"tiny@1.1.0"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			if tt.pkg == "" {
				if _, err := exec.LookPath(tt.bin); err != nil {
					t.Skip(tt.bin + " not in PATH")
				}
			} else {
				realTool(t, tt.bin, tt.pkg)
			}
			srv := registry(t, "1.0.0", "1.1.0", "1.2.0")
			dir := t.TempDir()
			disk := map[string]string{"package.json": tt.rootPkg, "packages/a/package.json": member}
			write(t, dir, disk)
			files := tt.files(srv.URL)
			files[tt.lock] = []byte{}
			if tt.lock == PnpmLock {
				files[tt.lock] = []byte("lockfileVersion: '9.0'\n")
			}
			// Lock tiny at 1.0.0 with the tool itself, then relax the lock to the range.
			out, err := Locker{}.Process(t.Context(), dir, pkgs("packages/a/package.json", []byte(strings.Replace(member, "^1.0.0", "1.0.0", 1))), files)
			if err != nil {
				t.Fatal(err)
			}
			lock := string(out[tt.lock])
			for _, p := range tt.pins {
				if !strings.Contains(lock, p[0]) {
					t.Fatalf("initial lock has no %s:\n%s", p[0], lock)
				}
				lock = strings.ReplaceAll(lock, p[0], p[1])
			}
			read := func(lock string) LockState {
				t.Helper()
				for n, b := range files {
					if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, tt.lock), []byte(lock), 0o644); err != nil {
					t.Fatal(err)
				}
				got, errs := ReadLocked(dir, []Want{{"packages/a/package.json", "tiny", "^1.0.0"}})
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				return got[0]
			}
			if got := read(lock); got != (LockState{Version: "1.0.0"}) {
				t.Fatalf("ReadLocked before = %+v\n%s", got, lock)
			}

			files[tt.lock] = []byte(lock)
			locks := map[string][]dep.Update{"packages/a/package.json": {lockUp("dependencies", "tiny", "^1.0.0", "1.1.0")}}
			out, err = Locker{}.ProcessLocked(t.Context(), dir, nil, files, locks)
			if err != nil {
				t.Fatal(err)
			}
			got := string(out[tt.lock])
			if !strings.Contains(got, tt.want) || strings.Contains(got, "1.2.0") || strings.Contains(got, "1.0.0.tgz") || !strings.Contains(got, tt.pins[0][1]) {
				t.Errorf("lock = %s", got)
			}
			if st := read(got); st != (LockState{Version: "1.1.0"}) {
				t.Errorf("ReadLocked after = %+v", st)
			}
			for p, c := range disk {
				if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); string(b) != c {
					t.Errorf("%s changed: %s", p, b)
				}
			}
		})
	}
}
