package customds

import (
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	doc := map[string]any{
		"releases": map[string]any{"v2": map[string]any{}, "v1": nil},
		"list":     []any{map[string]any{"tag": "a"}, map[string]any{"tag": jsontext.Value("2")}, map[string]any{"tag": true}},
		"s":        "x",
	}
	for _, c := range []struct{ sel, want, err string }{
		{".releases | keys", "v1 v2", ""},
		{" .list[] | .tag ", "a 2 true", ""},
		{".list[].tag", "a 2 true", ""},
		{".missing", "", ""},
		{".s", "x", ""},
		{".releases.v1", "", ""},
		{".list | keys", "", "keys of an array"},
		{".list.tag", "", `cannot index an array with "tag"`},
		{".missing[]", "", "cannot iterate over null"},
		{".s[]", "", "cannot iterate over a string"},
		{".s.x", "", `cannot index a string with "x"`},
		{".list[].tag.x", "", `cannot index a string with "x"`},
		{".list", "", "yields an array, want strings"},
		{".releases", "", "yields an object, want strings"},
	} {
		s, err := ParseSelector(c.sel)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", c.sel, err)
		}
		if s.String() != c.sel {
			t.Errorf("String() = %q", s.String())
		}
		got, err := s.Select(doc)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.sel, err, c.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, strings.Fields(c.want)) {
			t.Errorf("%s = %v, %v; want %q", c.sel, got, err, c.want)
		}
	}
}

func TestParseSelectorErrors(t *testing.T) {
	for sel, want := range map[string]string{
		"":            "empty stage",
		".a |":        "empty stage",
		"a":           `stage "a" must be keys or start with . or []`,
		".1a":         "expected a field name at offset 1",
		"..a":         "expected a field name at offset 1",
		".a[":         `unexpected '[' at offset 2`,
		".a b":        `unexpected ' ' at offset 2`,
		".a | keys x": `stage "keys x" must be keys`,
	} {
		if _, err := ParseSelector(sel); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseSelector(%q) = %v, want %q", sel, err, want)
		}
	}
}
