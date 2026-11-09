package toolversions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/hexpmbob"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// want is an expected dep; ver is also the expected span text of an updatable dep.
type want struct {
	name, ds, versioning, ver, skip string
	line                            int
}

// check compares deps with wants and applies an update to every updatable dep (news maps the
// current version to the new one), returning the patched file.
func check(t *testing.T, m manager.Manager, file, in string, wants []want, news map[string]string) string {
	t.Helper()
	b := []byte(in)
	deps, err := m.Extract(file, b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	var edits []patch.Edit
	for i, w := range wants {
		d := deps[i]
		if d.Manager != m.Name() || d.File != file || d.Name != w.name || d.Datasource != w.ds || d.Versioning != w.versioning ||
			d.Version != w.ver || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
			continue
		}
		if d.Skip != "" {
			if d.RefSpan != (dep.Span{}) {
				t.Errorf("%d: skipped dep has span %+v", i, d.RefSpan)
			}
			continue
		}
		if s := string(b[d.RefSpan.Start:d.RefSpan.End]); s != w.ver {
			t.Errorf("%d: span %q, want %q", i, s, w.ver)
		}
		nv, ok := news[d.Version]
		if !ok {
			continue
		}
		e, err := m.Edits(dep.Update{Dep: d, NewVersion: nv})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		m    manager.Manager
		path string
		ok   bool
	}{
		{NewNvm(), ".nvmrc", true},
		{NewNvm(), "web/.nvmrc", true},
		{NewNvm(), "nvmrc", false},
		{NewNvm(), ".nvmrc.bak", false},
		{NewNodenv(), ".node-version", true},
		{NewNodenv(), "a/b/.node-version", true},
		{NewNodenv(), "node-version", false},
		{NewRubyVersion(), ".ruby-version", true},
		{NewRubyVersion(), "apps/web/.ruby-version", true},
		{NewRubyVersion(), "ruby-version", false},
		{NewPyenv(), ".python-version", true},
		{NewPyenv(), "services/api/.python-version", true},
		{NewPyenv(), "python-version", false},
		{NewPyenv(), ".python-versions", false},
		{NewRuntimeVersion(), "runtime.txt", true},
		{NewRuntimeVersion(), "app/runtime.txt", true},
		{NewRuntimeVersion(), "my-runtime.txt", false},
		{NewRuntimeVersion(), "runtimeatxt", false},
		{NewAsdf(), ".tool-versions", true},
		{NewAsdf(), "svc/.tool-versions", true},
		{NewAsdf(), "tool-versions", false},
		{NewMise(), "mise.toml", true},
		{NewMise(), ".mise.toml", true},
		{NewMise(), "mise/config.toml", true},
		{NewMise(), ".mise/config.toml", true},
		{NewMise(), ".config/mise.toml", true},
		{NewMise(), ".config/mise/config.toml", true},
		{NewMise(), "sub/mise.toml", true},
		{NewMise(), "notmise.toml", false},
		{NewMise(), "mise.local.toml", false},
		{NewMise(), "config.toml", false},
		{NewMise(), ".config/config.toml", false},
	} {
		if got := c.m.Match(c.path); got != c.ok {
			t.Errorf("%s.Match(%q) = %v, want %v", c.m.Name(), c.path, got, c.ok)
		}
	}
}

func TestNames(t *testing.T) {
	for m, name := range map[manager.Manager]string{
		NewNvm(): NameNvm, NewNodenv(): NameNodenv, NewRubyVersion(): NameRubyVersion, NewPyenv(): NamePyenv,
		NewRuntimeVersion(): NameRuntimeVersion, NewAsdf(): NameAsdf, NewMise(): NameMise,
	} {
		if m.Name() != name {
			t.Errorf("Name() = %q, want %q", m.Name(), name)
		}
	}
}

func TestNodeFiles(t *testing.T) {
	for _, c := range []struct {
		name, in string
		w        want
		new, out string // new version for w.ver, patched file
	}{
		{"full", "20.11.1\n", want{"node", dsNode, "semver", "20.11.1", "", 1}, "22.3.0", "22.3.0\n"},
		{"full v", "v20.11.1\n", want{"node", dsNode, "semver", "v20.11.1", "", 1}, "v22.3.0", "v22.3.0\n"},
		{"major", "20", want{"node", dsNode, "tag", "20", "", 1}, "22", "22"},
		{"major v", "v20\n", want{"node", dsNode, "tag", "20", "", 1}, "22", "v22\n"},
		{"minor v crlf", "v20.11\r\n", want{"node", dsNode, "tag", "20.11", "", 1}, "20.12", "v20.12\r\n"},
		{"prerelease", "21.0.0-rc.1\n", want{"node", dsNode, "semver", "21.0.0-rc.1", "", 1}, "21.0.0", "21.0.0\n"},
		{"comments and blanks", "# pinned for CI\n\n  v18.20.4  # maintenance\n19.0.0\n", want{"node", dsNode, "semver", "v18.20.4", "", 3},
			"v18.20.5", "# pinned for CI\n\n  v18.20.5  # maintenance\n19.0.0\n"},
		{"bom", "\ufeff20.1.0\n", want{"node", dsNode, "semver", "20.1.0", "", 1}, "20.2.0", "\ufeff20.2.0\n"},
		{"lts any", "lts/*\n", want{"node", dsNode, "", "lts/*", "non-version value", 1}, "", "lts/*\n"},
		{"lts codename", "lts/iron", want{"node", dsNode, "", "lts/iron", "non-version value", 1}, "", "lts/iron"},
		{"node alias", "node\n", want{"node", dsNode, "", "node", "non-version value", 1}, "", "node\n"},
		{"stable", "stable\n", want{"node", dsNode, "", "stable", "non-version value", 1}, "", "stable\n"},
		{"system", "system\n", want{"node", dsNode, "", "system", "non-version value", 1}, "", "system\n"},
		{"range", ">=20\n", want{"node", dsNode, "tag", "20", "", 1}, "26", ">=26\n"},
		{"caret full", "^20.11.1\n", want{"node", dsNode, "semver", "20.11.1", "", 1}, "26.1.0", "^26.1.0\n"},
		{"tilde minor", "~20.11\n", want{"node", dsNode, "tag", "20.11", "", 1}, "20.12", "~20.12\n"},
		{"x range", "20.x\n", want{"node", dsNode, "tag", "20", "", 1}, "26", "26.x\n"},
		{"x minor range", "v20.11.x\n", want{"node", dsNode, "tag", "20.11", "", 1}, "20.19", "v20.19.x\n"},
		{"star range", "20.*\n", want{"node", dsNode, "tag", "20", "", 1}, "26", "26.*\n"},
		{"compound range", ">=20 <22\n", want{"node", dsNode, "", ">=20 <22", "unsupported range", 1}, "", ">=20 <22\n"},
		{"or range", "20||22\n", want{"node", dsNode, "", "20||22", "unsupported range", 1}, "", "20||22\n"},
	} {
		for _, m := range []manager.Manager{NewNvm(), NewNodenv()} {
			t.Run(m.Name()+"/"+c.name, func(t *testing.T) {
				out := check(t, m, ".nvmrc", c.in, []want{c.w}, map[string]string{c.w.ver: c.new})
				if out != c.out {
					t.Errorf("patched = %q, want %q", out, c.out)
				}
			})
		}
	}
	for _, in := range []string{"", "\n\n", "# only a comment\n", "   \n"} {
		deps, err := NewNvm().Extract(".nvmrc", []byte(in), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("Extract(%q) = %+v, %v; want none", in, deps, err)
		}
	}
}

func TestRubyVersionFile(t *testing.T) {
	for _, c := range []struct {
		name, in string
		w        want
		new, out string // new version for w.ver, patched file
	}{
		{"full", "3.4.7\n", want{"ruby", dsRuby, "ruby", "3.4.7", "", 1}, "4.0.9", "4.0.9\n"},
		{"engine prefix", "ruby-3.4.7\n", want{"ruby", dsRuby, "ruby", "3.4.7", "", 1}, "4.0.9", "ruby-4.0.9\n"},
		{"minor", "# rbenv\n3.4\n", want{"ruby", dsRuby, "tag", "3.4", "", 2}, "4.0", "# rbenv\n4.0\n"},
		{"preview", "3.5.0-preview1", want{"ruby", dsRuby, "ruby", "3.5.0-preview1", "", 1}, "4.0.0", "4.0.0"},
		{"jruby", "jruby-9.4.8.0\n", want{"ruby", dsRuby, "", "jruby-9.4.8.0", "non-version value", 1}, "", "jruby-9.4.8.0\n"},
		{"system", "system\n", want{"ruby", dsRuby, "", "system", "non-version value", 1}, "", "system\n"},
		{"gemset", "3.4.7@app\n", want{"ruby", dsRuby, "", "3.4.7@app", "unsupported range", 1}, "", "3.4.7@app\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := check(t, NewRubyVersion(), ".ruby-version", c.in, []want{c.w}, map[string]string{c.w.ver: c.new})
			if out != c.out {
				t.Errorf("patched = %q, want %q", out, c.out)
			}
		})
	}
	// asdf and mise name the same dep.
	check(t, NewAsdf(), ".tool-versions", "ruby 3.4.7\n", []want{{"ruby", dsRuby, "ruby", "3.4.7", "", 1}}, nil)
	check(t, NewMise(), "mise.toml", "[tools]\nruby = \"3.4\"\n", []want{{"ruby", dsRuby, "tag", "3.4", "", 2}}, nil)
}

func TestRubyFileEdits(t *testing.T) {
	root := t.TempDir()
	lock := "GEM\n  remote: https://rubygems.org/\n  specs:\n\nRUBY VERSION\n   ruby 3.4.7p58\n"
	for name, content := range map[string]string{"Gemfile": "ruby file: \".tool-versions\"\n", "Gemfile.lock": lock} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	deps, _ := NewAsdf().Extract(".tool-versions", []byte("nodejs 22.1.0\nruby 3.4.7\n"), manager.Env{})
	fe := NewAsdf().(manager.FileEditor)
	edits, err := fe.FileEdits(root, dep.Update{Dep: deps[1], NewVersion: "4.0.9"})
	if err != nil || len(edits["Gemfile.lock"]) != 1 || edits["Gemfile.lock"][0].New != "4.0.9" || edits["Gemfile.lock"][0].Old != "3.4.7p58" {
		t.Errorf("ruby edits = %v, %v", edits, err)
	}
	if edits, err := fe.FileEdits(root, dep.Update{Dep: deps[0], NewVersion: "24.0.0"}); edits != nil || err != nil {
		t.Errorf("node edits = %v, %v", edits, err)
	}
}

func TestPythonVersionFile(t *testing.T) {
	for _, c := range []struct {
		name, in string
		w        want
		new, out string // new version for w.ver, patched file
	}{
		{"full", "3.13.1\n", want{"python", dsPython, "pep440", "3.13.1", "", 1}, "3.14.10", "3.14.10\n"},
		{"minor", "3.13\n", want{"python", dsPython, "tag", "3.13", "", 1}, "3.14", "3.14\n"},
		{"major", "3", want{"python", dsPython, "tag", "3", "", 1}, "4", "4"},
		{"several versions", "# pyenv local 3.12 3.11\n3.12\n3.11\n", want{"python", dsPython, "tag", "3.12", "", 2}, "3.14", "# pyenv local 3.12 3.11\n3.14\n3.11\n"},
		{"pre-release", "3.15.0rc1\n", want{"python", dsPython, "pep440", "3.15.0rc1", "", 1}, "3.15.0", "3.15.0\n"},
		{"free-threaded", "3.13t\n", want{"python", dsPython, "tag", "3.13", "", 1}, "3.14", "3.14t\n"},
		{"free-threaded full crlf", "3.13.1t\r\n", want{"python", dsPython, "pep440", "3.13.1", "", 1}, "3.14.10", "3.14.10t\r\n"},
		{"pypy", "pypy3.10-7.3.17\n", want{"python", dsPython, "", "pypy3.10-7.3.17", "non-version value", 1}, "", "pypy3.10-7.3.17\n"},
		{"miniconda", "miniconda3-latest\n", want{"python", dsPython, "", "miniconda3-latest", "non-version value", 1}, "", "miniconda3-latest\n"},
		{"system", "system\n", want{"python", dsPython, "", "system", "non-version value", 1}, "", "system\n"},
		{"dev", "3.14-dev\n", want{"python", dsPython, "", "3.14-dev", "unsupported range", 1}, "", "3.14-dev\n"},
		{"range", ">=3.12\n", want{"python", dsPython, "", ">=3.12", "unsupported range", 1}, "", ">=3.12\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := check(t, NewPyenv(), ".python-version", c.in, []want{c.w}, map[string]string{c.w.ver: c.new})
			if out != c.out {
				t.Errorf("patched = %q, want %q", out, c.out)
			}
		})
	}
}

func TestRuntimeTxt(t *testing.T) {
	for _, c := range []struct {
		name, in string
		w        []want
		new, out string // new version for w[0].ver, patched file
	}{
		{"heroku", "python-3.13.1\n", []want{{"python", dsPython, "pep440", "3.13.1", "", 1}}, "3.14.10", "python-3.14.10\n"},
		{"minor", "\ufeff# Heroku\npython-3.13\r\n", []want{{"python", dsPython, "tag", "3.13", "", 2}}, "3.14", "\ufeff# Heroku\npython-3.14\r\n"},
		{"pypy", "pypy3.10-7.3.17\n", nil, "", "pypy3.10-7.3.17\n"},
		{"binder r", "r-4.4-2024-10-01\n", nil, "", "r-4.4-2024-10-01\n"},
		{"bare version", "3.13.1\n", nil, "", "3.13.1\n"},
		{"empty", "\n\n", nil, "", "\n\n"},
		{"prefix only", "python-\n", []want{{"python", dsPython, "", "", "non-version value", 1}}, "", "python-\n"},
		{"range", "python-3.13.x\n", []want{{"python", dsPython, "", "3.13.x", "unsupported range", 1}}, "", "python-3.13.x\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var news map[string]string
			if len(c.w) > 0 {
				news = map[string]string{c.w[0].ver: c.new}
			}
			if out := check(t, NewRuntimeVersion(), "runtime.txt", c.in, c.w, news); out != c.out {
				t.Errorf("patched = %q, want %q", out, c.out)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	gh := githubTool("helm/helm")
	for _, c := range []struct {
		t                         tool
		raw, ver, versioning, why string
		off                       int
	}{
		{nodeTool, "20.11.1", "20.11.1", "semver", "", 0},
		{pythonTool, "3.13.1", "3.13.1", "pep440", "", 0},
		{pythonTool, "3.13t", "3.13", "tag", "", 0},
		{pythonTool, "v3.13.1", "", "", "unsupported range", 0},
		{pythonTool, "graalpy-24.1.0", "", "", "non-version value", 0},
		{pythonTool, "prefix:3.13", "", "", "unsupported range", 0},
		{pythonTool, "", "", "", "non-version value", 0},
		{nodeTool, "v20", "20", "tag", "", 1},
		{nodeTool, "20.11", "20.11", "tag", "", 0},
		{rubyTool, "3.4.7", "3.4.7", "ruby", "", 0},
		{rubyTool, "ruby-3.4", "3.4", "tag", "", 5},
		{rubyTool, "v3.4.7", "", "", "unsupported range", 0},
		{rubyTool, "~> 3.4", "", "", "unsupported range", 0},
		{rubyTool, "ref:v3_4_7", "", "", "git ref", 0},
		{nodeTool, "lts", "", "", "non-version value", 0},
		{nodeTool, "v", "", "", "non-version value", 0},
		{nodeTool, "", "", "", "non-version value", 0},
		{goTool, "1.27.3", "1.27.3", "tag", "", 0},
		{goTool, "1.27", "1.27", "tag", "", 0},
		{goTool, "v1.27.3", "1.27.3", "tag", "", 1},
		{goTool, "1", "", "", "partial version", 0},
		{goTool, "1.27rc1", "", "", "unsupported range", 0},
		{gh, "3.16.0", "3.16.0", "semver", "", 0},
		{gh, "v3.16.0", "v3.16.0", "semver", "", 0},
		{gh, "3.16", "3.16", "tag", "", 0},
		{gh, "v3", "3", "tag", "", 1},
		{gh, "latest", "", "", "non-version value", 0},
		{gh, "system", "", "", "non-version value", 0},
		{gh, "latest:3.16", "", "", "non-version value", 0},
		{gh, "sub-1:latest", "", "", "non-version value", 0},
		{gh, "ref:main", "", "", "git ref", 0},
		{gh, "path:/opt/helm", "", "", "local path", 0},
		{gh, "prefix:3.16", "", "", "unsupported range", 0},
		{gh, "~3.16.0", "", "", "unsupported range", 0},
		{npmTool("pnpm"), "9.1.0", "9.1.0", "semver", "", 0},
		{npmTool("pnpm"), "9", "9", "tag", "", 0},
		{pypiTool("poetry"), "2.1.3", "2.1.3", "pep440", "", 0},
		{pypiTool("poetry"), "2.1", "2.1", "pep440", "", 0},
		{pypiTool("poetry"), "2.2.0rc1", "2.2.0rc1", "pep440", "", 0},
		{pypiTool("poetry"), "2", "2", "tag", "", 0},
		{pypiTool("poetry"), "~=2.1", "", "", "unsupported range", 0},
		{tool{datasource: dsCrate, name: "ripgrep"}, "14.1.1", "14.1.1", "semver", "", 0},
		{tool{datasource: dsCrate, name: "ripgrep"}, "14", "14", "tag", "", 0},
		{tool{datasource: dsGoMod, name: "golang.org/x/tools/gopls", bare: true}, "v0.20.0", "0.20.0", "semver", "", 1},
		{tool{datasource: dsGoMod, name: "golang.org/x/tools/gopls", bare: true}, "0.20.0", "0.20.0", "semver", "", 0},
		{tool{datasource: dsGitTags, refPrefix: "tag:"}, "tag:v0.3.1", "v0.3.1", "semver", "", 4},
		{tool{datasource: dsGitTags, refPrefix: "tag:"}, "branch:main", "", "", "git ref", 0},
		{tool{datasource: dsGitTags, refPrefix: "tag:"}, "rev:abc123", "", "", "git ref", 0},
		{tool{datasource: dsGitTags, refPrefix: "tag:"}, "0.3.1", "", "", "unsupported range", 0},
		{tools["flutter"], "3.24.5-stable", "3.24.5", "semver", "", 0},
		{tools["flutter"], "3.27.0-0.1.pre-beta", "3.27.0-0.1.pre", "semver", "", 0},
		{tools["dotnet-core"], "8.0.404", "8.0.404", "semver", "", 0},
		{nodeTool, ">=20", "20", "tag", "", 2},
		{nodeTool, "^20.11.1", "20.11.1", "semver", "", 1},
		{nodeTool, "v20.x", "20", "tag", "", 1},
		{nodeTool, "lts/iron", "", "", "non-version value", 0},
		{gradleTool, "8.11", "8.11", "maven", "", 0},
		{gradleTool, "8.11-rc-1", "8.11-rc-1", "maven", "", 0},
		{mavenTool, "3.9.9", "3.9.9", "maven", "", 0},
		{mavenTool, "~3.9", "", "", "unsupported range", 0},
		{mavenTool, "latest", "", "", "non-version value", 0},
		{tools["rust"], "1.82.0", "1.82.0", "semver", "", 0},
		{tools["rust"], "1.82", "1.82", "tag", "", 0},
		{tools["rust"], "nightly-2025-10-10", "nightly-2025-10-10", rustversion.NightlyVersioning, "", 0},
		{tools["rust"], "stable", "", "", "non-version value", 0},
		{tools["rust"], "nightly", "", "", "non-version value", 0},
		{tools["rust"], "nightly-2025-10", "", "", "non-version value", 0},
		{tools["elixir"], "1.17.3-otp-27", "1.17.3-otp-27", hexpmbob.ElixirVersioning, "", 0},
		{tools["elixir"], "1.18.0-rc.0", "1.18.0-rc.0", hexpmbob.ElixirVersioning, "", 0},
		{tools["elixir"], "1.17", "1.17", "tag", "", 0},
		{tools["elixir"], "main-otp-27", "", "", "non-version value", 0},
		{tools["elixir"], "ref:v1.17.3", "", "", "git ref", 0},
		{tools["erlang"], "27.1", "27.1", "loose", "", 0},
		{tools["erlang"], "26.2.5.4", "26.2.5.4", "loose", "", 0},
		{tools["erlang"], "27.0-rc1", "27.0-rc1", "loose", "", 0},
		{tools["erlang"], "27", "27", "tag", "", 0},
	} {
		ver, off, versioning, why := classify(c.t, c.raw)
		if ver != c.ver || off != c.off || versioning != c.versioning || why != c.why {
			t.Errorf("classify(%s, %q) = %q, %d, %q, %q; want %q, %d, %q, %q",
				c.t.name, c.raw, ver, off, versioning, why, c.ver, c.off, c.versioning, c.why)
		}
	}
}

func TestAsdf(t *testing.T) {
	in := "# runtime versions\n" +
		"nodejs 20.11.1 18.19.0\n" +
		"golang 1.27   # toolchain line\n" +
		"\tterraform\tv1.13.4\n" +
		"helm 3.16.0\r\n" +
		"kubectl v1.34.1 system\n" +
		"python 3.13.1\n" +
		"kustomize 5.4.3\n" +
		"jq 1.7.1\n" +
		"yq system\n" +
		"task ref:main\n" +
		"k9s path:/opt/k9s\n" +
		"kind latest\n" +
		"nodejs v20\n" +
		"flux2 2.6\n" +
		"lonely\n" +
		"   \n" +
		"golangci-lint 2.5.0"
	wants := []want{
		{"node", dsNode, "semver", "20.11.1", "", 2},
		{"go", dsGo, "tag", "1.27", "", 3},
		{"hashicorp/terraform", dsGitHub, "semver", "v1.13.4", "", 4},
		{"helm/helm", dsGitHub, "semver", "3.16.0", "", 5},
		{"kubernetes/kubernetes", dsGitHub, "semver", "v1.34.1", "", 6},
		{"python", dsPython, "pep440", "3.13.1", "", 7},
		{"kubernetes-sigs/kustomize", dsGitHub, "semver", "5.4.3", "", 8},
		{"jqlang/jq", dsGitHub, "semver", "1.7.1", "", 9},
		{"mikefarah/yq", dsGitHub, "", "system", "non-version value", 10},
		{"go-task/task", dsGitHub, "", "ref:main", "git ref", 11},
		{"derailed/k9s", dsGitHub, "", "path:/opt/k9s", "local path", 12},
		{"kubernetes-sigs/kind", dsGitHub, "", "latest", "non-version value", 13},
		{"node", dsNode, "tag", "20", "", 14},
		{"fluxcd/flux2", dsGitHub, "tag", "2.6", "", 15},
		{"golangci/golangci-lint", dsGitHub, "semver", "2.5.0", "", 18},
	}
	news := map[string]string{
		"20.11.1": "20.12.0", "1.27": "1.28", "v1.13.4": "v1.14.0", "3.16.0": "3.17.1",
		"v1.34.1": "v1.35.0", "20": "22", "2.5.0": "2.6.0", "3.13.1": "3.14.10", "2.6": "2.7",
	}
	got := check(t, NewAsdf(), ".tool-versions", in, wants, news)
	want := "# runtime versions\n" +
		"nodejs 20.12.0 18.19.0\n" +
		"golang 1.28   # toolchain line\n" +
		"\tterraform\tv1.14.0\n" +
		"helm 3.17.1\r\n" +
		"kubectl v1.35.0 system\n" +
		"python 3.14.10\n" +
		"kustomize 5.4.3\n" +
		"jq 1.7.1\n" +
		"yq system\n" +
		"task ref:main\n" +
		"k9s path:/opt/k9s\n" +
		"kind latest\n" +
		"nodejs v22\n" +
		"flux2 2.7\n" +
		"lonely\n" +
		"   \n" +
		"golangci-lint 2.6.0"
	if got != want {
		t.Errorf("patched =\n%s\nwant\n%s", got, want)
	}
	if d, _ := NewAsdf().Extract(".tool-versions", []byte(in), manager.Env{}); d[2].Source != "https://github.com/hashicorp/terraform" {
		t.Errorf("terraform source = %q", d[2].Source)
	}
}

func TestAsdfJVMTools(t *testing.T) {
	in := "java temurin-21.0.5+11.0.LTS\ngradle 8.10.2\nmaven 3.9.9\n"
	wants := []want{
		{"java", "", "", "temurin-21.0.5+11.0.LTS", "unsupported tool", 1},
		{"gradle", dsGradle, "maven", "8.10.2", "", 2},
		{"org.apache.maven:apache-maven", dsMaven, "maven", "3.9.9", "", 3},
	}
	got := check(t, NewAsdf(), ".tool-versions", in, wants, map[string]string{"8.10.2": "8.11", "3.9.9": "3.9.11"})
	if want := "java temurin-21.0.5+11.0.LTS\ngradle 8.11\nmaven 3.9.11\n"; got != want {
		t.Errorf("patched = %q, want %q", got, want)
	}
}

// Tools of the larger table (after Renovate's asdf/mise tables), as written in real
// .tool-versions files.
func TestAsdfMoreTools(t *testing.T) {
	in := "terraform-ls 0.36.4\n" +
		"kubectx 0.9.5\n" +
		"awscli 2.22.0\n" +
		"flutter 3.24.5-stable\n" +
		"poetry 2.1.3\n" +
		"protoc 29.2\n" +
		"dotnet-core 8.0.404\n" +
		"php 8.4.1\n" +
		"swift 6.0.2\n"
	wants := []want{
		{"hashicorp/terraform-ls", dsGitHub, "semver", "0.36.4", "", 1},
		{"ahmetb/kubectx", dsGitHub, "semver", "0.9.5", "", 2},
		{"aws/aws-cli", dsGitTags, "semver", "2.22.0", "", 3},
		{"flutter", dsFlutter, "semver", "3.24.5", "", 4},
		{"poetry", dsPypi, "pep440", "2.1.3", "", 5},
		{"protocolbuffers/protobuf", dsGitHub, "tag", "29.2", "", 6},
		{"dotnet-sdk", dsDotnet, "semver", "8.0.404", "", 7},
		{"php/php-src", dsGitTags, "semver", "8.4.1", "", 8},
		{"swiftlang/swift", dsGitHub, "semver", "6.0.2", "", 9},
	}
	news := map[string]string{"3.24.5": "3.27.1", "29.2": "31.1", "2.22.0": "2.27.4"}
	got := check(t, NewAsdf(), ".tool-versions", in, wants, news)
	for _, r := range [][2]string{{"flutter 3.24.5-stable", "flutter 3.27.1-stable"}, {"protoc 29.2", "protoc 31.1"}, {"awscli 2.22.0", "awscli 2.27.4"}} {
		in = strings.Replace(in, r[0], r[1], 1)
	}
	if got != in {
		t.Errorf("patched =\n%s\nwant\n%s", got, in)
	}
	d, _ := NewAsdf().Extract(".tool-versions", []byte("php 8.4.1\naws-cli 2.22.0\n"), manager.Env{})
	if d[0].ExtractVersion != `^php-(?<version>\d.*)$` || d[1].Source != "https://github.com/aws/aws-cli" {
		t.Errorf("php extract %q, aws-cli source %q", d[0].ExtractVersion, d[1].Source)
	}
}

// rust uses the rust-version datasource: releases, release lines and dated nightlies.
func TestAsdfRust(t *testing.T) {
	in := "rust 1.82.0\n" + "rust 1.82\n" + "rust nightly-2025-10-10\n" + "rust stable\n"
	wants := []want{
		{"rust", dsRust, "semver", "1.82.0", "", 1},
		{"rust", dsRust, "tag", "1.82", "", 2},
		{"rust", dsRust, rustversion.NightlyVersioning, "nightly-2025-10-10", "", 3},
		{"rust", dsRust, "", "stable", "non-version value", 4},
	}
	news := map[string]string{"1.82.0": "1.99.1", "1.82": "1.99", "nightly-2025-10-10": "nightly-2026-11-05"}
	got := check(t, NewAsdf(), ".tool-versions", in, wants, news)
	if want := "rust 1.99.1\nrust 1.99\nrust nightly-2026-11-05\nrust stable\n"; got != want {
		t.Errorf("patched = %q, want %q", got, want)
	}
}

// elixir uses hexpm-bob and keeps a build's OTP release; erlang uses erlang/otp's OTP tags.
func TestAsdfElixirErlang(t *testing.T) {
	in := "elixir 1.17.3-otp-27\n" + "erlang 27.1.2\n" + "elixir 1.17\n" + "erlang 27\n" + "elixir main-otp-27\n" + "erlang 26.2.5.4\n"
	wants := []want{
		{"elixir", dsHexBob, hexpmbob.ElixirVersioning, "1.17.3-otp-27", "", 1},
		{"erlang/otp", dsGitTags, "loose", "27.1.2", "", 2},
		{"elixir", dsHexBob, "tag", "1.17", "", 3},
		{"erlang/otp", dsGitTags, "tag", "27", "", 4},
		{"elixir", dsHexBob, "", "main-otp-27", "non-version value", 5},
		{"erlang/otp", dsGitTags, "loose", "26.2.5.4", "", 6},
	}
	news := map[string]string{"1.17.3-otp-27": "1.19.7-otp-27", "27.1.2": "28.1.1", "1.17": "1.19", "27": "28", "26.2.5.4": "28.1.1"}
	got := check(t, NewAsdf(), ".tool-versions", in, wants, news)
	if want := "elixir 1.19.7-otp-27\nerlang 28.1.1\nelixir 1.19\nerlang 28\nelixir main-otp-27\nerlang 28.1.1\n"; got != want {
		t.Errorf("patched = %q, want %q", got, want)
	}
	d, _ := NewAsdf().Extract(".tool-versions", []byte(in), manager.Env{})
	if d[1].ExtractVersion != `^OTP-(?<version>\d.*)$` || d[1].Source != "https://github.com/erlang/otp" {
		t.Errorf("erlang extract %q, source %q", d[1].ExtractVersion, d[1].Source)
	}
}

// MiseConfigs are files the mise manager reads; MiseTool finds a pinned tool in one.
func TestMiseTool(t *testing.T) {
	m := NewMise()
	for _, n := range MiseConfigs {
		if !m.Match("ws/" + n) {
			t.Errorf("mise does not read %s", n)
		}
	}
	for _, tc := range []struct{ src, want string }{
		{"[tools]\nnode = \"22\"\nrust = \"1.82\"\n", "1.82"},
		{"[tools]\nrust = { version = \"nightly-2025-10-10\", components = \"clippy\" }\n", "nightly-2025-10-10"},
		{"[tools]\nrust = \"stable\"\n", ""},
		{"[tools]\nnode = \"22\"\n", ""},
		{"[tools\n", ""},
	} {
		if got, ok := MiseTool([]byte(tc.src), "rust"); got != tc.want || ok != (tc.want != "") {
			t.Errorf("MiseTool(%q) = %q, %v", tc.src, got, ok)
		}
	}
}

func TestPartial(t *testing.T) {
	for _, c := range []struct {
		d    dep.Dep
		want bool
	}{
		{dep.Dep{Manager: NameAsdf, Datasource: dsGitHub, Versioning: "tag", Version: "2.6"}, true},
		{dep.Dep{Manager: NameMise, Datasource: dsNpm, Versioning: "tag", Version: "9"}, true},
		{dep.Dep{Manager: NameMise, Datasource: dsGitHub, Versioning: "semver", Version: "2.6.0"}, false},
		{dep.Dep{Manager: NameAsdf, Datasource: dsNode, Versioning: "tag", Version: "20"}, false},
		{dep.Dep{Manager: NameAsdf, Datasource: dsCrate, Versioning: "tag", Version: "14"}, false},
		{dep.Dep{Manager: NameMise, Datasource: dsRust, Versioning: "tag", Version: "1.82"}, false},
		{dep.Dep{Manager: NameNvm, Datasource: dsNode, Versioning: "tag", Version: "20"}, false},
		{dep.Dep{Manager: "github-actions", Datasource: dsGitTags, Versioning: "tag", Version: "v4"}, false},
	} {
		if got := Partial(c.d); got != c.want {
			t.Errorf("Partial(%+v) = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestMise(t *testing.T) {
	in := `min_version = "2024.1.0"

[env]
NODE_ENV = 'production'
PATHS = ["a", "b"]  # not tools

[tools]
# runtimes
node = "20.11.1"
"go" = '1.27'
terraform = ["v1.13.4", "1.12.0"]
helm = { version = "3.16.0", postinstall = "helm plugin install x" }
kubectl = [
  # multi-line array
  "1.34.1",
  "1.33.0",
]
"core:node" = "v22"
"github:cli/cli" = "2.60.0"
"ubi:BurntSushi/ripgrep[exe=rg]" = "14.1.1"
"npm:@biomejs/biome" = "1.9.4"
"aqua:helm/helm" = "v3.15.0"
"github:gitlab.com/x" = "1.0.0"
python = "3.13"
yq = "latest"
kind = "ref:v0.25.0"
task = "prefix:3"
golangci-lint = "2.5"
kustomize = "5.4.3"
escaped = "1.\u0032.3"
jq = """1.7.1"""
k9s = 1
flux2 = { os = ["linux"] }
pnpm.version = "9.12.0"

[tools.just]
version = "1.36.0"
os = ["linux", "macos"]

[[tasks.lint]]
version = "1.0.0"

[tasks.build]
run = """
echo "tools = [\"x\"]"
"""

[settings]
experimental = true
`
	wants := []want{
		{"node", dsNode, "semver", "20.11.1", "", 9},
		{"go", dsGo, "tag", "1.27", "", 10},
		{"hashicorp/terraform", dsGitHub, "semver", "v1.13.4", "", 11},
		{"helm/helm", dsGitHub, "semver", "3.16.0", "", 12},
		{"kubernetes/kubernetes", dsGitHub, "semver", "1.34.1", "", 15},
		{"node", dsNode, "tag", "22", "", 18},
		{"cli/cli", dsGitHub, "semver", "2.60.0", "", 19},
		{"BurntSushi/ripgrep", dsGitHub, "semver", "14.1.1", "", 20},
		{"@biomejs/biome", dsNpm, "semver", "1.9.4", "", 21},
		{"helm/helm", dsGitTags, "semver", "3.15.0", "", 22},
		{"github:gitlab.com/x", "", "", "1.0.0", "unsupported tool", 23},
		{"python", dsPython, "tag", "3.13", "", 24},
		{"mikefarah/yq", dsGitHub, "", "latest", "non-version value", 25},
		{"kubernetes-sigs/kind", dsGitHub, "", "ref:v0.25.0", "git ref", 26},
		{"go-task/task", dsGitHub, "", "prefix:3", "unsupported range", 27},
		{"golangci/golangci-lint", dsGitHub, "tag", "2.5", "", 28},
		{"kubernetes-sigs/kustomize", dsGitHub, "semver", "5.4.3", "", 29},
		{"escaped", "", "", `1.\u0032.3`, "unsupported tool", 30},
		{"jqlang/jq", dsGitHub, "", "1.7.1", "cannot locate value", 31},
		{"pnpm", dsNpm, "semver", "9.12.0", "", 34},
		{"casey/just", dsGitHub, "semver", "1.36.0", "", 37},
	}
	news := map[string]string{
		"20.11.1": "20.12.0", "1.27": "1.28", "v1.13.4": "v1.14.0", "3.16.0": "3.17.1", "1.34.1": "1.35.0",
		"22": "24", "2.60.0": "2.61.0", "14.1.1": "14.2.0", "1.9.4": "2.0.0", "9.12.0": "10.0.0", "1.36.0": "1.37.0", "3.13": "3.14",
		"3.15.0": "3.17.1", "2.5": "2.6",
	}
	got := check(t, NewMise(), "mise.toml", in, wants, news)
	for _, r := range [][2]string{
		{`node = "20.11.1"`, `node = "20.12.0"`},
		{`"go" = '1.27'`, `"go" = '1.28'`},
		{`terraform = ["v1.13.4", "1.12.0"]`, `terraform = ["v1.14.0", "1.12.0"]`},
		{`helm = { version = "3.16.0",`, `helm = { version = "3.17.1",`},
		{`  "1.34.1",`, `  "1.35.0",`},
		{`"core:node" = "v22"`, `"core:node" = "v24"`},
		{`"github:cli/cli" = "2.60.0"`, `"github:cli/cli" = "2.61.0"`},
		{`exe=rg]" = "14.1.1"`, `exe=rg]" = "14.2.0"`},
		{`"npm:@biomejs/biome" = "1.9.4"`, `"npm:@biomejs/biome" = "2.0.0"`},
		{`"aqua:helm/helm" = "v3.15.0"`, `"aqua:helm/helm" = "v3.17.1"`},
		{`golangci-lint = "2.5"`, `golangci-lint = "2.6"`},
		{`python = "3.13"`, `python = "3.14"`},
		{`pnpm.version = "9.12.0"`, `pnpm.version = "10.0.0"`},
		{`version = "1.36.0"`, `version = "1.37.0"`},
	} {
		in = strings.Replace(in, r[0], r[1], 1)
	}
	if got != in {
		t.Errorf("patched =\n%s\nwant\n%s", got, in)
	}
}

// The aqua, cargo, go, pipx and pypi backends, as written in real mise configs.
func TestMiseBackends(t *testing.T) {
	in := `[tools]
"aqua:cli/cli" = "2.83.0"
"aqua:ahmetb/kubectx/kubens" = "0.9.4"
"cargo:cargo-nextest" = "0.9.85"
"cargo:ripgrep[features=pcre2]" = "14"
"cargo:https://github.com/rust-lang/rustfmt" = "tag:v1.8.0"
"cargo:https://github.com/x/y" = "branch:main"
"cargo:https://github.com/x/z" = "1.0.0"
"go:github.com/golangci/golangci-lint/v2/cmd/golangci-lint" = "2.6.0"
"go:golang.org/x/tools/gopls" = "v0.20.0"
"go:mvdan.cc/gofumpt" = "latest"
"go:not a path" = "1.0.0"
"go:github.com/coder/whichtests" = "377b8dbf1cccae3b04b48bc8cfc2410813491414"
"pipx:black" = "24.10.0"
"pipx:psf/black" = "24.10.0"
"pipx:git+https://github.com/psf/black.git" = "24.10.0"
"pipx:https://example.com/black.zip" = "1.0.0"
"pypi:ruff" = "0.8"
"conda:numpy" = "2.1.0"
`
	wants := []want{
		{"cli/cli", dsGitTags, "semver", "2.83.0", "", 2},
		{"ahmetb/kubectx/kubens", dsGitTags, "semver", "0.9.4", "", 3},
		{"cargo-nextest", dsCrate, "semver", "0.9.85", "", 4},
		{"ripgrep", dsCrate, "tag", "14", "", 5},
		{"rust-lang/rustfmt", dsGitTags, "semver", "v1.8.0", "", 6},
		{"x/y", dsGitTags, "", "branch:main", "git ref", 7},
		{"x/z", dsGitTags, "", "1.0.0", "unsupported range", 8},
		{"github.com/golangci/golangci-lint/v2/cmd/golangci-lint", dsGoMod, "semver", "2.6.0", "", 9},
		{"golang.org/x/tools/gopls", dsGoMod, "semver", "0.20.0", "", 10},
		{"mvdan.cc/gofumpt", dsGoMod, "", "latest", "non-version value", 11},
		{"go:not a path", "", "", "1.0.0", "unsupported tool", 12},
		{"github.com/coder/whichtests", dsGoMod, "", "377b8dbf1cccae3b04b48bc8cfc2410813491414", "git ref", 13},
		{"black", dsPypi, "pep440", "24.10.0", "", 14},
		{"psf/black", dsGitTags, "semver", "24.10.0", "", 15},
		{"psf/black", dsGitTags, "semver", "24.10.0", "", 16},
		{"pipx:https://example.com/black.zip", "", "", "1.0.0", "unsupported tool", 17},
		{"ruff", dsPypi, "pep440", "0.8", "", 18},
		{"conda:numpy", "", "", "2.1.0", "unsupported tool", 19},
	}
	news := map[string]string{"2.83.0": "2.84.0", "v1.8.0": "v1.8.1", "0.20.0": "0.21.0", "14": "15"}
	got := check(t, NewMise(), "mise.toml", in, wants, news)
	for _, r := range [][2]string{
		{`"2.83.0"`, `"2.84.0"`}, {`"tag:v1.8.0"`, `"tag:v1.8.1"`}, {`"v0.20.0"`, `"v0.21.0"`}, {`pcre2]" = "14"`, `pcre2]" = "15"`},
	} {
		in = strings.Replace(in, r[0], r[1], 1)
	}
	if got != in {
		t.Errorf("patched =\n%s\nwant\n%s", got, in)
	}
	deps, _ := NewMise().Extract("mise.toml", []byte(in), manager.Env{})
	for i, w := range map[int][2]string{
		0:  {aquaExtract, "https://github.com/cli/cli"},
		4:  {"", "https://github.com/rust-lang/rustfmt"},
		7:  {goModExtract, ""},
		1:  {aquaExtract, "https://github.com/ahmetb/kubectx"},
		14: {"", "https://github.com/psf/black"},
	} {
		if deps[i].ExtractVersion != w[0] || deps[i].Source != w[1] {
			t.Errorf("%d: extractVersion %q, source %q; want %q", i, deps[i].ExtractVersion, deps[i].Source, w)
		}
	}
}

func TestMiseEscapedKnownTool(t *testing.T) {
	check(t, NewMise(), "mise.toml", "[tools]\nhelm = \"3.\\u0031.0\"\nterraform = '''1.13.4'''\n", []want{
		{"helm/helm", dsGitHub, "", `3.\u0031.0`, "cannot locate value", 2},
		{"hashicorp/terraform", dsGitHub, "", "1.13.4", "cannot locate value", 3},
	}, nil)
}

func TestMiseCRLF(t *testing.T) {
	in := "[tools]\r\nnode = \"\"\r\ngo = \"1.27.1\" # pinned\r\n[tools.helm]\r\nversion = 'v3.16.0'\r\n"
	out := check(t, NewMise(), "mise/config.toml", in, []want{
		{"node", dsNode, "", "", "non-version value", 2},
		{"go", dsGo, "tag", "1.27.1", "", 3},
		{"helm/helm", dsGitHub, "semver", "v3.16.0", "", 5},
	}, map[string]string{"1.27.1": "1.27.2", "v3.16.0": "v3.17.0"})
	if want := "[tools]\r\nnode = \"\"\r\ngo = \"1.27.2\" # pinned\r\n[tools.helm]\r\nversion = 'v3.17.0'\r\n"; out != want {
		t.Errorf("patched = %q", out)
	}
}

func TestMiseTopLevelDottedAndBOM(t *testing.T) {
	in := "\ufefftools.node = \"22.1.0\"\ntools = { }\n"
	out := check(t, NewMise(), ".mise.toml", in, []want{{"node", dsNode, "semver", "22.1.0", "", 1}}, map[string]string{"22.1.0": "22.2.0"})
	if out != "\ufefftools.node = \"22.2.0\"\ntools = { }\n" {
		t.Errorf("patched = %q", out)
	}
}

func TestMiseErrors(t *testing.T) {
	for _, in := range []string{
		"[tools\nnode = \"1\"\n",
		"[tools]\nnode = \"20\n",
		"[tools]\nnode = [\"20\"\n",
		"[tools]\nnode = { version = \"20\"\n",
		"[tools]\nnode \"20\"\n",
		"[tools]\nnode = \"20\" junk\n",
		"[tools]\nnode =\n",
		"[tools]\n= \"20\"\n",
		"[tools]\nnode = [\"20\" \"21\"]\n",
		"[tools]\nnode = { version = \"20\" os = \"x\" }\n",
		"[tools]\nnode = '''20\n",
	} {
		if deps, err := NewMise().Extract("mise.toml", []byte(in), manager.Env{}); err == nil {
			t.Errorf("Extract(%q) = %+v, want error", in, deps)
		}
	}
	if _, err := NewMise().Extract("mise.toml", []byte("[tools]\nnode = \"20\n"), manager.Env{}); err == nil || !strings.HasPrefix(err.Error(), "line 2:") {
		t.Errorf("error = %v, want line 2", err)
	}
}
