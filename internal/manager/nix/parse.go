package nix

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// binding is a string attribute below the top-level `inputs` of a flake.nix: path is its
// attribute path below `inputs` (`nixpkgs.url` = ["nixpkgs", "url"]; a `${…}` element is ""),
// value the string. span locates value when the file holds it verbatim (a "…" string without
// escapes or interpolation, a single-line ”…” string without escapes, or a URI literal);
// value is "" and span zero otherwise.
type binding struct {
	path  []string
	value string
	span  dep.Span
	line  int
}

// Token kinds; punctuation is its own byte.
const (
	tEOF    = 0
	tIdent  = 'a'
	tString = 's' // "…", ''…'' or a URI literal
	tOther  = 'o' // numbers, paths, operators
)

type token struct {
	kind       byte
	start, end int    // the token's bytes
	text       string // identifier; string value when plain
	plain      bool   // string: text is its value and content its bytes
	content    dep.Span
}

// lexer tokenizes Nix source as far as the inputs scanner needs: identifiers, strings (with
// their interpolations skipped), URI literals and single-byte punctuation; comments and
// whitespace are skipped.
type lexer struct {
	src []byte
	pos int
}

func (l *lexer) next() token {
	l.skipTrivia()
	if l.pos >= len(l.src) {
		return token{kind: tEOF, start: len(l.src), end: len(l.src)}
	}
	start, c := l.pos, l.src[l.pos]
	switch {
	case c == '"':
		return l.lexString()
	case c == '\'' && l.peek(1) == '\'':
		return l.lexIndented()
	case isLetter(c):
		if end := l.uriEnd(); end > 0 {
			l.pos = end
			return token{kind: tString, start: start, end: end, text: string(l.src[start:end]), plain: true, content: dep.Span{Start: start, End: end}}
		}
		l.pos++
		for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
			l.pos++
		}
		return token{kind: tIdent, start: start, end: l.pos, text: string(l.src[start:l.pos])}
	case '0' <= c && c <= '9':
		for l.pos < len(l.src) && (isIdentChar(l.src[l.pos]) || l.src[l.pos] == '.') {
			l.pos++
		}
		return token{kind: tOther, start: start, end: l.pos}
	case strings.IndexByte("{}[]();=.$", c) >= 0:
		l.pos++
		return token{kind: c, start: start, end: l.pos}
	}
	l.pos++
	return token{kind: tOther, start: start, end: l.pos}
}

func (l *lexer) peek(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

// skipTrivia skips whitespace, `#` line comments and `/* */` block comments.
func (l *lexer) skipTrivia() {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			l.pos++
		case c == '#':
			if i := bytes.IndexByte(l.src[l.pos:], '\n'); i >= 0 {
				l.pos += i + 1
			} else {
				l.pos = len(l.src)
			}
		case c == '/' && l.peek(1) == '*':
			if i := bytes.Index(l.src[l.pos+2:], []byte("*/")); i >= 0 {
				l.pos += i + 4
			} else {
				l.pos = len(l.src)
			}
		default:
			return
		}
	}
}

// uriEnd returns the end of the URI literal at l.pos (Nix's
// `[a-zA-Z][a-zA-Z0-9+\-.]*:[a-zA-Z0-9%/?:@&=+$,\-_.!~*']+`), or 0 when there is none.
func (l *lexer) uriEnd() int {
	i := l.pos + 1
	for i < len(l.src) && (isLetter(l.src[i]) || '0' <= l.src[i] && l.src[i] <= '9' || strings.IndexByte("+-.", l.src[i]) >= 0) {
		i++
	}
	if i+1 >= len(l.src) || l.src[i] != ':' || !isURIChar(l.src[i+1]) {
		return 0
	}
	for i++; i < len(l.src) && isURIChar(l.src[i]); i++ {
	}
	return i
}

// lexString reads a "…" string at l.pos. It is plain without `\` escapes and `${…}`
// interpolations (skipped as nested expressions); `$${` is a literal `$${`.
func (l *lexer) lexString() token {
	t := token{kind: tString, start: l.pos, plain: true}
	l.pos++
	t.content.Start = l.pos
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == '"':
			t.content.End = l.pos
			l.pos++
			return l.finish(t)
		case c == '\\':
			t.plain = false
			l.pos += 2
		case c == '$' && l.peek(1) == '$':
			l.pos += 2
		case c == '$' && l.peek(1) == '{':
			t.plain = false
			l.pos += 2
			l.skipInterpolation()
		default:
			l.pos++
		}
	}
	t.content.End = len(l.src)
	l.pos = len(l.src)
	return l.finish(t)
}

// lexIndented reads a ”…” string at l.pos. It is plain on a single line without `”`
// escapes (`”'`, `”$`, `”\x`) and interpolations; indentation stripping only matters
// across lines.
func (l *lexer) lexIndented() token {
	t := token{kind: tString, start: l.pos, plain: true}
	l.pos += 2
	t.content.Start = l.pos
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == '\'' && l.peek(1) == '\'':
			switch l.peek(2) {
			case '\'', '$':
				t.plain = false
				l.pos += 3
			case '\\':
				t.plain = false
				l.pos += 4
			default:
				t.content.End = l.pos
				l.pos += 2
				return l.finish(t)
			}
		case c == '\n':
			t.plain = false
			l.pos++
		case c == '$' && l.peek(1) == '$':
			l.pos += 2
		case c == '$' && l.peek(1) == '{':
			t.plain = false
			l.pos += 2
			l.skipInterpolation()
		default:
			l.pos++
		}
	}
	t.content.End = len(l.src)
	l.pos = len(l.src)
	return l.finish(t)
}

func (l *lexer) finish(t token) token {
	t.end = l.pos
	if t.plain {
		t.text = string(l.src[t.content.Start:t.content.End])
	} else {
		t.content = dep.Span{}
	}
	return t
}

// skipInterpolation skips the expression of a `${` up to its closing brace.
func (l *lexer) skipInterpolation() {
	depth := 1
	for {
		switch t := l.next(); t.kind {
		case tEOF:
			return
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return
			}
		}
	}
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isIdentChar(c byte) bool {
	return isLetter(c) || '0' <= c && c <= '9' || c == '_' || c == '\'' || c == '-'
}

func isURIChar(c byte) bool {
	return isLetter(c) || '0' <= c && c <= '9' || strings.IndexByte("%/?:@&=+$,-_.!~*'", c) >= 0
}

// parser reads the top-level attribute set of a flake.nix and collects the string bindings
// below `inputs`, whether written `inputs.x.url = …;` or `inputs = { x = { url = …; }; };`.
// Every other value is skipped as an expression up to its `;`.
type parser struct {
	lex      lexer
	back     *token
	bindings []binding
}

func (p *parser) next() token {
	if p.back != nil {
		t := *p.back
		p.back = nil
		return t
	}
	return p.lex.next()
}

func (p *parser) unread(t token) { p.back = &t }

func (p *parser) line(off int) int { return 1 + bytes.Count(p.lex.src[:off], []byte{'\n'}) }

// parseInputs returns the string bindings below the `inputs` attribute of a flake.nix, in
// file order. A file that is not an attribute set has none.
func parseInputs(src []byte) []binding {
	p := &parser{lex: lexer{src: src}}
	if p.next().kind != '{' {
		return nil
	}
	p.attrs(nil)
	return p.bindings
}

// attrs reads the bindings of an attribute set up to its closing brace; prefix is the
// attribute path of the set.
func (p *parser) attrs(prefix []string) {
	for {
		t := p.next()
		switch {
		case t.kind == '}' || t.kind == tEOF:
			return
		case t.kind == tIdent && t.text == "inherit":
			p.skipExpr()
			continue
		}
		path, ok := p.attrPath(t)
		if !ok {
			p.skipExpr()
			continue
		}
		full := append(append([]string(nil), prefix...), path...)
		if full[0] != "inputs" {
			p.skipExpr()
			continue
		}
		switch v := p.next(); v.kind {
		case '{':
			p.attrs(full)
			p.skipExpr()
		case tString:
			end := p.next()
			if end.kind != ';' {
				p.unread(end)
				p.skipExpr()
				break
			}
			b := binding{path: full[1:], line: p.line(v.start)}
			if v.plain {
				b.value, b.span = v.text, v.content
			}
			p.bindings = append(p.bindings, b)
		default:
			p.unread(v)
			p.skipExpr()
		}
	}
}

// attrPath reads an attribute path starting at t through its `=`: identifiers and strings
// separated by `.`; a `${…}` element reads as "". ok is false when no `=` follows.
func (p *parser) attrPath(t token) ([]string, bool) {
	var path []string
	for {
		switch t.kind {
		case tIdent:
			path = append(path, t.text)
		case tString:
			path = append(path, t.text)
		case '$':
			if p.next().kind != '{' {
				return nil, false
			}
			p.lex.skipInterpolation()
			path = append(path, "")
		default:
			p.unread(t)
			return nil, false
		}
		switch sep := p.next(); sep.kind {
		case '.':
			t = p.next()
		case '=':
			return path, true
		default:
			p.unread(sep)
			return nil, false
		}
	}
}

// skipExpr skips tokens up to the `;` ending the current binding (consumed) or the `}`
// closing the enclosing set (left unread). Brackets nest (a stray `]` or `)` is skipped);
// `let` opens a level `in` closes, and `with` and `assert` one their own `;` closes.
func (p *parser) skipExpr() {
	var stack []byte
	for {
		t := p.next()
		switch t.kind {
		case tEOF:
			return
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '(':
			stack = append(stack, ')')
		case '}':
			if len(stack) == 0 {
				p.unread(t)
				return
			}
			stack = stack[:len(stack)-1]
		case ']', ')':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case ';':
			if len(stack) == 0 {
				return
			}
			if stack[len(stack)-1] == ';' {
				stack = stack[:len(stack)-1]
			}
		case tIdent:
			switch t.text {
			case "let":
				stack = append(stack, 'i')
			case "in":
				if len(stack) > 0 && stack[len(stack)-1] == 'i' {
					stack = stack[:len(stack)-1]
				}
			case "with", "assert":
				stack = append(stack, ';')
			}
		}
	}
}

// refParts are the ref and rev a flake reference URL names, with their spans in the file
// when the URL is written verbatim (zero otherwise, and for a percent-encoded value).
type refParts struct {
	ref, rev         string
	refSpan, revSpan dep.Span
}

// parseRef reads the ref and rev of a flake reference URL whose first byte is at offset off
// (off < 0: the URL is not in the file): the `ref`/`rev` query parameters, the third path
// segment of `github:`, `gitlab:` and `sourcehut:` references, the `<ref>` of a
// `…/archive/<ref>.tar.gz` tarball and the channel of a NixOS channel tarball; a path segment
// that is a 40- or 64-hex commit is the rev.
func parseRef(s string, off int) refParts {
	var r refParts
	set := func(v string, start int, rev bool) {
		span := dep.Span{}
		if off >= 0 && !strings.ContainsRune(v, '%') {
			span = dep.Span{Start: off + start, End: off + start + len(v)}
		}
		if rev {
			r.rev, r.revSpan = v, span
		} else {
			r.ref, r.refSpan = v, span
		}
	}
	base, query, _ := strings.Cut(s, "?")
	pos := len(base) + 1
	for kv := range strings.SplitSeq(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if k == "ref" || k == "rev" {
			set(v, pos+len(k)+1, k == "rev")
		}
		pos += len(kv) + 1
	}
	scheme, rest, ok := strings.Cut(base, ":")
	switch {
	case !ok:
	case scheme == "github" || scheme == "gitlab" || scheme == "sourcehut":
		if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[2] != "" {
			set(parts[2], len(base)-len(parts[2]), isCommit(parts[2]))
		}
	default:
		if i := strings.LastIndex(base, "/archive/"); i >= 0 && strings.HasSuffix(base, ".tar.gz") {
			if v := base[i+len("/archive/") : len(base)-len(".tar.gz")]; v != "" {
				set(v, i+len("/archive/"), isCommit(v))
			}
			break
		}
		u := strings.TrimPrefix(base, "tarball+")
		if m := channelRe.FindStringSubmatchIndex(u); m != nil {
			set(u[m[2]:m[3]], len(base)-len(u)+m[2], false)
		}
	}
	return r
}

// isCommit reports whether s is a full SHA-1 or SHA-256 commit hash.
func isCommit(s string) bool { return manager.IsHex(s, 40, 64) }
