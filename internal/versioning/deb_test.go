package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below follow deb-version(7) and `dpkg --compare-versions`.

func TestParseDeb(t *testing.T) {
	for s, ok := range map[string]bool{
		"8.14.1-2": true, "1:2.36.1-8+deb11u1": true, "3.0.15-1~deb12u1": true, "2.39.3-0ubuntu0.24.04.2": true,
		"1.0~rc1": true, "20230311": true, "0:1.0": true, "1:1.0:2-3": true, "1.0-2-3": true, "7.88.1-10+deb12u8": true,
		"": false, "a1.0": false, "1.0-": false, ":1.0": false, "x:1.0": false, "1.0:2": false, "1.0_1": false,
		"1.0-a_b": false, "1.0-1:2": false, "-1": false, "1.0=1": false,
	} {
		if _, got := parseDeb(s); got != ok {
			t.Errorf("parseDeb(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareDeb(t *testing.T) {
	s := get(t, "deb")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "1.0-0", 0}, {"0:1.0", "1.0", 0}, {"1.0-1", "1.0-01", 0},
		{"1.0~rc1", "1.0", -1}, {"1.0~~", "1.0~", -1}, {"1.0~", "1.0", -1}, {"1.0", "1.0a", -1},
		{"1.0a", "1.0+", -1}, {"1.0+", "1.0.", -1}, {"1.0.", "1.0-1", 1},
		{"1:1.0", "2.0", 1}, {"2:0.1", "1:9.9", 1}, {"1.10", "1.9", 1}, {"1.002", "1.2", 0},
		{"3.0.15-1~deb12u1", "3.0.15-1", -1}, {"3.0.15-1~deb12u1", "3.0.14-1", 1},
		{"7.88.1-10+deb12u8", "7.88.1-10+deb12u12", -1}, {"2.36.1-8+deb11u1", "2.36.1-8", 1},
		{"2.39.3-0ubuntu0.24.04.2", "2.39.3-0ubuntu0.24.04.10", -1}, {"1.2.3-1ubuntu1", "1.2.3-1", 1},
		{"99999999999999999999999", "100000000000000000000000", -1}, {"1.0a", "1.0b", -1}, {"1.0A", "1.0a", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestDebBest(t *testing.T) {
	names := []string{"8.14.1-1", "8.14.1-2", "8.15.0-1~exp1", "8.15.0-1", "1:7.0-1", "garbage"}
	runBest(t, "deb", []bestCase{
		{"8.14.1-2", "1:7.0-1", names, nil},
		{"8.14.1-1", "8.15.0-1", names, func(v string) bool { return v[0] != '1' }},
		{"8.14.1-1", "8.14.1-2", []string{"8.14.1-2", "8.14.1-1"}, nil},
		{"1:7.0-1", "", names, nil},
		{"bad version", "", names, nil},
	})
}

func TestDebKindAndSemver(t *testing.T) {
	s := get(t, "deb")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"8.14.1-1", "8.14.1-2", dep.Patch}, {"8.14.1-2", "8.15.0-1", dep.Minor}, {"8.14.1-2", "9.0-1", dep.Major},
		{"8.14.1-2", "1:8.14.1-2", dep.Major}, {"3.0.15-1~deb12u1", "3.0.16-1~deb12u1", dep.Patch},
		{"2.36.1-8", "2.36.1-8+deb11u1", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	for v, want := range map[string]string{"1:2.36.1-8+deb11u1": "2.36.1", "20230311": "20230311.0.0", "1.0-3": "1.0.3"} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
	if s.Semver("") != nil {
		t.Error("Semver of the empty version")
	}
	r := s.Rank("8.14.1-1", []string{"9.0-1", "8.14.1-2"}, nil, nil)
	if r.Len() != 2 || r.At(0).Major != "9" || r.At(1).Major != "" {
		t.Errorf("Rank majors = %+v %+v", r.At(0), r.At(1))
	}
	if !s.Compatible("1.0-1", "1.0-2") || s.Compatible("1.0-2", "1.0-1") || s.Compatible("bad", "1.0") {
		t.Error("Compatible")
	}
}
