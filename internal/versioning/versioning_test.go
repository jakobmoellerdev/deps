package versioning

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	msemver "github.com/Masterminds/semver/v3"

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
			// commit hashes (7-40 lowercase hex with a letter) are not versions; all-digit ones,
			// uppercase hex and longer or shorter runs are.
			scheme: "docker", valid: []string{"1.25-alpine", "v2", "20.04", "20240101", "1234567890", "3a22eb", "3A22EBA", "3a22eba17f23b18faa27436ab2f9c3ea977b550b0", "3a22eba-alpine"},
			invalid:  []string{"latest", "alpine3.19", "99999999999999999999", "3a22eba", "3a22eba17f23b18faa27436ab2f9c3ea977b550b", "0000000f"},
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
}

const cosignRegex = `regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$`

func TestLooseBest(t *testing.T) {
	gl := []string{"1877.25", "1877.28", "1877.3", "1592.14", "1877.28.1", "amd64-1877.29", "1878.0-rc1", "nightly", "1877"}
	runBest(t, "loose", []bestCase{
		{"1877.25", "1877.28.1", gl, nil}, // 1878.0-rc1 is a prerelease, 1877 reads as 1877.0
		{"1877.25", "1877.28", gl, func(v string) bool { return strings.Count(v, ".") == 1 }},
		{"1878.0-rc0", "1878.0-rc1", gl, nil},
		{"v1.2.3.4.5.6", "v1.2.3.4.5.7", []string{"v1.2.3.4.5.7", "1.2.3.4.5.8", "v1.2.3.4.5.6.7"}, nil},
		{"2.0_beta.2", "2.0_beta.10", []string{"2.0_beta.10", "2.0_beta.9"}, nil},
		{"x", "", gl, nil},
	})
}

func TestRegexBest(t *testing.T) {
	cands := []string{"v3.1.4+ocm.6", "v3.1.5+ocm.1", "v3.1.4+ocm.7", "v3.1.5-ocm.2", "v3.1.5", "v3.2.0+ocm.1-rc", "v4.0.0+ocm.1", "v3.0.9+ocm.9"}
	runBest(t, cosignRegex, []bestCase{
		{"v3.1.4+ocm.6", "v4.0.0+ocm.1", cands, nil},
		{"v3.1.4+ocm.6", "v3.1.5-ocm.2", cands, func(v string) bool { return !strings.HasPrefix(v, "v4") }},
		{"v3.1.4-ocm.6", "v3.1.4+ocm.7", []string{"v3.1.4+ocm.7", "v3.1.4-ocm.5"}, nil},
		{"v3.1.4", "", cands, nil},
	})
	compat := `regex:^(?<major>\d+)\.(?<minor>\d+)(?:\.(?<patch>\d+))?(?:-(?<prerelease>rc\d+))?-(?<compatibility>alpine|bookworm)$`
	runBest(t, compat, []bestCase{
		{"1.2-alpine", "1.4-alpine", []string{"1.3-alpine", "1.4-alpine", "1.9-bookworm", "1.5-rc1-alpine", "1.3.1-alpine"}, nil},
		{"1.5-rc1-alpine", "1.5-alpine", []string{"1.5-rc2-bookworm", "1.5-alpine", "1.5-rc2-alpine"}, nil},
		{"1.5-rc1-alpine", "1.5-rc2-alpine", []string{"1.5-rc2-alpine", "1.5-rc0-alpine"}, nil},
	})
	rev := `regex:^(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\.(?<build>\d+)(?:\.(?<revision>\d+))?$`
	runBest(t, rev, []bestCase{
		{"1.0.0.5", "1.0.0.5.2", []string{"1.0.0.5.2", "1.0.0.5.1", "1.0.0.4.9"}, nil},
		{"1.0.0.5", "1.0.0.10", []string{"1.0.0.9", "1.0.0.10", "1.0.0.99999999999999999999"}, nil},
	})
}

func TestNewSchemesRejectReasons(t *testing.T) {
	tests := []struct {
		scheme, cur string
		cands       []string
		want        map[string][]string
	}{
		{"loose", "1.2", []string{"main", "v1.3", "1.3-rc1", "1.1", "1.4", "1.3"}, map[string][]string{
			"invalid": {"main"}, "prefix": {"v1.3"}, "prerelease": {"1.3-rc1"}, "not newer": {"1.1"}, "policy: nope": {"1.4"},
		}},
		{`regex:^(?<major>\d+)\.(?<minor>\d+)(?:-(?<prerelease>rc\d+))?-(?<compatibility>[a-z]+)$`, "1.2-alpine",
			[]string{"1.3", "1.3-bookworm", "1.3-rc1-alpine", "1.1-alpine", "1.4-alpine", "1.3-alpine"}, map[string][]string{
				"invalid": {"1.3"}, "compatibility": {"1.3-bookworm"}, "prerelease": {"1.3-rc1-alpine"}, "not newer": {"1.1-alpine"}, "policy: nope": {"1.4-alpine"},
			}},
	}
	for _, tt := range tests {
		got := map[string][]string{}
		accept := func(v string) bool { return v == "1.3" || v == "1.3-alpine" }
		get(t, tt.scheme).Best(tt.cur, tt.cands, acceptFn(accept), func(v, reason string) { got[reason] = append(got[reason], v) })
		if !maps.EqualFunc(got, tt.want, slices.Equal) {
			t.Errorf("%s rejects = %v, want %v", tt.scheme, got, tt.want)
		}
	}
}

func TestNewSchemeMethods(t *testing.T) {
	tests := []struct {
		scheme        string
		valid         []string
		invalid       []string
		order         []string // ascending
		kinds         map[[2]string]dep.UpdateType
		semverIn, out string
	}{
		{
			scheme: "loose", valid: []string{"1877.25", "v1", "1.2.3.4.5.6", "2.0-rc.1", "5_beta"},
			invalid: []string{"amd64-1877.25", "1.2.3.4.5.6.7", "1.2-", "1.2+x", "1..2", "1.2-a b", "99999999999999999999"},
			order:   []string{"1.2-alpha", "1.2-alpha.1", "1.2-beta", "1.2", "1.2.0", "1.2.0.1", "1.10"},
			kinds: map[[2]string]dep.UpdateType{{"1877.25", "1878.1"}: dep.Major, {"1877.25", "1877.28"}: dep.Minor,
				{"1.2.3", "1.2.4"}: dep.Patch, {"1.2.3.4", "1.2.3.5"}: dep.Patch, {"1.2-rc1", "1.2"}: dep.Patch, {"1", "1.1"}: dep.Minor},
			semverIn: "1877.25", out: "1877.25.0",
		},
		{
			scheme: cosignRegex, valid: []string{"v3.1.4+ocm.6", "v3.1.5-ocm.1"}, invalid: []string{"v3.1.4", "3.1.4+ocm.6", "v3.1.4+ocm.99999999999999999999"},
			order: []string{"v3.0.9-ocm.9", "v3.1.4+ocm.6", "v3.1.4+ocm.10", "v3.1.5-ocm.1", "v3.10.0+ocm.0"},
			kinds: map[[2]string]dep.UpdateType{{"v3.1.4+ocm.6", "v4.0.0+ocm.1"}: dep.Major, {"v3.1.4+ocm.6", "v3.2.0+ocm.1"}: dep.Minor,
				{"v3.1.4+ocm.6", "v3.1.5+ocm.1"}: dep.Patch, {"v3.1.4+ocm.6", "v3.1.4+ocm.7"}: dep.Patch},
			semverIn: "v3.1.4+ocm.6", out: "3.1.4",
		},
		{
			scheme: `regex:^(?<major>\d+)(?:-(?<prerelease>.+))?$`, valid: []string{"7", "7-rc.1"}, invalid: []string{"x7"},
			order:    []string{"7-rc.1", "7-rc.2", "7-rc.10", "7-rc.a", "7"},
			kinds:    map[[2]string]dep.UpdateType{{"7", "8"}: dep.Major, {"7-rc.1", "7"}: dep.Patch},
			semverIn: "7-rc.1", out: "7.0.0-rc.1",
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
		for i := range len(tt.order) - 1 {
			if a, b := tt.order[i], tt.order[i+1]; s.Compare(a, b) >= 0 || s.Compare(b, a) <= 0 || s.Compare(a, a) != 0 {
				t.Errorf("%s: Compare(%s, %s) not ascending", tt.scheme, a, b)
			}
		}
		for p, k := range tt.kinds {
			if got := s.Kind(p[0], p[1]); got != k {
				t.Errorf("%s: Kind(%s, %s) = %s, want %s", tt.scheme, p[0], p[1], got, k)
			}
		}
		if sv := s.Semver(tt.semverIn); sv == nil || sv.String() != tt.out {
			t.Errorf("%s: Semver(%s) = %v, want %s", tt.scheme, tt.semverIn, sv, tt.out)
		}
	}
}

func TestRankCandidates(t *testing.T) {
	for _, tc := range []struct {
		scheme, cur string
		cands       []string
		want        []Candidate
	}{
		{"tag", "v4.1.0", []string{"v4.1.1", "v6.0.0", "v4.2.0", "v5.0.0", "v4.0.0"}, []Candidate{
			{"v6.0.0", dep.Major, "6"}, {"v5.0.0", dep.Major, "5"}, {"v4.2.0", dep.Minor, ""}, {"v4.1.1", dep.Patch, ""},
		}},
		{"go", "v0.5.0", []string{"v1.0.0", "v0.6.0"}, []Candidate{{"v1.0.0", dep.Major, "1"}, {"v0.6.0", dep.Minor, ""}}},
		{"docker", "1.25-alpine", []string{"2.0-alpine", "1.26-alpine"}, []Candidate{{"2.0-alpine", dep.Major, "2"}, {"1.26-alpine", dep.Minor, ""}}},
		{"semver", "v1.2.3", []string{"2.0.0", "v1.2.4"}, []Candidate{{"v2.0.0", dep.Major, "2"}, {"v1.2.4", dep.Patch, ""}}},
		{"semver", "1.2.3", []string{"v1.3.0"}, []Candidate{{"1.3.0", dep.Minor, ""}}},
		{"loose", "1877.25", []string{"1877.28", "1878.1", "1880.0"}, []Candidate{{"1880.0", dep.Major, "1880"}, {"1878.1", dep.Major, "1878"}, {"1877.28", dep.Minor, ""}}},
		{cosignRegex, "v3.1.4+ocm.6", []string{"v3.1.5+ocm.1", "v4.0.0-ocm.1"}, []Candidate{{"v4.0.0-ocm.1", dep.Major, "4"}, {"v3.1.5+ocm.1", dep.Patch, ""}}},
		{"tag", "main", []string{"v1"}, nil},
	} {
		r := get(t, tc.scheme).Rank(tc.cur, tc.cands, nil, nil)
		var got []Candidate
		for i := range r.Len() {
			got = append(got, r.At(i))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s Rank(%s) = %v, want %v", tc.scheme, tc.cur, got, tc.want)
		}
	}
}

func TestParseScheme(t *testing.T) {
	for _, n := range Names {
		if _, err := Parse(n); err != nil {
			t.Errorf("Parse(%s): %v", n, err)
		}
	}
	a, errA := Parse(cosignRegex)
	b, errB := Parse(cosignRegex)
	if errA != nil || errB != nil || a == nil || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("Parse(cosign) = %v %v / %v %v", a, errA, b, errB)
	}
	if p, ok := Get(`regex:^(?P<major>\d+)$`); !ok || !p.Valid("12") {
		t.Error("(?P<name>) groups")
	}
	for in, want := range map[string]string{
		"calver":                      `must be one of tag, go, docker, semver, loose or regex:<RE2>, got "calver"`,
		"regex:(":                     "regex: invalid RE2 regexp: error parsing regexp",
		`regex:^(?<minor>\d+)$`:       "regex: must have a (?<major>...) group",
		`regex:(?<major>\d)(?<x>\d)`:  `regex: unknown group "x" (allowed: major, minor, patch, build, revision, prerelease, compatibility)`,
		`regex:(?<major>\d)(?<x>\d)x`: `regex: unknown group "x"`,
		"Regex:(?<major>1)":           `got "Regex:(?<major>1)"`,
	} {
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want %q", in, err, want)
		}
	}
	// Compare and Kind of invalid input fall back like the other schemes.
	s := get(t, cosignRegex)
	if s.Compare("x", "v3.1.4+ocm.6") == 0 || s.Kind("x", "v3.1.4+ocm.6") != dep.Major || s.Compatible("v3.1.4", "v3.1.5+ocm.1") {
		t.Error("invalid input")
	}
}

// TestParseSemverStrictFirstEquivalent guards the strict-first fast path in
// parseSemver: for every input it must return exactly what NewVersion alone
// would, including Original, so tag styling and ordering are unchanged.
func TestParseSemverStrictFirstEquivalent(t *testing.T) {
	corpus := []string{
		"1.2.3", "v1.2.3", "V1.2.3", "1.2", "v1", "1", "0.0.0",
		"1.2.3-beta.1", "1.2.3-rc.1+build.5", "1.2.3+meta", "v2.0.0-alpha",
		"01.2.3", "1.02.3", "1.2.03", "1.2.3-01", "1.2.3-0a", "1.2.3.4",
		"1.2.3-", "1.2.3+", "latest", "", "v", "1.2.3-beta..1", "=1.2.3",
		"5.0.0-dev.20240101", "1.2.3-x.7.z.92", "99999999999999999999.0.0",
	}
	for _, s := range corpus {
		want, wantErr := msemver.NewVersion(s)
		got, ok := parseSemver(s)
		if ok != (wantErr == nil) {
			t.Errorf("parseSemver(%q) ok = %v, NewVersion err = %v", s, ok, wantErr)
			continue
		}
		if !ok {
			continue
		}
		if !got.Equal(want) || got.Original() != want.Original() || got.Prerelease() != want.Prerelease() ||
			got.Metadata() != want.Metadata() || got.String() != want.String() {
			t.Errorf("parseSemver(%q) = %#v, NewVersion = %#v", s, got, want)
		}
	}
}

// benchSemverCorpus mimics a large npm packument: mostly strict versions
// with prereleases, plus a few v-prefixed tags that take the fallback.
func benchSemverCorpus() []string {
	var out []string
	for i := range 4000 {
		switch i % 10 {
		case 0:
			out = append(out, fmt.Sprintf("5.%d.0-dev.2024%04d", i/100, i))
		case 1:
			out = append(out, fmt.Sprintf("v%d.%d.%d", i/1000, i/100%10, i%100))
		default:
			out = append(out, fmt.Sprintf("%d.%d.%d", i/1000, i/100%10, i%100))
		}
	}
	return out
}

func BenchmarkParseSemver(b *testing.B) {
	corpus := benchSemverCorpus()
	b.Run("strict-first", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, s := range corpus {
				parseSemver(s)
			}
		}
	})
	b.Run("NewVersion", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, s := range corpus {
				_, _ = msemver.NewVersion(s)
			}
		}
	})
}
