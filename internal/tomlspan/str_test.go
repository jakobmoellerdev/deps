package tomlspan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// strByteWise is the byte-at-a-time str that copied every plain byte separately; str must
// return the same value, error and parser position for every input.
func (p *parser) strByteWise() (*Value, error) {
	line := p.line
	q := p.peek()
	multi := p.hasPrefix(string([]byte{q, q, q}))
	delim := 1
	if multi {
		delim = 3
	}
	p.pos += delim
	if multi {
		p.newline()
	}
	var b strings.Builder
	exact := true
	contentStart := p.pos
	for {
		if p.eof() {
			return nil, p.errf("unterminated string")
		}
		c := p.peek()
		if c == q && (!multi || p.closesMulti(q)) {
			end := p.pos
			if multi {
				for n := 0; n < 2 && p.pos+3 < len(p.src) && p.src[p.pos+3] == q; n++ {
					b.WriteByte(q)
					p.pos++
					end++
				}
			}
			p.pos += delim
			s := b.String()
			return &Value{Kind: String, Str: s, Start: contentStart, End: end, Exact: exact && s == string(p.src[contentStart:end]), Line: line}, nil
		}
		switch {
		case c == '\n' || c == '\r':
			if !multi {
				return nil, p.errf("newline in string")
			}
			nlStart := p.pos
			if !p.newline() {
				return nil, p.errf("bare carriage return in string")
			}
			b.Write(p.src[nlStart:p.pos])
		case c == '\\' && q == '"':
			exact = false
			if err := p.escape(&b, multi); err != nil {
				return nil, err
			}
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

// checkStrParity parses the string starting at every quote of src with str and strByteWise.
func checkStrParity(t *testing.T, src []byte) {
	t.Helper()
	for i, c := range src {
		if c != '"' && c != '\'' {
			continue
		}
		got, want := &parser{src: src, pos: i, line: 1}, &parser{src: src, pos: i, line: 1}
		gv, gerr := got.str()
		wv, werr := want.strByteWise()
		if !reflect.DeepEqual(gv, wv) || fmt.Sprint(gerr) != fmt.Sprint(werr) || got.pos != want.pos || got.line != want.line {
			t.Fatalf("str at %d of %q: got %+v, %v, pos %d, line %d; want %+v, %v, pos %d, line %d",
				i, src, gv, gerr, got.pos, got.line, wv, werr, want.pos, want.line)
		}
	}
}

func TestStrParityFixtures(t *testing.T) {
	for _, src := range fixtures(t) {
		checkStrParity(t, src)
	}
}

func FuzzStrParity(f *testing.F) {
	for _, s := range []string{
		`"plain"`, `'lit\eral'`, `"a\tb\u00e9\"c"`, "\"\"\"\nmulti\r\nline\"\"\"\"\"", "'''x\n''y'''''",
		`"unterminated`, "\"new\nline\"", "\"\"\"bare\rcr\"\"\"", `"bad \x escape"`, `"\`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(checkStrParity)
}
