package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestParseTag(t *testing.T) {
	for s, ok := range map[string]bool{"v4": true, "4.1": true, "v7.0.1": true, "v1.2.3.4": false, "v1-beta": false, "v1.0.0-rc1": false, "main": false, "": false} {
		if _, got := ParseTag(s); got != ok {
			t.Errorf("ParseTag(%q) = %v", s, got)
		}
	}
}

func TestBestTag(t *testing.T) {
	names := []string{"v4", "v5", "v6", "v7", "v6.0.0", "v6.1.0", "v7.0.1", "v7.0.2", "7.1.0", "v8.0.0-beta", "v7.0.1.1"}
	for _, tc := range []struct {
		cur, want string
		accept    func(Tag) bool
	}{
		{"v4", "v7", nil},
		{"v7.0.1", "v7.0.2", nil},
		{"v6.0.0", "v7.0.2", nil},
		{"v6.0.0", "v6.1.0", func(t Tag) bool { return t.Parts[0] < 7 }},
		{"7.0.0", "7.1.0", nil},
		{"v7", "", nil},
	} {
		cur, _ := ParseTag(tc.cur)
		got, ok := BestTag(cur, names, tc.accept)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("BestTag(%s) = %q", tc.cur, got)
		}
	}
}

func TestTagKind(t *testing.T) {
	p := func(s string) Tag { t, _ := ParseTag(s); return t }
	for _, tc := range []struct {
		a, b string
		k    dep.UpdateType
	}{{"v4", "v5", dep.Major}, {"v4.1", "v4.2", dep.Minor}, {"v4.1.1", "v4.1.2", dep.Patch}} {
		if k := TagKind(p(tc.a), p(tc.b)); k != tc.k {
			t.Errorf("%s->%s = %s", tc.a, tc.b, k)
		}
	}
	if s := p("v4").Semver().String(); s != "4.0.0" {
		t.Errorf("Semver = %s", s)
	}
}

func TestBestGo(t *testing.T) {
	vs := []string{"v1.9.1", "v1.10.0", "v1.10.2", "v1.11.0-rc.1", "v2.0.0", "v2.0.0+incompatible", "v1.11.1-0.20240101000000-abcdefabcdef", "bad"}
	for _, tc := range []struct {
		cur, want string
		vs        []string
		accept    func(string, dep.UpdateType) bool
	}{
		{"v1.9.1", "v1.10.2", vs, nil},
		{"v1.9.1", "v1.10.0", vs, func(v string, _ dep.UpdateType) bool { return v != "v1.10.2" }},
		{"v1.11.0-rc.0", "v1.11.0-rc.1", vs, nil},
		{"v2.0.0+incompatible", "v2.1.0+incompatible", []string{"v2.1.0+incompatible", "v3.0.0+incompatible"}, nil},
		{"v1.9.1", "", []string{"v2.0.0+incompatible"}, nil},
		{"v0.5.0", "v1.0.0", []string{"v0.4.0", "v1.0.0", "v2.0.0"}, nil},
		{"v1.10.2", "", vs, func(string, dep.UpdateType) bool { return false }},
	} {
		got, ok := BestGo(tc.cur, tc.vs, tc.accept)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("BestGo(%s) = %q", tc.cur, got)
		}
	}
	for _, tc := range []struct {
		a, b string
		k    dep.UpdateType
	}{{"v0.5.0", "v1.0.0", dep.Major}, {"v1.9.1", "v1.10.0", dep.Minor}, {"v1.10.0", "v1.10.2", dep.Patch}} {
		if k := GoKind(tc.a, tc.b); k != tc.k {
			t.Errorf("GoKind %s->%s = %s", tc.a, tc.b, k)
		}
	}
	if GoSemver("v1.2.3").String() != "1.2.3" || GoSemver("x") != nil {
		t.Error("GoSemver")
	}
}
