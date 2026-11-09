package download

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLocate(t *testing.T) {
	env := manager.Env{ForgeURL: "https://git.example.org"}
	for _, tc := range []struct {
		url, ds, name, source, version, extract, artifact string
		spans                                             int
	}{
		{"https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz", "github-releases", "jqlang/jq", "https://github.com/jqlang/jq", "1.7.1", `^jq-(?<version>\d.*)$`, "https://github.com/jqlang/jq/releases/download/jq-{{version}}/jq-{{version}}.tar.gz", 2},
		{"https://github.com/BurntSushi/ripgrep/archive/refs/tags/14.1.0.tar.gz", "git-tags", "BurntSushi/ripgrep", "https://github.com/BurntSushi/ripgrep", "14.1.0", "", "https://github.com/BurntSushi/ripgrep/archive/refs/tags/{{version}}.tar.gz", 1},
		{"https://git.example.org/team/deps/releases/download/v0.7.0/deps_linux_amd64.tar.xz", forge.GiteaReleases, "team/deps", "https://git.example.org", "0.7.0", `^v(?<version>\d.*)$`, "https://git.example.org/team/deps/releases/download/v{{version}}/deps_linux_amd64.tar.xz", 1},
		{"https://codeberg.org/a/b/releases/download/v1.2.3/b-1.2.3.tgz", forge.ForgejoReleases, "a/b", "https://codeberg.org", "1.2.3", `^v(?<version>\d.*)$`, "https://codeberg.org/a/b/releases/download/v{{version}}/b-{{version}}.tgz", 2},
		{"https://gitlab.com/g/sub/p/-/archive/v2.0.0/p-v2.0.0.tar.bz2", "git-tags", "g/sub/p", "https://gitlab.com/g/sub/p", "2.0.0", `^v(?<version>\d.*)$`, "https://gitlab.com/g/sub/p/-/archive/v{{version}}/p-v{{version}}.tar.bz2", 2},
		{"https://registry.npmjs.org/@scope/pkg/-/pkg-3.1.0.tgz", "npm", "@scope/pkg", "", "3.1.0", "", "https://registry.npmjs.org/@scope/pkg/-/pkg-{{version}}.tgz", 1},
	} {
		var d dep.Dep
		if !Locate(&d, tc.url, 10, env) {
			t.Errorf("Locate(%s) = false", tc.url)
			continue
		}
		if d.Datasource != tc.ds || d.Name != tc.name || d.Source != tc.source || d.Version != tc.version || d.ExtractVersion != tc.extract || d.ChecksumSource.Artifact != tc.artifact {
			t.Errorf("Locate(%s) = %+v %+v", tc.url, d, d.ChecksumSource)
		}
		n := 0
		for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s != (dep.Span{}) {
				n++
				if got := tc.url[s.Start-10 : s.End-10]; got != tc.version {
					t.Errorf("Locate(%s): span %v = %q", tc.url, s, got)
				}
			}
		}
		if n != tc.spans {
			t.Errorf("Locate(%s): %d spans, want %d", tc.url, n, tc.spans)
		}
	}
	for _, u := range []string{
		"https://example.com/a/b/archive/v1.0.0.tar.gz",                                  // unknown forge host
		"https://github.com/a/b/archive/latest.tar.gz",                                   // no digit
		"https://github.com/a/b/archive/0123456789abcdef0123456789abcdef01234567.tar.gz", // commit
		"https://registry.npmjs.org/pkg/-/other-1.0.0.tgz",
	} {
		var d dep.Dep
		if Locate(&d, u, 0, env) {
			t.Errorf("Locate(%s) = true", u)
		}
	}
}

func TestFollow(t *testing.T) {
	var d dep.Dep
	d.Manager, d.File = "homebrew", "Formula/x.rb"
	if !Locate(&d, "https://github.com/a/b/archive/v1.2.3.tar.gz", 0, manager.Env{}) {
		t.Fatal("Locate = false")
	}
	f, ok := Follow(d, "mirror", "https://mirror.example/1.2.3/b-1.2.3.tar.gz", 100, 7)
	if !ok || f.Manager != "homebrew" || f.DepType != "mirror" || f.Line != 7 || f.Name != "a/b" || f.ChecksumSource != nil ||
		f.RefSpan != (dep.Span{Start: 123, End: 128}) || f.VersionSpan != (dep.Span{Start: 131, End: 136}) {
		t.Errorf("Follow = %+v, %v", f, ok)
	}
	if _, ok := Follow(d, "mirror", "https://mirror.example/11.2.3/b.tar.gz", 0, 1); ok {
		t.Error("Follow inside a longer version = true")
	}
	d.Skip = "x"
	if _, ok := Follow(d, "version", "1.2.3", 0, 1); ok {
		t.Error("Follow of a skipped dep = true")
	}
}
