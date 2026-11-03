package npm

import (
	"io"
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
    "alias": "npm:other@^1.0.0",
    "file": "file:../lib",
    "link": "link:../lib",
    "ws": "workspace:*",
    "rel": "./vendor/x",
    "gh": "github:owner/repo",
    "gitplus": "git+https://example.com/x.git",
    "git": "git://example.com/x.git",
    "short": "owner/repo#v1",
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
  "overrides": {"caret": "1.0.0"},
  "resolutions": {"caret": "1.0.0"},
  "engines": {"node": ">=18"}, "workspaces": {"packages": ["packages/*"], "dependencies": {"no": "1.0.0"}},
  "packageManager": "npm@12.2.0"
}
`

type want struct {
	name, depType, version, skip string
	line                         int
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
	ws := []want{
		{"caret", "dependencies", "1.2.3", "", 0},
		{"tilde", "dependencies", "2.0.0", "", 0},
		{"gte", "dependencies", "3.1.4", "", 0},
		{"eq", "dependencies", "4.0.0", "", 0},
		{"exact", "dependencies", "5.6.7", "", 0},
		{"pre", "dependencies", "1.0.0-beta.2", "", 0},
		{"@scope/pkg", "dependencies", "0.4.6", "", 0},
		{"alias", "dependencies", "npm:other@^1.0.0", "npm alias", 0},
		{"file", "dependencies", "file:../lib", "local dependency", 0},
		{"link", "dependencies", "link:../lib", "local dependency", 0},
		{"ws", "dependencies", "workspace:*", "local dependency", 0},
		{"rel", "dependencies", "./vendor/x", "local dependency", 0},
		{"gh", "dependencies", "github:owner/repo", "git dependency", 0},
		{"gitplus", "dependencies", "git+https://example.com/x.git", "git dependency", 0},
		{"git", "dependencies", "git://example.com/x.git", "git dependency", 0},
		{"short", "dependencies", "owner/repo#v1", "git dependency", 0},
		{"tarball", "dependencies", "https://example.com/x.tgz", "url dependency", 0},
		{"latest", "dependencies", "latest", "tag", 0},
		{"next", "dependencies", "next", "tag", 0},
		{"or", "dependencies", "^1.0.0 || ^2.0.0", "unsupported range", 0},
		{"space", "dependencies", ">= 1.0.0", "unsupported range", 0},
		{"x", "dependencies", "1.x", "unsupported range", 0},
		{"xx", "dependencies", "x", "unsupported range", 0},
		{"star", "dependencies", "*", "unsupported range", 0},
		{"partial", "dependencies", "^1.2", "unsupported range", 0},
		{"lt", "dependencies", "<2.0.0", "unsupported range", 0},
		{"hyphen", "dependencies", "1.0.0 - 2.0.0", "unsupported range", 0},
		{"vprefix", "dependencies", "v1.2.3", "unsupported range", 0},
		{"empty", "dependencies", "", "unsupported range", 0},
		{"escaped", "dependencies", "^1.0.0", "cannot locate value", 0},
		{"dev", "devDependencies", "9.0.0", "", 0},
		{"opt", "optionalDependencies", "1.1.1", "", 0},
		{"peer", "peerDependencies", "18.0.0", "", 0},
		{"npm", "packageManager", "12.2.0", "", 0},
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
		if w.skip == "" {
			if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.version {
				t.Errorf("%s: span %q, want %q", w.name, got, w.version)
			}
		} else if d.RefSpan != (dep.Span{}) {
			t.Errorf("%s: skipped dep has span %+v", w.name, d.RefSpan)
		}
	}
}

func TestPackageManager(t *testing.T) {
	for _, tt := range []struct{ value, name, version, skip string }{
		{"pnpm@9.12.1", "pnpm", "9.12.1", ""},
		{"yarn@4.5.0-rc.1", "yarn", "4.5.0-rc.1", ""},
		{"npm@12.2.0+sha512.abc", "npm", "npm@12.2.0+sha512.abc", "packageManager with hash"},
		{"bun@1.0.0", "bun@1.0.0", "bun@1.0.0", "unsupported packageManager"},
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
