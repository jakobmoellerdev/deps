package versioning

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestCargoParse(t *testing.T) {
	s, _ := Get("cargo")
	for _, v := range []string{"1", "0.4", "1.2.3", "0.1.0-beta.1", "1.0.0+meta", "10.20.30"} {
		if !s.Valid(v) {
			t.Errorf("Valid(%q) = false", v)
		}
	}
	for _, v := range []string{"", "v1", "1.", "1.2.3.4", "01", "1.02", "1.2-beta", "1.x", "*", "1.2.3-", "99999999999999999999", "1.2.3 "} {
		if s.Valid(v) {
			t.Errorf("Valid(%q) = true", v)
		}
	}
}

func TestCargoKind(t *testing.T) {
	s, _ := Get("cargo")
	for _, tc := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"1.2.3", "1.2.4", dep.Patch},
		{"1.2.3", "1.3.0", dep.Minor},
		{"1.2.3", "2.0.0", dep.Major},
		{"0.4.1", "0.4.2", dep.Patch},
		{"0.4.1", "0.5.0", dep.Major}, // Cargo: 0.4 and 0.5 are incompatible
		{"0.4", "0.5", dep.Major},
		{"0.0.3", "0.0.4", dep.Major},
		{"0.9.0", "1.0.0", dep.Major},
		{"1", "2", dep.Major},
		{"1.2", "1.4", dep.Minor},
	} {
		if got := s.Kind(tc.cur, tc.next); got != tc.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tc.cur, tc.next, got, tc.want)
		}
	}
}

func TestCargoRank(t *testing.T) {
	s, _ := Get("cargo")
	cands := []string{"0.4.9", "0.5.0", "0.6.1", "0.6.2-rc.1", "0.4", "0.5", "0.6", "0", "1.0.0", "1", "1.0", "bogus"}
	var rejected []string
	r := s.Rank("0.4.2", cands, nil, func(v, why string) { rejected = append(rejected, v+":"+why) })
	var got []Candidate
	for i := range r.Len() {
		got = append(got, r.At(i))
	}
	want := []Candidate{{"1.0.0", dep.Major, "1"}, {"0.6.1", dep.Major, "0.6"}, {"0.5.0", dep.Major, "0.5"}, {"0.4.9", dep.Patch, ""}}
	if !slices.Equal(got, want) {
		t.Errorf("Rank(0.4.2) = %v, want %v", got, want)
	}
	if want := []string{"0.6.2-rc.1:prerelease", "0.4:precision", "0.5:precision", "0.6:precision", "0:precision", "1:precision", "1.0:precision", "bogus:invalid"}; !slices.Equal(rejected, want) {
		t.Errorf("rejected = %v", rejected)
	}
	if best, _ := s.Best("0.4", cands, nil, nil); best != "1.0" {
		t.Errorf("Best(0.4) = %q", best)
	}
	if best, _ := s.Best("0.1.0-beta.1", []string{"0.1.0-beta.2", "0.1.0"}, nil, nil); best != "0.1.0" {
		t.Errorf("Best(prerelease) = %q", best)
	}
	if sv := s.Semver("0.4"); sv == nil || sv.String() != "0.4.0" {
		t.Errorf("Semver(0.4) = %v", sv)
	}
}
