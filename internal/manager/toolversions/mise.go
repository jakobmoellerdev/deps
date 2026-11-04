package toolversions

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// ghRepoRe is a github.com owner/repo; owners cannot contain dots, so `gitlab.com/x` is no match.
var ghRepoRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

// miseLookup resolves a mise tool key: a tool name, `core:<tool>`, `github:` or
// `ubi:<owner>/<repo>[options]` (github-releases) or `npm:<package>`. Other backends are
// unsupported.
func miseLookup(key string) tool {
	backend, name, ok := strings.Cut(key, ":")
	if !ok {
		return lookup(key)
	}
	switch backend {
	case "core":
		return lookup(name)
	case "github", "ubi":
		if repo, _, _ := strings.Cut(name, "["); ghRepoRe.MatchString(repo) {
			return githubTool(repo)
		}
	case "npm":
		if name != "" {
			return npmTool(name)
		}
	}
	return tool{name: key, skip: skipTool}
}

// extractMise returns the tools of a mise config: the entries of the `[tools]` table
// (`tool = "v"`, `tool = ["v", …]` (first only), `tool = { version = "v", … }`), dotted keys
// to them and `[tools.<tool>]` tables with a `version` key. The file is scanned as TOML
// without decoding it, so spans point into src.
func extractMise(file string, src []byte) ([]dep.Dep, error) {
	s := tomlScanner{src: src}
	if bytes.HasPrefix(src, bom) {
		s.pos = len(bom)
	}
	var deps []dep.Dep
	var ls []int
	err := s.document(func(path []string, v *tomlValue) {
		var key string
		switch {
		case len(path) == 2 && path[0] == "tools":
			key, v = path[1], v.toolVersion()
		case len(path) == 3 && path[0] == "tools" && path[2] == "version" && v.kind == kindString:
			key = path[1]
		}
		if key == "" || v == nil {
			return
		}
		if ls == nil {
			ls = lineStarts(src)
		}
		t := miseLookup(key)
		if t.skip == "" && !v.exact {
			t.skip = "cannot locate value"
		}
		deps = append(deps, newDep(NameMise, DepTypeTool, file, lineAt(ls, v.start), t, v.str, v.start))
	})
	return deps, err
}

const (
	kindOther = iota
	kindString
	kindArray
	kindTable
)

// tomlValue is a scanned TOML value. Only what tool entries need is kept: the content and
// offset of strings, the first element of arrays and the `version` key of inline tables.
type tomlValue struct {
	kind    int
	str     string     // string: content (raw, escapes not decoded)
	start   int        // string: offset of the content
	exact   bool       // string: single-line without escapes, so src[start:start+len(str)] == str
	first   *tomlValue // array: first element
	version *tomlValue // inline table: the `version` key
}

// toolVersion returns the version string of a tool entry: the value itself, the first element
// of an array or the version key of an inline table; nil when there is none.
func (v *tomlValue) toolVersion() *tomlValue {
	switch v.kind {
	case kindString:
		return v
	case kindArray:
		if v.first != nil && v.first.kind == kindString {
			return v.first
		}
	case kindTable:
		return v.version
	}
	return nil
}

// tomlScanner walks TOML keys and values byte by byte.
type tomlScanner struct {
	src []byte
	pos int
}

func (s *tomlScanner) errorf(format string, args ...any) error {
	line := 1 + bytes.Count(s.src[:s.pos], []byte{'\n'})
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

func (s *tomlScanner) peek() byte {
	if s.pos < len(s.src) {
		return s.src[s.pos]
	}
	return 0
}

// triple reports whether three q quotes (a multi-line string delimiter) start at the position.
func (s *tomlScanner) triple(q byte) bool {
	return s.pos+2 < len(s.src) && s.src[s.pos] == q && s.src[s.pos+1] == q && s.src[s.pos+2] == q
}

// space skips spaces and tabs.
func (s *tomlScanner) space() {
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
}

// blank skips whitespace, newlines and comments.
func (s *tomlScanner) blank() {
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		case '#':
			s.comment()
		default:
			return
		}
	}
}

// comment skips to the end of the line.
func (s *tomlScanner) comment() {
	if i := bytes.IndexByte(s.src[s.pos:], '\n'); i >= 0 {
		s.pos += i
	} else {
		s.pos = len(s.src)
	}
}

// eol requires the rest of the line to be blank or a comment.
func (s *tomlScanner) eol() error {
	s.space()
	if s.peek() == '#' {
		s.comment()
	}
	switch {
	case s.pos == len(s.src), s.peek() == '\n':
		return nil
	case s.peek() == '\r' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '\n':
		return nil
	}
	return s.errorf("expected end of line, found %q", s.peek())
}

func (s *tomlScanner) expect(c byte) error {
	if s.peek() != c {
		return s.errorf("expected %q, found %q", c, s.peek())
	}
	s.pos++
	return nil
}

// document calls entry with the full key path (table plus dotted key) of every key/value pair
// outside arrays of tables.
func (s *tomlScanner) document(entry func(path []string, v *tomlValue)) error {
	var table []string
	inArray := false
	for {
		s.blank()
		if s.pos == len(s.src) {
			return nil
		}
		if s.peek() == '[' {
			s.pos++
			inArray = s.peek() == '['
			if inArray {
				s.pos++
			}
			s.space()
			key, err := s.key()
			if err != nil {
				return err
			}
			if err := s.expect(']'); err != nil {
				return err
			}
			if inArray {
				if err := s.expect(']'); err != nil {
					return err
				}
			}
			if err := s.eol(); err != nil {
				return err
			}
			table = key
			continue
		}
		key, err := s.key()
		if err != nil {
			return err
		}
		if err := s.expect('='); err != nil {
			return err
		}
		s.space()
		v, err := s.value()
		if err != nil {
			return err
		}
		if err := s.eol(); err != nil {
			return err
		}
		if !inArray {
			entry(append(slices.Clip(table), key...), v)
		}
	}
}

// key scans a dotted key and the blanks after it.
func (s *tomlScanner) key() ([]string, error) {
	var parts []string
	for {
		var part string
		switch c := s.peek(); c {
		case '"', '\'':
			v, err := s.str(c)
			if err != nil {
				return nil, err
			}
			part = v.str
		default:
			start := s.pos
			for s.pos < len(s.src) && bareKey(s.src[s.pos]) {
				s.pos++
			}
			if s.pos == start {
				return nil, s.errorf("expected key, found %q", c)
			}
			part = string(s.src[start:s.pos])
		}
		parts = append(parts, part)
		s.space()
		if s.peek() != '.' {
			return parts, nil
		}
		s.pos++
		s.space()
	}
}

func bareKey(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-'
}

// value scans a value: a string, an array, an inline table or another scalar.
func (s *tomlScanner) value() (*tomlValue, error) {
	switch c := s.peek(); c {
	case '"', '\'':
		return s.str(c)
	case '[':
		return s.array()
	case '{':
		return s.table()
	}
	start := s.pos
	for s.pos < len(s.src) && strings.IndexByte(" \t\r\n,]}#", s.src[s.pos]) < 0 {
		s.pos++
	}
	if s.pos == start {
		return nil, s.errorf("expected value, found %q", s.peek())
	}
	return &tomlValue{kind: kindOther}, nil
}

// str scans a basic (q is a double quote) or literal (q is a single quote) string, single- or
// multi-line.
func (s *tomlScanner) str(q byte) (*tomlValue, error) {
	if s.triple(q) {
		s.pos += 3
		start := s.pos
		for {
			if s.pos >= len(s.src) {
				return nil, s.errorf("unterminated string")
			}
			if q == '"' && s.src[s.pos] == '\\' {
				s.pos += 2
				continue
			}
			if s.triple(q) {
				// Up to two quotes may precede the closing delimiter.
				for s.pos+3 < len(s.src) && s.src[s.pos+3] == q {
					s.pos++
				}
				v := &tomlValue{kind: kindString, str: string(s.src[start:s.pos]), start: start}
				s.pos += 3
				return v, nil
			}
			s.pos++
		}
	}
	s.pos++
	start := s.pos
	exact := true
	for {
		if s.pos >= len(s.src) || s.src[s.pos] == '\n' {
			return nil, s.errorf("unterminated string")
		}
		switch c := s.src[s.pos]; {
		case c == q:
			v := &tomlValue{kind: kindString, str: string(s.src[start:s.pos]), start: start, exact: exact}
			s.pos++
			return v, nil
		case c == '\\' && q == '"':
			exact = false
			s.pos++
		}
		s.pos++
	}
}

// array scans an array, possibly spanning lines with comments.
func (s *tomlScanner) array() (*tomlValue, error) {
	s.pos++
	a := &tomlValue{kind: kindArray}
	for {
		s.blank()
		if s.peek() == ']' {
			s.pos++
			return a, nil
		}
		if s.pos == len(s.src) {
			return nil, s.errorf("unterminated array")
		}
		v, err := s.value()
		if err != nil {
			return nil, err
		}
		if a.first == nil {
			a.first = v
		}
		s.blank()
		switch s.peek() {
		case ',':
			s.pos++
		case ']':
			s.pos++
			return a, nil
		default:
			return nil, s.errorf("expected ',' or ']' in array, found %q", s.peek())
		}
	}
}

// table scans an inline table; newlines are tolerated (TOML 1.1).
func (s *tomlScanner) table() (*tomlValue, error) {
	s.pos++
	t := &tomlValue{kind: kindTable}
	for {
		s.blank()
		if s.peek() == '}' {
			s.pos++
			return t, nil
		}
		if s.pos == len(s.src) {
			return nil, s.errorf("unterminated inline table")
		}
		key, err := s.key()
		if err != nil {
			return nil, err
		}
		if err := s.expect('='); err != nil {
			return nil, err
		}
		s.space()
		v, err := s.value()
		if err != nil {
			return nil, err
		}
		if len(key) == 1 && key[0] == "version" && v.kind == kindString {
			t.version = v
		}
		s.blank()
		switch s.peek() {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return t, nil
		default:
			return nil, s.errorf("expected ',' or '}' in inline table, found %q", s.peek())
		}
	}
}
