package manager

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestIsHex(t *testing.T) {
	sha1 := strings.Repeat("aF0", 13) + "9"
	for _, tc := range []struct {
		s    string
		lens []int
		want bool
	}{
		{sha1, []int{40}, true},
		{sha1, []int{40, 64}, true},
		{strings.Repeat("a", 64), []int{40, 64}, true},
		{strings.Repeat("a", 64), []int{40}, false},
		{sha1[:39], []int{40, 64}, false},
		{sha1[:39] + "g", []int{40}, false},
		{"", nil, false},
	} {
		if got := IsHex(tc.s, tc.lens...); got != tc.want {
			t.Errorf("IsHex(%q, %v) = %v, want %v", tc.s, tc.lens, got, tc.want)
		}
	}
}

func TestQuoteChar(t *testing.T) {
	for s, want := range map[yaml.Style]byte{
		0:                      0,
		yaml.LiteralStyle:      0,
		yaml.SingleQuotedStyle: '\'',
		yaml.DoubleQuotedStyle: '"',
		yaml.SingleQuotedStyle | yaml.TaggedStyle: '\'',
	} {
		if got := QuoteChar(s); got != want {
			t.Errorf("QuoteChar(%v) = %q, want %q", s, got, want)
		}
	}
}
