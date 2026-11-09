package nix

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	emptyHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	emptySRI = "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	onesHex  = "1111111111111111111111111111111111111111111111111111111111111111"
	onesSRI  = "sha256-ERERERERERERERERERERERERERERERERERERERERERE="
)

// renderDownload prints a download dep: line, type, name, datasource, version, the text at
// its version spans, its checksum and artifact template, or its skip reason.
func renderDownload(d dep.Dep, src []byte) string {
	if d.Skip != "" {
		return fmt.Sprintf("%d %s %s skip %q", d.Line, d.DepType, d.Name, d.Skip)
	}
	var spans []string
	for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
		if s != (dep.Span{}) {
			spans = append(spans, fmt.Sprintf("%d:%s", s.Start, src[s.Start:s.End]))
		}
	}
	s := fmt.Sprintf("%d %s %s %s %s %s [%s]", d.Line, d.DepType, d.Name, d.Datasource, d.Version, d.Source, strings.Join(spans, " "))
	if d.ExtractVersion != "" {
		s += " extract=" + d.ExtractVersion
	}
	if d.Checksum != "" {
		s += fmt.Sprintf(" sum=%s %q", d.Checksum[:8], src[d.ChecksumSpan.Start:d.ChecksumSpan.End])
	}
	if d.ChecksumSource != nil {
		s += " artifact=" + d.ChecksumSource.Artifact
	}
	return s
}

func TestExtractDownloads(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  manager.Env
		src  string
		want []string
	}{{
		name: "sri",
		src: `{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.jq = nixpkgs.legacyPackages.x86_64-linux.fetchurl {
      hash = "` + emptySRI + `";
      url = "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz";
    };
  };
}`,
		want: []string{
			`5 download jqlang/jq github-releases 1.7.1 https://github.com/jqlang/jq [244:1.7.1 253:1.7.1] extract=^jq-(?<version>\d.*)$ sum=e3b0c442 "` + emptySRI + `" artifact=https://github.com/jqlang/jq/releases/download/jq-{{version}}/jq-{{version}}.tar.gz`,
		},
	}, {
		name: "hex",
		src: `{
  outputs = _: let
    src = builtins.fetchurl { url = "https://github.com/BurntSushi/ripgrep/archive/refs/tags/14.1.0.tar.gz"; sha256 = "` + emptyHex + `"; };
  in { };
}`,
		want: []string{
			`3 download BurntSushi/ripgrep git-tags 14.1.0 https://github.com/BurntSushi/ripgrep [114:14.1.0] sum=e3b0c442 "` + emptyHex + `" artifact=https://github.com/BurntSushi/ripgrep/archive/refs/tags/{{version}}.tar.gz`,
		},
	}, {
		name: "per-system with version",
		src: `{
  outputs = { self, nixpkgs }: let
    version = "2.4.0";
    # url = "https://github.com/acme/tool/releases/download/v0.0.1/x.tar.gz"; hash = "` + emptySRI + `";
    sources = {
      x86_64-linux = {
        url = "https://github.com/acme/tool/releases/download/v2.4.0/tool_2.4.0_linux_amd64.tar.gz";
        hash = "` + emptySRI + `";
      };
      aarch64-darwin = {
        url = "https://github.com/acme/tool/releases/download/v2.4.0/tool_2.4.0_darwin_arm64.tar.gz";
        sha256 = "` + onesHex + `";
      };
    };
    script = ''
      url = "https://github.com/acme/tool/releases/download/v1.0.0/tool.tar.gz"; hash = "` + emptySRI + `";
    '';
  in { };
}`,
		want: []string{
			`7 download acme/tool github-releases 2.4.0 https://github.com/acme/tool [302:2.4.0 313:2.4.0] extract=^v(?<version>\d.*)$ sum=e3b0c442 "` + emptySRI + `" artifact=https://github.com/acme/tool/releases/download/v{{version}}/tool_{{version}}_linux_amd64.tar.gz`,
			`11 download acme/tool github-releases 2.4.0 https://github.com/acme/tool [507:2.4.0 518:2.4.0] extract=^v(?<version>\d.*)$ sum=11111111 "` + onesHex + `" artifact=https://github.com/acme/tool/releases/download/v{{version}}/tool_{{version}}_darwin_arm64.tar.gz`,
			`3 version acme/tool github-releases 2.4.0 https://github.com/acme/tool [52:2.4.0] extract=^v(?<version>\d.*)$`,
		},
	}, {
		name: "gitea release asset",
		env:  manager.Env{ForgeURL: "https://git.example.org"},
		src: `{
  outputs = _: {
    bin = pkgs.fetchurl rec {
      version = "0.7.0";
      url = "https://git.example.org/team/deps/releases/download/v0.7.0/deps_linux_amd64.tar.xz";
      hash = "` + emptySRI + `";
    };
  };
}`,
		want: []string{
			`5 download team/deps gitea-releases 0.7.0 https://git.example.org [140:0.7.0] extract=^v(?<version>\d.*)$ sum=e3b0c442 "` + emptySRI + `" artifact=https://git.example.org/team/deps/releases/download/v{{version}}/deps_linux_amd64.tar.xz`,
			`4 version team/deps gitea-releases 0.7.0 https://git.example.org [66:0.7.0] extract=^v(?<version>\d.*)$`,
		},
	}, {
		name: "skips",
		src: `{
  inputs.tool = { url = "https://github.com/acme/tool/archive/v1.0.0.tar.gz"; flake = false; };
  outputs = _: {
    nix32 = fetchurl { url = "https://github.com/acme/a/archive/v1.0.0.tar.gz"; sha256 = "0aqdx4hm5c9dfspbsiqzbkvfhgrncaflzvlhl6n6ph6ri5szsgy6"; };
    sha512 = fetchurl { url = "https://github.com/acme/b/archive/v1.0.0.tar.gz"; hash = "sha512-AAAA"; };
    nohash = { url = "https://github.com/acme/c/archive/v1.0.0.tar.gz"; };
    zip = fetchzip { url = "https://github.com/acme/d/archive/v1.0.0.tar.gz"; hash = "` + emptySRI + `"; };
    other = fetchurl { url = "https://example.com/e-1.0.0.tar.gz"; hash = "` + emptySRI + `"; };
    git = builtins.fetchGit { url = "https://example.com/f.git"; rev = "abc"; };
    interpolated = fetchurl { url = "https://github.com/acme/g/archive/v${v}.tar.gz"; hash = "` + emptySRI + `"; };
  };
}`,
		want: []string{
			`4 download acme/a skip "unsupported hash format"`,
			`5 download acme/b skip "unsupported hash format"`,
			`6 download acme/c skip "no hash"`,
			`7 download acme/d skip "hash of unpacked content"`,
			`8 download https://example.com/e-1.0.0.tar.gz skip "unsupported download url"`,
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			deps, err := New().Extract("flake.nix", src, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range deps {
				if isDownload(d) {
					if d.Manager != Name || d.File != "flake.nix" {
						t.Errorf("bad dep %+v", d)
					}
					got = append(got, renderDownload(d, src))
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestEditsDownloadSRI moves a per-system download and its version attribute: the SRI hash
// is written back as SRI, the hex one as hex.
func TestEditsDownloadSRI(t *testing.T) {
	src := []byte(`{
  outputs = _: let
    version = "2.4.0";
    linux = { url = "https://github.com/acme/tool/releases/download/v2.4.0/tool-2.4.0.tar.gz"; hash = "` + emptySRI + `"; };
    darwin = { url = "https://github.com/acme/tool/releases/download/v2.4.0/tool-2.4.0-darwin.tar.gz"; sha256 = "` + emptyHex + `"; };
  in { };
}`)
	deps, err := New().Extract("flake.nix", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 {
		t.Fatalf("deps = %+v", deps)
	}
	var edits []patch.Edit
	for _, d := range deps {
		u := dep.Update{Dep: d, NewVersion: "2.5.0"}
		if d.ChecksumSource != nil {
			u.NewChecksum = onesHex
		}
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  outputs = _: let
    version = "2.5.0";
    linux = { url = "https://github.com/acme/tool/releases/download/v2.5.0/tool-2.5.0.tar.gz"; hash = "` + onesSRI + `"; };
    darwin = { url = "https://github.com/acme/tool/releases/download/v2.5.0/tool-2.5.0-darwin.tar.gz"; sha256 = "` + onesHex + `"; };
  in { };
}`
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	// The re-extracted SRI hash decodes to the new digest.
	again, err := New().Extract("flake.nix", out, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Checksum != onesHex || again[0].Version != "2.5.0" {
		t.Errorf("re-extracted %+v", again[0])
	}
	// A version change without the new hash is refused.
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "2.5.0"}); err == nil {
		t.Error("Edits without NewChecksum: want error")
	}
}

func TestParseHash(t *testing.T) {
	for s, want := range map[string]string{
		emptySRI:                  emptyHex,
		emptyHex:                  emptyHex,
		strings.ToUpper(emptyHex): strings.ToUpper(emptyHex),
		"sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU": "", // unpadded: not canonical
		"sha256:" + emptyHex: "",
		"sha512-AAAA":        "",
		"0aqdx4hm5c9dfspbsiqzbkvfhgrncaflzvlhl6n6ph6ri5szsgy6": "",
	} {
		got, ok := parseHash(s)
		if got != want || ok != (want != "") {
			t.Errorf("parseHash(%q) = %q, %v", s, got, ok)
		}
	}
	if sri(emptyHex) != emptySRI {
		t.Errorf("sri = %s", sri(emptyHex))
	}
}

// TestDownloadsWithoutLock: downloads do not need flake.lock, and their updates alone do not
// run nix.
func TestDownloadsWithoutLock(t *testing.T) {
	src := []byte(`{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = _: { tool = fetchurl { url = "https://github.com/acme/tool/archive/v1.0.0.tar.gz"; hash = "` + emptySRI + `"; }; };
}`)
	deps, err := New().Extract("flake.nix", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if errs := New().(manager.LockReader).ReadLocks(t.TempDir(), [][]dep.Dep{deps}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(deps) != 2 || deps[0].Skip != skipNoLock || deps[1].DepType != DepTypeDownload || deps[1].Skip != "" {
		t.Fatalf("deps = %+v", deps)
	}
	t.Setenv("PATH", t.TempDir()) // no nix
	ups := map[string][]dep.Update{"flake.nix": {{Dep: deps[1], NewVersion: "1.1.0", NewChecksum: onesHex}}}
	out, err := Locker{}.ProcessUpdates(t.Context(), t.TempDir(), map[string][]byte{"flake.nix": src}, map[string][]byte{"flake.lock": []byte("lock\n")}, ups, nil)
	if err != nil || out != nil {
		t.Errorf("ProcessUpdates = %v, %v", out, err)
	}
}
