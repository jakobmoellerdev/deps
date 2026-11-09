package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// fakeRefs is fakeBranches whose releases are the branches for a dep under nixpkgs versioning,
// as gittags.Refs lists them; any other dep's release lookup fails the test.
type fakeRefs fakeBranches

func (f fakeRefs) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	if d.Versioning != versioning.Nixpkgs {
		return nil, fmt.Errorf("releases listed for %s", d.Name)
	}
	var rels []Release
	for k, c := range f {
		if src, branch, _ := strings.Cut(k, "#"); src == d.Source {
			rels = append(rels, Release{Version: branch, Digest: c})
		}
	}
	slices.SortFunc(rels, func(a, b Release) int { return cmp.Compare(a.Version, b.Version) })
	return rels, nil
}

func (f fakeRefs) Digest(ctx context.Context, d dep.Dep, ref string) (string, error) {
	return fakeBranches(f).Digest(ctx, d, ref)
}

// Flake inputs tracking a branch get digest updates that only move flake.lock, inputs on a
// version tag tag updates that rewrite flake.nix, pinned inputs digest updates of the rev in
// flake.nix; the post-processor gets every update of the flake.
func TestNixInputs(t *testing.T) {
	const (
		nixpkgs, lib, pinned = "https://github.com/NixOS/nixpkgs", "https://github.com/acme/lib", "https://github.com/acme/pinned"
		old                  = "0000000000000000000000000000000000000000"
		head                 = "1111111111111111111111111111111111111111"
	)
	flake := "{\n  inputs.nixpkgs.url = \"github:NixOS/nixpkgs/nixos-unstable\";\n" +
		"  inputs.lib.url = \"github:acme/lib/v1.0.0\";\n" +
		"  inputs.pinned = { url = \"github:acme/pinned/" + old + "\"; flake = false; };\n" +
		"  outputs = { self, ... }: { };\n}\n"
	node := func(repo, ref, rev, locked string) string {
		orig := `"owner": "acme", "repo": "` + repo + `", "type": "github"`
		if ref != "" {
			orig += `, "ref": "` + ref + `"`
		}
		if rev != "" {
			orig += `, "rev": "` + rev + `"`
		}
		return `{"locked": {"owner": "acme", "repo": "` + repo + `", "rev": "` + locked + `", "type": "github"}, "original": {` + orig + `}}`
	}
	lock := `{"nodes": {"nixpkgs": ` + strings.Replace(node("nixpkgs", "nixos-unstable", "", old), `"owner": "acme"`, `"owner": "NixOS"`, 2) +
		`, "lib": ` + node("lib", "v1.0.0", "", sha(lib+"@v1.0.0")) + `, "pinned": ` + node("pinned", "", old, old) +
		`, "root": {"inputs": {"nixpkgs": "nixpkgs", "lib": "lib", "pinned": "pinned"}}}, "root": "root", "version": 7}`
	ms := []manager.Manager{nix.New()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"git-refs": fakeRefs{nixpkgs + "#nixos-unstable": head, nixpkgs + "#nixos-24.11": old, pinned + "#HEAD": head},
		"git-tags": fakeTags{lib: {"v1.0.0", "v1.1.0"}},
	}}
	root, cfg := setup(t, "", map[string]string{"flake.nix": flake, "flake.lock": lock})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Skipped) > 0 {
		t.Fatalf("err = %v, errors = %v, skipped = %+v", err, res.Errors, res.Skipped)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{{"nixpkgs", "nixos-unstable", head, dep.Digest}, {"lib", "v1.1.0", sha(lib + "@v1.1.0"), dep.Minor}, {"pinned", "HEAD", head, dep.Digest}}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}

	var inputs []string
	post := map[string]PostProcessor{nix.Name: pinPost{
		postFunc: postFunc{siblings: []string{"flake.lock"}},
		pinned: func(ups, locks map[string][]dep.Update) (map[string][]byte, error) {
			for _, u := range ups["flake.nix"] {
				inputs = append(inputs, u.Dep.Name)
			}
			return map[string][]byte{"flake.lock": []byte("new lock\n")}, nil
		},
	}}
	changes, errs := Build(t.Context(), root, ms, res.Updates, post)
	wantFlake := strings.NewReplacer("lib/v1.0.0", "lib/v1.1.0", "pinned/"+old, "pinned/"+head).Replace(flake)
	if len(errs) > 0 || len(changes) != 2 || changes[0].Path != "flake.lock" || string(changes[0].New) != "new lock\n" ||
		changes[1].Path != "flake.nix" || string(changes[1].New) != wantFlake {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
	if !slices.Equal(inputs, []string{"nixpkgs", "lib", "pinned"}) {
		t.Errorf("post-processed inputs = %v", inputs)
	}
	// plan (no post-processors): the branch input's update has no diff.
	if changes, errs := Build(t.Context(), root, ms, res.Updates[:1], nil); len(changes) != 0 || len(errs) != 0 {
		t.Errorf("plan: changes = %+v, %v", changes, errs)
	}
}

// NixOS/nixpkgs release branches and release channels move to the next release branch
// (nixpkgs versioning, branches of git-refs as candidates): flake.nix is rewritten and the
// post-processor re-locks the input. A channel has no digest; a fork's branch gets digest
// updates only and lists no branches.
func TestNixpkgsReleaseBranches(t *testing.T) {
	const (
		nixpkgs, fork = "https://github.com/NixOS/nixpkgs", "https://github.com/acme/nixpkgs"
		old           = "0000000000000000000000000000000000000000"
	)
	flake := "{\n  inputs.stable.url = \"github:NixOS/nixpkgs/nixos-24.05\";\n" +
		"  inputs.channel.url = \"https://channels.nixos.org/nixos-24.05/nixexprs.tar.xz\";\n" +
		"  inputs.fork.url = \"github:acme/nixpkgs/nixos-24.05\";\n" +
		"  outputs = { self, ... }: { };\n}\n"
	gh := func(owner string) string {
		return `{"locked": {"owner": "` + owner + `", "repo": "nixpkgs", "rev": "` + old + `", "type": "github"}, "original": {"owner": "` + owner + `", "repo": "nixpkgs", "ref": "nixos-24.05", "type": "github"}}`
	}
	lock := `{"nodes": {"stable": ` + gh("NixOS") + `, "fork": ` + gh("acme") +
		`, "channel": {"locked": {"rev": "` + old + `", "type": "tarball", "url": "https://releases.nixos.org/nixos/24.05/nixos-24.05.1.000000000000/nixexprs.tar.xz"}, "original": {"type": "tarball", "url": "https://channels.nixos.org/nixos-24.05/nixexprs.tar.xz"}}` +
		`, "root": {"inputs": {"stable": "stable", "channel": "channel", "fork": "fork"}}}, "root": "root", "version": 7}`
	branches := fakeRefs{fork + "#nixos-24.05": sha("fork")}
	for _, b := range []string{"master", "nixos-23.11", "nixos-24.05", "nixos-24.05-small", "nixos-24.11", "nixos-25.05", "nixos-unstable", "release-24.11"} {
		branches[nixpkgs+"#"+b] = sha(b)
	}
	ms := []manager.Manager{nix.New()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{"git-refs": branches}}
	root, cfg := setup(t, "", map[string]string{"flake.nix": flake, "flake.lock": lock})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Skipped) > 0 {
		t.Fatalf("err = %v, errors = %v, skipped = %+v", err, res.Errors, res.Skipped)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{
		{"stable", "nixos-24.11", sha("nixos-24.11"), dep.Minor}, {"stable", "nixos-25.05", sha("nixos-25.05"), dep.Major},
		{"channel", "nixos-24.11", "", dep.Minor}, {"channel", "nixos-25.05", "", dep.Major},
		{"fork", "nixos-24.05", sha("fork"), dep.Digest},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}

	var inputs []string
	post := map[string]PostProcessor{nix.Name: pinPost{
		postFunc: postFunc{siblings: []string{"flake.lock"}},
		pinned: func(ups, _ map[string][]dep.Update) (map[string][]byte, error) {
			for _, u := range ups["flake.nix"] {
				inputs = append(inputs, u.Dep.Name+"@"+u.NewVersion)
			}
			return map[string][]byte{"flake.lock": []byte("new lock\n")}, nil
		},
	}}
	local, _ := Local(res.Updates)
	changes, errs := Build(t.Context(), root, ms, local, post)
	wantFlake := strings.NewReplacer("nixpkgs/nixos-24.05", "nixpkgs/nixos-24.11", "org/nixos-24.05/", "org/nixos-24.11/").Replace(flake)
	wantFlake = strings.Replace(wantFlake, "acme/nixpkgs/nixos-24.11", "acme/nixpkgs/nixos-24.05", 1)
	if len(errs) > 0 || len(changes) != 2 || changes[1].Path != "flake.nix" || string(changes[1].New) != wantFlake {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
	if !slices.Equal(inputs, []string{"stable@nixos-24.11", "channel@nixos-24.11", "fork@nixos-24.05"}) {
		t.Errorf("post-processed inputs = %v", inputs)
	}
}
