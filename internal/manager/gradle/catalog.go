package gradle

import (
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// richKeys are the fields of a rich version table.
var richKeys = []string{"strictly", "require", "prefer", "reject", "rejectAll"}

// entryKeys are the fields of a [libraries] or [plugins] entry.
var entryKeys = []string{"module", "group", "name", "id", "version"}

// extractCatalog returns the [libraries] and [plugins] deps of a version catalog, libraries
// first, each in document order. A file that is not valid TOML is an error (Gradle rejects it
// too).
func extractCatalog(file string, src []byte) ([]dep.Dep, error) {
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	versions := root.Get("versions")
	var deps []dep.Dep
	for _, sec := range []string{"libraries", "plugins"} {
		t := root.Get(sec)
		if t == nil || t.Kind != tomlspan.Table {
			continue
		}
		walkEntries(t, 0, func(e *tomlspan.Value) {
			var d dep.Dep
			var ok bool
			if sec == "libraries" {
				d, ok = library(file, e, versions)
			} else {
				d, ok = plugin(file, e, versions)
			}
			if ok {
				deps = append(deps, d)
			}
		})
	}
	return deps, nil
}

// walkEntries calls f for each entry below t in document order: a string (notation) or a
// table with entry fields; other tables are alias prefixes (`androidx.core = {…}` written as
// dotted keys) and are descended.
func walkEntries(t *tomlspan.Value, depth int, f func(*tomlspan.Value)) {
	if depth > maxDepth {
		return
	}
	for _, k := range t.Keys {
		switch v := t.Fields[k]; {
		case v.Kind == tomlspan.String:
			f(v)
		case v.Kind != tomlspan.Table:
		case slices.ContainsFunc(entryKeys, func(k string) bool { return v.Fields[k] != nil }):
			f(v)
		default:
			walkEntries(v, depth+1, f)
		}
	}
}

func library(file string, e, versions *tomlspan.Value) (dep.Dep, bool) {
	d := newDep(file, DepTypeDependencies, e.Line)
	if e.Kind == tomlspan.String {
		parts := strings.Split(e.Str, ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return d, false // "group:name" without a version is managed by a platform
		}
		d.Name = parts[0] + ":" + parts[1]
		start := e.Start + len(parts[0]) + len(parts[1]) + 2
		return literalVersion(d, parts[2], dep.Span{Start: start, End: e.End}, e.Exact)
	}
	if m := e.Fields["module"]; m != nil {
		g, a, found := strings.Cut(m.Str, ":")
		if m.Kind != tomlspan.String || !found || g == "" || a == "" || strings.Contains(a, ":") {
			return d, false
		}
		d.Name = m.Str
	} else {
		g, a := e.Fields["group"], e.Fields["name"]
		if g == nil || a == nil || g.Kind != tomlspan.String || a.Kind != tomlspan.String || g.Str == "" || a.Str == "" {
			return d, false
		}
		d.Name = g.Str + ":" + a.Str
	}
	return version(d, e, versions)
}

func plugin(file string, e, versions *tomlspan.Value) (dep.Dep, bool) {
	d := newDep(file, DepTypePlugin, e.Line)
	if e.Kind == tomlspan.String {
		id, v, found := strings.Cut(e.Str, ":")
		if !found || id == "" {
			return d, false
		}
		d.Name = id
		return literalVersion(d, v, dep.Span{Start: e.Start + len(id) + 1, End: e.End}, e.Exact)
	}
	id := e.Fields["id"]
	if id == nil || id.Kind != tomlspan.String || id.Str == "" {
		return d, false
	}
	d.Name = id.Str
	return version(d, e, versions)
}

// version resolves the version field of an entry: a string, `{ ref = "k" }` (also written
// `version.ref = "k"`) or a rich version table; no version means not a dep.
func version(d dep.Dep, e, versions *tomlspan.Value) (dep.Dep, bool) {
	v := e.Fields["version"]
	switch {
	case v == nil:
		return d, false
	case v.Kind != tomlspan.Table:
		return literalValue(d, v)
	}
	r := v.Fields["ref"]
	if r == nil {
		if len(v.Keys) == 0 {
			return d, false
		}
		d.Skip = "rich version"
		return d, true
	}
	var def *tomlspan.Value
	if versions != nil {
		if def = versions.Fields[r.Str]; def == nil {
			def = versions.Get(strings.Split(r.Str, ".")...)
		}
	}
	switch {
	case def == nil || def.Kind == tomlspan.Table && !slices.ContainsFunc(richKeys, func(k string) bool { return def.Fields[k] != nil }):
		d.Skip = "unresolved version.ref " + r.Str
		return d, true
	case def.Kind == tomlspan.Table:
		d.Skip = "rich version"
		return d, true
	}
	d.VersionRef = r.Str
	return literalValue(d, def)
}

// literalValue sets the version from a string value; other kinds cannot be edited.
func literalValue(d dep.Dep, v *tomlspan.Value) (dep.Dep, bool) {
	if v.Kind != tomlspan.String {
		d.Skip = "cannot locate value"
		return d, true
	}
	return literalVersion(d, v.Str, dep.Span{Start: v.Start, End: v.End}, v.Exact)
}

// literalVersion sets the version s at span; exact is false when the source bytes differ
// from the decoded string (escapes), which cannot be edited in place.
func literalVersion(d dep.Dep, s string, span dep.Span, exact bool) (dep.Dep, bool) {
	switch {
	case !exact:
		d.Skip = "cannot locate value"
	case s == "":
		return d, false
	default:
		setVersion(&d, s, span)
	}
	return d, true
}
