package homebrew

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures: https://raw.githubusercontent.com/Homebrew/homebrew-core/main/Formula/<letter>/<name>.rb
// (fetched 2026-11-06): gh, ripgrep (tag archives), jq (release asset), prettier, gemini-cli
// (npm tarballs), zlib (zlib.net, unsupported; its zpipe.c resource has a later url+sha256).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMatch(t *testing.T) {
	m := New()
	for path, want := range map[string]bool{
		"Formula/jq.rb":        true,
		"Formula/j/jq.rb":      true,
		"Formula/lib/jq.rb":    true,
		"Formula/a/b/jq.rb":    false,
		"tap/Formula/jq.rb":    false,
		"Formula/jq.json":      false,
		"Casks/jq.rb":          false,
		"Formula/x-y/a-b@2.rb": false,
		"Formula/a-b@2.rb":     true,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v", path, got)
		}
	}
}

type want struct {
	ds, name, source, version, extract, skip string
	line, spans                              int
	checksum, artifact                       string
	deps                                     int // deps extracted in total; 0 = 1
}

func TestExtract(t *testing.T) {
	const sum = "71b8d6e8f5fe81f6c6d0d110e3892251f6ce76ed095abd315e26e6e1193af3af"
	for _, tc := range []struct {
		name, src string
		env       manager.Env
		want      want
	}{
		{name: "gh", src: fixture(t, "gh.rb"), want: want{ds: "git-tags", name: "cli/cli", source: "https://github.com/cli/cli", version: "2.103.3", extract: `^v(?<version>\d.*)$`,
			line: 4, spans: 1, checksum: "bb148138a3a2fe023e57c78042fe3b42fc09171c0e8fff4cf983b227b7a2eade",
			artifact: "https://github.com/cli/cli/archive/refs/tags/v{{version}}.tar.gz"}},
		{name: "ripgrep", src: fixture(t, "ripgrep.rb"), want: want{ds: "git-tags", name: "BurntSushi/ripgrep", source: "https://github.com/BurntSushi/ripgrep", version: "15.2.0",
			line: 4, spans: 1, checksum: "7605249d3eb0d5f170e3414498e3344e26b1e7a147aec518b57090b80036a562",
			artifact: "https://github.com/BurntSushi/ripgrep/archive/refs/tags/{{version}}.tar.gz"}},
		{name: "jq", src: fixture(t, "jq.rb"), want: want{ds: "github-releases", name: "jqlang/jq", source: "https://github.com/jqlang/jq", version: "1.8.2", extract: `^jq-(?<version>\d.*)$`,
			line: 4, spans: 2, checksum: sum,
			artifact: "https://github.com/jqlang/jq/releases/download/jq-{{version}}/jq-{{version}}.tar.gz"}},
		{name: "prettier", src: fixture(t, "prettier.rb"), want: want{ds: "npm", name: "prettier", version: "3.9.10",
			line: 4, spans: 1, checksum: "9dc72045591793dff972432fb78722f7ab91aaa90a7fa8c1261ede55969246b7",
			artifact: "https://registry.npmjs.org/prettier/-/prettier-{{version}}.tgz"}},
		{name: "gemini-cli", src: fixture(t, "gemini-cli.rb"), want: want{ds: "npm", name: "@google/gemini-cli", version: "0.67.0",
			line: 4, spans: 1, checksum: "3f42374e88171031459ad16e5fac32b161adba330292a1822fb7aae7fb476fc2",
			artifact: "https://registry.npmjs.org/@google/gemini-cli/-/gemini-cli-{{version}}.tgz"}},
		{name: "release v asset bare", src: "class Repo < Formula\n  url \"https://github.com/owner/repo/releases/download/v1.2.3/repo-1.2.3-darwin.zip\"\n  sha256 \"" + sum + "\"\nend\n",
			want: want{ds: "github-releases", name: "owner/repo", source: "https://github.com/owner/repo", version: "1.2.3", extract: `^v(?<version>\d.*)$`, line: 2, spans: 2, checksum: sum,
				artifact: "https://github.com/owner/repo/releases/download/v{{version}}/repo-{{version}}-darwin.zip"}},
		{name: "short archive, longer version elsewhere", src: "url \"https://github.com/o/r/archive/1.2.zip\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "o/r", source: "https://github.com/o/r", version: "1.2", line: 1, spans: 1, checksum: sum, artifact: "https://github.com/o/r/archive/{{version}}.zip"}},
		{name: "zlib", src: fixture(t, "zlib.rb"), want: want{name: "https://zlib.net/zlib-1.4.tar.gz", line: 4, skip: SkipUnsupported, deps: 2}},
		{name: "git url", src: "url \"https://github.com/o/r.git\", tag: \"v1.0.0\"\n", want: want{name: "https://github.com/o/r.git", line: 1, skip: SkipUnsupported}},
		{name: "tag without digit", src: "url \"https://github.com/o/r/archive/main.tar.gz\"\nsha256 \"" + sum + "\"\n", want: want{name: "https://github.com/o/r/archive/main.tar.gz", line: 1, skip: SkipUnsupported}},
		{name: "no sha256", src: "url \"https://github.com/o/r/archive/v1.tar.gz\"\nurl \"https://x\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "o/r", source: "https://github.com/o/r", version: "1", extract: `^v(?<version>\d.*)$`, line: 1, skip: SkipNoSHA256, deps: 2}},
		{name: "bad sha256", src: "url \"https://github.com/o/r/archive/v1.tar.gz\"\nsha256 \"abc\"\n",
			want: want{ds: "git-tags", name: "o/r", source: "https://github.com/o/r", version: "1", extract: `^v(?<version>\d.*)$`, line: 1, skip: SkipNoSHA256}},
		{name: "digit in tag prefix", src: "url \"https://github.com/rockdaboot/wget2/archive/refs/tags/wget2-2.1.0.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "rockdaboot/wget2", source: "https://github.com/rockdaboot/wget2", version: "2.1.0", extract: `^wget2-(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://github.com/rockdaboot/wget2/archive/refs/tags/wget2-{{version}}.tar.gz"}},
		{name: "number in tag prefix", src: "url \"https://github.com/pnggroup/libpng/archive/refs/tags/libpng16-1.6.43.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "pnggroup/libpng", source: "https://github.com/pnggroup/libpng", version: "1.6.43", extract: `^libpng16-(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://github.com/pnggroup/libpng/archive/refs/tags/libpng16-{{version}}.tar.gz"}},
		{name: "digit and v in tag prefix", src: "url \"https://github.com/k3s-io/k3s/archive/refs/tags/k3s-v1.2.3.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "k3s-io/k3s", source: "https://github.com/k3s-io/k3s", version: "1.2.3", extract: `^k3s-v(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://github.com/k3s-io/k3s/archive/refs/tags/k3s-v{{version}}.tar.gz"}},
		{name: "letter prefix without boundary", src: "url \"https://github.com/o/r/archive/r1234.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "o/r", source: "https://github.com/o/r", version: "1234", extract: `^r(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://github.com/o/r/archive/r{{version}}.tar.gz"}},
		{name: "commit archive", src: "url \"https://github.com/o/r/archive/0a1b2c3d4e5f60718293a4b5c6d7e8f901234567.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{name: "https://github.com/o/r/archive/0a1b2c3d4e5f60718293a4b5c6d7e8f901234567.tar.gz", line: 1, skip: SkipUnsupported}},
		{name: "npm prerelease", src: "url \"https://registry.npmjs.org/foo/-/foo-1.0.0-1.tgz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "npm", name: "foo", version: "1.0.0-1", line: 1, spans: 1, checksum: sum, artifact: "https://registry.npmjs.org/foo/-/foo-{{version}}.tgz"}},
		{name: "npm scoped hyphenated", src: "url \"https://registry.npmjs.org/@s/a-2/-/a-2-3.0.0-rc.1.tgz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "npm", name: "@s/a-2", version: "3.0.0-rc.1", line: 1, spans: 1, checksum: sum, artifact: "https://registry.npmjs.org/@s/a-2/-/a-2-{{version}}.tgz"}},
		{name: "npm filename of other package", src: "url \"https://registry.npmjs.org/@s/a/-/b-1.0.0.tgz\"\nsha256 \"" + sum + "\"\n",
			want: want{name: "https://registry.npmjs.org/@s/a/-/b-1.0.0.tgz", line: 1, skip: SkipUnsupported}},
		{name: "gitea release on the checkout's forge", env: manager.Env{ForgeURL: "https://git.example.org"},
			src: "url \"https://git.example.org/o/r/releases/download/v1.2.3/r_darwin_arm64\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "gitea-releases", name: "o/r", source: "https://git.example.org", version: "1.2.3", extract: `^v(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://git.example.org/o/r/releases/download/v{{version}}/r_darwin_arm64"}},
		{name: "release on another forge", src: "url \"https://git.example.org/o/r/releases/download/v1.2.3/r\"\nsha256 \"" + sum + "\"\n",
			want: want{name: "https://git.example.org/o/r/releases/download/v1.2.3/r", line: 1, skip: SkipUnsupported}},
		{name: "codeberg release", src: "url \"https://codeberg.org/o/r/releases/download/1.2.3/r-1.2.3.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "forgejo-releases", name: "o/r", source: "https://codeberg.org", version: "1.2.3", line: 1, spans: 2, checksum: sum,
				artifact: "https://codeberg.org/o/r/releases/download/{{version}}/r-{{version}}.tar.gz"}},
		{name: "codeberg archive", src: "url \"https://codeberg.org/o/r/archive/v2.0.tar.gz\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "o/r", source: "https://codeberg.org/o/r", version: "2.0", extract: `^v(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://codeberg.org/o/r/archive/v{{version}}.tar.gz"}},
		{name: "gitlab archive in a subgroup", src: "url \"https://gitlab.example.com/g/sub/p/-/archive/v1.4.0/p-v1.4.0.tar.bz2\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "git-tags", name: "g/sub/p", source: "https://gitlab.example.com/g/sub/p", version: "1.4.0", extract: `^v(?<version>\d.*)$`, line: 1, spans: 2, checksum: sum,
				artifact: "https://gitlab.example.com/g/sub/p/-/archive/v{{version}}/p-v{{version}}.tar.bz2"}},
		{name: "gitlab release asset", src: "url \"https://gitlab.com/g/p/-/releases/v3.1.0/downloads/bin/p_linux_amd64\"\nsha256 \"" + sum + "\"\n",
			want: want{ds: "gitlab-releases", name: "g/p", source: "https://gitlab.com", version: "3.1.0", extract: `^v(?<version>\d.*)$`, line: 1, spans: 1, checksum: sum,
				artifact: "https://gitlab.com/g/p/-/releases/v{{version}}/downloads/bin/p_linux_amd64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := New().Extract("Formula/x.rb", []byte(tc.src), tc.env)
			if err != nil || len(ds) != max(tc.want.deps, 1) {
				t.Fatalf("deps %+v, err %v", ds, err)
			}
			d, w := ds[0], tc.want
			if d.Manager != Name || d.DepType != DepType || d.Datasource != w.ds || d.Name != w.name || d.Source != w.source || d.Version != w.version ||
				d.ExtractVersion != w.extract || d.Line != w.line || d.Skip != w.skip {
				t.Fatalf("dep = %+v, want %+v", d, w)
			}
			if w.skip != "" {
				return
			}
			n := 0
			for _, s := range []dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
				if s.End > s.Start {
					n++
					if got := tc.src[s.Start:s.End]; got != w.version {
						t.Errorf("span %v = %q", s, got)
					}
				}
			}
			if n != w.spans {
				t.Errorf("%d spans, want %d", n, w.spans)
			}
			if d.Checksum != w.checksum || tc.src[d.ChecksumSpan.Start:d.ChecksumSpan.End] != w.checksum {
				t.Errorf("checksum %q span %v", d.Checksum, d.ChecksumSpan)
			}
			if d.ChecksumSource == nil || *d.ChecksumSource != (dep.ChecksumSource{Artifact: w.artifact}) {
				t.Errorf("ChecksumSource = %+v", d.ChecksumSource)
			}
		})
	}
	for _, src := range []string{"class X < Formula\nend\n", "  url :stable\n"} {
		if ds, err := New().Extract("Formula/x.rb", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("Extract(%q) = %+v, %v", src, ds, err)
		}
	}
	// More than three occurrences of the version cannot be tracked.
	src := "url \"https://github.com/o/r/releases/download/v1.0/r-1.0-1.0-1.0.tgz\"\n"
	if ds, _ := New().Extract("Formula/x.rb", []byte(src), manager.Env{}); len(ds) != 1 || ds[0].Skip != SkipUnsupported {
		t.Errorf("deps = %+v", ds)
	}
}

func TestEdits(t *testing.T) {
	const newSum = "0000000000000000000000000000000000000000000000000000000000000001"
	src := fixture(t, "jq.rb")
	ds, err := New().Extract("Formula/jq.rb", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	edits, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "1.9.0", NewChecksum: newSum})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`url "https://github.com/jqlang/jq/releases/download/jq-1.9.0/jq-1.9.0.tar.gz"`,
		`sha256 "` + newSum + `"`,
		`url "https://github.com/jqlang/jq.git", branch: "master"`,
	} {
		if !strings.Contains(string(out), s) {
			t.Errorf("missing %q in\n%s", s, out)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "1.9.0"}); err == nil {
		t.Error("version update without checksum succeeded")
	}
}

// TestExtractScopes covers per-platform downloads, mirrors, the version statement, resources
// and the blocks whose statements are no downloads (head, livecheck, bottle, patch, method
// bodies with heredocs, test).
func TestExtractScopes(t *testing.T) {
	src := fixture(t, "multi.rb")
	ds, err := New().Extract("Formula/tool.rb", []byte(src), manager.Env{ForgeURL: "https://git.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		depType, ds, name, version string
		line, spans                int
		checksum                   bool
	}
	want := []got{
		{DepType, "gitea-releases", "o/tool", "1.2.3", 15, 1, true},
		{DepType, "gitea-releases", "o/tool", "1.2.3", 19, 1, true},
		{DepType, "gitea-releases", "o/tool", "1.2.3", 25, 1, true},
		{DepTypeMirror, "gitea-releases", "o/tool", "1.2.3", 26, 1, false},
		{DepTypeVersion, "gitea-releases", "o/tool", "1.2.3", 4, 1, false},
		{DepTypeResource, "git-tags", "o/docs", "4.5", 59, 2, true},
	}
	if len(ds) != len(want) {
		t.Fatalf("%d deps, want %d: %+v", len(ds), len(want), ds)
	}
	for i, d := range ds {
		n := 0
		for _, s := range []dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s.End > s.Start {
				n++
				if src[s.Start:s.End] != d.Version {
					t.Errorf("dep %d: span %v = %q", i, s, src[s.Start:s.End])
				}
			}
		}
		g := got{d.DepType, d.Datasource, d.Name, d.Version, d.Line, n, d.ChecksumSpan.End > d.ChecksumSpan.Start}
		if g != want[i] || d.Skip != "" || !d.Together {
			t.Errorf("dep %d = %+v (skip %q, together %v), want %+v", i, g, d.Skip, d.Together, want[i])
		}
	}
}

// TestEditsFollowers updates every dep of a formula to one version, as the group of its
// downloads does: the urls, their sha256, the mirror and the version statement move together.
func TestEditsFollowers(t *testing.T) {
	src := fixture(t, "multi.rb")
	ds, err := New().Extract("Formula/tool.rb", []byte(src), manager.Env{ForgeURL: "https://git.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	const newSum = "abababababababababababababababababababababababababababababababab"
	var edits []patch.Edit
	for _, d := range ds[:5] {
		u := dep.Update{Dep: d, NewVersion: "1.3.0"}
		if d.ChecksumSource != nil {
			u.NewChecksum = newSum
		}
		e, err := New().Edits(u)
		if err != nil {
			t.Fatalf("%s line %d: %v", d.DepType, d.Line, err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`version "1.3.0"`,
		`url "https://git.example.org/o/tool/releases/download/v1.3.0/tool_darwin_arm64"`,
		`url "https://git.example.org/o/tool/releases/download/v1.3.0/tool_linux_amd64"`,
		`mirror "https://mirror.example.net/tool/11.2.3/1.3.0/tool_linux_amd64"`,
		`url "https://git.example.org/o/tool/releases/download/v0.0.1/x"`,
		`url "https://git.example.org/o/tool/releases/download/v9.9.9/never"`,
	} {
		if !strings.Contains(string(out), s) {
			t.Errorf("missing %q in\n%s", s, out)
		}
	}
	if n := strings.Count(string(out), `sha256 "`+newSum+`"`); n != 3 {
		t.Errorf("%d sha256 updated, want 3", n)
	}
}
