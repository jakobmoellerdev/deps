package gradle

import (
	"bytes"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// maxDepth bounds the nesting of map literals and catalog alias tables.
const maxDepth = 32

// def is a variable definition with a literal string value.
type def struct {
	name  string // canonical name (dep.Dep.VersionRef)
	value string
	span  dep.Span
	line  int
}

// defs are the definitions of one file by every key they are referenced by.
type defs map[string]def

// add records d under each key; the first definition of a key wins.
func (m defs) add(d def, keys ...string) {
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			m[k] = d
		}
	}
}

var (
	identPathRe = regexp.MustCompile(`^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*$`)
	// getRe: versions.getValue("k"), versions.get("k").
	getRe = regexp.MustCompile(`^([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)\.(?:getValue|get)\(\s*["']([^"'\s]+)["']\s*\)$`)
	// propCallRe: property("x"), findProperty("x"), providers.gradleProperty("x").get(), ext.get("x").
	propCallRe = regexp.MustCompile(`^(?:providers\.gradleProperty|findProperty|property|(?:ext|extra)\.get)\(\s*["']([^"'\s]+)["']\s*\)(?:\.get\(\))?$`)
	// indexRe: name["k"], extra["x"].
	indexRe = regexp.MustCompile(`^([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)\[\s*["']([^"'\s]+)["']\s*\]$`)
	// exprRe is the extent of a bare reference in code (string contents blanked).
	exprRe = regexp.MustCompile(`^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*|\[[ \t]*["'] *["'][ \t]*\]|\([ \t]*["'] *["'][ \t]*\)|\(\))*(?:[ \t]+as[ \t]+String\??)?(?:!!)?`)
)

// refKey returns the definition key an expression refers to: `x`, `project.x`,
// `rootProject.ext.x`, `property("x")`, `extra["x"]`, `Versions.x`, `versions['x']`.
func refKey(expr string) (string, bool) {
	e := strings.TrimSpace(expr)
	e = strings.TrimSuffix(e, "!!")
	if i := strings.Index(e, " as "); i >= 0 {
		e = strings.TrimSpace(e[:i])
	}
	for {
		t := strings.TrimPrefix(strings.TrimPrefix(e, "project."), "rootProject.")
		if t == e {
			break
		}
		e = t
	}
	if m := propCallRe.FindStringSubmatch(e); m != nil {
		return m[1], true
	}
	for _, p := range []string{"ext.", "extra."} {
		e = strings.TrimPrefix(e, p)
	}
	if m := indexRe.FindStringSubmatch(e); m != nil {
		switch m[1] {
		case "ext", "extra", "properties":
			return m[2], true
		}
		return m[1] + "." + m[2], true
	}
	if m := getRe.FindStringSubmatch(e); m != nil {
		return m[1] + "." + m[2], true
	}
	if identPathRe.MatchString(e) {
		return e, true
	}
	return "", false
}

// templateRef returns the key of a string content that is exactly one reference: `$x`
// (Groovy also `$a.b`) or `${expr}`. ok is false when s contains no reference at all; key
// is "" when s mixes references with text or the expression is not a reference.
func templateRef(s string, groovy bool) (key string, ok bool) {
	if !strings.Contains(s, "$") {
		return "", false
	}
	if inner, found := strings.CutPrefix(s, "${"); found && strings.HasSuffix(inner, "}") {
		inner = inner[:len(inner)-1]
		if e := templateEnd([]byte(s), 2); e == len(s) {
			k, _ := refKey(inner)
			return k, true
		}
		return "", true
	}
	name, found := strings.CutPrefix(s, "$")
	if !found || !identPathRe.MatchString(name) || !groovy && strings.Contains(name, ".") {
		return "", true
	}
	return name, true
}

// literalDef reports whether s can be a definition value: non-empty, no template or escape.
func literalDef(s string) bool {
	return s != "" && !strings.ContainsAny(s, `$\`)
}

// Definition forms; each match ends at the opening quote of the value, except mapDeclRe,
// which ends at the '['.
var (
	// declRe: def x = '1', val x = "1", const val x = "1", val x: String = "1", String x = '1'.
	declRe = regexp.MustCompile(`\b(?:def|val|var|String)[ \t]+([A-Za-z_]\w*)(?:[ \t]*:[ \t]*String\??)?[ \t]*=[ \t]*["']`)
	// extAssignRe: ext.x = '1', project.ext.x = '1', rootProject.ext.x = '1'.
	extAssignRe = regexp.MustCompile(`\b(?:(?:project|rootProject)\.)?(?:ext|extra)\.([A-Za-z_]\w*)[ \t]*=[ \t]*["']`)
	// extIndexRe: extra["x"] = "1", ext['x'] = '1' (the key literal is group 1's quote).
	extIndexRe = regexp.MustCompile(`\b(?:(?:project|rootProject)\.)?(?:ext|extra)\[[ \t]*(["']) *["'][ \t]*\][ \t]*=[ \t]*["']`)
	// extSetRe: ext.set('x', '1'), extra.set("x", "1"), set("x", "1") inside an ext block.
	extSetRe = regexp.MustCompile(`\b((?:(?:project|rootProject)\.)?(?:ext|extra)\.)?set\([ \t]*(["']) *["'][ \t]*,[ \t]*["']`)
	// blockAssignRe: x = '1' at the top of an ext { } block.
	blockAssignRe = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_]\w*)[ \t]*=[ \t]*["']`)
	// byExtraRe: val x by extra("1").
	byExtraRe = regexp.MustCompile(`\bval[ \t]+([A-Za-z_]\w*)(?:[ \t]*:[ \t]*String\??)?[ \t]+by[ \t]+(?:(?:project|rootProject)\.)?extra\([ \t]*["']`)
	// mapDeclRe: versions = [...], versions += [...], ext.versions = [...], def versions = [...],
	// deps['test'] = [...].
	mapDeclRe = regexp.MustCompile(`(?:\b(?:(?:project|rootProject)\.)?(?:ext|extra)\.|\bdef[ \t]+|\b)([A-Za-z_]\w*)(?:\[[ \t]*(["']) *["'][ \t]*\])?[ \t]*\+?=[ \t]*\[`)
	// mapAssignRe: versions.kotlin = '1' / versions['kotlin'] = '1' for a declared map.
	mapAssignRe = regexp.MustCompile(`\b([A-Za-z_]\w*)(?:\.([A-Za-z_]\w*)|\[[ \t]*(["']) *["'][ \t]*\])[ \t]*=[ \t]*["']`)
	// kotlinMapRe: val versions = mapOf("kotlin" to "1.9"), also mutableMapOf<String, String>(…);
	// the match ends at the '('.
	kotlinMapRe = regexp.MustCompile(`\b(?:val|var)[ \t]+([A-Za-z_]\w*)(?:[ \t]*:[^=\n]*)?[ \t]*=[ \t]*(?:mutableMapOf|mapOf|hashMapOf|linkedMapOf)(?:<[^>\n]*>)?\(`)
	// mapOfRe is a nested Kotlin map value.
	mapOfRe = regexp.MustCompile(`^(?:mutableMapOf|mapOf|hashMapOf|linkedMapOf)(?:<[^>\n]*>)?\(`)
)

// extBlocks are the labels of blocks whose assignments and set calls define extra properties.
var extBlocks = map[string]bool{"ext": true, "project.ext": true, "rootProject.ext": true, "extra.apply": true, "ext.apply": true}

// assignment is a definition-form literal: a variable definition when its value is literal,
// a dep when it is a coordinate.
type assignment struct {
	keys []string // keys[0] is the canonical name
	lit  strLit
}

// assignments returns the definition-form literals of s in no particular order.
func (s *source) assignments() []assignment {
	var out []assignment
	add := func(lit strLit, ok bool, keys ...string) {
		if ok {
			out = append(out, assignment{keys: keys, lit: lit})
		}
	}
	for _, m := range declRe.FindAllSubmatchIndex(s.code, -1) {
		lit, ok := s.lit(m[1])
		add(lit, ok, s.qualified(m[0], string(s.src[m[2]:m[3]]))...)
	}
	for _, m := range extAssignRe.FindAllSubmatchIndex(s.code, -1) {
		lit, ok := s.lit(m[1])
		add(lit, ok, string(s.src[m[2]:m[3]]))
	}
	for _, m := range byExtraRe.FindAllSubmatchIndex(s.code, -1) {
		lit, ok := s.lit(m[1])
		add(lit, ok, string(s.src[m[2]:m[3]]))
	}
	for _, m := range extIndexRe.FindAllSubmatchIndex(s.code, -1) {
		key, kok := s.lit(m[2] + 1)
		lit, ok := s.lit(m[1])
		add(lit, ok && kok, s.text(key))
	}
	for _, m := range extSetRe.FindAllSubmatchIndex(s.code, -1) {
		if m[2] < 0 { // bare set(): only inside an ext block
			if b := s.enclosing(m[0]); b < 0 || !extBlocks[s.blocks[b].label] {
				continue
			}
		}
		key, kok := s.lit(m[4] + 1)
		lit, ok := s.lit(m[1])
		add(lit, ok && kok, s.text(key))
	}
	for _, m := range blockAssignRe.FindAllSubmatchIndex(s.code, -1) {
		if b := s.enclosing(m[2]); b < 0 || !extBlocks[s.blocks[b].label] {
			continue
		}
		lit, ok := s.lit(m[1])
		add(lit, ok, string(s.src[m[2]:m[3]]))
	}
	maps := map[string]bool{}
	for _, m := range mapDeclRe.FindAllSubmatchIndex(s.code, -1) {
		name := string(s.src[m[2]:m[3]])
		if m[4] >= 0 {
			key, ok := s.lit(m[4] + 1)
			if !ok {
				continue
			}
			name += "." + s.text(key)
		}
		maps[name] = true
		out = s.mapEntries(out, name, m[1]-1, 0, false)
	}
	for _, m := range kotlinMapRe.FindAllSubmatchIndex(s.code, -1) {
		out = s.mapEntries(out, string(s.src[m[2]:m[3]]), m[1]-1, 0, true)
	}
	if len(maps) > 0 {
		for _, m := range mapAssignRe.FindAllSubmatchIndex(s.code, -1) {
			name := string(s.src[m[2]:m[3]])
			if !maps[name] {
				continue
			}
			var key string
			if m[4] >= 0 {
				key = string(s.src[m[4]:m[5]])
			} else if k, ok := s.lit(m[6] + 1); ok {
				key = s.text(k)
			}
			lit, ok := s.lit(m[1])
			add(lit, ok && key != "", name+"."+key)
		}
	}
	return out
}

// qualified returns the keys of a declaration of name at off: the name, and within
// `object`/`class` bodies also the names qualified by the enclosing types (Versions.kotlin,
// Deps.Versions.kotlin); the most qualified one is canonical.
func (s *source) qualified(off int, name string) []string {
	keys := []string{name}
	q := name
	for b := s.enclosing(off); b >= 0; b = s.blocks[b].parent {
		if s.blocks[b].object {
			q = s.blocks[b].label + "." + q
			keys = append(keys, q)
		}
	}
	for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
		keys[i], keys[j] = keys[j], keys[i]
	}
	return keys
}

// mapEntries appends the entries of the map literal whose opener is at open, as
// `<name>.<key>`: Groovy `[key: 'value']` or, with kotlin, `mapOf("key" to "value")` (open
// at the '('); nested maps recurse.
func (s *source) mapEntries(out []assignment, name string, open, depth int, kotlin bool) []assignment {
	if depth > maxDepth {
		return out
	}
	closer := byte(']')
	if kotlin {
		closer = ')'
	}
	i := open + 1
	for i < len(s.code) {
		i = s.skipBlank(i)
		if i >= len(s.code) || s.code[i] == closer {
			return out
		}
		// key: string, or a Groovy identifier
		var key string
		if l, ok := s.litAfter(i); ok {
			key, i = s.text(l), l.end+1
		} else if !kotlin {
			j := i
			for j < len(s.code) && isIdent(s.code[j]) {
				j++
			}
			key, i = string(s.src[i:j]), j
		}
		i = s.skipBlank(i)
		switch {
		case key == "" || i >= len(s.code):
			return out
		case kotlin && bytes.HasPrefix(s.code[i:], []byte("to")) && i+2 < len(s.code) && !isIdent(s.code[i+2]):
			i += 2
		case !kotlin && s.code[i] == ':':
			i++
		default:
			return out // a list or an expression: not a map entry
		}
		i = s.skipBlank(i)
		if m := mapOfRe.Find(s.code[i:]); kotlin && m != nil {
			out = s.mapEntries(out, name+"."+key, i+len(m)-1, depth+1, true)
		} else if !kotlin && i < len(s.code) && s.code[i] == '[' {
			out = s.mapEntries(out, name+"."+key, i, depth+1, false)
		} else if l, ok := s.litAfter(i); ok {
			if j := s.skipBlank(l.end + 1); j < len(s.code) && (s.code[j] == ',' || s.code[j] == closer) {
				out = append(out, assignment{keys: []string{name + "." + key}, lit: l})
			}
		}
		// skip the rest of the value
		nest := 0
	skip:
		for ; i < len(s.code); i++ {
			switch s.code[i] {
			case '[', '(', '{':
				nest++
			case ']', ')', '}':
				if nest == 0 {
					break skip
				}
				nest--
			case ',':
				if nest == 0 {
					break skip
				}
			}
		}
		if i >= len(s.code) || s.code[i] != ',' {
			return out
		}
		i++
	}
	return out
}

// properties parses a gradle.properties file: `key=value`, `key: value` and `key value`;
// entries with escapes or line continuations are left out.
func properties(src []byte) defs {
	out := defs{}
	off, line := 0, 0
	cont := false
	for off < len(src) {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += off
		}
		line++
		l := src[off:end]
		start := off
		off = end + 1
		wasCont := cont
		i := 0
		for i < len(l) && (l[i] == ' ' || l[i] == '\t' || l[i] == '\f') {
			i++
		}
		if !wasCont && (i == len(l) || l[i] == '#' || l[i] == '!') {
			continue
		}
		trimmed := bytes.TrimRight(l, " \t\f\r")
		bs := len(trimmed) - len(bytes.TrimRight(trimmed, `\`))
		cont = bs%2 == 1
		if wasCont || cont {
			continue
		}
		k := i
		for k < len(l) && strings.IndexByte("=: \t\f\r", l[k]) < 0 {
			k++
		}
		key := string(l[i:k])
		for k < len(l) && (l[k] == ' ' || l[k] == '\t' || l[k] == '\f') {
			k++
		}
		if k < len(l) && (l[k] == '=' || l[k] == ':') {
			k++
			for k < len(l) && (l[k] == ' ' || l[k] == '\t' || l[k] == '\f') {
				k++
			}
		}
		v := string(bytes.TrimRight(l[k:], " \t\f\r"))
		if strings.Contains(key, `\`) || !literalDef(v) {
			continue
		}
		out.add(def{name: key, value: v, span: dep.Span{Start: start + k, End: start + k + len(v)}, line: line}, key)
	}
	return out
}
