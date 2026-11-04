package versioning

import (
	"maps"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Ordering vectors from Maven's ComparableVersionTest (maven-artifact 3.8.x):
// https://github.com/apache/maven/blob/maven-3.8.8/maven-artifact/src/test/java/org/apache/maven/artifact/versioning/ComparableVersionTest.java
func TestMavenOrder(t *testing.T) {
	s := get(t, "maven")
	chains := map[string][]string{
		"qualifier": {"1-alpha2snapshot", "1-alpha2", "1-alpha-123", "1-beta-2", "1-beta123", "1-m2", "1-m11", "1-rc", "1-cr2",
			"1-rc123", "1-SNAPSHOT", "1", "1-sp", "1-sp2", "1-sp123", "1-abc", "1-def", "1-pom-1", "1-1-snapshot", "1-1", "1-2", "1-123"},
		"number": {"2.0", "2-1", "2.0.a", "2.0.0.a", "2.0.2", "2.0.123", "2.1.0", "2.1-a", "2.1b", "2.1-c", "2.1-1", "2.1.0.1",
			"2.2", "2.123", "11.a2", "11.a11", "11.b2", "11.b11", "11.m2", "11.m11", "11", "11.a", "11b", "11c", "11m"},
		"basic":  {"1", "1.1", "1.1.1", "1.2", "1.10", "2"},
		"gradle": {"8.10.2", "8.11-milestone-1", "8.11-rc-1", "8.11-rc-2", "8.11", "8.11.1"},
		"spring": {"5.3.39", "6.2.0-M1", "6.2.0-RC1", "6.2.0-SNAPSHOT", "6.2.0"},
		"jetty":  {"9.4.50.v20221201", "9.4.51.v20230217", "9.4.52.v20230823"},
	}
	for name, c := range chains {
		for i := range c {
			for j := range c {
				want := 0
				switch {
				case i < j:
					want = -1
				case i > j:
					want = 1
				}
				if got := s.Compare(c[i], c[j]); got != want {
					t.Errorf("%s: Compare(%s, %s) = %d, want %d", name, c[i], c[j], got, want)
				}
			}
		}
	}
	equal := [][]string{
		{"1", "1.0", "1.0.0", "1-0", "1.0-final", "1-ga", "1.0.0.RELEASE", "1-release", "1.0.0-0.0.0"},
		{"1-cr1", "1-rc1", "1-RC-1", "1.0-cr-1"},
		{"1a1", "1-alpha-1", "1-ALPHA1", "1A1"},
		{"1b2", "1-beta-2"},
		{"1m3", "1-milestone-3"},
		{"1-SNAPSHOT", "1-snapshot", "1.0-SnapShot"},
		{"1x", "1-x"}, // but 1.x differs: [1, x] vs [1, [x]]
	}
	for _, e := range equal {
		for _, a := range e {
			for _, b := range e {
				if got := s.Compare(a, b); got != 0 {
					t.Errorf("Compare(%s, %s) = %d, want 0", a, b, got)
				}
			}
		}
	}
}

func TestMavenValid(t *testing.T) {
	s := get(t, "maven")
	for v, want := range map[string]bool{
		"1": true, "1.0": true, "33.3.1-jre": true, "9.4.51.v20230217": true, "1.0-SNAPSHOT": true, "2.0_1": true,
		"": false, "${jackson.version}": false, "[1.0,2.0)": false, "LATEST": false, "RELEASE": false, "Hoxton.SR3": false,
		"v1.0": false, "1.0+build": false, "1 .0": false,
	} {
		if got := s.Valid(v); got != want {
			t.Errorf("Valid(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestMavenParse(t *testing.T) {
	for _, tt := range []struct {
		v             string
		pre, snap     bool
		suffix, major string
		semver        string
	}{
		{"33.3.1-jre", false, false, "jre", "33", "33.3.1"},
		{"33.3.1-android", false, false, "android", "33", "33.3.1"},
		{"9.4.51.v20230217", false, false, "", "9", "9.4.51"},
		{"6.2250.va_d01d559a_209", false, false, "", "6", "6.2250.0"},
		{"2.0.0-beta-jre8", true, false, "jre", "2", "2.0.0-beta-jre8"},
		{"2.0.0-dev-1234", false, false, "dev", "2", "2.0.0"},
		{"6.2.0-M1", true, false, "", "6", "6.2.0-m1"},
		{"8.11-rc-1", true, false, "", "8", "8.11.0-rc-1"},
		{"1.0-SNAPSHOT", true, true, "", "1", "1.0.0-snapshot"},
		{"1.0.0.RELEASE", false, false, "", "1", "1.0.0"},
		{"2.1-sp1", false, false, "", "2", "2.1.0"},
		{"007", false, false, "", "7", "7.0.0"},
	} {
		v, ok := parseMaven(tt.v)
		if !ok {
			t.Fatalf("parseMaven(%s) failed", tt.v)
		}
		if v.pre != tt.pre || v.snapshot != tt.snap || v.suffix != tt.suffix || v.major() != tt.major || v.semver().String() != tt.semver {
			t.Errorf("parseMaven(%s) = pre %v snap %v suffix %q major %s semver %s", tt.v, v.pre, v.snapshot, v.suffix, v.major(), v.semver())
		}
	}
}

func TestMavenKind(t *testing.T) {
	s := get(t, "maven")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"2.17.0", "2.18.2", dep.Minor},
		{"2.17.0", "2.17.1", dep.Patch},
		{"5.3.39", "6.2.0", dep.Major},
		{"8.10.2", "8.11", dep.Minor},
		{"1.0", "1.0.0.1", dep.Patch},
		{"9.4.51.v20230217", "9.4.52.v20230823", dep.Patch},
		{"1", "1.1", dep.Minor},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %v, want %v", tt.cur, tt.next, got, tt.want)
		}
	}
}

func TestMavenRejectReasons(t *testing.T) {
	tests := []struct {
		cur   string
		cands []string
		want  map[string][]string
	}{
		{"32.1.3-jre", []string{"${v}", "33.0.0-android", "33.0.0-rc1-jre", "33.1.0-SNAPSHOT", "31.0-jre", "33.4.0-jre", "33.3.1-jre"}, map[string][]string{
			"invalid": {"${v}"}, "suffix": {"33.0.0-android"}, "prerelease": {"33.0.0-rc1-jre", "33.1.0-SNAPSHOT"},
			"not newer": {"31.0-jre"}, "policy: nope": {"33.4.0-jre"},
		}},
		// A prerelease may move to a newer prerelease, but never to a SNAPSHOT.
		{"6.2.0-M1", []string{"6.2.0-M2", "6.2.0-SNAPSHOT", "6.1.0"}, map[string][]string{
			"prerelease": {"6.2.0-SNAPSHOT"}, "not newer": {"6.1.0"}, "policy: nope": {"6.2.0-M2"},
		}},
	}
	accept := func(v string) bool { return v == "33.3.1-jre" }
	for _, tt := range tests {
		got := map[string][]string{}
		get(t, "maven").Best(tt.cur, tt.cands, acceptFn(accept), func(v, reason string) { got[reason] = append(got[reason], v) })
		if !maps.EqualFunc(got, tt.want, slices.Equal) {
			t.Errorf("Best(%s) rejects = %v, want %v", tt.cur, got, tt.want)
		}
	}
}

func TestMavenBest(t *testing.T) {
	runBest(t, "maven", []bestCase{
		{cur: "2.17.0", want: "2.18.2", cands: []string{"2.16.1", "2.17.0", "2.17.2", "2.18.0", "2.18.2", "2.19.0-rc1"}},
		{cur: "32.1.3-jre", want: "33.3.1-jre", cands: []string{"32.1.3-android", "33.3.1-android", "33.3.1-jre", "33.2.0-jre", "33.4.0-rc1-jre"}},
		{cur: "5.3.39", want: "6.1.14", cands: []string{"6.2.0-M1", "6.1.14", "6.2.0-SNAPSHOT"}},
		{cur: "5.3.39", want: "", cands: []string{"6.2.0-M1", "5.3.38"}},
		{cur: "6.2.0-M1", want: "6.2.0", cands: []string{"6.2.0-RC1", "6.2.0", "6.2.1-SNAPSHOT"}},
		{cur: "8.10.2", want: "8.11", cands: []string{"8.11-rc-1", "8.11", "8.10"}},
		// Jetty dates and Jenkins incrementals hashes follow a "." and are not suffixes.
		{cur: "9.4.51.v20230217", want: "9.4.56.v20240826", cands: []string{"9.4.56.v20240826", "9.4.50.v20221201"}},
		{cur: "9.4.51.v20230217", want: "12.0.15", cands: []string{"9.4.56.v20240826", "10.0.24", "12.0.15"}},
		{cur: "5983.v443959746f1f", want: "6012.v4ef1f7cc0d1c", cands: []string{"5983.v443959746f1f", "6012.v4ef1f7cc0d1c", "5990.vd6d1e0b_a_b_b"}},
		{cur: "6.2236.v12dd4c483242", want: "6.2250.va_d01d559a_209", cands: []string{"6.2250.va_d01d559a_209", "6.2240.v1a2b"}},
		// An unknown qualifier after "-" ranks above the release, so it must match.
		{cur: "2.0.0", want: "2.0.1", cands: []string{"2.0.1", "2.0.1-dev-1234"}},
		{cur: "1.0", want: "1.0.1", cands: []string{"1.0.0", "1.0-final", "1.0.1"}},
		{cur: "5.3.39", want: "", cands: nil},
	})
}

func TestMavenRank(t *testing.T) {
	r := get(t, "maven").Rank("5.3.30", []string{"6.2.0-M1", "5.3.39", "6.1.14", "5.3.31", "6.2.0", "5.3.39-SNAPSHOT"}, nil, nil)
	var got []Candidate
	for i := range r.Len() {
		got = append(got, r.At(i))
	}
	want := []Candidate{
		{Version: "6.2.0", Kind: dep.Major, Major: "6"},
		{Version: "6.1.14", Kind: dep.Major, Major: "6"},
		{Version: "5.3.39", Kind: dep.Patch},
		{Version: "5.3.31", Kind: dep.Patch},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Rank = %v, want %v", got, want)
	}
	if !get(t, "maven").Compatible("8.10.2", "8.11") || get(t, "maven").Compatible("8.10.2", "8.11-rc-1") {
		t.Error("Compatible")
	}
}
