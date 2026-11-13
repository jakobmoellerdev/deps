package npm

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const src = `{
  "name": "pkgname",
  "scripts": {"build": "tsc", "nested": [1, {"dependencies": {"no": "1.0.0"}}]},
  "dependencies": {
    "caret": "^1.2.3",
    "tilde": "~2.0.0",
    "gte": ">=3.1.4",
    "eq": "=4.0.0",
    "exact": "5.6.7",
    "pre": "^1.0.0-beta.2",
    "@scope/pkg": "^0.4.6",
    "file": "file:../lib",
    "link": "link:../lib",
    "ws": "workspace:*",
    "rel": "./vendor/x",
    "tarball": "https://example.com/x.tgz",
    "latest": "latest",
    "next": "next",
    "or": "^1.0.0 || ^2.0.0",
    "space": ">= 1.0.0",
    "x": "1.x",
    "xx": "x",
    "star": "*",
    "partial": "^1.2",
    "lt": "<2.0.0",
    "hyphen": "1.0.0 - 2.0.0",
    "vprefix": "v1.2.3",
    "empty": "",
    "escaped": "\u005e1.0.0",
    "number": 1,
    "object": {"a": ["b"]}
  },
  "devDependencies": {"dev": "^9.0.0"},
  "optionalDependencies": {"opt": "~1.1.1"},
  "peerDependencies": {"peer": ">=18.0.0"},
  "bundleDependencies": ["caret"],
  "engines": {"node": ">=18"}, "workspaces": {"packages": ["packages/*"], "dependencies": {"no": "1.0.0"}},
  "packageManager": "npm@12.2.0"
}
`

// want is an expected dep; rng is the Range of a dependency-section dep, the text RefSpan
// covers ("" = the version).
type want struct {
	name, depType, version, skip string
	line                         int
	rng                          string
}

func lineOf(s, needle string) int {
	before, _, _ := strings.Cut(s, needle)
	return strings.Count(before, "\n") + 1
}

func TestExtract(t *testing.T) {
	b := []byte(src)
	deps, err := New().Extract("web/package.json", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	// engines/volta are covered by TestEnginesVolta.
	deps = slices.DeleteFunc(deps, func(d dep.Dep) bool { return d.DepType == DepTypeEngines || d.DepType == DepTypeVolta })
	ws := []want{
		{"caret", "dependencies", "1.2.3", "", 0, "^1.2.3"},
		{"tilde", "dependencies", "2.0.0", "", 0, "~2.0.0"},
		{"gte", "dependencies", "3.1.4", "", 0, ">=3.1.4"},
		{"eq", "dependencies", "4.0.0", "", 0, "=4.0.0"},
		{"exact", "dependencies", "5.6.7", "", 0, "5.6.7"},
		{"pre", "dependencies", "1.0.0-beta.2", "", 0, "^1.0.0-beta.2"},
		{"@scope/pkg", "dependencies", "0.4.6", "", 0, "^0.4.6"},
		{"file", "dependencies", "file:../lib", "local dependency", 0, ""},
		{"link", "dependencies", "link:../lib", "local dependency", 0, ""},
		{"ws", "dependencies", "workspace:*", "local dependency", 0, ""},
		{"rel", "dependencies", "./vendor/x", "local dependency", 0, ""},
		{"tarball", "dependencies", "https://example.com/x.tgz", "url dependency", 0, ""},
		{"latest", "dependencies", "latest", "tag", 0, ""},
		{"next", "dependencies", "next", "tag", 0, ""},
		// The rest of the range grammar: Version is the anchor.
		{"or", "dependencies", "2.0.0", "", 0, "^1.0.0 || ^2.0.0"},
		{"space", "dependencies", "1.0.0", "", 0, ">= 1.0.0"},
		{"x", "dependencies", "1.0.0", "", 0, "1.x"},
		{"xx", "dependencies", "x", "unsupported range", 0, ""},
		{"star", "dependencies", "*", "unsupported range", 0, ""},
		{"partial", "dependencies", "1.2.0", "", 0, "^1.2"},
		{"lt", "dependencies", "2.0.0", "", 0, "<2.0.0"},
		{"hyphen", "dependencies", "1.0.0", "", 0, "1.0.0 - 2.0.0"},
		{"vprefix", "dependencies", "1.2.3", "", 0, "v1.2.3"},
		{"empty", "dependencies", "", "unsupported range", 0, ""},
		{"escaped", "dependencies", "^1.0.0", "cannot locate value", 0, ""},
		{"dev", "devDependencies", "9.0.0", "", 0, "^9.0.0"},
		{"opt", "optionalDependencies", "1.1.1", "", 0, "~1.1.1"},
		{"peer", "peerDependencies", "18.0.0", "", 0, ">=18.0.0"},
		{"npm", "packageManager", "12.2.0", "", 0, ""},
	}
	if len(deps) != len(ws) {
		for _, d := range deps {
			t.Logf("%+v", d)
		}
		t.Fatalf("got %d deps, want %d", len(deps), len(ws))
	}
	for i, w := range ws {
		d := deps[i]
		needle := `"` + w.name + `"`
		if w.depType == DepTypePackageManager {
			needle = `"packageManager"`
		}
		if d.Manager != Name || d.Datasource != "npm" || d.Versioning != "semver" || d.File != "web/package.json" ||
			d.Name != w.name || d.DepType != w.depType || d.Version != w.version || d.Skip != w.skip || d.Line != lineOf(src, needle) {
			t.Errorf("%d: %+v, want %+v line %d", i, d, w, lineOf(src, needle))
		}
		switch {
		case w.skip != "":
			if d.RefSpan != (dep.Span{}) {
				t.Errorf("%s: skipped dep has span %+v", w.name, d.RefSpan)
			}
		case string(b[d.RefSpan.Start:d.RefSpan.End]) != cmp.Or(w.rng, w.version):
			t.Errorf("%s: span %q, want %q", w.name, b[d.RefSpan.Start:d.RefSpan.End], cmp.Or(w.rng, w.version))
		}
		if d.Range != w.rng {
			t.Errorf("%s: Range %q, want %q", w.name, d.Range, w.rng)
		}
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	for p, c := range map[string]string{
		"web/package.json":      `{"dependencies": {"a": "^1.0.0", "file": "file:x"}, "packageManager": "npm@12.0.0"}`,
		"web/package-lock.json": `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": "1.4.0"}}}`,
		"pn/package.json":       `{"dependencies": {"a": "^1.0.0"}}`,
		"pn/pnpm-lock.yaml":     "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      a:\n        specifier: ^1.0.0\n        version: 1.3.0\n",
		"y/package.json":        `{"dependencies": {"a": "^1.0.0"}}`,
		"y/yarn.lock":           "# yarn lockfile v1\n\n\na@^1.0.0:\n  version \"1.2.0\"\n", // keyed by the range
		"bad/package.json":      `{"dependencies": {"a": "^1.0.0"}}`,
		"bad/package-lock.json": "[",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New().(manager.LockReader)
	var files [][]dep.Dep
	for _, f := range []string{"web/package.json", "pn/package.json", "y/package.json", "bad/package.json"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		deps, err := New().Extract(f, b, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	errs := m.ReadLocks(root, files)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "bad/package-lock.json: ") {
		t.Errorf("errs = %v", errs)
	}
	var got []string
	for _, f := range files {
		for _, d := range f {
			got = append(got, fmt.Sprintf("%s %s locked=%q unread=%v", d.File, d.Name, d.Locked, d.LockUnread))
		}
	}
	want := []string{
		`web/package.json a locked="1.4.0" unread=false`,
		`web/package.json file locked="" unread=false`, // skipped deps are not looked up
		`web/package.json npm locked="" unread=false`,  // nor is packageManager
		`pn/package.json a locked="1.3.0" unread=false`,
		`y/package.json a locked="1.2.0" unread=false`,
		`bad/package.json a locked="" unread=true`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("deps =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if errs := m.ReadLocks(root, [][]dep.Dep{{{Name: "npm", DepType: DepTypePackageManager}}}); errs != nil {
		t.Errorf("no wants: %v", errs)
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	for p, c := range map[string]string{
		"package.json":            `{"private": true, "workspaces": ["packages/*"]}`,
		"package-lock.json":       "{}",
		"packages/a/package.json": `{"name": "a"}`,
		"packages/b/package.json": `{"name": "b"}`,
		"standalone/package.json": `{"name": "s"}`, // no lockfile of its own, not a member
		"pn/package.json":         `{"packageManager": "pnpm@9.1.0"}`,
		"pn/package-lock.json":    "{}",
		"pn/pnpm-lock.yaml":       "", // the packageManager tool's lockfile wins
		"y/package.json":          `{}`,
		"y/yarn.lock":             "",
		"ws/pnpm-workspace.yaml":  "packages: [m]\n", // no root package.json
		"ws/pnpm-lock.yaml":       "",
		"ws/m/package.json":       `{}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"y/package.json", "packages/b/package.json", "standalone/package.json", "pn/package.json", "ws/m/package.json", "packages/a/package.json", "package.json"}
	got := New().(manager.LockMaintainer).LockRoots(root, files)
	if fmt.Sprint(got) != "[{package.json package-lock.json} {pn/package.json pn/pnpm-lock.yaml} {y/package.json y/yarn.lock}]" {
		t.Errorf("lock roots = %v", got)
	}
}

// TestLockRootsMembers pins that the members of one workspace, whose lookups the pass caches,
// still yield exactly their shared lock root.
func TestLockRootsMembers(t *testing.T) {
	root, files := monorepo(t, 5)
	files = append(files, "package.json", "outside/package.json") // the root and a non-member
	got := New().(manager.LockMaintainer).LockRoots(root, files)
	if fmt.Sprint(got) != "[{package.json pnpm-lock.yaml}]" {
		t.Errorf("lock roots = %v", got)
	}
}

func TestPackageManager(t *testing.T) {
	for _, tt := range []struct{ value, name, version, skip string }{
		{"pnpm@9.12.1", "pnpm", "9.12.1", ""},
		{"yarn@4.5.0-rc.1", "yarn", "4.5.0-rc.1", ""},
		{"npm@12.2.0+sha512.abc", "npm", "npm@12.2.0+sha512.abc", "packageManager with hash"},
		{"bun@1.2.3", "bun", "1.2.3", ""},
		{"deno@2.0.0", "deno@2.0.0", "deno@2.0.0", "unsupported packageManager"},
		{"npm", "npm", "npm", "unsupported packageManager"},
		{"npm@12", "npm", "npm@12", "unsupported range"},
	} {
		b := []byte(`{"packageManager": "` + tt.value + `"}`)
		deps, err := New().Extract("package.json", b, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 1 {
			t.Fatalf("%s: %d deps", tt.value, len(deps))
		}
		d := deps[0]
		if d.Name != tt.name || d.Version != tt.version || d.Skip != tt.skip || d.DepType != DepTypePackageManager || d.Line != 1 {
			t.Errorf("%s: %+v", tt.value, d)
		}
		if tt.skip == "" && string(b[d.RefSpan.Start:d.RefSpan.End]) != tt.version {
			t.Errorf("%s: span %q", tt.value, b[d.RefSpan.Start:d.RefSpan.End])
		}
	}
}

// TestExtractBOMAndEdits covers a package.json written with a UTF-8 byte order mark: the
// decoder reads the mark as whitespace, so the deps are those of the same file without it and
// the spans still point into the original bytes.
func TestExtractBOMAndEdits(t *testing.T) {
	m := New()
	plain, err := m.Extract("package.json", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	marked, err := m.Extract("package.json", []byte("\ufeff"+src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != len(plain) || len(plain) == 0 {
		t.Fatalf("%d deps with a byte order mark, %d without", len(marked), len(plain))
	}
	for i, d := range marked {
		if w := plain[i]; d.Name != w.Name || d.Version != w.Version || d.Range != w.Range ||
			d.DepType != w.DepType || d.Line != w.Line || d.Skip != w.Skip {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
		}
	}

	in := "{\n  \"packageManager\": \"npm@12.2.0\",\n  \"dependencies\": {\"a\": \"^1.2.3\"}\n}\n"
	b := []byte("\ufeff" + in)
	deps, err := m.Extract("package.json", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	news := []string{"12.3.0", "2.0.0"}
	var edits []patch.Edit
	for i, d := range deps {
		e, err := m.Edits(dep.Update{Dep: d, NewVersion: news[i]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	wantOut := "\ufeff" + strings.NewReplacer("npm@12.2.0", "npm@12.3.0", "^1.2.3", "^2.0.0").Replace(in)
	if string(out) != wantOut {
		t.Errorf("patched =\n%q\nwant\n%q", out, wantOut)
	}
}

func TestExtractCRLFAndEdits(t *testing.T) {
	in := "{\r\n  \"packageManager\": \"npm@12.2.0\",\r\n  \"dependencies\": {\r\n    \"a\": \"^1.2.3\",\r\n    \"b\":\"~0.1.0\"\r\n  },\r\n  \"dependencies2\": null,\r\n  \"devDependencies\": null,\r\n  \"peerDependencies\": [\"x\"]\r\n}\r\n"
	b := []byte(in)
	m := New()
	deps, err := m.Extract("package.json", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 || deps[0].Line != 2 || deps[1].Line != 4 || deps[2].Line != 5 {
		t.Fatalf("deps = %+v", deps)
	}
	news := []string{"12.3.0", "2.0.0", "0.2.0"}
	var edits []patch.Edit
	for i, d := range deps {
		e, err := m.Edits(dep.Update{Dep: d, NewVersion: news[i]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	wantOut := strings.NewReplacer("npm@12.2.0", "npm@12.3.0", "^1.2.3", "^2.0.0", "~0.1.0", "~0.2.0").Replace(in)
	if string(out) != wantOut {
		t.Errorf("patched =\n%q\nwant\n%q", out, wantOut)
	}
}

func TestExtractErrors(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{`[]`, "top level is not an object"},
		{``, io.ErrUnexpectedEOF.Error()},
		{`{"dependencies": {"a": "1.0.0"`, ""},
		{`{"scripts": {"a": [1, 2`, ""},
		{`{"a": }`, ""},
		{`{"dependencies": {"a": tru}}`, ""},
		{`{"packageManager": }`, ""},
		{`{"dependencies": }`, ""},
	} {
		_, err := New().Extract("package.json", []byte(tt.in), manager.Env{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q: err = %v, want %q", tt.in, err, tt.want)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"package.json": true, "web/package.json": true, "package.json5": false, "mypackage.json": false, "package-lock.json": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
	if m.Name() != "npm" {
		t.Errorf("Name = %q", m.Name())
	}
}
