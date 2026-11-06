package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below follow apk-tools' version.c (`apk version -t a b`) and Renovate's
// apk versioning; the versions are real Alpine and Wolfi package versions.

func TestParseApk(t *testing.T) {
	for s, ok := range map[string]bool{
		"3.2.1-r0": true, "2.39.0_rc1-r0": true, "6.5_p20250503-r0": true, "1.1.1w-r1": true, "8.12.1": true,
		"1.36.1-r29": true, "0.2.5_git20240101~a1b2c3d-r0": true, "1_alpha": true, "1.2_pre_p2": true, "20250127": true,
		"": false, "v1.2": false, "1..2": false, "1.2-": false, "1.2-r": false, "1.2-rc1": false, "1.2_": false,
		"1.2_foo": false, "1.2~": false, "1.2~XYZ": false, "a": false, "1.2ab": false, "1.2-r+1": false,
		"99999999999999999999999": false, "_rc1": false,
	} {
		if _, got := parseApk(s); got != ok {
			t.Errorf("parseApk(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareApk(t *testing.T) {
	s := get(t, "apk")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.36.1-r29", "1.36.1-r30", -1}, {"1.36.1-r30", "1.37.0-r0", -1}, {"1.2", "1.2.1", -1},
		{"1.10", "1.9", 1}, {"2.39.0_rc1-r0", "2.39.0-r0", -1}, {"2.39.0_alpha1", "2.39.0_beta1", -1},
		{"2.39.0_rc1", "2.39.0_rc2", -1}, {"6.5-r0", "6.5_p20250503-r0", -1}, {"6.5_p1", "6.5_p2", -1},
		{"6.5_p1", "6.5.1", -1}, {"1.1.1v-r0", "1.1.1w-r0", -1}, {"1.1.1-r9", "1.1.1a-r0", -1},
		{"1.0_git1", "1.0_p1", -1}, {"1.0~abc-r1", "1.0~def-r1", 0}, {"1.0", "1.0-r0", 0},
		{"3.2.1-r0", "3.2.1-r0", 0},
		// A component after the first that starts with 0 sorts below plain numbers, then by its
		// leading zeros (more first), then by value; checked against apk-tools 2.14.4 `apk version -t`.
		{"4.5.14", "4.09-r1", 1}, {"1.02", "1.1", -1}, {"1.05", "1.5", -1}, {"1.005", "1.05", -1},
		{"1.00", "1.0", -1}, {"1.05", "1.0.5", 1}, {"1.05a", "1.5", -1}, {"1.010", "1.01", 1},
		{"1.02", "1.03-r1", -1}, {"1.2.12.04", "1.2.12.05", -1}, {"1.07.5", "1.07", 1}, {"1.06-r6", "006", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestApkBest(t *testing.T) {
	names := []string{"5.2.21-r0", "5.2.26-r0", "5.2.37-r0", "5.2.37-r2", "5.3_rc1-r0", "5.3-r0", "5.3", "6.0_alpha1-r0"}
	runBest(t, "apk", []bestCase{
		{"5.2.37-r0", "5.3-r0", names, nil},
		{"5.2.37-r0", "5.2.37-r2", names, func(v string) bool { return v < "5.3" }},
		{"5.3", "", names, nil},                     // no newer version without a revision
		{"5.3_rc0-r0", "6.0_alpha1-r0", names, nil}, // from a pre-release
		{"bad", "", names, nil},
	})
}

func TestApkKindAndSemver(t *testing.T) {
	s := get(t, "apk")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"5.2.37-r0", "6.0-r0", dep.Major}, {"5.2.37-r0", "5.3-r0", dep.Minor}, {"5.2.37-r0", "5.2.37-r2", dep.Patch},
		{"1.1.1v-r0", "1.1.1w-r0", dep.Patch}, {"6.5_p1", "6.5_p2", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	for v, want := range map[string]string{"5.2.37-r2": "5.2.37", "2.39.0_rc1-r0": "2.39.0-pre", "7": "7.0.0"} {
		if got := s.Semver(v).String(); got != want {
			t.Errorf("Semver(%s) = %s, want %s", v, got, want)
		}
	}
	if s.Semver("bad") != nil {
		t.Error("Semver(bad) != nil")
	}
	if got := (apkVersion{nums: []apkPart{{n: 3}}}).major(); got != "3" {
		t.Errorf("major = %s", got)
	}
}
