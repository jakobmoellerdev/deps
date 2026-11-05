package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below follow Bazel's bzlmod Version.java (and Renovate's bazel-module
// versioning, which mirrors it).

func TestParseBazel(t *testing.T) {
	for s, ok := range map[string]bool{
		"1": true, "1.2.3": true, "1.2.3.bcr.1": true, "20250814.1": true, "2025-11-05.bcr.1": true,
		"1.5.6-9.bcr.2": true, "0.0.0-20250604-de157ca3": true, "1.0.0-rc.1+build.5": true, "v1.2": true,
		"abc": true, "1.0+x-y": true,
		"": false, "1.": false, ".1": false, "1..2": false, "1.0-": false, "1.0-a.": false, "1.0+": false,
		"1_0": false, "1.0\t": false, "1.0+a+b": false, "1.0-a..b": false,
	} {
		if _, got := parseBazel(s); got != ok {
			t.Errorf("parseBazel(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareBazel(t *testing.T) {
	s := get(t, "bazel-module")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3.bcr.1", -1}, {"1.2.3.bcr.1", "1.2.3.bcr.2", -1}, {"1.2.3.bcr.2", "1.2.4", -1},
		{"1.2.3.1", "1.2.3.bcr.1", -1}, // digit-only identifiers come first
		{"1.10", "1.9", 1}, {"1.0", "1.0.0", -1}, {"01", "1", -1}, {"2", "10", -1},
		{"1.0.0-rc1", "1.0.0", -1}, {"1.0.0-alpha", "1.0.0-beta", -1}, {"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0+a", "1.0.0+b", 0}, {"2025-11-05.bcr.1", "2025-11-13", -1}, {"2025-11-05", "2025", -1},
		{"abc", "1", 1}, {"99999999999999999999999", "100000000000000000000000", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestBazelBest(t *testing.T) {
	names := []string{"1.7.1", "1.8.0", "1.8.1", "1.8.2", "1.8.2.bcr.1", "1.9.0-rc1", "1.9.0", "2.0.0-rc.1"}
	runBest(t, "bazel-module", []bestCase{
		{"1.8.1", "1.9.0", names, nil},
		{"1.8.1", "1.8.2.bcr.1", names, func(v string) bool { return v < "1.9" }},
		{"1.9.0-rc0", "2.0.0-rc.1", names, nil}, // from a prerelease
		{"1.9.0", "", names, nil},
		{"bad version", "", names, nil},
		{"1.0", "", []string{"junk/1", ""}, nil},
	})
}

func TestBazelKindAndSemver(t *testing.T) {
	s := get(t, "bazel-module")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"1.8.2", "1.8.2.bcr.1", dep.Patch}, {"1.8.2", "1.9.0", dep.Minor}, {"1.8.2", "2.0.0", dep.Major},
		{"20250512.1", "20250814.0", dep.Major}, {"2025-11-05", "2025-11-13", dep.Patch}, {"0.0.5", "0.1", dep.Minor},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	for v, want := range map[string]string{"1.2.3.bcr.1": "1.2.3", "1.5.6-9.bcr.2": "1.5.6-9.bcr.2", "abc": "0.0.0", "7": "7.0.0"} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
	if s.Semver("") != nil {
		t.Error("Semver of the empty version")
	}
	r := s.Rank("1.8.1", []string{"2.0.0", "1.9.0"}, nil, nil)
	if r.Len() != 2 || r.At(0).Major != "2" || r.At(1).Major != "" {
		t.Errorf("Rank majors = %+v %+v", r.At(0), r.At(1))
	}
}
