package yamlspan

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestLineStarts(t *testing.T) {
	for src, want := range map[string][]int{
		"":          {0},
		"a":         {0},
		"a\n":       {0, 2},
		"a\nbc\n\n": {0, 2, 5, 6},
		"\r\nx":     {0, 2},
	} {
		if got := LineStarts([]byte(src)); !slices.Equal(got, want) {
			t.Errorf("LineStarts(%q) = %v, want %v", src, got, want)
		}
	}
}

func parse(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return doc.Content[0]
}

func TestLocate(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      string // the source from the located offset, "" for -1
	}{
		{"plain", "k: v1.2\n", "v1.2\n"},
		{"single quoted", "k: 'v1.2'\n", "v1.2'\n"},
		{"double quoted", "k: \"v1.2\"\n", "v1.2\"\n"},
		{"runes before value", "äöü: v1\n", "v1\n"},
		{"anchor", "k: &a v1\n", "v1\n"},
		{"anchor and tab", "k: &a \t'v1'\n", "v1'\n"},
		{"anchored quoted", "k: &a \"v1\"\n", "v1\"\n"},
		{"nested", "m:\n  k: v1 # c\n", "v1 # c\n"},
		{"flow", "m: {k: v1}\n", "v1}\n"},
		{"no trailing newline", "k: v1", "v1"},
		{"empty value", "k: ''\n", "'\n"},
		{"escape", "k: \"a\\tb\"\n", ""},
		{"literal block", "k: |\n  v1\n", ""},
		{"folded block", "k: >\n  v1\n", ""},
		{"multi-line plain", "k: a\n  b\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := parse(t, tc.src)
			for v.Kind == yaml.MappingNode {
				v = v.Content[1]
			}
			off := Locate([]byte(tc.src), LineStarts([]byte(tc.src)), v)
			got := ""
			if off >= 0 {
				got = tc.src[off:]
			}
			if got != tc.want {
				t.Errorf("Locate = %d (%q), want %q", off, got, tc.want)
			}
		})
	}
}

func TestLocateOutOfRange(t *testing.T) {
	src := []byte("k: v\n")
	lines := LineStarts(src)
	for _, v := range []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "v", Line: 0, Column: 1},
		{Kind: yaml.ScalarNode, Value: "v", Line: 3, Column: 1},
		{Kind: yaml.ScalarNode, Value: "v", Line: 1, Column: 9}, // past the end of its line
		{Kind: yaml.ScalarNode, Value: "v", Line: 2, Column: 2}, // past the end of src
		{Kind: yaml.ScalarNode, Value: "x", Line: 1, Column: 4}, // bytes differ
		{Kind: yaml.ScalarNode, Value: "v", Line: 1, Column: 1}, // points at the key
		{Kind: yaml.ScalarNode, Value: "", Line: 2, Column: 1, Style: yaml.DoubleQuotedStyle},
	} {
		if off := Locate(src, lines, v); off != -1 {
			t.Errorf("Locate(%+v) = %d, want -1", v, off)
		}
	}
	// An anchor the source does not hold at the column is not skipped.
	if off := Locate(src, lines, &yaml.Node{Kind: yaml.ScalarNode, Value: "v", Anchor: "a", Line: 1, Column: 4}); off != 3 {
		t.Errorf("Locate with absent anchor = %d, want 3", off)
	}
}

func TestGetStrValue(t *testing.T) {
	root := parse(t, "a: 1\nm: {b: x}\ns: [y]\nn: ~\n")
	if v := Value(Get(Get(root, "m"), "b")); v != "x" {
		t.Errorf("m.b = %q", v)
	}
	if v := Value(Get(root, "a")); v != "1" {
		t.Errorf("a = %q", v)
	}
	if n := Get(root, "missing"); n != nil {
		t.Errorf("missing = %v", n)
	}
	if n := Get(nil, "a"); n != nil {
		t.Errorf("Get(nil) = %v", n)
	}
	if n := Get(Get(root, "s"), "y"); n != nil {
		t.Errorf("Get on a sequence = %v", n)
	}
	odd := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "k"}}}
	if n := Get(odd, "k"); n != nil {
		t.Errorf("Get of a key without value = %v", n)
	}
	if n := Str(Get(root, "m")); n != nil {
		t.Errorf("Str of a mapping = %v", n)
	}
	if n := Str(nil); n != nil {
		t.Errorf("Str(nil) = %v", n)
	}
	if n := Get(root, "n"); Str(n) != n || Value(n) != "~" {
		t.Errorf("Str/Value of a null scalar = %v, %q", Str(n), Value(n))
	}
	if v := Value(Get(root, "s")); v != "" {
		t.Errorf("Value of a sequence = %q", v)
	}
}
