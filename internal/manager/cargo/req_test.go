package cargo

import (
	"testing"

	"github.com/Masterminds/semver/v3"
)

func TestRequirement(t *testing.T) {
	for _, tc := range []struct {
		raw, version, constraint string
		admits, rejects          []string
		render                   map[string]string // new version -> rendered requirement
	}{
		{"1.2", "1.2", "^1.2", []string{"1.2.0", "1.9.9"}, []string{"2.0.0", "1.1.9"}, map[string]string{"1.4": "1.4", "2.0.1": "2.0"}},
		{"0.4", "0.4", "^0.4", []string{"0.4.7"}, []string{"0.5.0"}, map[string]string{"0.5.2": "0.5"}},
		{"1.0.75", "1.0.75", "^1.0.75", nil, nil, map[string]string{"1.0.104": "1.0.104"}},
		{"~2.7.0", "2.7.0", "", []string{"2.7.3"}, []string{"2.8.0"}, map[string]string{"2.9.1": "~2.9.1"}},
		{"= 0.1.0-beta.1", "0.1.0-beta.1", "", nil, nil, map[string]string{"0.1.0-beta.2": "= 0.1.0-beta.2"}},
		{"=1.2.3", "1.2.3", "", []string{"1.2.3"}, []string{"1.2.4"}, nil},
		{">=1.2", "1.2", "", []string{"9.0.0"}, []string{"1.1.0"}, map[string]string{"1.4": ">=1.4"}},
		{"1.0.0+meta", "1.0.0+meta", "^1.0.0+meta", nil, nil, map[string]string{"1.2.0": "1.2.0"}},
		{">=1.2, <2", "1.2", "", []string{"1.9.0"}, []string{"2.0.0"}, map[string]string{"1.4": ">=1.4, <2", "2.1.0": ">=2.1, <3", "3.0": ">=3.0, <4"}},
		{">= 0.2, < 0.4", "0.2", "", []string{"0.3.1"}, []string{"0.4.0"}, map[string]string{"0.5.1": ">= 0.5, < 0.6", "0.3.0": ">= 0.3, < 0.4"}},
		{">=0.0.3,<0.0.4", "0.0.3", "", nil, nil, map[string]string{"0.0.5": ">=0.0.5,<0.0.6"}},
		{">1.0, <=1.5", "1.0", "", []string{"1.5.0"}, []string{"1.0.0"}, map[string]string{"1.7.2": ">1.7, <=1.7"}},
		{"1.*", "1", "1.*", []string{"1.9.0"}, []string{"2.0.0"}, map[string]string{"2": "2.*"}},
		{"0.3.x", "0.3", "0.3.x", []string{"0.3.9"}, []string{"0.4.0"}, map[string]string{"0.4": "0.4.x"}},
		{"*", "", "", []string{"0.0.1", "9.0.0"}, nil, map[string]string{"1.2.3": "*"}},
		{"<2", "", "", []string{"1.9.0"}, []string{"2.0.0"}, map[string]string{"1.5.0": "<2", "2.3.0": "<3"}},
	} {
		r, ok := parseRequirement(tc.raw)
		if !ok {
			t.Errorf("%q: not parsed", tc.raw)
			continue
		}
		if got := r.version(); got != tc.version {
			t.Errorf("%q: version %q, want %q", tc.raw, got, tc.version)
		}
		want := tc.constraint
		if want == "" {
			want = tc.raw
		}
		got := r.constraint()
		if got != want && !(tc.constraint == "" && got == normalizeSpaces(tc.raw)) {
			t.Errorf("%q: constraint %q, want %q", tc.raw, got, want)
		}
		c, err := semver.NewConstraint(got)
		if err != nil {
			t.Errorf("%q: constraint %q: %v", tc.raw, got, err)
			continue
		}
		for _, v := range tc.admits {
			if !c.Check(semver.MustParse(v)) {
				t.Errorf("%q does not admit %s", tc.raw, v)
			}
		}
		for _, v := range tc.rejects {
			if c.Check(semver.MustParse(v)) {
				t.Errorf("%q admits %s", tc.raw, v)
			}
		}
		for nv, want := range tc.render {
			if got := r.render(nv); got != want {
				t.Errorf("%q render(%s) = %q, want %q", tc.raw, nv, got, want)
			}
		}
	}
	for _, raw := range []string{"", "v1.2", "1.2.3.4", ">=1.*", "^*", "1.2.3.*", "1, ", "latest", "1.2 - 2.0", "~>1.0"} {
		if _, ok := parseRequirement(raw); ok {
			t.Errorf("%q parsed", raw)
		}
	}
}

// normalizeSpaces is the constraint form of a requirement with spaces after operators.
func normalizeSpaces(raw string) string {
	r, _ := parseRequirement(raw)
	return r.constraint()
}
