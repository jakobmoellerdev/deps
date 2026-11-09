package deno

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return ds
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"deno.json": true, "deno.jsonc": true, "packages/core/deno.json": true,
		"deno.lock": false, "xdeno.json": false, "deno.json5": false, "package.json": false, "import_map.json": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if m.Name() != Name {
		t.Error("name")
	}
}

// TestExtractFixtures reads real configuration files: denoland/dnt@e47d4e3 (deno.jsonc with
// comments), denoland/deployctl@87e43e5 (subpath imports), denoland/saaskit@bc8043e
// (deno.land/x URLs, `npm:/` specifiers, `lock: false`) and denoland/fresh@86d6cde (a workspace
// root with tasks and compilerOptions).
func TestExtractFixtures(t *testing.T) {
	type want struct {
		line                                int
		depType, datasource, name, ver, rng string
		skip                                string
	}
	for _, tt := range []struct {
		file  string
		n     int
		check []want
	}{
		{"dnt/deno.jsonc", 8, []want{
			{72, DepTypeImports, "jsr", "@david/publish-on-tag", "0.2.0", "^0.2.0", ""},
			{74, DepTypeImports, "jsr", "@std/assert", "1.0.0", "1", ""},
			{79, DepTypeImports, "jsr", "@david/code-block-writer", "13.0.3", "^13.0.3", ""},
		}},
		{"deployctl/deno.jsonc", 16, []want{
			{18, DepTypeImports, "jsr", "@std/fmt", "0.217.0", "0.217", ""},
			{32, DepTypeImports, "jsr", "@deno/emit", "0.46.0", "0.46.0", ""},
		}},
		{"saaskit/deno.json", 21, []want{
			{19, DepTypeJSXImportSource, "", "preact", "", "", skipBare},
			{21, DepTypeImports, "", "./", "", "", skipLocal},
			{22, DepTypeImports, "deno", "https://deno.land/x/fresh", "1.7.3", "", ""},
			{24, DepTypeImports, "npm", "preact", "10.22.1", "10.22.1", ""},
			{37, DepTypeImports, "npm", "tailwindcss", "3.4.1", "3.4.1", ""},
			{38, DepTypeImports, "npm", "stripe", "13.5.0", "13.5.0", ""},
			{39, DepTypeImports, "deno", "https://deno.land/x/deno_kv_oauth", "v0.9.1", "", ""},
		}},
		{"fresh/deno.json", 53, []want{
			{58, DepTypeImports, "npm", "@docsearch/js", "3.5.2", "^3.5.2", ""},
			{77, DepTypeImports, "jsr", "@astral/astral", "0.5.6", "^0.5.6", ""},
			{120, DepTypeTypes, "", "vite/client", "", "", skipBare},
		}},
	} {
		src, err := os.ReadFile("testdata/" + tt.file)
		if err != nil {
			t.Fatal(err)
		}
		ds := extract(t, tt.file, src)
		if len(ds) != tt.n {
			t.Errorf("%s: %d deps, want %d", tt.file, len(ds), tt.n)
		}
		for _, w := range tt.check {
			i := slices.IndexFunc(ds, func(d dep.Dep) bool { return d.Line == w.line })
			if i < 0 {
				t.Errorf("%s:%d: no dep", tt.file, w.line)
				continue
			}
			d := ds[i]
			if d.Manager != Name || d.DepType != w.depType || d.Datasource != w.datasource || d.Name != w.name || d.Version != w.ver || d.Range != w.rng || d.Skip != w.skip || d.File != tt.file {
				t.Errorf("%s:%d: got %+v, want %+v", tt.file, w.line, d, w)
			}
			if d.Skip == "" {
				if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != w.rng && got != w.ver {
					t.Errorf("%s:%d: RefSpan reads %q", tt.file, w.line, got)
				}
			}
		}
	}
}

func TestExtractForms(t *testing.T) {
	src := `{
  // JSON with comments and trailing commas
  "imports": {
    "@std/path": "jsr:@std/path@^1.0.0",
    "@std/path/posix": "jsr:@std/path@^1.0.0/posix",
    "chalk": "npm:chalk@5",
    "types": "npm:@types/node@~22.1.0/fs",
    "slash": "npm:/stripe@13.5.0",
    "std/": "https://deno.land/std@0.224.0/",
    "oak": "https://deno.land/x/oak@v12.6.1/mod.ts",
    "nover": "https://deno.land/x/oak/mod.ts",
    "manual": "https://deno.land/manual",
    "badver": "https://deno.land/std@main/",
    "esm": "https://esm.sh/preact@10",
    "node": "node:fs",
    "file": "file:///tmp/x.ts",
    "unversioned": "jsr:@std/fmt",
    "tag": "npm:chalk@latest",
    "badjsr": "jsr:std/fmt@1",
    "badjsr2": "jsr:@std",
    "badnpm": "npm:Chalk@1",
    "esc": "jsr:@std/fs@\u005e1.0.0",
    "number": 1,
  },
  "scopes": {
    "https://deno.land/x/example/": { "@std/assert": "jsr:@std/assert@1.0.0" },
    "bad": [],
  },
  "tasks": {
    "vite": "deno run -A npm:create-vite@^6.0.0 && deno task npm:build",
    "serve": { "description": "x", "command": "deno run \"jsr:@std/http@1.0.0/file-server\" https://deno.land/std@0.224.0/http/file_server.ts" },
    "other": 42,
  },
  "compilerOptions": {
    "types": ["npm:@types/node@^22", "./types.d.ts"],
    "jsxImportSource": "npm:preact@^10.22.0",
    "jsxImportSourceTypes": "npm:@types/react@^18",
    "strict": true,
  },
  "lint": { "plugins": ["jsr:@acme/plugin@^1.2.0"], "rules": {} },
  "lock": false,
  "workspace": ["./a"],
}`
	ds := extract(t, "deno.jsonc", []byte(src))
	type want struct{ depType, datasource, name, ver, rng, skip, at string }
	wants := []want{
		{DepTypeImports, "jsr", "@std/path", "1.0.0", "^1.0.0", "", "^1.0.0"},
		{DepTypeImports, "jsr", "@std/path", "1.0.0", "^1.0.0", "", "^1.0.0"},
		{DepTypeImports, "npm", "chalk", "5.0.0", "5", "", "5"},
		{DepTypeImports, "npm", "@types/node", "22.1.0", "~22.1.0", "", "~22.1.0"},
		{DepTypeImports, "npm", "stripe", "13.5.0", "13.5.0", "", "13.5.0"},
		{DepTypeImports, "deno", "https://deno.land/std", "0.224.0", "", "", "0.224.0"},
		{DepTypeImports, "deno", "https://deno.land/x/oak", "v12.6.1", "", "", "v12.6.1"},
		{DepTypeImports, "deno", "https://deno.land/x/oak", "", "", skipUnversioned, ""},
		{DepTypeImports, "", "https://deno.land/manual", "", "", skipURL, ""},
		{DepTypeImports, "deno", "https://deno.land/std", "", "", skipVersion, ""},
		{DepTypeImports, "", "https://esm.sh/preact@10", "", "", skipURL, ""},
		{DepTypeImports, "", "node:fs", "", "", skipURL, ""},
		{DepTypeImports, "", "file:///tmp/x.ts", "", "", skipLocal, ""},
		{DepTypeImports, "jsr", "@std/fmt", "", "", skipUnversioned, ""},
		{DepTypeImports, "npm", "chalk", "", "", "tag", ""},
		{DepTypeImports, "jsr", "std", "", "", skipName, ""},
		{DepTypeImports, "", `jsr:@std`, "", "", skipName, ""},
		{DepTypeImports, "npm", "Chalk", "", "", skipName, ""},
		{DepTypeImports, "", `jsr:@std/fs@\u005e1.0.0`, "", "", skipEscaped, ""},
		{DepTypeScopes, "jsr", "@std/assert", "1.0.0", "1.0.0", "", "1.0.0"},
		{DepTypeTasks, "npm", "create-vite", "6.0.0", "^6.0.0", "", "^6.0.0"},
		{DepTypeTaskCommand, "jsr", "@std/http", "1.0.0", "1.0.0", "", "1.0.0"},
		{DepTypeTaskCommand, "deno", "https://deno.land/std", "0.224.0", "", "", "0.224.0"},
		{DepTypeTypes, "npm", "@types/node", "22.0.0", "^22", "", "^22"},
		{DepTypeTypes, "", "./types.d.ts", "", "", skipLocal, ""},
		{DepTypeJSXImportSource, "npm", "preact", "10.22.0", "^10.22.0", "", "^10.22.0"},
		{DepTypeJSXImportSourceTypes, "npm", "@types/react", "18.0.0", "^18", "", "^18"},
		{DepTypeLintPlugins, "jsr", "@acme/plugin", "1.2.0", "^1.2.0", "", "^1.2.0"},
	}
	if len(ds) != len(wants) {
		for _, d := range ds {
			t.Logf("%+v", d)
		}
		t.Fatalf("%d deps, want %d", len(ds), len(wants))
	}
	for i, w := range wants {
		d := ds[i]
		got := want{d.DepType, d.Datasource, d.Name, d.Version, d.Range, d.Skip, src[d.RefSpan.Start:d.RefSpan.End]}
		if got != w {
			t.Errorf("dep %d (line %d) = %+v, want %+v", i, d.Line, got, w)
		}
	}
	if ds[0].Line != 4 || ds[len(ds)-1].Line != 40 {
		t.Errorf("lines = %d…%d", ds[0].Line, ds[len(ds)-1].Line)
	}
}

func TestExtractErrors(t *testing.T) {
	for _, src := range []string{`[]`, `{"imports": {"a": "jsr:@std/fs@1"}`, `{} {}`, `{"imports": {"a": }}`, ``} {
		if _, err := New().Extract("deno.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("Extract(%q) succeeded", src)
		}
	}
	if ds := extract(t, "deno.json", []byte(`{"imports": [], "scopes": "x", "tasks": [], "compilerOptions": 1, "lint": null}`)); len(ds) != 0 {
		t.Errorf("deps of wrong shapes = %+v", ds)
	}
}

func TestParseSpecifier(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want Specifier
		ok   bool
	}{
		{"jsr:@std/path@^1.0.0/posix", Specifier{"jsr", "@std/path", "^1.0.0", 14}, true},
		{"npm:/@types/node@22/fs", Specifier{"npm", "@types/node", "22", 17}, true},
		{"npm:chalk", Specifier{"npm", "chalk", "", -1}, true},
		{"npm:chalk/sub", Specifier{"npm", "chalk", "", -1}, true},
		{"jsr:@std/path", Specifier{"jsr", "@std/path", "", -1}, true},
		{"jsr:@std", Specifier{}, false},
		{"npm:", Specifier{}, false},
		{"node:fs", Specifier{}, false},
		{"chalk", Specifier{}, false},
	} {
		got, ok := ParseSpecifier(tt.s)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseSpecifier(%q) = %+v, %v; want %+v, %v", tt.s, got, ok, tt.want, tt.ok)
		}
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`{"imports": {"a": "jsr:@std/path@^1.0.0/posix", "b": "https://deno.land/x/oak@v12.6.1/mod.ts", "c": "npm:chalk@5.3.0"}}`)
	ds := extract(t, "deno.json", src)
	for _, tt := range []struct {
		d             dep.Dep
		v, rangeStrat string
		want          string
	}{
		{ds[0], "1.2.0", "", `"jsr:@std/path@^1.2.0/posix"`},
		{ds[0], "2.0.0", "", `"jsr:@std/path@^2.0.0/posix"`},
		{ds[0], "2.0.0", dep.RangeWiden, `"jsr:@std/path@^2.0.0/posix"`}, // no `||` in a specifier
		{ds[0], "2.0.0", dep.RangePin, `"jsr:@std/path@2.0.0/posix"`},
		{ds[1], "v17.2.0", "", `"https://deno.land/x/oak@v17.2.0/mod.ts"`},
		{ds[2], "5.4.0", "", `"npm:chalk@5.4.0"`},
	} {
		edits, err := New().Edits(dep.Update{Dep: tt.d, NewVersion: tt.v, RangeStrategy: tt.rangeStrat})
		if err != nil {
			t.Errorf("%s %s: %v", tt.d.Name, tt.v, err)
			continue
		}
		out, err := patch.Apply(src, edits)
		if err != nil || !strings.Contains(string(out), tt.want) {
			t.Errorf("%s %s %s: %s, %v; want %s", tt.d.Name, tt.v, tt.rangeStrat, out, err, tt.want)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "1.0.0", NewDigest: "sha256:x"}); err == nil {
		t.Error("digest pin succeeded")
	}
	if _, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "not a version"}); err == nil {
		t.Error("unwritable range succeeded")
	}
	rr := New().(manager.RangeRewriter)
	if got, ok := rr.NewRange(ds[0], "2.0.0", ""); got != "^2.0.0" || !ok {
		t.Errorf("NewRange = %q, %v", got, ok)
	}
	if _, ok := rr.NewRange(ds[1], "v17.2.0", ""); ok {
		t.Error("NewRange of a deno.land URL")
	}
}
