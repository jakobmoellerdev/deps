package renovate

import (
	"math"
	"reflect"
	"testing"
)

func TestParseJSON5(t *testing.T) {
	tests := []struct {
		name, src string
		want      any
	}{
		{"json", `{"a": [1, "x", true, false, null], "b": {"c": -2.5e3}}`,
			map[string]any{"a": []any{1.0, "x", true, false, nil}, "b": map[string]any{"c": -2500.0}}},
		{"comments", "// head\n{/* in */ a: 1, // tail\n b: /* x */ 2}\n/* end */",
			map[string]any{"a": 1.0, "b": 2.0}},
		{"trailing commas", `{a: [1, 2,], b: 3,}`, map[string]any{"a": []any{1.0, 2.0}, "b": 3.0}},
		{"regex string", `{a: '\\d+'}`, map[string]any{"a": `\d+`}},
		{"unknown escape", `'\d\q'`, "dq"},
		{"unquoted keys", `{$a_1: 1, _b: 2, 'c d': 3}`, map[string]any{"$a_1": 1.0, "_b": 2.0, "c d": 3.0}},
		{"hex", `[0x1F, 0XfF, -0x10, +0x1]`, []any{31.0, 255.0, -16.0, 1.0}},
		{"dots", `[.5, 5., +1, -.25]`, []any{0.5, 5.0, 1.0, -0.25}},
		{"escapes", `"\n\t\r\b\f\v\0\'\"\\\/\x41\u00e9"`, "\n\t\r\b\f\v\x00'\"\\/Aé"},
		{"continuation", "'ab\\\ncd\\\r\nef'", "abcdef"},
		{"surrogate pair", `"\uD83D\uDE00"`, "😀"},
		{"duplicate keys", `{a: 1, a: 2}`, map[string]any{"a": 2.0}},
		{"nested", `{a: {b: [[], {}, [{c: 'd'}]]}}`,
			map[string]any{"a": map[string]any{"b": []any{[]any{}, map[string]any{}, []any{map[string]any{"c": "d"}}}}}},
		{"unicode space", "\ufeff\u00a0{a:\u2028 1}", map[string]any{"a": 1.0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJSON5([]byte(tt.src))
			if err != nil {
				t.Fatalf("parseJSON5(%q) error: %v", tt.src, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseJSON5(%q) = %#v, want %#v", tt.src, got, tt.want)
			}
		})
	}
}

func TestParseJSON5Special(t *testing.T) {
	got, err := parseJSON5([]byte(`[Infinity, +Infinity, -Infinity, NaN, -NaN]`))
	if err != nil {
		t.Fatal(err)
	}
	a := got.([]any)
	if !math.IsInf(a[0].(float64), 1) || !math.IsInf(a[1].(float64), 1) || !math.IsInf(a[2].(float64), -1) ||
		!math.IsNaN(a[3].(float64)) || !math.IsNaN(a[4].(float64)) {
		t.Errorf("got %v", a)
	}
}

func TestParseJSON5Errors(t *testing.T) {
	tests := []struct{ src, want string }{
		{``, "1:1: unexpected end of input"},
		{"{\n  a: 1,\n  b: 'x}", "3:6: unterminated string"},
		{"'ab\ncd'", "1:4: unterminated string"},
		{"{\n  a: 1,\n    }}", "3:6: unexpected character '}'"},
		{`{a: tru}`, "1:5: unexpected character 't'"},
		{`[1 2]`, "1:4: unexpected character '2'"},
		{`{a: 1} x`, "1:8: unexpected character 'x'"},
		{`/* x`, "1:1: unterminated block comment"},
		{`{a: 1`, "1:6: unexpected end of input"},
		{`{"é": é}`, "1:7: unexpected character 'é'"},
		{`'\x4'`, `1:2: invalid \x escape`},
		{`1x`, "1:2: unexpected character 'x'"},
	}
	for _, tt := range tests {
		_, err := parseJSON5([]byte(tt.src))
		if err == nil || err.Error() != tt.want {
			t.Errorf("parseJSON5(%q) err = %v, want %q", tt.src, err, tt.want)
		}
	}
}

func TestParseJSON5Depth(t *testing.T) {
	src := make([]byte, 2000)
	for i := range src {
		src[i] = '['
	}
	if _, err := parseJSON5(src); err == nil || err.Error() != "1:1002: nesting too deep" {
		t.Errorf("err = %v", err)
	}
}
