// Package properties reads Java properties files with byte spans, for byte-span edits of the
// Gradle and Maven wrapper properties.
package properties

import (
	"bytes"
	"strings"
)

// Property is a key's raw value (escapes kept) with its byte span in the file and its
// 1-based line.
type Property struct {
	Value      string
	Start, End int
	Line       int
}

// Parse returns the single-line `key=value`, `key = value`, `key: value` and `key value`
// entries of a Java properties file; the last occurrence of a key wins. Values keep their
// escapes; continuation lines are not joined.
func Parse(src []byte) map[string]Property {
	props := map[string]Property{}
	off := 0
	for n, line := range bytes.SplitAfter(src, []byte("\n")) {
		start := off
		off += len(line)
		line = bytes.TrimRight(line, "\r\n")
		i := 0
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i == len(line) || line[i] == '#' || line[i] == '!' {
			continue
		}
		k := i
		for i < len(line) && !isSpace(line[i]) && line[i] != '=' && line[i] != ':' {
			if line[i] == '\\' {
				i++
			}
			i++
		}
		key := string(line[k:min(i, len(line))])
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i < len(line) && (line[i] == '=' || line[i] == ':') {
			i++
		}
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		v := bytes.TrimRight(line[min(i, len(line)):], " \t\f")
		vs := start + min(i, len(line))
		props[key] = Property{Value: string(v), Start: vs, End: vs + len(v), Line: n + 1}
	}
	return props
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\f' }

// Unescape drops the backslash of properties escapes such as `\:`.
func Unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Values returns the unescaped value of every key of src.
func Values(src []byte) map[string]string {
	props := Parse(src)
	out := make(map[string]string, len(props))
	for k, p := range props {
		out[Unescape(k)] = Unescape(p.Value)
	}
	return out
}
