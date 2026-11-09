package renovate

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// The JSONata transformTemplates shapes jsonataVersions translates (whitespace removed):
//
//	{"releases": $map(<list>, function($v){ {"version": <value>} })}
//	{"releases": <path>.{"version": <field>}}   (also wrapped in [ ])
//
// <list> is <path>, $keys(<path>) or $distinct(<list>); <value> is $v, $v.<field> or
// $replace(<that>, /^v/, "").
var (
	jsonataMapRe   = regexp.MustCompile(`^\{"releases":\$map\((.+),function\(\$(\w+)\)\{\{"version":(.+)\}\}\)\}$`)
	jsonataPathRe  = regexp.MustCompile(`^\{"releases":\[?(\$?[\w.$]*)\.\{"version":([\w.]+)\}\]?\}$`)
	jsonataStripRe = regexp.MustCompile(`^\$replace\((.+),/\^v/,(""|'')\)$`)
	jsonataFieldRe = regexp.MustCompile(`^[A-Za-z_]\w*(\.[A-Za-z_]\w*)*$`)
)

// stripV is the versionRegex of $replace(…, /^v/, "").
const stripV = `^v?(?P<version>.+)$`

// jsonataVersions translates a JSONata transformTemplates entry that maps a list of a
// version document to {"releases": [{"version": …}]} into a deps versions selector and
// versionRegex; ok is false for any other expression.
func jsonataVersions(expr string) (versions, versionRegex string, ok bool) {
	e := strings.Join(strings.Fields(expr), "")
	if m := jsonataPathRe.FindStringSubmatch(e); m != nil {
		p, ok := jsonataPath(m[1])
		if !ok || p == "" || !jsonataFieldRe.MatchString(m[2]) {
			return "", "", false
		}
		return p + "[]." + m[2], "", true
	}
	m := jsonataMapRe.FindStringSubmatch(e)
	if m == nil {
		return "", "", false
	}
	list, arg, value := m[1], "$"+m[2], m[3]
	for {
		inner, found := strings.CutPrefix(list, "$distinct(")
		if !found || !strings.HasSuffix(inner, ")") {
			break
		}
		list = strings.TrimSuffix(inner, ")") // deps drops duplicate versions anyway
	}
	keys := false
	if inner, found := strings.CutPrefix(list, "$keys("); found && strings.HasSuffix(inner, ")") {
		list, keys = strings.TrimSuffix(inner, ")"), true
	}
	p, ok := jsonataPath(list)
	if !ok || p == "" {
		return "", "", false
	}
	if s := jsonataStripRe.FindStringSubmatch(value); s != nil {
		value, versionRegex = s[1], stripV
	}
	field := ""
	switch {
	case value == arg:
	case strings.HasPrefix(value, arg+".") && !keys && jsonataFieldRe.MatchString(value[len(arg)+1:]):
		field = "." + value[len(arg)+1:]
	default:
		return "", "", false
	}
	if keys {
		return p + " | keys", versionRegex, true
	}
	return p + "[]" + field, versionRegex, true
}

// jsonataPath converts a JSONata path ($.a.b, a.b, $) into a deps selector path (.a.b, "" for
// the root).
func jsonataPath(p string) (string, bool) {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "$"), ".")
	if p == "" {
		return "", true
	}
	if !jsonataFieldRe.MatchString(p) {
		return "", false
	}
	return "." + p, true
}

// jsonataMerge returns the customManagers query q with the string fields of merge set on each
// of its result objects, as Renovate's literal templates override the query's fields.
func jsonataMerge(q string, merge omap) string {
	if len(merge) == 0 {
		return q
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // keep regex groups (?<version>) readable
	str := func(s string) {
		_ = enc.Encode(s)       // a string always encodes
		b.Truncate(b.Len() - 1) // Encode's newline
	}
	b.WriteString("(" + q + ") ~> $map(function($d) { $merge([$d, {")
	for i, f := range merge {
		if i > 0 {
			b.WriteString(", ")
		}
		str(f.k)
		b.WriteString(": ")
		if e, ok := f.v.(jsonataExpr); ok {
			b.WriteString(string(e))
			continue
		}
		v, _ := f.v.(string)
		str(v)
	}
	b.WriteString("}]) })")
	return b.String()
}

// jsonataExpr is a merge value written as a JSONata expression over the result object $d
// instead of as a string literal.
type jsonataExpr string

// templateJSONata converts a Renovate handlebars template whose expressions only reference
// fields ({{depName}}, {{{packageName}}}) into a JSONata string concatenation over the query's
// result object $d; a missing field renders as "" in both. Otherwise it returns the first
// unsupported expression (a helper or block such as {{#if …}} or {{replace …}}).
func templateJSONata(s string) (jsonataExpr, string) {
	var parts []string
	lit := false
	for s != "" {
		i := strings.Index(s, "{{")
		if i < 0 {
			i = len(s)
		}
		if i > 0 {
			enc, _ := json.Marshal(s[:i]) // a string always encodes
			parts = append(parts, strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">").Replace(string(enc)))
			lit = true
			s = s[i:]
			continue
		}
		open, end := "{{", "}}"
		if strings.HasPrefix(s, "{{{") {
			open, end = "{{{", "}}}"
		}
		j := strings.Index(s[len(open):], end)
		if j < 0 {
			return "", s
		}
		expr := s[:len(open)+j+len(end)]
		name := strings.TrimSpace(s[len(open) : len(open)+j])
		if !jsonataFieldRe.MatchString(name) || name == "else" || name == "this" {
			return "", expr
		}
		parts = append(parts, "$d."+name)
		s = s[len(expr):]
	}
	if !lit { // & renders a missing field as "" and casts non-strings, as handlebars does
		parts = append([]string{`""`}, parts...)
	}
	return jsonataExpr(strings.Join(parts, " & ")), ""
}
