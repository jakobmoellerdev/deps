package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations follow Renovate's glasskube versioning tests
// (lib/modules/versioning/glasskube/index.spec.ts).

func TestGlasskubeValid(t *testing.T) {
	s := get(t, "glasskube")
	for v, want := range map[string]bool{
		"alpha": false, "v1": false, "v1.2": false, "v1.2.3": true, "v1.2.3+1": true, "v1.2.3-1": true,
		"v1.2.3-1+1": true, "1.2.3-1+1": true, "v01.2.3": false, "v1.2.3+": false, "vv1.2.3": false, "": false,
		"v1.17.0+2": true, "v1.2.3+build.5": true,
	} {
		if got := s.Valid(v); got != want {
			t.Errorf("Valid(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareGlasskube(t *testing.T) {
	s := get(t, "glasskube")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		// Renovate's isGreaterThan(b, a) cases.
		{"v1.2.3", "v1.2.3+1", -1},
		{"v1.2.3+1", "v1.2.3+2", -1},
		{"v1.2.3-1", "v1.2.3+1", -1},
		{"v1.2.3-1+1", "v1.2.3+1", -1},
		{"v1.2.3-1", "v1.2.3-1+1", -1},
		// The revision is a fourth release part, absent = 0; other build identifiers count not.
		{"v1.2.3", "v1.2.3+0", 0},
		{"v1.2.3+2", "v1.2.3+10", -1},
		{"v1.2.3+2abc", "v1.2.3+2", 0},
		{"v1.2.3+abc", "v1.2.3", 0},
		{"v1.2.3+1.9", "v1.2.3+1", 0},
		{"v1.17.0+2", "v1.17.1+1", -1},
		{"v1.17.0+2", "1.17.0+2", 0},
		{"v2.0.0-rc.1+1", "v2.0.0-rc.2+1", -1},
		{"v2.0.0-rc.2", "v2.0.0-rc.10", -1},
		{"v1.2.3+99999999999999999999", "v1.2.3+18446744073709551615", 0},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestGlasskubeBest(t *testing.T) {
	// The versions of cert-manager in Glasskube's package repository.
	names := []string{"v1.13.3+1", "v1.14.2+1", "v1.14.5+1", "v1.15.0+1", "v1.16.3+1", "v1.17.0+1", "v1.17.0+2", "v1.17.1+1", "v1.19.1+1", "v2.0.0-rc.1+1", "junk"}
	runBest(t, "glasskube", []bestCase{
		{"v1.17.0+1", "v1.19.1+1", names, nil},
		{"v1.17.0+1", "v1.17.0+2", names, func(v string) bool { return v < "v1.17.1" }},
		{"v1.19.1+1", "", names, nil},
		{"v2.0.0-rc.0+1", "v2.0.0-rc.1+1", names, nil},
		{"junk", "", names, nil},
	})
}

func TestGlasskubeKindAndSemver(t *testing.T) {
	s := get(t, "glasskube")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"v1.17.0+1", "v1.17.0+2", dep.Patch}, {"v1.17.0+2", "v1.17.1+1", dep.Patch},
		{"v1.17.0+2", "v1.18.0+1", dep.Minor}, {"v1.17.0+2", "v2.0.0+1", dep.Major},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	if got := s.Semver("v1.17.0+2"); got == nil || got.String() != "1.17.0+2" {
		t.Errorf("Semver(v1.17.0+2) = %v", got)
	}
	if s.Semver("v1.2") != nil {
		t.Error("Semver of an invalid version")
	}
	r := s.Rank("v1.17.0+1", []string{"v1.17.0+2", "v2.1.0+1", "v1.18.0+1"}, nil, nil)
	if r.Len() != 3 || r.At(0).Major != "2" || r.At(1).Kind != dep.Minor || r.At(2).Version != "v1.17.0+2" {
		t.Errorf("Rank = %+v %+v %+v", r.At(0), r.At(1), r.At(2))
	}
}
