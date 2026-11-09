package nix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// extract extracts the testdata flakes named and reads their locks, as the engine does.
func extract(t *testing.T, names ...string) ([][]dep.Dep, map[string][]byte) {
	t.Helper()
	var files [][]dep.Dep
	srcs := map[string][]byte{}
	for _, n := range names {
		file := n + "/flake.nix"
		src := read(t, filepath.Join("testdata", file))
		deps, err := New().Extract(file, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files, srcs[file] = append(files, deps), src
	}
	if errs := New().(manager.LockReader).ReadLocks("testdata", files); len(errs) > 0 {
		t.Fatal(errs)
	}
	return files, srcs
}

// render prints a dep with the text at its spans.
func render(d dep.Dep, src []byte) string {
	span := func(s dep.Span) string {
		if s == (dep.Span{}) {
			return "-"
		}
		return string(src[s.Start:s.End])
	}
	digest := d.Digest
	if len(digest) > 7 {
		digest = digest[:7]
	}
	s := fmt.Sprintf("%d %s %s %s@%s %s ref=%s rev=%s", d.Line, d.Name, d.Datasource, d.Version, digest, d.Source, span(d.RefSpan), span(d.DigestSpan))
	if d.Skip != "" {
		s = fmt.Sprintf("%d %s skip %q", d.Line, d.Name, d.Skip)
	}
	return s
}

func renderAll(deps []dep.Dep, src []byte) []string {
	out := make([]string, len(deps))
	for i, d := range deps {
		if d.Manager != Name || d.DepType != DepTypeInput || d.Versioning != "tag" {
			out[i] = "bad dep " + fmt.Sprint(d)
			continue
		}
		out[i] = render(d, src)
	}
	return out
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{"flake.nix": true, "nix/hosts/flake.nix": true, "flake.lock": false, "default.nix": false, "myflake.nix": false} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

// TestExtractReal reads the flakes of NixOS/nix (channel tarball, pinned revs, follows), devenv
// (branch refs, pinned revs, attribute sets per input), Hyprland (one `inputs = { … }` set)
// and home-manager.
func TestExtractReal(t *testing.T) {
	files, srcs := extract(t, "nixos-nix", "devenv", "hyprland", "home-manager")
	want := [][]string{{
		`4 nixpkgs skip "nixos channel (lockFileMaintenance)"`,
		"6 nixpkgs-regression git-refs HEAD@215d4d0 https://github.com/NixOS/nixpkgs ref=- rev=215d4d0fd80ca5163643b03a33fde804a29cc1e2",
		"7 nixpkgs-23-11 git-refs HEAD@a62e6ed https://github.com/NixOS/nixpkgs ref=- rev=a62e6edd6d5e1fa0329b8653c801147986f8d446",
		"9 flake-compat git-refs HEAD@5edf11c https://github.com/NixOS/flake-compat ref=- rev=-",
		"14 flake-parts git-refs HEAD@024633c https://github.com/hercules-ci/flake-parts ref=- rev=-",
		"15 git-hooks-nix git-refs HEAD@1ba1dbf https://github.com/cachix/git-hooks.nix ref=- rev=-",
	}, {
		"10 nixpkgs git-refs rolling@d97234e https://github.com/cachix/devenv-nixpkgs ref=- rev=-",
		"12 git-hooks git-refs HEAD@1ba1dbf https://github.com/cachix/git-hooks.nix ref=- rev=-",
		"19 flake-compat git-refs HEAD@5edf11c https://github.com/edolstra/flake-compat ref=- rev=-",
		"23 flake-parts git-refs HEAD@024633c https://github.com/hercules-ci/flake-parts ref=- rev=-",
		"29 nix git-refs devenv-2.35@082f141 https://github.com/cachix/nix ref=- rev=-",
		"40 nixd git-refs HEAD@87012f5 https://github.com/nix-community/nixd ref=- rev=-",
		"48 crate2nix git-refs HEAD@ba5dd39 https://github.com/rossng/crate2nix ref=- rev=ba5dd398e31ee422fbe021767eb83b0650303a6e",
		"52 rust-overlay git-refs HEAD@c7e9d51 https://github.com/oxalica/rust-overlay ref=- rev=-",
		"59 ghostty git-refs HEAD@22d1317 https://github.com/ghostty-org/ghostty ref=- rev=22d13172cde98a0a4dda05d3d6a3fcb0dd8ed018",
	}}
	for i, w := range want {
		if got := renderAll(files[i], srcs[[]string{"nixos-nix", "devenv"}[i]+"/flake.nix"]); !slices.Equal(got, w) {
			t.Errorf("deps %d =\n%s\nwant\n%s", i, strings.Join(got, "\n"), strings.Join(w, "\n"))
		}
	}
	hypr := renderAll(files[2], srcs["hyprland/flake.nix"])
	if len(hypr) != 14 || hypr[0] != "5 nixpkgs git-refs nixos-unstable@1c89083 https://github.com/NixOS/nixpkgs ref=- rev=-" ||
		hypr[12] != "88 xdph git-refs HEAD@d1467fe https://github.com/hyprwm/xdg-desktop-portal-hyprland ref=- rev=-" {
		t.Errorf("hyprland =\n%s", strings.Join(hypr, "\n"))
	}
	if got := renderAll(files[3], srcs["home-manager/flake.nix"]); !slices.Equal(got, []string{`4 nixpkgs skip "nixos channel (lockFileMaintenance)"`}) {
		t.Errorf("home-manager = %v", got)
	}
}

// TestExtractForms covers every input form: tags in paths, query parameters, attribute
// sets and URI literals, the sources of each input type, and the skips.
func TestExtractForms(t *testing.T) {
	files, srcs := extract(t, "forms")
	want := []string{
		"9 tagged git-tags v1.2.3@1111111 https://github.com/acme/tagged ref=v1.2.3 rev=-",
		"11 pinned-tag git-tags v2.0.0@aaaaaaa https://git.example.com/acme/pinned-tag ref=v2.0.0 rev=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"14 sub git-refs main@2222222 https://gitlab.com/group/sub/project ref=- rev=-",
		"15 hut git-refs HEAD@3333333 https://git.sr.ht/~user/hut ref=- rev=-",
		"16 attrs git-tags v0.1.0@4444444 https://github.com/acme/attrs ref=v0.1.0 rev=-",
		"17 uri git-tags v3.0.0@5555555 https://github.com/acme/uri ref=v3.0.0 rev=-",
		`18 escaped skip "cannot locate ref"`,
		"19 ssh git-refs develop@7777777 https://github.com/acme/ssh ref=- rev=-",
		"20 lix git-tags 2.93.3@8888888 https://git.lix.systems/lix-project/lix ref=2.93.3 rev=-",
		`21 hub skip "unsupported tarball url"`,
		`22 gh-archive skip "no locked revision"`,
		`23 local skip "local path input"`,
		`24 local-git skip "local path input"`,
		`25 registry skip "indirect input (flake registry)"`,
		`26 hg skip "unsupported input type hg"`,
		`27 stale skip "not in flake.lock"`,
		`28 followed skip "follows another input"`,
		`32 channel skip "nixos channel (lockFileMaintenance)"`,
	}
	if got := renderAll(files[0], srcs["forms/flake.nix"]); !slices.Equal(got, want) {
		t.Errorf("deps =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReadLocksErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"broken/flake.lock": "{", "noroot/flake.lock": `{"nodes": {}, "version": 7}`})
	dep1 := func(file string) []dep.Dep {
		return []dep.Dep{{Manager: Name, File: file, Name: "nixpkgs"}, {Manager: Name, File: file, Name: "x", Skip: "earlier"}}
	}
	nolock, broken, noroot := dep1("nolock/flake.nix"), dep1("broken/flake.nix"), dep1("noroot/flake.nix")
	errs := New().(manager.LockReader).ReadLocks(root, [][]dep.Dep{nil, nolock, broken, noroot})
	if len(errs) != 2 || !strings.HasPrefix(errs[0].Error(), "broken/flake.lock: ") || errs[1].Error() != "noroot/flake.lock: not a flake.lock: no root node" {
		t.Fatalf("errs = %v", errs)
	}
	for _, c := range []struct {
		deps []dep.Dep
		skip string
	}{{nolock, skipNoLock}, {broken, skipUnreadable}, {noroot, skipUnreadable}} {
		if c.deps[0].Skip != c.skip || c.deps[1].Skip != "earlier" {
			t.Errorf("%s: %+v", c.deps[0].File, c.deps)
		}
	}
	// A node without locked or original reference is not locked; unusable repository URLs.
	l, err := parseLock([]byte(`{"nodes": {"root": {"inputs": {"a": "a"}}, "a": {}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		node lockNode
		skip string
	}{
		{lockNode{}, skipNotLocked},
		{lockNode{Locked: &flakeRef{Type: "git", Rev: "x"}, Original: &flakeRef{Type: "git", URL: "https://example.com/repo.git"}}, skipURL},
		{lockNode{Locked: &flakeRef{Type: "gitlab", Rev: "x"}, Original: &flakeRef{Type: "gitlab", Owner: "bad%zz", Repo: "r"}}, skipURL},
	} {
		l.Nodes["a"] = c.node
		d := dep.Dep{Name: "a"}
		l.resolve(&d)
		if d.Skip != c.skip {
			t.Errorf("%+v: skip %q", c.node, d.Skip)
		}
	}
}

// TestReadLocksChannel: a NixOS channel only advances after Hydra passes, so its commit lags
// the head of the nixpkgs branch of the same name, and `nix flake update` keeps locking the
// channel's commit. A git-refs digest update to the branch head would be proposed on every run
// without flake.lock ever reaching it; channel inputs are skipped, as Renovate has no digest
// update for them either, and move with lock file maintenance.
func TestReadLocksChannel(t *testing.T) {
	l, err := parseLock([]byte(`{"nodes": {"root": {"inputs": {"nixpkgs": "nixpkgs"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"https://channels.nixos.org/nixos-24.05/nixexprs.tar.xz",
		"https://channels.nixos.org/nixpkgs-unstable/nixexprs.tar.zst",
		"https://nixos.org/channels/nixos-unstable/nixexprs.tar.xz",
		"tarball+https://channels.nixos.org/nixos-24.11/nixexprs.tar.gz",
	} {
		l.Nodes["nixpkgs"] = lockNode{
			Locked:   &flakeRef{Type: "tarball", Rev: strings.Repeat("b", 40), URL: "https://releases.nixos.org/nixos/24.05/nixos-24.05.1.bbbbbbbbbbbb/nixexprs.tar.xz"},
			Original: &flakeRef{Type: "tarball", URL: u},
		}
		d := dep.Dep{Name: "nixpkgs"}
		l.resolve(&d)
		if d.Skip != skipChannel || d.Datasource != "" || d.Digest != "" {
			t.Errorf("%s: %+v", u, d)
		}
	}
}

// TestReadLocksInputNames: an unpinned branch input only moves with `nix flake update
// <input>`, which cannot name `foo.bar` (rejected) or `a/b` (input b of input a): skipped.
// Tag and rev updates of such inputs change flake.nix and stay.
func TestReadLocksInputNames(t *testing.T) {
	rev := strings.Repeat("1", 40)
	src := []byte(`{ inputs."foo.bar".url = "github:acme/a"; inputs."a/b".url = "github:acme/b/v1.0.0"; inputs."x.y".url = "github:acme/c/` + rev + `"; inputs.ok_1-x.url = "github:acme/d"; }`)
	deps, err := New().Extract("flake.nix", src, manager.Env{})
	if err != nil || len(deps) != 4 {
		t.Fatal(deps, err)
	}
	node := func(repo, ref, pin string) string {
		return `{"locked": {"owner": "acme", "repo": "` + repo + `", "rev": "` + rev + `", "type": "github"}, "original": {"owner": "acme", "repo": "` + repo + `", "ref": "` + ref + `", "rev": "` + pin + `", "type": "github"}}`
	}
	l, err := parseLock([]byte(`{"nodes": {"a": ` + node("a", "", "") + `, "b": ` + node("b", "v1.0.0", "") + `, "c": ` + node("c", "", rev) + `, "d": ` + node("d", "", "") +
		`, "root": {"inputs": {"foo.bar": "a", "a/b": "b", "x.y": "c", "ok_1-x": "d"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i := range deps {
		l.resolve(&deps[i])
	}
	want := []string{
		`1 foo.bar skip "input name not addressable by nix flake update"`,
		"1 a/b git-tags v1.0.0@1111111 https://github.com/acme/b ref=v1.0.0 rev=-",
		"1 x.y git-refs HEAD@1111111 https://github.com/acme/c ref=- rev=" + rev,
		"1 ok_1-x git-refs HEAD@1111111 https://github.com/acme/d ref=- rev=-",
	}
	if got := renderAll(deps, src); !slices.Equal(got, want) {
		t.Errorf("deps =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`{ inputs.a.url = "github:acme/a/v1.0.0"; inputs.b.url = "github:acme/b/` + strings.Repeat("0", 40) + `"; inputs.c.url = "github:acme/c/main"; }`)
	deps, err := New().Extract("flake.nix", src, manager.Env{})
	if err != nil || len(deps) != 3 {
		t.Fatal(deps, err)
	}
	a, b, c := deps[0], deps[1], deps[2]
	a.Datasource = dsTags // as ReadLocks sets it
	edits, err := New().Edits(dep.Update{Dep: a, NewVersion: "v1.1.0", NewDigest: strings.Repeat("1", 40)})
	if err != nil || len(edits) != 1 || edits[0].Old != "v1.0.0" || edits[0].New != "v1.1.0" || string(src[edits[0].Start:edits[0].End]) != "v1.0.0" {
		t.Errorf("tag edits = %+v, %v", edits, err)
	}
	b.Version = defaultBranch
	edits, err = New().Edits(dep.Update{Dep: b, NewVersion: defaultBranch, NewDigest: strings.Repeat("1", 40)})
	if err != nil || len(edits) != 1 || edits[0].Old != b.Digest || edits[0].New != strings.Repeat("1", 40) {
		t.Errorf("rev edits = %+v, %v", edits, err)
	}
	if edits, err := New().Edits(dep.Update{Dep: b, NewVersion: defaultBranch, NewDigest: b.Digest}); err != nil || len(edits) != 0 {
		t.Errorf("same rev = %+v, %v", edits, err)
	}
	// A branch input: only flake.lock moves.
	c.RefSpan = dep.Span{}
	if edits, err := New().Edits(dep.Update{Dep: c, NewVersion: "main", NewDigest: strings.Repeat("1", 40)}); err != nil || len(edits) != 0 {
		t.Errorf("branch edits = %+v, %v", edits, err)
	}
	if _, err := New().Edits(dep.Update{Dep: c, NewVersion: "develop"}); err == nil {
		t.Error("branch change accepted")
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"flake.nix": "{}", "flake.lock": "{}", "b/flake.nix": "{}", "b/flake.lock": "{}", "c/flake.nix": "{}", "d/flake.lock/x": ""})
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"b/flake.nix", "flake.nix", "c/flake.nix", "d/flake.nix"})
	want := []manager.LockRoot{{Manifest: "b/flake.nix", Lockfile: "b/flake.lock"}, {Manifest: "flake.nix", Lockfile: "flake.lock"}}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %+v", got)
	}
	if _, ok := (Locker{}).Scope(t.Context(), root, "flake.lock"); ok {
		t.Error("scope of a non-flake.nix")
	}
	if (Locker{}).Siblings()[0] != lockFile {
		t.Error("siblings")
	}
}
