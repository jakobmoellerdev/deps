package versioning

import (
	"strings"
	"testing"
)

// The expectations below follow Elixir's Version module (Version.match?/3 with
// allow_pre: false, as Hex resolves; https://hexdocs.pm/elixir/Version.html).

func TestHexMatchRange(t *testing.T) {
	m, ok := get(t, "hex").(RangeMatcher)
	if !ok {
		t.Fatal("hex is not a RangeMatcher")
	}
	all := []string{"0.9.0", "1.0.0", "1.6.9", "1.7.0", "1.7.2", "1.7.14", "1.8.0", "2.0.0-rc.0", "2.0.0", "2.1.3", "3.0.0"}
	for rng, want := range map[string]string{
		"~> 1.7":                       "1.7.0 1.7.2 1.7.14 1.8.0",
		"~> 1.7.2":                     "1.7.2 1.7.14",
		"~>1.7.2":                      "1.7.2 1.7.14",
		"== 1.7.2":                     "1.7.2",
		"1.7.2":                        "1.7.2",
		">= 0.0.0":                     "0.9.0 1.0.0 1.6.9 1.7.0 1.7.2 1.7.14 1.8.0 2.0.0 2.1.3 3.0.0",
		">= 1.7.0 and < 2.0.0":         "1.7.0 1.7.2 1.7.14 1.8.0",
		">=1.7.0 and <2.0.0":           "1.7.0 1.7.2 1.7.14 1.8.0",
		"~> 1.0 or ~> 2.0":             "1.0.0 1.6.9 1.7.0 1.7.2 1.7.14 1.8.0 2.0.0 2.1.3",
		"< 1.7.0 or ~> 1.7.14":         "0.9.0 1.0.0 1.6.9 1.7.14",
		"> 1.7.2 and != 1.8.0":         "1.7.14 2.0.0 2.1.3 3.0.0",
		"<= 1.0.0":                     "0.9.0 1.0.0",
		"~> 2.0.0-rc.0":                "2.0.0-rc.0 2.0.0",
		">= 1.8.0 and <= 2.0.0-rc.0":   "1.8.0 2.0.0-rc.0",
		"~> 1.7 and != 1.7.2 or 0.9.0": "0.9.0 1.7.0 1.7.14 1.8.0",
	} {
		match, err := m.MatchRange(rng)
		if err != nil {
			t.Errorf("MatchRange(%q): %v", rng, err)
			continue
		}
		var got []string
		for _, v := range all {
			if match(v) {
				got = append(got, v)
			}
		}
		if strings.Join(got, " ") != want {
			t.Errorf("MatchRange(%q) admits %v, want %s", rng, got, want)
		}
	}
	match, _ := m.MatchRange("~> 1.7")
	if match("v1.7.3") || match("1.7") || match("junk") {
		t.Error("only full SemVer versions are versions")
	}
	for _, rng := range []string{"", "~> 1", ">= 1.0", "1.0", "== 01.0.0", ">= 1.0.0 and", "or 1.0.0", "1.0.0 and and 2.0.0", "~> x.y", "1.0.0and", "1.0.0 & 2.0.0", "^1.0.0", "~1.0.0", "1.0.0 || 2.0.0", "=> 1.0.0"} {
		if _, err := m.MatchRange(rng); err == nil {
			t.Errorf("MatchRange(%q) = nil error", rng)
		}
	}
}

func TestHexScheme(t *testing.T) {
	s := get(t, "hex")
	for v, ok := range map[string]bool{"1.7.14": true, "2.0.0-rc.0": true, "1.0.0+build.1": true, "v1.0.0": false, "1.0": false, "01.0.0": false, "1.0.0-01": false} {
		if s.Valid(v) != ok {
			t.Errorf("Valid(%q) = %v", v, !ok)
		}
	}
	if s.Compare("2.0.0-rc.0", "2.0.0") >= 0 || s.Compare("1.10.0", "1.9.0") <= 0 {
		t.Error("SemVer order")
	}
	if best, ok := s.Best("1.7.2", []string{"1.7.14", "1.8.0", "2.0.0-rc.0", "v3.0.0"}, nil, nil); !ok || best != "1.8.0" {
		t.Errorf("Best = %s, %v", best, ok)
	}
}

func TestParseHexRequirementSpans(t *testing.T) {
	const s = ">= 1.0.0 and < 2.0.0 or ~>3.1"
	r, err := ParseHexRequirement(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Alts) != 2 || s[r.Alts[0].Start:r.Alts[0].End] != ">= 1.0.0 and < 2.0.0" || s[r.Alts[1].Start:r.Alts[1].End] != "~>3.1" {
		t.Fatalf("alternatives = %+v", r.Alts)
	}
	c := r.Alts[0].Cmps[1]
	if c.Op != "<" || c.Ver != "2.0.0" || s[c.Start:c.End] != "< 2.0.0" || s[c.Off:c.End] != "2.0.0" || c.Parts() != 3 {
		t.Errorf("comparator = %+v", c)
	}
	if c := r.Alts[1].Cmps[0]; c.Parts() != 2 || s[c.Start:c.Off] != "~>" {
		t.Errorf("comparator = %+v", c)
	}
}
