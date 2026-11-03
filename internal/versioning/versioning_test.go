package versioning

import (
	"maps"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestParseTag(t *testing.T) {
	for s, ok := range map[string]bool{"v4": true, "4.1": true, "v7.0.1": true, "v1.2.3.4": false, "v1-beta": false, "v1.0.0-rc1": false, "main": false, "": false, "v99999999999999999999": false} {
		if _, got := ParseTag(s); got != ok {
			t.Errorf("ParseTag(%q) = %v", s, got)
		}
	}
}

func TestCompareTag(t *testing.T) {
	p := func(s string) Tag { t, _ := ParseTag(s); return t }
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1", "v2", -1}, {"v2.0", "v1.9", 1}, {"v1", "v1.0", -1}, {"v1.0.0", "v1.0", 1}, {"v1.2", "1.2", 0},
	} {
		if got := CompareTag(p(tc.a), p(tc.b)); got != tc.want {
			t.Errorf("CompareTag(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func get(t *testing.T, name string) Scheme {
	t.Helper()
	s, ok := Get(name)
	if !ok {
		t.Fatalf("scheme %q missing", name)
	}
	return s
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("calver"); ok {
		t.Error("Get(calver) ok")
	}
}

// acceptFn adapts a bool predicate to the Scheme accept signature.
func acceptFn(f func(string) bool) func(string, dep.UpdateType) (bool, string) {
	if f == nil {
		return nil
	}
	return func(v string, _ dep.UpdateType) (bool, string) { return f(v), "nope" }
}

type bestCase struct {
	cur, want string
	cands     []string
	accept    func(string) bool
}

func runBest(t *testing.T, scheme string, cases []bestCase) {
	t.Helper()
	s := get(t, scheme)
	for _, tc := range cases {
		got, ok := s.Best(tc.cur, tc.cands, acceptFn(tc.accept), nil)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s Best(%s) = %q, want %q", scheme, tc.cur, got, tc.want)
		}
	}
}

func TestTagBest(t *testing.T) {
	names := []string{"v4", "v5", "v6", "v7", "v6.0.0", "v6.1.0", "v7.0.1", "v7.0.2", "7.1.0", "v8.0.0-beta", "v7.0.1.1"}
	runBest(t, "tag", []bestCase{
		{"v4", "v7", names, nil},
		{"v7.0.1", "v7.0.2", names, nil},
		{"v6.0.0", "v7.0.2", names, nil},
		{"v6.0.0", "v6.1.0", names, func(v string) bool { return v < "v7" }},
		{"7.0.0", "7.1.0", names, nil},
		{"v7", "", names, nil},
		{"v1.1", "v1.4", []string{"v1.3", "v1.2", "v1.4", "v1.0"}, nil}, // highest wins regardless of order
		{"main", "", names, nil},
	})
}

func TestBestRejectReasons(t *testing.T) {
	tests := []struct {
		scheme, cur string
		cands       []string
		want        map[string][]string
	}{
		{"tag", "v6.0.0", []string{"main", "6.1.0", "v6.1", "v5.0.0", "v6.0.0", "v7.0.0", "v6.1.0"}, map[string][]string{
			"invalid": {"main"}, "prefix": {"6.1.0"}, "precision": {"v6.1"}, "not newer": {"v5.0.0", "v6.0.0"}, "policy: nope": {"v7.0.0"},
		}},
		{"go", "v1.0.0", []string{"bad", "v1.1.0-0.20240101000000-abcdefabcdef", "v1.2.0-rc.1", "v2.0.0+incompatible", "v2.0.0", "v1.0.0", "v1.3.0"}, map[string][]string{
			"invalid": {"bad", "v1.1.0-0.20240101000000-abcdefabcdef"}, "prerelease": {"v1.2.0-rc.1"}, "suffix": {"v2.0.0+incompatible"},
			"major": {"v2.0.0"}, "not newer": {"v1.0.0"},
		}},
		{"docker", "1.25-alpine", []string{"latest", "v1.26-alpine", "1.26.1-alpine", "1.26", "1.24-alpine", "1.27-alpine"}, map[string][]string{
			"invalid": {"latest"}, "prefix": {"v1.26-alpine"}, "precision": {"1.26.1-alpine"}, "suffix": {"1.26"}, "not newer": {"1.24-alpine"}, "policy: nope": {"1.27-alpine"},
		}},
		{"semver", "1.2.3", []string{"x", "1.3.0-rc.1", "1.2.0", "1.4.0"}, map[string][]string{
			"invalid": {"x"}, "prerelease": {"1.3.0-rc.1"}, "not newer": {"1.2.0"}, "policy: nope": {"1.4.0"},
		}},
	}
	for _, tt := range tests {
		got := map[string][]string{}
		accept := func(v string) bool { return v == "v1.3.0" || v == "v6.1.0" }
		get(t, tt.scheme).Best(tt.cur, tt.cands, acceptFn(accept), func(v, reason string) { got[reason] = append(got[reason], v) })
		if !maps.EqualFunc(got, tt.want, slices.Equal) {
			t.Errorf("%s rejects = %v, want %v", tt.scheme, got, tt.want)
		}
	}
}

func TestGoBest(t *testing.T) {
	vs := []string{"v1.9.1", "v1.10.0", "v1.10.2", "v1.11.0-rc.1", "v2.0.0", "v2.0.0+incompatible", "v1.11.1-0.20240101000000-abcdefabcdef", "bad"}
	runBest(t, "go", []bestCase{
		{"v1.9.1", "v1.10.2", vs, nil},
		{"v1.9.1", "v1.10.0", vs, func(v string) bool { return v != "v1.10.2" }},
		{"v1.11.0-rc.0", "v1.11.0-rc.1", vs, nil},
		{"v2.0.0+incompatible", "v2.1.0+incompatible", []string{"v2.1.0+incompatible", "v3.0.0+incompatible"}, nil},
		{"v1.9.1", "", []string{"v2.0.0+incompatible"}, nil},
		{"v0.5.0", "v1.0.0", []string{"v0.4.0", "v1.0.0", "v2.0.0"}, nil},
		{"v1.10.2", "", vs, func(string) bool { return false }},
	})
}

func TestDockerBest(t *testing.T) {
	runBest(t, "docker", []bestCase{
		{"1.25-alpine", "1.27-alpine", []string{"1.27-alpine", "1.26-alpine", "1.28", "1.28-bookworm"}, nil},
		{"3.19", "3.20", []string{"3.20", "3.20.1", "latest"}, nil},
		{"v2.1.0", "v2.2.0", []string{"v2.2.0", "2.3.0"}, nil},
		{"latest", "", []string{"1.0"}, nil},
	})
}

func TestSemverBest(t *testing.T) {
	runBest(t, "semver", []bestCase{
		{"1.2.3", "1.4.0", []string{"v1.3.0", "v1.4.0", "1.4.0-rc.1"}, nil},
		{"v1.2.3", "v2.0", []string{"1.3.0", "2.0"}, nil},
		{"1.0.0-rc.1", "1.0.0-rc.2", []string{"1.0.0-rc.2"}, nil},
		{"x", "", []string{"1.0.0"}, nil},
	})
}

func TestSchemeMethods(t *testing.T) {
	tests := []struct {
		scheme        string
		valid         []string
		invalid       []string
		cmp           [3]string // a < b; c == a
		kinds         map[[2]string]dep.UpdateType
		semverIn, out string
		compat        [][2]string
		incompat      [][2]string
	}{
		{
			scheme: "tag", valid: []string{"v4", "1.2.3"}, invalid: []string{"main", "v1.2.3.4"},
			cmp:      [3]string{"v1.2", "v1.10", "v1.2"},
			kinds:    map[[2]string]dep.UpdateType{{"v4", "v5"}: dep.Major, {"v4.1", "v4.2"}: dep.Minor, {"v4.1.1", "v4.1.2"}: dep.Patch},
			semverIn: "v4", out: "4.0.0",
			compat:   [][2]string{{"v4", "v5"}},
			incompat: [][2]string{{"v4", "v4"}, {"v4", "v4.1"}, {"v4", "5"}, {"main", "v5"}},
		},
		{
			scheme: "go", valid: []string{"v1.2.3", "v2.0.0+incompatible"}, invalid: []string{"1.2.3", "v1.1.1-0.20240101000000-abcdefabcdef"},
			cmp:      [3]string{"v1.9.0", "v1.10.0", "v1.9.0"},
			kinds:    map[[2]string]dep.UpdateType{{"v0.5.0", "v1.0.0"}: dep.Major, {"v1.9.1", "v1.10.0"}: dep.Minor, {"v1.10.0", "v1.10.2"}: dep.Patch},
			semverIn: "v1.2.3", out: "1.2.3",
			compat:   [][2]string{{"v1.0.0", "v1.1.0"}, {"v0.1.0", "v1.0.0"}},
			incompat: [][2]string{{"v1.0.0", "v2.0.0"}, {"v1.0.0", "v1.1.0-rc.1"}},
		},
		{
			scheme: "docker", valid: []string{"1.25-alpine", "v2", "20.04"}, invalid: []string{"latest", "alpine3.19", "99999999999999999999"},
			cmp:      [3]string{"1.9-alpine", "1.10-alpine", "1.9-alpine"},
			kinds:    map[[2]string]dep.UpdateType{{"1", "2"}: dep.Major, {"1.1-a", "1.2-a"}: dep.Minor, {"1.1.1.1", "1.1.1.2"}: dep.Patch},
			semverIn: "1.25-alpine", out: "1.25.0",
			compat:   [][2]string{{"1.25-alpine", "1.26-alpine"}},
			incompat: [][2]string{{"1.25-alpine", "1.26"}, {"1.25-alpine", "1.24-alpine"}},
		},
		{
			scheme: "semver", valid: []string{"1", "v1.2", "1.2.3-rc.1"}, invalid: []string{"x", "1.2.3.4"},
			cmp:      [3]string{"1.2.0", "v1.10", "v1.2"},
			kinds:    map[[2]string]dep.UpdateType{{"1.0.0", "2.0.0"}: dep.Major, {"1.0.0", "1.1.0"}: dep.Minor, {"1.0.0", "1.0.1"}: dep.Patch},
			semverIn: "v1.2", out: "1.2.0",
			compat:   [][2]string{{"1.0.0", "3.0.0"}},
			incompat: [][2]string{{"1.0.0", "1.1.0-rc.1"}, {"1.0.0", "0.9.0"}},
		},
	}
	for _, tt := range tests {
		s := get(t, tt.scheme)
		for _, v := range tt.valid {
			if !s.Valid(v) {
				t.Errorf("%s: Valid(%q) = false", tt.scheme, v)
			}
		}
		for _, v := range tt.invalid {
			if s.Valid(v) || s.Semver(v) != nil {
				t.Errorf("%s: Valid(%q) = true or Semver non-nil", tt.scheme, v)
			}
		}
		if s.Compare(tt.cmp[0], tt.cmp[1]) >= 0 || s.Compare(tt.cmp[1], tt.cmp[0]) <= 0 || s.Compare(tt.cmp[2], tt.cmp[0]) != 0 {
			t.Errorf("%s: Compare order wrong for %v", tt.scheme, tt.cmp)
		}
		for p, k := range tt.kinds {
			if got := s.Kind(p[0], p[1]); got != k {
				t.Errorf("%s: Kind(%s, %s) = %s, want %s", tt.scheme, p[0], p[1], got, k)
			}
		}
		if got := s.Kind(tt.invalid[0], tt.valid[0]); got != dep.Major {
			t.Errorf("%s: Kind of invalid = %s", tt.scheme, got)
		}
		if got := s.Compare(tt.invalid[0], tt.valid[0]); got == 0 {
			t.Errorf("%s: Compare of invalid = 0", tt.scheme)
		}
		if sv := s.Semver(tt.semverIn); sv == nil || sv.String() != tt.out {
			t.Errorf("%s: Semver(%s) = %v, want %s", tt.scheme, tt.semverIn, sv, tt.out)
		}
		for _, p := range tt.compat {
			if !s.Compatible(p[0], p[1]) {
				t.Errorf("%s: Compatible(%s, %s) = false", tt.scheme, p[0], p[1])
			}
		}
		for _, p := range tt.incompat {
			if s.Compatible(p[0], p[1]) {
				t.Errorf("%s: Compatible(%s, %s) = true", tt.scheme, p[0], p[1])
			}
		}
	}
	if GoSemver("x") != nil {
		t.Error("GoSemver(x) non-nil")
	}
}
