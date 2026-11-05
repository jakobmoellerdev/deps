package config

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Problem is one configuration problem: a syntax error, an unknown key, a value of the
// wrong type or a failed validation.
type Problem struct {
	Path      string // config file
	Line, Col int    // 1-based position of the key or value; 0 when unknown
	Field     string // field path such as rules[0].allow; empty for syntax errors
	Msg       string
}

// Error formats p as "<path>:<line>:<col>: <field>: <msg>", leaving out the position when
// it is unknown and the field when it is empty.
func (p *Problem) Error() string {
	var b strings.Builder
	b.WriteString(p.Path)
	if p.Line > 0 {
		fmt.Fprintf(&b, ":%d:%d", p.Line, p.Col)
	}
	b.WriteString(": ")
	if p.Field != "" {
		b.WriteString(p.Field)
		b.WriteString(": ")
	}
	b.WriteString(p.Msg)
	return b.String()
}

// Problems returns the problems e reports, or nil when e is no problem of the file's content
// (an unreadable file, a datasource that cannot be built).
func (e *Error) Problems() []*Problem {
	if joined, ok := errors.AsType[interface {
		error
		Unwrap() []error
	}](e.Err); ok {
		var ps []*Problem
		for _, x := range joined.Unwrap() {
			if p, ok := errors.AsType[*Problem](x); ok {
				ps = append(ps, p)
			}
		}
		return ps
	}
	if p, ok := errors.AsType[*Problem](e.Err); ok {
		return []*Problem{p}
	}
	return nil
}

// joinProblems sorts ps by position (unknown positions last, in their order) and joins
// them; nil when ps is empty.
func joinProblems(ps []*Problem) error {
	if len(ps) == 0 {
		return nil
	}
	slices.SortStableFunc(ps, func(a, b *Problem) int {
		if (a.Line == 0) != (b.Line == 0) {
			if a.Line == 0 {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
	})
	errs := make([]error, len(ps))
	for i, p := range ps {
		errs[i] = p
	}
	return errors.Join(errs...)
}

// position is a 1-based line and column in the config file.
type position struct{ line, col int }

// Position returns the 1-based line and column of field (a field path of the merged
// configuration, such as rules[3].allow) in the loaded config file, or of its nearest
// ancestor present in the file (customDatasources.x for a missing customDatasources.x.url).
// A field written in an extended file is at the local extends entry it came through. 0, 0
// without one.
func (c *Config) Position(field string) (line, col int) {
	ly, f := c.written(field)
	switch {
	case ly == nil:
		return 0, 0
	case ly.via >= 0:
		return c.layers[len(c.layers)-1].position(fmt.Sprintf("extends[%d]", ly.via))
	}
	return ly.position(f)
}

// lookupPosition returns the position of field in pos, or of its nearest ancestor there.
func lookupPosition(pos map[string]position, field string) (line, col int) {
	for f := field; f != ""; f = parentField(f) {
		if p, ok := pos[f]; ok {
			return p.line, p.col
		}
	}
	return 0, 0
}

// parentField strips the last .key or [i] of a field path.
func parentField(f string) string {
	if i := strings.LastIndexAny(f, ".["); i > 0 {
		return f[:i]
	}
	return ""
}

func joinField(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// decoder checks a yaml document against the Config struct before it is decoded: it reports
// unknown keys and values of the wrong kind, removes them from the document so the rest
// decodes, and records the position of every field for validation errors.
type decoder struct {
	path     string
	pos      map[string]position
	problems []*Problem
	bad      []string // fields with a malformed value; validation errors below them are dropped
	fields   map[reflect.Type][]structField
}

type structField struct {
	name string
	typ  reflect.Type
}

// scopes are the field paths of the structs that RenovateOptions refer to.
var scopes = map[reflect.Type]string{
	reflect.TypeFor[Config]():           "",
	reflect.TypeFor[Rule]():             "rules[]",
	reflect.TypeFor[PostUpgradeTasks](): "rules[].postUpgradeTasks",
	reflect.TypeFor[Custom]():           "custom[]",
	reflect.TypeFor[CustomDatasource](): "customDatasources.<name>",
}

func (d *decoder) add(line, col int, field, format string, a ...any) {
	d.problems = append(d.problems, &Problem{Path: d.path, Line: line, Col: col, Field: field, Msg: fmt.Sprintf(format, a...)})
}

// suppressed reports whether field is at or below a field with a malformed value.
func (d *decoder) suppressed(field string) bool { return suppressed(d.bad, field) }

// suppressed reports whether field is at or below one of the fields bad.
func suppressed(bad []string, field string) bool {
	return slices.ContainsFunc(bad, func(b string) bool {
		return b == "" || field == b || strings.HasPrefix(field, b+".") || strings.HasPrefix(field, b+"[")
	})
}

// structFields returns the yaml keys of struct t in declaration order.
func (d *decoder) structFields(t reflect.Type) []structField {
	if fs, ok := d.fields[t]; ok {
		return fs
	}
	var fs []structField
	for f := range t.Fields() {
		if name := yamlKey(f); name != "" {
			fs = append(fs, structField{name, f.Type})
		}
	}
	d.fields[t] = fs
	return fs
}

func (d *decoder) walk(n *yaml.Node, t reflect.Type, field string) {
	if field != "" {
		d.pos[field] = position{n.Line, n.Column}
	}
	at := n // the node in the document: an alias reports its own position and is replaced, not its anchor
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[PostUpgradeTasks]() && n.Kind == yaml.SequenceNode {
		// postUpgradeTasks also takes a plain list of commands (PostUpgradeTasks.UnmarshalYAML).
		d.walk(n, reflect.TypeFor[[]string](), field)
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if d.want(at, n, yaml.MappingNode, field) {
			d.walkStruct(n, t, field)
		}
	case reflect.Map:
		if !d.want(at, n, yaml.MappingNode, field) {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			d.walk(n.Content[i+1], t.Elem(), joinField(field, n.Content[i].Value))
		}
	case reflect.Slice:
		if !d.want(at, n, yaml.SequenceNode, field) {
			return
		}
		for i, e := range n.Content {
			d.walk(e, t.Elem(), field+"["+strconv.Itoa(i)+"]")
		}
	default:
		if n.Kind != yaml.ScalarNode || n.Decode(reflect.New(t).Interface()) != nil {
			d.mismatch(at, n, field, wantName(t))
		}
	}
}

func (d *decoder) walkStruct(n *yaml.Node, t reflect.Type, field string) {
	fields := d.structFields(t)
	kept := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag == "!!merge" {
			// <<: *base or <<: [*a, *b] merges mappings into this one.
			kept = append(kept, k, v)
			ms := []*yaml.Node{v}
			if v.Kind == yaml.SequenceNode {
				ms = v.Content
			}
			for _, m := range ms {
				at := m
				if m.Kind == yaml.AliasNode && m.Alias != nil {
					m = m.Alias
				}
				if d.want(at, m, yaml.MappingNode, field) {
					d.walkStruct(m, t, field)
				}
			}
			continue
		}
		j := slices.IndexFunc(fields, func(f structField) bool { return f.name == k.Value })
		if j < 0 {
			d.unknownKey(k, t, field, fields)
			continue
		}
		kept = append(kept, k, v)
		d.walk(v, fields[j].typ, joinField(field, k.Value))
	}
	n.Content = kept
}

// want reports whether n, the value of the document node at (n itself, or the alias of n),
// has kind, else reports a type mismatch.
func (d *decoder) want(at, n *yaml.Node, kind yaml.Kind, field string) bool {
	if n.Kind == kind {
		return true
	}
	d.mismatch(at, n, field, kindName(kind))
	return false
}

// mismatch reports that n is not what field needs and replaces the document node at (n, or
// an alias of n) with null, so the document decodes without it. An anchor stays intact for
// its other aliases.
func (d *decoder) mismatch(at, n *yaml.Node, field, want string) {
	got := kindName(n.Kind)
	if n.Kind == yaml.ScalarNode {
		got = strconv.Quote(n.Value)
	}
	d.add(at.Line, at.Column, field, "want %s, got %s", want, got)
	d.bad = append(d.bad, field)
	*at = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Line: at.Line, Column: at.Column}
}

// renamedKeys are rules[] keys of earlier deps versions or common slips, by the key they mean.
var renamedKeys = map[string]string{"updates": "allowedUpdateTypes", "manager": "managers"}

func (d *decoder) unknownKey(k *yaml.Node, t reflect.Type, parent string, fields []structField) {
	scope, ok := scopes[t]
	if !ok {
		scope = "?"
	}
	msg := "unknown key"
	if h := renovateHint(k.Value, scope); h != "" {
		msg += " (" + h + ")"
	} else if t == reflect.TypeFor[Config]() && slices.ContainsFunc(d.structFields(reflect.TypeFor[Rule]()), func(f structField) bool { return f.name == k.Value }) {
		msg += " (" + k.Value + " is a key of rules[] entries)"
	} else if s := renamedKeys[k.Value]; t == reflect.TypeFor[Rule]() && s != "" {
		msg += " (did you mean " + s + "?)"
	} else {
		names := make([]string, len(fields))
		for i, f := range fields {
			names[i] = f.name
		}
		if s := Closest(k.Value, names); s != "" {
			msg += " (did you mean " + s + "?)"
		}
	}
	d.add(k.Line, k.Column, joinField(parent, k.Value), "%s", msg)
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias"
	}
	return "a scalar"
}

func wantName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.String:
		return "a string"
	}
	return "a " + t.Kind().String()
}

// syntaxProblem converts a yaml parse error into a located problem.
func syntaxProblem(path string, err error) *Problem {
	if le, ok := errors.AsType[*yaml.LoadError](err); ok {
		return &Problem{Path: path, Line: le.Mark.Line, Col: le.Mark.Column, Msg: le.Message}
	}
	if les, ok := errors.AsType[*yaml.LoadErrors](err); ok && len(les.Errors) > 0 {
		return syntaxProblem(path, les.Errors[0])
	}
	return &Problem{Path: path, Msg: err.Error()}
}

// renovateHint returns the hint for Renovate option key found in scope, or "".
func renovateHint(key, scope string) string {
	h, ok := RenovateOptions[key]
	if !ok {
		return ""
	}
	if h.To == "" {
		return h.Note
	}
	to := h.To
	switch {
	case h.Scope != scope && h.Scope != "":
		to = h.Scope + "." + to
	case h.Scope == "" && scope != "":
		to = "the top-level " + to
	}
	msg := fmt.Sprintf("Renovate's %s is %s here", cmp.Or(h.From, key), to)
	if h.Note != "" {
		msg += "; " + h.Note
	}
	return msg
}

// maxListed is the longest value list an enum error spells out; longer ones point at
// deps config describe.
const maxListed = 10

// oneOf is the message of value v, which is none of vals: the list (or, when it is longer
// than maxListed, a pointer to deps config describe key) with a did-you-mean suggestion
// when a value is close.
func oneOf(key string, vals []string, v string) string {
	s := Closest(v, vals)
	if len(vals) > maxListed {
		if s != "" {
			return fmt.Sprintf("unknown value %q (did you mean %s?)", v, s)
		}
		return fmt.Sprintf("unknown value %q (see deps config describe %s)", v, key)
	}
	return fmt.Sprintf("must be one of %s, got %q%s", list(vals), v, didYouMean(v, vals))
}

// didYouMean returns " (did you mean <c>?)" for the candidate closest to v, or "".
func didYouMean(v string, candidates []string) string {
	if s := Closest(v, candidates); s != "" {
		return " (did you mean " + s + "?)"
	}
	return ""
}

// Closest returns the first candidate with the smallest case-insensitive edit distance to s
// (optimal string alignment: a transposition counts 1) within max(1, min(3, len(s)/3)), or
// "" when none is that close.
func Closest(s string, candidates []string) string {
	if s == "" {
		return ""
	}
	limit := max(1, min(3, len(s)/3))
	best, bestD := "", limit+1
	ls := strings.ToLower(s)
	for _, c := range candidates {
		if d := editDistance(ls, strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// editDistance is the optimal string alignment distance of a and b (bytes).
func editDistance(a, b string) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
