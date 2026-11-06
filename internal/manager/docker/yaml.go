package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

const dockerPrefix = "docker://"

// yaml parses src (through p, the file's shared parse) and runs walk on the top-level mapping.
func (x *extractor) yaml(p *yamlspan.Parsed, walk func(root *yaml.Node)) error {
	doc, err := p.Document(x.src)
	if err != nil {
		return err
	}
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		walk(doc.Content[0])
	}
	return nil
}

// manifestParses counts YAML parses of manifests mode (tests assert the byte prefilter).
var manifestParses atomic.Int64

// manifests extracts Kubernetes container images and Helm image values from every document
// of a multi-document YAML file; a document shaped like a Compose file (isCompose) is extracted
// as one. A file that does not parse (a Helm template) is read line by line (imageLines).
func (x *extractor) manifests() {
	manifestParses.Add(1)
	dec := yaml.NewDecoder(bytes.NewReader(yamlspan.KeepDocuments(x.src, mayHaveImage)))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			x.deps = nil
			x.imageLines()
			level := slog.LevelDebug
			if len(x.deps) > 0 {
				level = slog.LevelInfo
			}
			slog.Log(context.Background(), level, "unparsable yaml, image lines read one by one", "file", x.file, "images", len(x.deps), "err", err)
			return
		}
		if len(doc.Content) == 1 && isCompose(doc.Content[0]) {
			x.compose(doc.Content[0])
		} else {
			x.walk(&doc)
		}
	}
}

// imageLineRe matches a block-style `image:` key (optionally a sequence item) and its value up
// to the end of the line.
var imageLineRe = regexp.MustCompile(`(?m)^[ \t]*(?:-[ \t]+)?image:[ \t]+(\S.*?)[ \t]*\r?$`)

// imageLines extracts the values of `image:` lines (Kubernetes containers) of a file that is
// not valid YAML, typically a Helm template: a value with `{{` is a "template value", a plain
// or quoted value is a reference (a trailing comment is dropped). Other values (escapes,
// multi-line or flow values) are none.
func (x *extractor) imageLines() {
	for _, m := range imageLineRe.FindAllSubmatchIndex(x.src, -1) {
		off, v := m[2], string(x.src[m[2]:m[3]])
		if strings.Contains(v, "{{") {
			x.add(TypeKubernetes, x.lineOf(off), off, v)
			continue
		}
		switch q := v[0]; q {
		case '"', '\'':
			end := strings.IndexByte(v[1:], q) + 1
			rest := strings.TrimLeft(v[end+1:], " \t")
			if end == 0 || rest != "" && rest[0] != '#' || strings.ContainsAny(v[1:end], `\ `) {
				continue
			}
			off, v = off+1, v[1:end]
		case '#', '[', '{', '|', '>', '&', '*', '!':
			continue
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimRight(v[:i], " \t")
			}
			if strings.ContainsAny(v, " \t") {
				continue
			}
		}
		if v != "" {
			x.add(TypeKubernetes, x.lineOf(off), off, v)
		}
	}
}

// mayHaveImage reports whether manifests must parse a document: each reference it extracts
// sits under an "image" or "repository" key (a Helm image mapping keyed ...Image or ...image
// has a repository key) or is a compose "docker-image://" context, and a templated document
// ("{{") is parsed too, so that an unparsable Helm template is read line by line. Large
// CustomResourceDefinitions mention images in descriptions; matching keys skips them.
func mayHaveImage(doc []byte) bool {
	return hasKey(doc, "image") || hasKey(doc, "repository") ||
		bytes.Contains(doc, []byte("docker-image://")) || bytes.Contains(doc, []byte("{{"))
}

// hasKey reports whether word occurs in doc followed by ':', optionally after a closing quote
// and blanks: the shape of a YAML key word (plain, quoted or in a flow mapping). Prose
// mentioning word rarely matches.
func hasKey(doc []byte, word string) bool {
	for {
		i := bytes.Index(doc, []byte(word))
		if i < 0 {
			return false
		}
		doc = doc[i+len(word):]
		rest := doc
		if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
			rest = rest[1:]
		}
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) > 0 && rest[0] == ':' {
			return true
		}
	}
}

var containerKeys = map[string]bool{"containers": true, "initContainers": true, "ephemeralContainers": true}

// walk visits every mapping below n. A `containers` item with a `container` key is an Azure
// Pipelines container resource (`resources.containers`), which the azure-pipelines manager
// extracts; Kubernetes containers have none.
func (x *extractor) walk(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			switch {
			case containerKeys[k] && v.Kind == yaml.SequenceNode:
				for _, c := range v.Content {
					if yamlspan.Get(c, "container") == nil {
						x.scalar(TypeKubernetes, yamlspan.Get(c, "image"), "")
					}
				}
			case v.Kind == yaml.MappingNode && strings.HasSuffix(strings.ToLower(k), "image"):
				x.helm(v)
			}
		}
	}
	for _, c := range n.Content {
		x.walk(c)
	}
}

// helm extracts `{registry?, repository, tag, digest?}` image values with RefSpan on the tag.
// NameParts locate the repository value (NameWhole), or with a registry key the registry and
// the repository (NameRegistry, NameRepository), which replacements rewrite.
func (x *extractor) helm(m *yaml.Node) {
	repo, tag := yamlspan.Str(yamlspan.Get(m, "repository")), yamlspan.Str(yamlspan.Get(m, "tag"))
	if repo == nil || tag == nil || repo.Value == "" {
		return
	}
	name := repo.Value
	registry := yamlspan.Str(yamlspan.Get(m, "registry"))
	if registry != nil && registry.Value != "" {
		name = registry.Value + "/" + name
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeHelm, File: x.file, Line: tag.Line, Name: name}
	off := yamlspan.Locate(x.src, x.lines, tag)
	resolved, aliased := Alias(name, x.aliases)
	switch {
	case strings.Contains(name, "{{") || strings.Contains(tag.Value, "{{"):
		d.Skip = "template value"
	case strings.Contains(resolved, "$") || strings.Contains(tag.Value, "$"):
		d.Skip = skipVariable
	case tag.Value == "":
		// Charts default an empty tag to their appVersion, not to latest.
		ParseRef(&d, 0, resolved)
		if d.Skip == "" {
			d.Version, d.RefSpan, d.Digest, d.DigestSpan, d.InsertAt, d.Skip = "", dep.Span{}, "", dep.Span{}, 0, "no tag"
		}
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		ref := resolved + ":" + tag.Value
		ParseRef(&d, off+len(tag.Value)-len(ref), ref)
		if dg := yamlspan.Str(yamlspan.Get(m, "digest")); d.Skip == "" && d.Digest == "" && dg != nil && strings.HasPrefix(dg.Value, "sha256:") {
			if o := yamlspan.Locate(x.src, x.lines, dg); o >= 0 {
				d.Digest, d.DigestSpan = dg.Value, dep.Span{Start: o, End: o + len(dg.Value)}
			} else {
				d.Skip = "cannot locate value"
			}
		}
		if d.Skip == "" && !aliased {
			x.helmNameParts(&d, registry, repo)
		}
	}
	x.deps = append(x.deps, d)
}

// helmNameParts sets d's NameParts from the registry (nil or empty: none) and repository
// values; none when a value cannot be located.
func (x *extractor) helmNameParts(d *dep.Dep, registry, repo *yaml.Node) {
	ro := yamlspan.Locate(x.src, x.lines, repo)
	if ro < 0 {
		return
	}
	rp := dep.NamePart{Span: dep.Span{Start: ro, End: ro + len(repo.Value)}, Text: repo.Value}
	if registry == nil || registry.Value == "" {
		d.NameParts = []dep.NamePart{rp}
		return
	}
	gof := yamlspan.Locate(x.src, x.lines, registry)
	if gof < 0 {
		return
	}
	rp.Role = dep.NameRepository
	d.NameParts = []dep.NamePart{{Span: dep.Span{Start: gof, End: gof + len(registry.Value)}, Text: registry.Value, Role: dep.NameRegistry}, rp}
}

// workflow extracts jobs.*.container[.image], jobs.*.services.*.image and
// `uses: docker://` steps of workflows, and runs.image / `uses: docker://` steps of actions.
func (x *extractor) workflow(root *yaml.Node) {
	for _, job := range mappingValues(yamlspan.Get(root, "jobs")) {
		if c := yamlspan.Get(job, "container"); c != nil && c.Kind == yaml.MappingNode {
			x.scalar(TypeWorkflow, yamlspan.Get(c, "image"), "")
		} else {
			x.scalar(TypeWorkflow, c, "")
		}
		for _, svc := range mappingValues(yamlspan.Get(job, "services")) {
			x.scalar(TypeWorkflow, yamlspan.Get(svc, "image"), "")
		}
		x.steps(TypeWorkflow, yamlspan.Get(job, "steps"))
	}
	runs := yamlspan.Get(root, "runs")
	if u := yamlspan.Get(runs, "using"); u != nil && u.Value == "docker" {
		x.scalar(TypeAction, yamlspan.Get(runs, "image"), dockerPrefix)
	}
	x.steps(TypeAction, yamlspan.Get(runs, "steps"))
}

func (x *extractor) steps(depType string, steps *yaml.Node) {
	for _, s := range yamlspan.Seq(steps) {
		x.scalar(depType, yamlspan.Get(s, "uses"), dockerPrefix)
	}
}

// scalar adds the image reference in v. With a non-empty prefix, only values starting with it
// are references, and the prefix is not part of the reference.
func (x *extractor) scalar(depType string, v *yaml.Node, prefix string) {
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" || !strings.HasPrefix(v.Value, prefix) || v.Value == prefix {
		return
	}
	off := yamlspan.Locate(x.src, x.lines, v)
	if off >= 0 {
		off += len(prefix)
	}
	x.add(depType, v.Line, off, v.Value[len(prefix):])
}

// mappingValues returns the values of mapping m (nil if m is not a mapping).
func mappingValues(m *yaml.Node) []*yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	vs := make([]*yaml.Node, 0, len(m.Content)/2)
	for i := 1; i < len(m.Content); i += 2 {
		vs = append(vs, m.Content[i])
	}
	return vs
}
