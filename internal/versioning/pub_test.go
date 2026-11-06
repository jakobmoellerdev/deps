package versioning

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestPubMatchRange checks constraints against the versions pub_semver 2.2.0 admits
// (`VersionConstraint.parse(c).allows(Version.parse(v))`, Dart 3.13.7).
func TestPubMatchRange(t *testing.T) {
	m, ok := get(t, "pub").(RangeMatcher)
	if !ok {
		t.Fatal("pub is not a RangeMatcher")
	}
	versions := strings.Fields("0.0.3 0.0.4 0.1.0 0.1.5 0.2.0 1.0.0 1.2.3 1.2.3+1 1.2.4 1.9.0 2.0.0-dev.1 2.0.0 2.0.0+1 2.1.0 3.0.0")
	for rng, want := range map[string]string{
		"^1.2.3":               "1.2.3 1.2.3+1 1.2.4 1.9.0",
		"^0.1.0":               "0.1.0 0.1.5",
		"^0.0.3":               "0.0.3 0.0.4",
		">=1.2.3 <2.0.0":       "1.2.3 1.2.3+1 1.2.4 1.9.0",
		">=1.2.3<2.0.0":        "1.2.3 1.2.3+1 1.2.4 1.9.0",
		">= 1.2.3 < 2.0.0":     "1.2.3 1.2.3+1 1.2.4 1.9.0",
		">1.2.3":               "1.2.3+1 1.2.4 1.9.0 2.0.0-dev.1 2.0.0 2.0.0+1 2.1.0 3.0.0",
		"<=1.2.3":              "0.0.3 0.0.4 0.1.0 0.1.5 0.2.0 1.0.0 1.2.3",
		"<2.0.0":               "0.0.3 0.0.4 0.1.0 0.1.5 0.2.0 1.0.0 1.2.3 1.2.3+1 1.2.4 1.9.0",
		"1.2.3":                "1.2.3",
		"any":                  strings.Join(versions, " "),
		">=2.0.0-dev.1 <2.0.0": "2.0.0-dev.1",
		">=1.0.0 <=2.0.0":      "1.0.0 1.2.3 1.2.3+1 1.2.4 1.9.0 2.0.0-dev.1 2.0.0",
	} {
		match, err := m.MatchRange(rng)
		if err != nil {
			t.Fatalf("MatchRange(%q): %v", rng, err)
		}
		var got []string
		for _, v := range versions {
			if match(v) {
				got = append(got, v)
			}
		}
		if g := strings.Join(got, " "); g != want {
			t.Errorf("MatchRange(%q) admits %q, want %q", rng, g, want)
		}
	}
	for _, bad := range []string{"", "^1.2", "1.x", ">=1.0", "~1.2.3", "^1.2.3 ||", "v1.2.3"} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("MatchRange(%q) accepted", bad)
		}
	}
	if match, _ := m.MatchRange("^1.0.0"); match("1.2") {
		t.Error("an invalid version matched")
	}
}

func TestParsePubConstraint(t *testing.T) {
	c, err := ParsePubConstraint(">= 1.2.3 <2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Terms) != 2 || c.Terms[0] != (PubTerm{Op: ">=", V: "1.2.3", Start: 3, End: 8}) || c.Terms[1] != (PubTerm{Op: "<", V: "2.0.0", Start: 10, End: 15}) {
		t.Errorf("terms = %+v", c.Terms)
	}
	if f, ok := c.Floor(); !ok || f.V != "1.2.3" {
		t.Errorf("Floor = %+v, %v", f, ok)
	}
	if c, _ := ParsePubConstraint("<2.0.0"); func() bool { _, ok := c.Floor(); return ok }() {
		t.Error("<2.0.0 has a floor")
	}
	if c, _ := ParsePubConstraint(" any "); !c.Any {
		t.Error("any")
	}
	for v, want := range map[string]string{"1.2.3": "2.0.0", "0.4.1": "0.5.0", "0.0.3": "0.1.0"} {
		if got, ok := PubNextBreaking(v); !ok || got != want {
			t.Errorf("PubNextBreaking(%s) = %s, %v", v, got, ok)
		}
	}
	if _, ok := PubNextBreaking("1.2"); ok {
		t.Error("PubNextBreaking(1.2)")
	}
}

func TestPubCompareAndKind(t *testing.T) {
	s := get(t, "pub")
	for _, v := range []string{"1.2", "v1.2.3", "1.2.3.4", "01.2.3"} {
		if s.Valid(v) {
			t.Errorf("Valid(%s)", v)
		}
	}
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.2.3+1", "1.2.3", 1}, {"1.2.3+2", "1.2.3+1", 1}, {"1.2.4", "1.2.3+9", 1}, {"1.0.0-dev.1", "1.0.0", -1}, {"1.2.3", "1.2.3", 0},
	} {
		if got := ComparePub(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"1.2.3", "2.0.0", dep.Major}, {"1.2.3", "1.4.0", dep.Minor}, {"1.2.3", "1.2.3+1", dep.Patch},
		{"0.4.1", "0.5.0", dep.Major}, {"0.4.1", "0.4.2", dep.Patch}, {"0.0.3", "0.0.4", dep.Patch}, {"0.0.3", "0.1.0", dep.Major},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	runBest(t, "pub", []bestCase{
		{"1.2.3", "2.1.0", []string{"1.4.0", "2.0.0", "2.1.0", "3.0.0-dev.1", "1.5"}, nil},
		{"3.0.0-dev.1", "3.0.0-dev.2", []string{"3.0.0-dev.2", "2.0.0"}, nil},
		{"0.12.0+1", "0.12.0+2", []string{"0.12.0+2", "0.12.0"}, nil},
	})
	if r := s.Rank("0.4.0", []string{"0.5.0", "1.1.0"}, nil, nil); r.Len() != 2 || r.At(0).Major != "1" || r.At(1).Major != "0.5" {
		t.Errorf("Rank major buckets = %+v %+v", r.At(0), r.At(1))
	}
}
