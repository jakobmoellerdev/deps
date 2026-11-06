package sbt

import (
	"strings"
)

// termKind is the kind of a parsed expression.
type termKind uint8

const (
	termNone  termKind = iota // not an expression this scanner reads
	termStr                   // a string literal
	termRef                   // a reference `a`, `a.b.c`
	termOther                 // any other expression (a call, a parenthesized expression)
)

// term is one parsed expression: a string literal (tok) or a reference (ref), with the
// objects enclosing it (scope, `A.B`; "" = top level) that the reference is resolved from.
type term struct {
	kind  termKind
	tok   token
	ref   string
	scope string
}

// module is a ModuleID expression `group %[%[%]] artifact % version [% config]` with its
// cross-version (`cross CrossVersion.full`).
type module struct {
	group, artifact, version term
	cross                    string // the operator after the group: "%", "%%" or "%%%"
	crossVersion             string // CrossVersion member of `.cross(…)`: full, binary, disabled, for3Use2_13, …; "" = none
	config                   string // the first `% config` (`"test"`, Test), as written
	line                     int
}

// defKind is the kind of a definition's value.
type defKind uint8

const (
	defString defKind = iota // `val x = "1.0"`
	defRef                   // `val x = y.z`
	defModule                // `val x = "g" %% "a" % "1.0"`
	defSeq                   // `val x = Seq(a, "b")`
)

// def is a `val`, `lazy val`, `var` or parameterless `def` whose value this scanner reads.
type def struct {
	path  string // qualified by its enclosing objects: `Dependencies.Compile.scalaXml`
	kind  defKind
	tok   token   // defString
	ref   string  // defRef
	mod   *module // defModule
	items []term  // defSeq
}

// setting is a `scalaVersion := …` setting and the dep it produces.
type setting struct {
	value term
	line  int
}

// fileScan is what one sbt or Scala file declares.
type fileScan struct {
	modules   []found
	defs      []def
	scala     []setting // scalaVersion settings in file order
	cross     []term    // first element (or the reference) of each crossScalaVersions setting
	resolvers []string  // `"name" at "url"` URLs of resolvers settings
}

// found is a ModuleID in a dependency position, with its role.
type found struct {
	mod            *module
	plugin         bool   // addSbtPlugin
	compilerPlugin bool   // addCompilerPlugin, compilerPlugin
	owner          string // the definition the module is the value of; "" = none
}

type scanner struct {
	toks  []token
	out   fileScan
	objs  []scope // enclosing objects
	brace int
}

type scope struct {
	name  string
	depth int // brace depth inside the object's body
}

// scan reads the deps, definitions and settings of an sbt or Scala build file.
func scan(src []byte) fileScan {
	s := &scanner{toks: lex(src)}
	for i := 0; i < len(s.toks); {
		i = s.step(i)
	}
	return s.out
}

func (s *scanner) tok(i int) token {
	if i < 0 || i >= len(s.toks) {
		return token{kind: tOther}
	}
	return s.toks[i]
}

// afterDot reports whether the token at i follows a `.` (a member, not the start of a path).
func (s *scanner) afterDot(i int) bool { return s.tok(i-1).is(tPunct, ".") }

// step reads the construct at i and returns the index to continue at.
func (s *scanner) step(i int) int {
	t := s.toks[i]
	switch {
	case t.is(tPunct, "{"):
		s.brace++
		return i + 1
	case t.is(tPunct, "}"):
		s.brace--
		for len(s.objs) > 0 && s.objs[len(s.objs)-1].depth > s.brace {
			s.objs = s.objs[:len(s.objs)-1]
		}
		return i + 1
	case t.kind == tIdent && !s.afterDot(i):
		switch t.text {
		case "object", "class", "trait":
			if n := s.tok(i + 1); n.kind == tIdent {
				// The body is the next brace; bodies without braces hold no definitions here.
				for j := i + 2; j < len(s.toks) && j < i+64; j++ {
					if s.toks[j].is(tPunct, "{") {
						s.objs = append(s.objs, scope{name: n.text, depth: s.brace + 1})
						break
					}
					if s.toks[j].is(tPunct, "}") || s.toks[j].kind == tIdent && (s.toks[j].text == "object" || s.toks[j].text == "val" || s.toks[j].text == "def") {
						break
					}
				}
				return i + 2
			}
		case "val", "var", "def":
			if j, ok := s.definition(i); ok {
				return j
			}
		case "addSbtPlugin", "addCompilerPlugin", "compilerPlugin":
			if s.tok(i+1).is(tPunct, "(") {
				j := i + 2
				for s.tok(j).is(tPunct, "(") {
					j++
				}
				if m, next, ok := s.module(j); ok {
					s.out.modules = append(s.out.modules, found{mod: m, plugin: t.text == "addSbtPlugin", compilerPlugin: t.text != "addSbtPlugin"})
					return next
				}
			}
		case "scalaVersion":
			if j, ok := s.assignment(i); ok {
				v, next := s.term(j)
				if v.kind != termNone {
					s.out.scala = append(s.out.scala, setting{value: v, line: t.line})
				}
				return next
			}
		case "crossScalaVersions":
			if j, ok := s.assignment(i); ok {
				if items, next, ok := s.seq(j); ok {
					if len(items) > 0 {
						s.out.cross = append(s.out.cross, items[0])
					}
					return next
				}
				v, next := s.term(j)
				if v.kind == termRef {
					s.out.cross = append(s.out.cross, term{kind: termRef, ref: v.ref + ".head", scope: v.scope}) // a Seq definition
				}
				return next
			}
		case "resolvers":
			if j, ok := s.assignment(i); ok {
				return s.resolvers(j)
			}
		}
	}
	if t.kind == tString || t.kind == tIdent && !s.afterDot(i) {
		if m, next, ok := s.module(i); ok {
			s.out.modules = append(s.out.modules, found{mod: m})
			return next
		}
	}
	return i + 1
}

// assignment returns the index after `:=`, `+=` or `++=` following the setting key at i
// (`key in ThisBuild :=` and `(ThisBuild / key) :=` included).
func (s *scanner) assignment(i int) (int, bool) {
	j := i + 1
	if s.tok(j).is(tIdent, "in") && s.tok(j+1).kind == tIdent {
		j += 2
	}
	if s.tok(j).is(tPunct, ")") {
		j++
	}
	switch t := s.tok(j); {
	case t.is(tOp, ":="), t.is(tOp, "+="), t.is(tOp, "++="):
		return j + 1, true
	}
	return 0, false
}

// path reads a reference `a.b.c` starting at the identifier at i.
func (s *scanner) path(i int) (string, int) {
	var b strings.Builder
	b.WriteString(s.toks[i].text)
	j := i + 1
	for s.tok(j).is(tPunct, ".") && s.tok(j+1).kind == tIdent {
		b.WriteByte('.')
		b.WriteString(s.toks[j+1].text)
		j += 2
	}
	return b.String(), j
}

// skipGroup returns the index after the bracketed group opening at i.
func (s *scanner) skipGroup(i int) int {
	depth := 0
	for j := i; j < len(s.toks); j++ {
		switch t := s.toks[j]; {
		case t.is(tPunct, "("), t.is(tPunct, "["), t.is(tPunct, "{"):
			depth++
		case t.is(tPunct, ")"), t.is(tPunct, "]"), t.is(tPunct, "}"):
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(s.toks)
}

// term reads the expression at i: a string literal, a reference, or (a call or a
// parenthesized expression) termOther.
func (s *scanner) term(i int) (term, int) {
	t := s.tok(i)
	switch {
	case t.kind == tString:
		return term{kind: termStr, tok: t}, i + 1
	case t.kind == tIdent && !keyword(t.text):
		ref, j := s.path(i)
		if s.tok(j).is(tPunct, "(") || s.tok(j).is(tPunct, "[") {
			return term{kind: termOther}, s.skipGroup(j)
		}
		return term{kind: termRef, ref: ref, scope: strings.TrimSuffix(s.qualify(""), ".")}, j
	case t.is(tPunct, "("):
		return term{kind: termOther}, s.skipGroup(i)
	}
	return term{}, i
}

func keyword(s string) bool {
	switch s {
	case "val", "var", "def", "lazy", "if", "else", "new", "match", "case", "object", "class", "trait",
		"import", "package", "return", "yield", "for", "while", "do", "true", "false", "null", "this", "super":
		return true
	}
	return false
}

// module reads a ModuleID `group % artifact % version` (`%%`/`%%%` after the group) with its
// `% config` and `cross CrossVersion.x` / `.cross(CrossVersion.x)` suffixes, starting at i.
func (s *scanner) module(i int) (*module, int, bool) {
	g, j := s.term(i)
	if g.kind != termStr && g.kind != termRef {
		return nil, i, false
	}
	op := s.tok(j)
	if op.kind != tOp || op.text != "%" && op.text != "%%" && op.text != "%%%" {
		return nil, i, false
	}
	a, j := s.term(j + 1)
	if a.kind != termStr && a.kind != termRef || !s.tok(j).is(tOp, "%") {
		return nil, i, false
	}
	v, j := s.term(j + 1)
	if v.kind == termNone {
		return nil, i, false
	}
	m := &module{group: g, artifact: a, version: v, cross: op.text, line: s.toks[i].line}
	for s.tok(j).is(tOp, "%") {
		c, next := s.term(j + 1)
		if c.kind == termNone {
			break
		}
		if m.config == "" {
			switch c.kind {
			case termStr:
				m.config = c.tok.text
			case termRef:
				m.config = c.ref
			}
		}
		j = next
	}
	k := j
	if s.tok(k).is(tPunct, ")") {
		k++
	}
	switch {
	case s.tok(k).is(tPunct, ".") && s.tok(k+1).is(tIdent, "cross") && s.tok(k+2).is(tPunct, "("):
		if c, _ := s.term(k + 3); c.kind == termRef {
			m.crossVersion = crossMember(c.ref)
		}
	case s.tok(k).is(tIdent, "cross"):
		if c, _ := s.term(k + 1); c.kind == termRef {
			m.crossVersion = crossMember(c.ref)
		}
	}
	return m, j, true
}

// crossMember returns the member of a `CrossVersion.<member>` reference.
func crossMember(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "CrossVersion."); ok {
		return rest
	}
	return ""
}

// definition reads `val|var|def name [: Type] = value` at i, recording the value when it is a
// string, a reference, a ModuleID or a Seq/List of strings and references.
func (s *scanner) definition(i int) (int, bool) {
	n := s.tok(i + 1)
	if n.kind != tIdent || keyword(n.text) {
		return 0, false
	}
	j := i + 2
	if s.tok(j).is(tOp, ":") { // type annotation
		for k := j + 1; k < len(s.toks) && k < j+32; k++ {
			if s.toks[k].is(tOp, "=") {
				j = k
				break
			}
			if s.toks[k].is(tPunct, "{") || s.toks[k].is(tPunct, "}") || s.toks[k].is(tPunct, "(") && s.toks[i].text == "def" {
				return 0, false
			}
		}
	}
	if !s.tok(j).is(tOp, "=") {
		return 0, false
	}
	j++
	d := def{path: s.qualify(n.text)}
	v := s.tok(j)
	switch {
	case v.kind == tString && s.tok(j+1).kind != tOp && !s.tok(j+1).is(tPunct, "."):
		d.kind, d.tok = defString, v
		s.out.defs = append(s.out.defs, d)
		return j + 1, true
	case v.is(tIdent, "new") && s.tok(j+1).is(tPunct, "{"):
		// An anonymous object: its vals are `name.field`.
		s.objs = append(s.objs, scope{name: n.text, depth: s.brace + 1})
		return j + 1, true
	case v.kind == tIdent && (v.text == "Seq" || v.text == "List" || v.text == "Vector") && s.tok(j+1).is(tPunct, "("):
		if items, next, ok := s.seq(j); ok {
			d.kind, d.items = defSeq, items
			s.out.defs = append(s.out.defs, d)
			return next, true
		}
	}
	start := j
	if s.tok(j).is(tPunct, "(") { // `val x = ("g" %% "a" % "1").cross(…)`
		start = j + 1
	}
	if m, next, ok := s.module(start); ok {
		d.kind, d.mod = defModule, m
		s.out.defs = append(s.out.defs, d)
		s.out.modules = append(s.out.modules, found{mod: m, owner: d.path})
		return next, true
	}
	if v.kind == tIdent && !keyword(v.text) {
		ref, next := s.path(j)
		if !s.tok(next).is(tPunct, "(") && !s.tok(next).is(tPunct, "[") && s.tok(next).kind != tOp && !s.tok(next).is(tPunct, "{") {
			d.kind, d.ref = defRef, ref
			s.out.defs = append(s.out.defs, d)
			return next, true
		}
	}
	return j, true
}

// qualify returns name qualified by the enclosing objects.
func (s *scanner) qualify(name string) string {
	if len(s.objs) == 0 {
		return name
	}
	var b strings.Builder
	for _, o := range s.objs {
		b.WriteString(o.name)
		b.WriteByte('.')
	}
	b.WriteString(name)
	return b.String()
}

// seq reads `Seq(a, "b", …)` (or List, Vector) at i: its elements when each is a string or
// a reference.
func (s *scanner) seq(i int) ([]term, int, bool) {
	t := s.tok(i)
	if t.kind != tIdent || t.text != "Seq" && t.text != "List" && t.text != "Vector" || !s.tok(i+1).is(tPunct, "(") {
		return nil, i, false
	}
	end := s.skipGroup(i + 1)
	var items []term
	for j := i + 2; j < end-1; {
		v, next := s.term(j)
		if v.kind != termStr && v.kind != termRef || next >= end || !s.tok(next).is(tPunct, ",") && next != end-1 {
			return nil, i, false // not a plain list
		}
		items = append(items, v)
		j = next + 1
	}
	return items, end, true
}

// resolvers records the `"name" at "url"` URLs of a resolvers setting value at i: one
// resolver, or a Seq/List of them.
func (s *scanner) resolvers(i int) int {
	end := i + 3
	if t := s.tok(i); t.kind == tIdent && (t.text == "Seq" || t.text == "List") && s.tok(i+1).is(tPunct, "(") {
		end = s.skipGroup(i + 1)
	}
	end = min(end, len(s.toks))
	for j := i; j+2 < end; j++ {
		if s.toks[j].kind == tString && s.toks[j+1].is(tIdent, "at") && s.toks[j+2].kind == tString {
			if u := s.toks[j+2]; u.plain && (strings.HasPrefix(u.text, "https://") || strings.HasPrefix(u.text, "http://")) {
				s.out.resolvers = append(s.out.resolvers, u.text)
			}
		}
	}
	return max(end, i+1)
}
