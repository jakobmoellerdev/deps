package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations follow Renovate's nixpkgs versioning
// (lib/modules/versioning/nixpkgs/index.spec.ts).

func TestNixpkgsScheme(t *testing.T) {
	s := get(t, Nixpkgs)
	for v, ok := range map[string]bool{
		"nixos-24.05": true, "nixos-24.05-small": true, "nixpkgs-24.05-darwin": true, "release-24.05": true,
		"nixos-unstable": true, "nixpkgs-unstable": true, "nixos-unstable-small": true, "release-24.05-aarch64": true,
		"nixos-24.5": false, "nixos-2024.05": false, "nixos-24.05-beta": false, "master": false, "staging-24.05": false, "24.05": false,
	} {
		if s.Valid(v) != ok {
			t.Errorf("Valid(%q) = %v", v, !ok)
		}
		if NixpkgsRelease(v) != (ok && v != "nixos-unstable" && v != "nixpkgs-unstable" && v != "nixos-unstable-small") {
			t.Errorf("NixpkgsRelease(%q) = %v", v, !NixpkgsRelease(v))
		}
	}
	for _, c := range [][2]string{{"nixos-23.11", "nixos-24.05"}, {"nixos-24.05", "nixos-24.11"}, {"nixos-24.11", "nixos-unstable"}} {
		if s.Compare(c[0], c[1]) >= 0 || s.Compare(c[1], c[0]) <= 0 {
			t.Errorf("Compare(%s, %s) order", c[0], c[1])
		}
	}
	if s.Compare("nixos-unstable", "nixpkgs-unstable") != 0 || s.Compare("nixos-24.05", "nixos-24.05-small") != 0 {
		t.Error("unstable and same releases compare equal")
	}
	for c, k := range map[[2]string]dep.UpdateType{
		{"nixos-24.05", "nixos-24.11"}: dep.Minor, {"nixos-24.11", "nixos-25.05"}: dep.Major,
		{"nixos-24.05", "nixos-24.05"}: dep.Patch, {"nixos-24.05", "nixos-unstable"}: dep.Major,
	} {
		if got := s.Kind(c[0], c[1]); got != k {
			t.Errorf("Kind(%s, %s) = %s, want %s", c[0], c[1], got, k)
		}
	}
	if sv := s.Semver("nixos-24.05"); sv == nil || sv.String() != "24.5.0" {
		t.Errorf("Semver = %v", sv)
	}
	if s.Semver("nixos-unstable") != nil {
		t.Error("unstable has a semver")
	}
}

// TestNixpkgsBest: a release branch moves to the newest release of the same prefix and
// suffix, never to unstable; an unstable branch has nothing newer.
func TestNixpkgsBest(t *testing.T) {
	branches := []string{
		"master", "staging", "nixos-23.11", "nixos-24.05", "nixos-24.05-small", "nixos-24.11", "nixos-24.11-small",
		"nixos-25.05", "nixos-unstable", "nixos-unstable-small", "nixpkgs-24.05-darwin", "nixpkgs-24.11-darwin",
		"nixpkgs-unstable", "release-24.05", "release-24.11", "release-25.05",
	}
	runBest(t, Nixpkgs, []bestCase{
		{cur: "nixos-24.05", want: "nixos-25.05", cands: branches},
		{cur: "nixos-24.05", want: "nixos-24.11", cands: branches, accept: func(v string) bool { return v != "nixos-25.05" }},
		{cur: "nixos-24.05-small", want: "nixos-24.11-small", cands: branches},
		{cur: "nixpkgs-24.05-darwin", want: "nixpkgs-24.11-darwin", cands: branches},
		{cur: "release-24.05", want: "release-25.05", cands: branches},
		{cur: "nixos-25.05", want: "", cands: branches},
		{cur: "nixos-unstable", want: "", cands: branches},
		{cur: "nixpkgs-unstable", want: "", cands: branches},
	})
	var reasons = map[string]string{}
	get(t, Nixpkgs).Best("nixos-24.05", branches, nil, func(v, why string) { reasons[v] = why })
	for v, want := range map[string]string{"master": "invalid", "nixos-unstable": "prerelease", "nixos-24.05-small": "compatibility", "release-24.11": "compatibility", "nixos-23.11": "not newer"} {
		if reasons[v] != want {
			t.Errorf("%s rejected as %q, want %q", v, reasons[v], want)
		}
	}
	if k := get(t, Nixpkgs).Rank("nixos-24.05", branches, nil, nil); k.Len() != 2 || k.At(0).Version != "nixos-25.05" || k.At(0).Major != "25" || k.At(1).Kind != dep.Minor {
		t.Errorf("Rank = %d candidates", k.Len())
	}
}
