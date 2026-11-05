package manager

import (
	"slices"

	"go.yaml.in/yaml/v4"
)

// IsHex reports whether s consists only of hex digits (either case) and its length is one
// of lens, e.g. IsHex(ref, 40, 64) for a full SHA-1 or SHA-256 commit hash.
func IsHex(s string, lens ...int) bool {
	if !slices.Contains(lens, len(s)) {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// QuoteChar returns the quote byte of a single- or double-quoted YAML scalar style, or 0
// for any other style.
func QuoteChar(s yaml.Style) byte {
	switch {
	case s&yaml.SingleQuotedStyle != 0:
		return '\''
	case s&yaml.DoubleQuotedStyle != 0:
		return '"'
	}
	return 0
}
