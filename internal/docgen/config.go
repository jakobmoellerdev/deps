package docgen

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonschema"
)

// MarkdownDescription is the schema keyword holding a key's full markdown description (its
// description as markdown, then details), which editors show instead of description.
const MarkdownDescription = "markdownDescription"

// ConfigKey is one documented key: its path (e.g. rules[].allow), depth (0 = top level) and
// schema.
type ConfigKey struct {
	Path  string
	Depth int
	// Schema is the key's property schema; its object, if any, is a $ref into the root.
	Schema *jsonschema.Schema
}

// Summary is the first paragraph of the key's markdown description.
func (k ConfigKey) Summary() string {
	summary, _, _ := strings.Cut(text(k.Schema), "\n\n")
	return summary
}

// Details is the key's markdown description after its first paragraph ("" when none).
func (k ConfigKey) Details() string {
	_, rest, _ := strings.Cut(text(k.Schema), "\n\n")
	return rest
}

// Fact is one machine-readable constraint of a key: a label (Type, Default, Values, …) and its
// value as markdown.
type Fact struct{ Label, Value string }

// ConfigKeys lists every key of the reflected schema s in schema order, nested keys as paths
// (actions.defaultHost, rules[].allow, customDatasources.<name>.url), as Config documents them.
func ConfigKeys(s *jsonschema.Schema) ([]ConfigKey, error) {
	return configKeys(s, s, "", 0, map[string]bool{})
}

// Facts lists the machine-readable constraints of key k of the reflected schema root: type,
// default, allowed values, pattern and limits.
func Facts(root *jsonschema.Schema, k ConfigKey) []Fact { return facts(root, k.Schema) }

// Config renders the configuration reference from the reflected schema s: the root's
// description, a table of contents, then one section per key at every level, in schema
// order. Nested keys are written as paths: actions.defaultHost, rules[].allow,
// customDatasources.<name>.url. Each section lists type, default, allowed values, pattern
// and limits, then the description.
func Config(s *jsonschema.Schema) ([]byte, error) {
	keys, err := ConfigKeys(s)
	if err != nil {
		return nil, err
	}
	d := newDoc(s.Title)
	d.para(text(s))
	if len(keys) == 0 {
		return d.bytes()
	}
	d.heading(2, "Contents")
	toc := make([]string, 0, len(keys))
	for _, k := range keys {
		toc = append(toc, strings.Repeat("  ", k.Depth)+"- ["+codeSpan(k.Path)+"](#"+Anchor(codeSpan(k.Path))+")")
	}
	d.b.WriteString("\n" + strings.Join(toc, "\n") + "\n")
	for _, k := range keys {
		d.heading(2+min(k.Depth, 2), codeSpan(k.Path))
		d.para(k.Summary())
		fs := facts(s, k.Schema)
		items := make([]string, len(fs))
		for i, f := range fs {
			items[i] = "**" + f.Label + ":** " + f.Value
		}
		d.list(items)
		d.para(k.Details())
	}
	return d.bytes()
}

// configKeys lists the properties of n (and, recursively, of the objects they hold) as keys
// under prefix.
func configKeys(root, n *jsonschema.Schema, prefix string, depth int, seen map[string]bool) ([]ConfigKey, error) {
	if n.Properties == nil {
		return nil, nil
	}
	var keys []ConfigKey
	for key, p := range n.Properties.All() {
		path := prefix + key
		keys = append(keys, ConfigKey{Path: path, Depth: depth, Schema: p})
		child, sep, err := Children(root, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if child == nil {
			continue
		}
		ref := child.Ref
		if seen[ref] {
			return nil, fmt.Errorf("%s: recursive definition %s", path, ref)
		}
		seen[ref] = true
		sub, err := configKeys(root, Resolve(root, child), path+sep, depth+1, seen)
		delete(seen, ref)
		if err != nil {
			return nil, err
		}
		keys = append(keys, sub...)
	}
	return keys, nil
}

// Children returns the object schema reference held by p (directly, as list items or as map
// values) and the path separator to its keys ("." , "[]." or ".<name>."), or nil when p holds
// no object.
func Children(root, p *jsonschema.Schema) (*jsonschema.Schema, string, error) {
	var c *jsonschema.Schema
	sep := "."
	switch {
	case p.Ref != "":
		c = p
	case p.Items != nil && p.Items.Ref != "":
		c, sep = p.Items, "[]."
	case p.AdditionalProperties != nil && p.AdditionalProperties.Ref != "":
		c, sep = p.AdditionalProperties, ".<name>."
	case slices.ContainsFunc(p.AnyOf, func(a *jsonschema.Schema) bool { return a.Ref != "" }): // a list or an object
		c = p.AnyOf[slices.IndexFunc(p.AnyOf, func(a *jsonschema.Schema) bool { return a.Ref != "" })]
	default:
		return nil, "", nil
	}
	if Resolve(root, c) == nil {
		return nil, "", fmt.Errorf("unresolvable $ref %q", c.Ref)
	}
	return c, sep, nil
}

// Resolve follows a local "#/$defs/<name>" reference of s to its definition in root (nil when
// root has none); other schemas are returned as is.
func Resolve(root, s *jsonschema.Schema) *jsonschema.Schema {
	name, ok := strings.CutPrefix(s.Ref, "#/$defs/")
	if !ok {
		return s
	}
	return root.Definitions[name]
}

// facts lists the machine-readable constraints of key schema k.
func facts(root, k *jsonschema.Schema) []Fact {
	var out []Fact
	add := func(label, value string) {
		if value != "" {
			out = append(out, Fact{label, value})
		}
	}
	add("Type", typeName(k))
	if k.Default != nil {
		add("Default", value(k.Default))
	}
	v := k
	if k.Items != nil {
		v = k.Items
	}
	add("Values", values(v))
	if v.AnyOf == nil {
		add("Pattern", pattern(v.Pattern))
	}
	if k.PropertyNames != nil {
		add("Names", "must match "+pattern(k.PropertyNames.Pattern))
	}
	if k.Minimum != nil {
		add("Minimum", "`"+string(k.Minimum)+"`")
	}
	if k.MinItems != nil {
		add("Minimum entries", strconv.FormatUint(*k.MinItems, 10))
	}
	// configKeys has resolved the object k holds (if any) already, so errors cannot occur here.
	if c, _, _ := Children(root, k); c != nil && len(Resolve(root, c).Required) > 0 {
		req := Resolve(root, c).Required
		quoted := make([]string, len(req))
		for i, q := range req {
			quoted[i] = "`" + q + "`"
		}
		add("Required keys", strings.Join(quoted, ", "))
	}
	return out
}

// typeName describes the type of s in words: string, list of strings, object, map of name
// to object, ….
func typeName(s *jsonschema.Schema) string {
	switch {
	case s.Ref != "":
		return "object"
	case s.Type == "array" && s.Items != nil:
		if t := typeName(s.Items); t != "" {
			return "list of " + t + "s"
		}
		return "list"
	case s.Type == "object" && s.AdditionalProperties != nil && s.AdditionalProperties != jsonschema.FalseSchema:
		return "map of name → " + typeName(s.AdditionalProperties)
	case s.Type != "":
		return s.Type
	case len(s.AnyOf) > 0:
		var ts []string
		for _, a := range s.AnyOf {
			if t := typeName(a); !slices.Contains(ts, t) {
				ts = append(ts, t)
			}
		}
		return strings.Join(ts, " or ")
	}
	return ""
}

// values lists the enum of s, or of its anyOf alternatives with their patterns.
func values(s *jsonschema.Schema) string {
	alts := s.AnyOf
	if len(alts) == 0 {
		alts = []*jsonschema.Schema{s}
	}
	var parts []string
	for _, a := range alts {
		for _, e := range a.Enum {
			parts = append(parts, value(e))
		}
		if len(s.AnyOf) > 0 && a.Pattern != "" {
			parts = append(parts, "values matching "+pattern(a.Pattern))
		}
	}
	return strings.Join(parts, ", ")
}

func pattern(p string) string {
	if p == "" {
		return ""
	}
	return codeSpan(p)
}

// value renders a JSON value as a code span; lists render their elements.
func value(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return "`\"\"`"
		}
		return codeSpan(x)
	case []string:
		return value(toAny(x))
	case []any:
		if len(x) == 0 {
			return "`[]`"
		}
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = value(e)
		}
		return strings.Join(parts, ", ")
	}
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return codeSpan(fmt.Sprint(v))
	}
	return codeSpan(string(b))
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// text is the markdown description of s: MarkdownDescription, else the description converted
// from plain text. Its first paragraph is the summary.
func text(s *jsonschema.Schema) string {
	if md, ok := s.Extras[MarkdownDescription].(string); ok {
		return md
	}
	return PlainToMarkdown(s.Description)
}
