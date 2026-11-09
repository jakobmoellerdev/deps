package versioning

import "testing"

// TestParseIvyRevision follows Renovate's ivy versioning tests (parseDynamicRevision, isValid,
// matches).
func TestParseIvyRevision(t *testing.T) {
	for _, tc := range []struct {
		in     string
		kind   IvyKind // 0 = not a revision
		anchor string
		admit  []string
		deny   []string
	}{
		{in: "latest", kind: IvyLatest, admit: []string{"0", "1.2.3"}},
		{in: "latest.integration", kind: IvyLatest, admit: []string{"0"}},
		{in: "latest.release", kind: IvyLatest, admit: []string{"1.0"}, deny: []string{"latest"}},
		{in: "1.0.+", kind: IvySubRevision, anchor: "1.0", admit: []string{"1.0.0", "1.0.9-RC1"}, deny: []string{"0", "1", "1.1", "1.00"}},
		{in: "1.2.+", kind: IvySubRevision, anchor: "1.2", admit: []string{"1.2.0", "1.2.milestone"}, deny: []string{"1.1.0", "1.3", "1.20.0"}},
		{in: "[1.0,2.0]", kind: IvyRange, anchor: "1.0", admit: []string{"1.0", "2.0"}, deny: []string{"2.0.1"}},
		{in: "[1.0,2.0[", kind: IvyRange, anchor: "1.0", admit: []string{"1.0", "1.9"}, deny: []string{"2.0"}},
		{in: "]1.0,2.0]", kind: IvyRange, anchor: "1.0", admit: []string{"1.0.1", "2.0"}, deny: []string{"1.0"}},
		{in: "]0,1[", kind: IvyRange, anchor: "0", admit: []string{"0.5"}, deny: []string{"0", "1"}},
		{in: "[1.0,)", kind: IvyRange, anchor: "1.0", admit: []string{"99"}, deny: []string{"0.9"}},
		{in: "]1.0,)", kind: IvyRange, anchor: "1.0", admit: []string{"1.1"}, deny: []string{"1.0"}},
		{in: "(,2.0]", kind: IvyRange, anchor: "2.0", admit: []string{"2.0"}, deny: []string{"2.1"}},
		{in: "(,2.0[", kind: IvyRange, anchor: "2.0", admit: []string{"1.9"}, deny: []string{"2.0", "not a version"}},
		{in: "(0,2)", kind: IvyRange, anchor: "0", admit: []string{"1"}, deny: []string{"0", "2"}},
		{in: "[1.0]", kind: IvyRange, anchor: "1.0", admit: []string{"1.0"}, deny: []string{"1.1"}},
		{in: ""},
		{in: ".+"},
		{in: "1.0+"},
		{in: "+"},
		{in: "[0,1),(1,)"},
		{in: "[0,1),(1,2]"},
		{in: "1.0"},
		{in: "]1.0"},
	} {
		r, ok := ParseIvyRevision(tc.in)
		if ok != (tc.kind != 0) || r.Kind != tc.kind {
			t.Errorf("%q: kind %d, %v, want %d", tc.in, r.Kind, ok, tc.kind)
			continue
		}
		if r.Anchor() != tc.anchor {
			t.Errorf("%q: anchor %q, want %q", tc.in, r.Anchor(), tc.anchor)
		}
		for _, v := range tc.admit {
			if !r.Admits(v) {
				t.Errorf("%q does not admit %s", tc.in, v)
			}
		}
		for _, v := range tc.deny {
			if r.Admits(v) {
				t.Errorf("%q admits %s", tc.in, v)
			}
		}
	}
	if r, _ := ParseIvyRevision("[1.0]"); !r.Exact() {
		t.Error("[1.0] is not exact")
	}
	if r, _ := ParseIvyRevision("[1.0,2.0]"); r.Exact() {
		t.Error("[1.0,2.0] is exact")
	}
}

// TestIvyExtend: ranges move like Maven ranges (Renovate's ivy getNewValue is
// autoExtendMavenRange) and keep Ivy's brackets; latest and sub-revisions are kept.
func TestIvyExtend(t *testing.T) {
	for _, tc := range []struct{ in, v, want string }{
		{"[1.0,2.0)", "2.5.1", "[1.0,2.6)"},
		{"[1.0,2.0[", "2.5.1", "[1.0,2.6["},
		{"]1.0,2.0[", "2.5.1", "]1.0,2.6["},
		{"[1,2[", "4.3.2", "[4,5["},
		{"]1.0,2.0]", "2.1", "]1.0,2.1]"},
		{"(,1.0[", "1.0", "(,1.1["},
		{"[1.0]", "1.1", "[1.1]"},
		{"[1.0,2.0)", "1.5", ""},
		{"[1.0,)", "3.0", ""},
		{"1.2.+", "1.3.0", ""},
		{"latest.integration", "9", ""},
	} {
		r, ok := ParseIvyRevision(tc.in)
		if !ok {
			t.Fatalf("%q does not parse", tc.in)
		}
		got, ok := r.Extend(tc.v)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("Extend(%q, %s) = %q, %v, want %q", tc.in, tc.v, got, ok, tc.want)
		}
	}
}

func TestIsIvyDynamic(t *testing.T) {
	for v, want := range map[string]bool{
		"latest.integration": true, "Latest.Release": true, "latest": true, "[1.0,2.0)": true, "]1.0,2.0[": true,
		"(,2.0]": true, "1.+": true, "1.0+": true, "1.0": false, "latestx": false, "": false,
	} {
		if IsIvyDynamic(v) != want {
			t.Errorf("IsIvyDynamic(%q) = %v", v, !want)
		}
	}
}

// The ivy scheme orders versions as maven: Renovate's ivy versioning reuses maven's.
func TestIvyScheme(t *testing.T) {
	s, ok := Get("ivy")
	if !ok {
		t.Fatal("no ivy scheme")
	}
	if !s.Valid("2.1.1.RELEASE") || s.Valid("1.0.+") || s.Valid("latest.release") || s.Valid("[1.0,2.0)") {
		t.Error("Valid")
	}
	if s.Compare("1.0-RC1", "1.0") >= 0 || s.Compare("1.10", "1.9") <= 0 {
		t.Error("Compare")
	}
	if got, ok := s.Best("1.0", []string{"1.1", "2.0-M1", "1.2"}, nil, nil); !ok || got != "1.2" {
		t.Errorf("Best = %q, %v", got, ok)
	}
}
