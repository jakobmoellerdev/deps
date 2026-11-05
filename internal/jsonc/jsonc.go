// Package jsonc turns JSON with comments and trailing commas (JSONC: devcontainer.json,
// bun.lock) into JSON a strict decoder reads, keeping every byte offset.
package jsonc

import (
	"bytes"
	"slices"
)

// bom is the UTF-8 byte order mark.
var bom = []byte("\ufeff")

// Strip returns a copy of src with a leading byte order mark, `//` and `/* */` comments
// (outside strings) and trailing commas (before `}` or `]`) replaced by blanks. Newlines in
// block comments are kept, so byte offsets and line numbers stay those of src.
func Strip(src []byte) []byte {
	out := slices.Clone(src)
	i := 0
	if bytes.HasPrefix(out, bom) {
		blank(out[:len(bom)])
		i = len(bom)
	}
	comma := -1
	for i < len(out) {
		switch c := out[i]; {
		case c == '"':
			comma = -1
			i = stringEnd(out, i+1)
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			end := bytes.IndexByte(out[i:], '\n')
			if end < 0 {
				end = len(out) - i
			}
			blank(out[i : i+end])
			i += end
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			end := bytes.Index(out[i+2:], []byte("*/"))
			if end < 0 {
				end = len(out) // unterminated: the decoder reports the rest as invalid
			} else {
				end += i + 4
			}
			blank(out[i:end])
			i = end
			continue
		case c == ',':
			comma = i
		case c == '}' || c == ']':
			if comma >= 0 {
				out[comma] = ' '
			}
			comma = -1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			comma = -1
		}
		i++
	}
	return out
}

// stringEnd returns the offset after the closing quote of the string whose content starts at
// i, or len(b) when it is unterminated.
func stringEnd(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		}
		i++
	}
	return len(b)
}

// blank replaces the bytes of b except line breaks by spaces.
func blank(b []byte) {
	for i, c := range b {
		if c != '\n' && c != '\r' {
			b[i] = ' '
		}
	}
}
