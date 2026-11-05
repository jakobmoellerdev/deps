package docker

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const dockerImagePrefix = "docker-image://"

// composeTopKeys are the top-level keys of the Compose specification.
var composeTopKeys = map[string]bool{
	"version": true, "name": true, "include": true, "services": true, "networks": true,
	"volumes": true, "configs": true, "secrets": true, "models": true,
}

// isCompose reports whether root is a Compose file that is not named like one: every top-level
// key is a Compose key or an `x-` extension, and `services` is a mapping of mappings, at least
// one with `image` or `build`. Kubernetes manifests (apiVersion, kind), workflows (on, jobs) and
// Helm values (other keys) fail the key check.
func isCompose(root *yaml.Node) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if k := root.Content[i].Value; !composeTopKeys[k] && !strings.HasPrefix(k, "x-") {
			return false
		}
	}
	found := false
	for _, s := range mappingValues(get(root, "services")) {
		if s.Kind == yaml.AliasNode {
			s = s.Alias
		}
		if s == nil || s.Kind != yaml.MappingNode {
			return false
		}
		found = found || lookup(s, "image", 0) != nil || lookup(s, "build", 0) != nil
	}
	return found
}

// compose extracts images of services (`image`, `build.additional_contexts` with
// `docker-image://`) and of mappings with `image` at any depth of top-level `x-` extension fields.
// An anchored value is extracted once, where its bytes are, however often it is aliased or merged.
func (x *extractor) compose(root *yaml.Node) {
	x.seen = map[*yaml.Node]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		switch {
		case k == "services":
			for _, svc := range mappingValues(v) {
				x.service(svc)
			}
		case strings.HasPrefix(k, "x-"):
			x.extension(v)
		}
	}
}

// service extracts the image and the docker-image additional contexts of service mapping m.
func (x *extractor) service(m *yaml.Node) {
	x.composeImage(lookup(m, "image", 0), "")
	ac := lookup(lookup(m, "build", 0), "additional_contexts", 0)
	if ac != nil && ac.Kind == yaml.AliasNode {
		ac = ac.Alias
	}
	switch {
	case ac == nil:
	case ac.Kind == yaml.MappingNode:
		for _, v := range mappingValues(ac) {
			x.composeImage(v, dockerImagePrefix)
		}
	case ac.Kind == yaml.SequenceNode:
		// `- name=docker-image://<ref>`
		for _, v := range ac.Content {
			if s := str(v); s != nil {
				if i := strings.Index(s.Value, "="+dockerImagePrefix); i > 0 {
					x.composeImage(s, s.Value[:i+1+len(dockerImagePrefix)])
				}
			}
		}
	}
}

// extension visits every mapping below an `x-` field. Environment and label values are data.
func (x *extractor) extension(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		x.service(n)
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "image", "build", "environment", "labels":
			default:
				x.extension(n.Content[i+1])
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			x.extension(c)
		}
	}
}

// lookup returns the value of key in mapping m (or the mapping an alias m points to), following
// `<<` merge keys: own keys win, then the merged mappings in order.
func lookup(m *yaml.Node, key string, depth int) *yaml.Node {
	if m != nil && m.Kind == yaml.AliasNode {
		m = m.Alias
	}
	if v := get(m, key); v != nil || m == nil || depth > 8 {
		return v
	}
	merge := get(m, "<<")
	if merge == nil {
		return nil
	}
	srcs := []*yaml.Node{merge}
	if merge.Kind == yaml.SequenceNode {
		srcs = merge.Content
	}
	for _, s := range srcs {
		if v := lookup(s, key, depth+1); v != nil {
			return v
		}
	}
	return nil
}

// composeImage adds the image reference in v (after prefix) once per node. References with
// `${VAR:-default}` / `${VAR-default}` resolve to their defaults; the spans point into the
// default bytes. Other interpolation is skipped as `interpolated image`.
func (x *extractor) composeImage(v *yaml.Node, prefix string) {
	if v != nil && v.Kind == yaml.AliasNode {
		v = v.Alias
	}
	if v == nil || x.seen[v] {
		return
	}
	x.seen[v] = true
	if !strings.Contains(v.Value, "$") {
		x.scalar(TypeCompose, v, prefix)
		return
	}
	if v.Kind != yaml.ScalarNode || !strings.HasPrefix(v.Value, prefix) {
		return
	}
	ref := v.Value[len(prefix):]
	skipped := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeCompose, File: x.file, Line: v.Line, Name: ref}
	res, pos, ok := interpolate(ref)
	off := x.locate(v)
	switch {
	case !ok:
		skipped.Skip = "interpolated image"
	case off < 0:
		skipped.Skip = "cannot locate value"
	default:
		off += len(prefix)
		d := skipped
		d.Line = x.lineOf(off)
		ParseRef(&d, 0, res)
		if d.Skip != "" || remap(&d, pos, off) {
			x.deps = append(x.deps, d)
			return
		}
		skipped.Skip = "interpolated image"
	}
	x.deps = append(x.deps, skipped)
}

// remap moves d's spans from resolved-reference offsets to source offsets (off + pos[i]). It
// fails when a span covers bytes that are not a contiguous run of the source (it crosses a
// variable boundary or contains an escaped `$$`).
func remap(d *dep.Dep, pos []int, off int) bool {
	span := func(s dep.Span) (dep.Span, bool) {
		a := pos[s.Start]
		for i := s.Start; i < s.End; i++ {
			if pos[i] < 0 || pos[i] != a+i-s.Start {
				return s, false
			}
		}
		return dep.Span{Start: off + a, End: off + a + s.End - s.Start}, true
	}
	ok := true
	if d.Version != "" {
		d.RefSpan, ok = span(d.RefSpan)
	}
	if ok && d.Digest != "" {
		d.DigestSpan, ok = span(d.DigestSpan)
	}
	if !ok || d.InsertAt == 0 || pos[d.InsertAt-1] < 0 {
		return false
	}
	d.InsertAt = off + pos[d.InsertAt-1] + 1
	return true
}

// interpolate resolves Compose interpolation in s to the defaults of `${VAR:-default}` and
// `${VAR-default}`, with `$$` as a literal `$`. pos[i] is the offset in s of resolved byte i
// (-1 for the `$` of `$$`). ok is false for any other interpolation (`$VAR`, `${VAR}`,
// `${VAR:?err}`, `${VAR:+alt}`, nested variables in a default).
func interpolate(s string) (resolved string, pos []int, ok bool) {
	var b strings.Builder
	b.Grow(len(s))
	pos = make([]int, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			pos = append(pos, i)
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "$$") {
			b.WriteByte('$')
			pos = append(pos, -1)
			i += 2
			continue
		}
		if !strings.HasPrefix(s[i:], "${") {
			return "", nil, false
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return "", nil, false
		}
		body := s[i+2 : i+end]
		n := varNameLen(body)
		var d int
		switch {
		case n == 0:
			return "", nil, false
		case strings.HasPrefix(body[n:], ":-"):
			d = n + 2
		case strings.HasPrefix(body[n:], "-"):
			d = n + 1
		default:
			return "", nil, false
		}
		if strings.Contains(body[d:], "$") {
			return "", nil, false
		}
		for k := i + 2 + d; k < i+end; k++ {
			b.WriteByte(s[k])
			pos = append(pos, k)
		}
		i += end + 1
	}
	return b.String(), pos, true
}

// varNameLen returns the length of the variable name ([A-Za-z_][A-Za-z0-9_]*) at the start of s.
func varNameLen(s string) int {
	for i := range len(s) {
		c := s[i]
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return i
	}
	return len(s)
}
