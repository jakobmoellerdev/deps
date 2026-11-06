package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestParseRPM(t *testing.T) {
	for s, ok := range map[string]bool{
		"1.0": true, "1.0.0-1.azl3": true, "2:1.8.7-1.fc43": true, "0:1.2-3.el9_4": true, "1.0~rc1-0.1.fc43": true,
		"1.0^20240101git1a2b3c-1": true, "1.2.3+git.abc": true, "1_0": true,
		"": false, "latest": false, "v1.0": false, "1.0-": false, "-1": false, "a:1.0": false, ":1.0": false,
		"3.12.0-1-2": false, // RPM versions and releases have no "-"
		"1.0-1 x":    false, "1.0/2": false, "1.0-a/b": false, "99999999999999999999999:1": false,
	} {
		if _, got := parseRPM(s); got != ok {
			t.Errorf("parseRPM(%q) = %v, want %v", s, got, ok)
		}
	}
	v, _ := parseRPM("2:1.8.7-1.fc43")
	if v.epoch != 2 || v.version != "1.8.7" || v.release != "1.fc43" || v.pre {
		t.Errorf("parseRPM = %+v", v)
	}
	if v, _ := parseRPM("1.0~rc1-0.1.fc43"); !v.pre || v.release != "0.1.fc43" {
		t.Errorf("prerelease: %+v", v)
	}
}

// The rpmvercmp expectations are RPM's own test suite (tests/rpmvercmp.at).
func TestRPMVerCmp(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "2.0", -1}, {"2.0.1", "2.0", 1}, {"2.0.1a", "2.0.1", 1},
		{"5.5p1", "5.5p2", -1}, {"5.5p10", "5.5p1", 1}, {"10xyz", "10.1xyz", -1}, {"xyz10", "xyz10.1", -1},
		{"xyz.4", "xyz.4", 0}, {"xyz.4", "8", -1}, {"xyz.4", "2", -1}, {"5.5p2", "5.6p1", -1},
		{"5.6p1", "6.5p1", -1}, {"6.0.rc1", "6.0", 1}, {"10b2", "10a1", 1}, {"1.0aa", "1.0a", 1},
		{"10.0001", "10.1", 0}, {"10.0001", "10.0039", -1}, {"4.999.9", "5.0", -1}, {"20101121", "20101122", -1},
		{"2_0", "2.0", 0}, {"2.0", "2_0", 0}, {"a", "a", 0}, {"a+", "a_", 0}, {"+", "_", 0},
		{"1.0~rc1", "1.0~rc1", 0}, {"1.0~rc1", "1.0", -1}, {"1.0~rc1", "1.0~rc2", -1}, {"1.0~rc1~git123", "1.0~rc1", -1},
		{"1.0^", "1.0^", 0}, {"1.0^", "1.0", 1}, {"1.0^git1", "1.0^git2", -1}, {"1.0^git1", "1.01", -1},
		{"1.0^20160101", "1.0.1", -1}, {"1.0^20160101^git1", "1.0^20160101", 1}, {"1.0~rc1^git1", "1.0~rc1", 1},
		{"1.0^git1~pre", "1.0^git1", -1}, {"1.0", "", 1},
	} {
		if got := rpmvercmp(tt.a, tt.b); got != tt.want {
			t.Errorf("rpmvercmp(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := rpmvercmp(tt.b, tt.a); got != -tt.want {
			t.Errorf("rpmvercmp(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestCompareRPM(t *testing.T) {
	s := get(t, "rpm")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0.0-1.azl3", "1.0.0-2.azl3", -1}, {"1.0.0-10.azl3", "1.0.0-9.azl3", 1}, {"1.0.1-1.azl3", "1.0.0-9.azl3", 1},
		{"1:1.0-1", "2.0-1", 1}, {"0:1.0-1", "1.0-1", 0}, {"1.0", "1.0-1", -1}, {"1.0~rc1-1", "1.0-1", -1},
		{"3.12.0-1.fc43", "3.12.0-1.fc43^20231110", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestRPMBest(t *testing.T) {
	names := []string{"1.0.0-1.azl3", "1.0.0-2.azl3", "1.1.0-1.azl3", "1.2.0~rc1-1.azl3", "2.0.0-1.azl3", "junk"}
	runBest(t, "rpm", []bestCase{
		{"1.0.0-1.azl3", "2.0.0-1.azl3", names, nil},
		{"1.0.0-1.azl3", "1.0.0-2.azl3", names, func(v string) bool { return v < "1.1" }},
		{"1.2.0~rc0-1.azl3", "2.0.0-1.azl3", names, nil},
		{"1.1.0-1.azl3", "2.0.0-1.azl3", names, func(v string) bool { return v != "1.2.0~rc1-1.azl3" }},
		{"2.0.0-1.azl3", "", names, nil},
		{"latest", "", names, nil},
	})
	if got, ok := get(t, "rpm").Best("1.1.0-1.azl3", []string{"1.2.0~rc1-1.azl3"}, nil, nil); ok {
		t.Errorf("prerelease from a release: %s", got)
	}
}

func TestRPMKindAndSemver(t *testing.T) {
	s := get(t, "rpm")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"1.0.0-1.azl3", "1.0.0-2.azl3", dep.Patch}, {"1.0.0-1", "1.0.1-1", dep.Patch}, {"1.0.0-1", "1.1.0-1", dep.Minor},
		{"1.0.0-1", "2.0.0-1", dep.Major}, {"1.0-1", "1:1.0-1", dep.Major}, {"5.5p1", "5.5p2", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	for v, want := range map[string]string{
		"1.2.3-4.fc43": "1.2.3", "2:1.8-1": "1.8.0", "1.0~rc1-1": "1.0.0-pre", "7": "7.0.0",
		"99999999999999999999.1": "18446744073709551615.1.0",
	} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
	if s.Semver("x") != nil {
		t.Error("Semver of an invalid version")
	}
	r := s.Rank("1.8-1", []string{"1:1.8-2", "2.0-1", "1.9-1"}, nil, nil)
	if r.Len() != 3 || r.At(0).Major != "1:1" || r.At(1).Major != "2" || r.At(2).Major != "" {
		t.Errorf("Rank majors = %+v %+v %+v", r.At(0), r.At(1), r.At(2))
	}
}
