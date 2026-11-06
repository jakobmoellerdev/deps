package beam

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The rewrites follow Renovate's hex versioning (lib/modules/versioning/hex, on top of its npm
// versioning) and the examples of the mix and gleam manager docs.
func TestNewRange(t *testing.T) {
	for _, tt := range []struct {
		rng, v, strategy, want string
	}{
		// replace (update-lockfile, replace and the default rewrite): the last alternative.
		{"~> 1.7", "2.1.0", "", "~> 2.1"},
		{"~> 1.7", "2.1.0", "replace", "~> 2.1"},
		{"~> 1.7.2", "2.1.0", "update-lockfile", "~> 2.1.0"},
		{"~>1.7.2", "2.1.0", "", "~>2.1.0"},
		{"== 0.12.0", "0.13.0", "", "== 0.13.0"},
		{"0.12.0", "0.13.0", "", "0.13.0"},
		{">= 1.0.0 and < 2.0.0", "2.1.0", "", ">= 2.1.0 and < 3.0.0"},
		{"> 1.0.0 and < 2.0.0", "2.1.0", "", ">= 2.1.0 and < 3.0.0"},
		{"<= 0.38.0", "0.39.0", "", "<= 0.39.0"},
		{"< 0.15.0", "0.16.0", "", "< 0.17.0"},
		{"~> 1.0 or ~> 2.0", "3.1.0", "", "~> 3.1"},
		{"~> 1.7 and != 1.7.2", "2.0.0", "", "~> 2.0 and != 1.7.2"},
		{"~> 2.0.0-rc.0", "2.1.0-rc.1", "", "~> 2.1.0-rc.1"},
		// widen: upper bounds move, else the replaced last alternative is appended.
		{">= 0.44.0 and < 2.0.0", "2.1.0", dep.RangeWiden, ">= 0.44.0 and < 3.0.0"},
		{">= 0.14.0 and < 0.15.0", "0.16.0", dep.RangeWiden, ">= 0.14.0 and < 0.17.0"},
		{"<= 0.38.0", "0.39.0", dep.RangeWiden, "<= 0.39.0"},
		{"~> 1.7", "2.1.0", dep.RangeWiden, "~> 1.7 or ~> 2.1"},
		{"~> 1.0 or ~> 2.0", "3.1.0", dep.RangeWiden, "~> 1.0 or ~> 2.0 or ~> 3.1"},
		{"< 1.7.0 or ~> 1.7.1", "1.8.2", dep.RangeWiden, "< 1.7.0 or ~> 1.7.1 or ~> 1.8.2"},
		{"== 0.12.0", "0.13.0", dep.RangeWiden, "== 0.13.0"},
		// bump: the lower bounds of the first alternative admitting the version.
		{"~> 1.7", "1.9.3", "bump", "~> 1.9"},
		{"~> 1.7.2", "1.7.5", "bump", "~> 1.7.5"},
		{">= 1.0.0 and < 2.0.0", "1.4.0", "bump", ">= 1.4.0 and < 2.0.0"},
		{"> 1.0.0", "1.4.0", "bump", ">= 1.4.0"},
		{"~> 0.4 or ~> 1.0", "0.5.1", "bump", "~> 0.5 or ~> 1.0"},
		// pin
		{"~> 1.7", "1.9.3", dep.RangePin, "== 1.9.3"},
		{"1.7.2", "1.9.3", dep.RangePin, "1.9.3"},
	} {
		got, ok := newRange(tt.rng, tt.v, tt.strategy)
		if !ok || got != tt.want {
			t.Errorf("newRange(%q, %s, %q) = %q, %v; want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	for _, tt := range []struct{ rng, v, strategy string }{
		{"~> 1.9", "1.9.3", "bump"},         // already the floor at its precision
		{"== 1.2.3", "1.2.3", "bump"},       // exact
		{"< 2.0.0", "1.5.0", "bump"},        // no lower bound to raise
		{"== 1.2.3", "1.2.3", dep.RangePin}, // already pinned
		{"~> 1.7", "v2.0.0", "replace"},     // not a version
		{"^1.7", "2.0.0", "replace"},        // not a requirement
		{"~> 1.7", "2.0.0-rc.1", "replace"},
	} {
		if got, ok := newRange(tt.rng, tt.v, tt.strategy); ok {
			t.Errorf("newRange(%q, %s, %q) = %q, want none", tt.rng, tt.v, tt.strategy, got)
		}
	}
}

func TestAnchorAndComplex(t *testing.T) {
	for rng, want := range map[string]string{
		"~> 1.7": "1.7.0", "~> 1.7.2": "1.7.2", "== 1.2.3": "1.2.3", "1.2.3": "1.2.3", "> 1.0.0": "1.0.0",
		">= 0.0.0": "", "< 2.0.0": "", ">= 0.44.0 and < 2.0.0": "0.44.0", "~> 0.4 or ~> 1.0": "1.0.0",
		"~> 2.0-rc.1": "2.0.0-rc.1", ">= 0.0.0 and ~> 1.2": "1.2.0",
	} {
		if got, ok := anchor(rng); !ok || got != want {
			t.Errorf("anchor(%q) = %q, %v; want %q", rng, got, ok, want)
		}
	}
	if _, ok := anchor("^1.0"); ok {
		t.Error("anchor of an invalid requirement")
	}
	for rng, want := range map[string]bool{"~> 1.7": false, "== 1.2.3": false, ">= 1.0.0 and < 2.0.0": true, "~> 1.0 or ~> 2.0": true, "^1": false} {
		if Complex(rng) != want {
			t.Errorf("Complex(%q) = %v", rng, !want)
		}
	}
}
