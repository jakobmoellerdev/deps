package gradle

import (
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

var (
	// catVersionRe: version("k", "1"), val k = version("k", v), version("k") { strictly(…) };
	// a value follows the comma.
	catVersionRe = regexp.MustCompile(`(?:\b(?:val|def)[ \t]+([A-Za-z_]\w*)[ \t]*=[ \t]*)?\bversion\([ \t]*(["']) *["'][ \t]*(,|\)[ \t]*\{)[ \t]*`)
	// catEntryRe: library("alias", …), plugin("alias", …); the arguments after the alias follow.
	catEntryRe = regexp.MustCompile(`\b(library|plugin)\([ \t]*["'] *["'][ \t]*,[ \t]*`)
	// catChainRe: .version(…), .versionRef(…), .version { … }, .withoutVersion() after an entry.
	catChainRe = regexp.MustCompile(`^[ \t\r\n]*\.[ \t]*(versionRef|version|withoutVersion)[ \t]*([({])[ \t]*`)
)

// catVersion is a version("k", …) of a settings catalog.
type catVersion struct {
	v    value
	rich bool
}

// settingsCatalogs extracts the libraries and plugins of the catalogs declared in
// `dependencyResolutionManagement { versionCatalogs { create("libs") { … } } }` (Groovy also
// `libs { … }`), like a TOML catalog.
func (x *extractor) settingsCatalogs() {
	s := x.s
	for b, blk := range s.blocks {
		if blk.parent < 0 || s.blocks[blk.parent].label != "versionCatalogs" {
			continue
		}
		inner := s.code[blk.open+1 : blk.end]
		base := blk.open + 1
		versions := map[string]catVersion{}
		vals := map[string]string{} // Kotlin val bound to a version alias
		for _, m := range catVersionRe.FindAllSubmatchIndex(inner, -1) {
			if s.enclosing(base+m[0]) != b {
				continue
			}
			key, ok := s.lit(base + m[4] + 1)
			if !ok {
				continue
			}
			k := s.text(key)
			if m[2] >= 0 {
				vals[string(s.src[base+m[2]:base+m[3]])] = k
			}
			if s.code[base+m[6]] != ',' {
				versions[k] = catVersion{rich: true}
				continue
			}
			if v, _ := x.argValue(base + m[1]); v.exists {
				versions[k] = catVersion{v: v}
				if v.isLit {
					x.taken[v.lit.start] = true
				}
			}
		}
		for _, m := range catEntryRe.FindAllSubmatchIndex(inner, -1) {
			if s.enclosing(base+m[0]) == b {
				x.catalogEntry(base+m[0], base+m[1], string(s.code[base+m[2]:base+m[3]]) == "plugin", versions, vals)
			}
		}
	}
}

// catalogEntry extracts `library("a", "g", "n")…`, `library("a", "g:n:v")` or
// `plugin("a", "id")…` whose arguments after the alias start at i.
func (x *extractor) catalogEntry(off, i int, plugin bool, versions map[string]catVersion, vals map[string]string) {
	s := x.s
	first, ok := s.litAfter(i)
	if !ok {
		return
	}
	i = s.skipSpace(first.end + 1)
	var second strLit
	if i < len(s.code) && s.code[i] == ',' {
		if plugin {
			return
		}
		if second, ok = s.litAfter(s.skipSpace(i + 1)); !ok {
			return
		}
		i = s.skipSpace(second.end + 1)
	} else if !plugin {
		x.coordinate(first, classLibrary, false)
		return
	}
	if i >= len(s.code) || s.code[i] != ')' {
		return
	}
	i++
	m := catChainRe.FindSubmatchIndex(s.code[i:])
	if len(m) < 4 || string(s.code[i+m[2]:i+m[3]]) == "withoutVersion" {
		return // managed by a platform
	}
	depType, class, name := DepTypeDependencies, classLibrary, s.text(first)+":"+s.text(second)
	if plugin {
		depType, class, name = DepTypePlugin, classPlugin, s.text(first)
	}
	d := newDep(x.file, depType, s.lines.at(off))
	d.Name = name
	meta := depMeta{class: class}
	method, open := string(s.code[i+m[2]:i+m[3]]), s.code[i+m[4]]
	arg, _ := x.argValue(i + m[1])
	switch {
	case open == '{':
		d.Skip = "rich version"
	case !arg.exists:
		return
	case method == "versionRef":
		ref := arg.expr
		if arg.isLit {
			ref = s.text(arg.lit)
		} else if k, ok := vals[ref]; ok {
			ref = k
		}
		def, ok := versions[ref]
		switch {
		case !ok:
			d.Skip = "unresolved version.ref " + ref
		case def.rich:
			d.Skip = "rich version"
		default:
			d.VersionRef = ref
			x.catalogVersion(&d, &meta, def.v)
		}
	default:
		x.catalogVersion(&d, &meta, arg)
		if arg.isLit {
			x.taken[arg.lit.start] = true
		}
	}
	if d.Name == ":" || d.Name == "" || d.Skip == "" && d.Version == "" && meta.ref == "" {
		return
	}
	switch {
	case !plainString(d.Name):
	case plugin:
		d.NameParts = []dep.NamePart{namePart(d.Name, first.start, dep.NameWhole)}
	default:
		d.NameParts = []dep.NamePart{namePart(s.text(first), first.start, dep.NameGroup), namePart(s.text(second), second.start, dep.NameArtifact)}
	}
	x.taken[first.start], x.taken[second.start] = true, true
	x.add(off, d, meta)
}

// catalogVersion sets the version of a settings catalog dep from a literal or a variable.
func (x *extractor) catalogVersion(d *dep.Dep, meta *depMeta, v value) {
	if !v.isLit {
		x.exprVersion(d, meta, v.expr)
		return
	}
	if t := x.s.text(v.lit); t != "" {
		x.version(d, meta, t, dep.Span{Start: v.lit.start, End: v.lit.end})
	}
}
