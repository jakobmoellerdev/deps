// Package docker extracts container image references from Dockerfiles, compose files,
// workflows (`container:`, `services:`, `uses: docker://`), Docker actions (`runs.image`),
// Kubernetes manifests (container `image:`) and Helm values (`*image: {repository, tag}`).
package docker

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "docker"

// Dep types.
const (
	TypeDockerfile = "dockerfile"
	TypeCompose    = "compose"
	TypeWorkflow   = "workflow"
	TypeAction     = "action"
	TypeKubernetes = "kubernetes"
	TypeHelm       = "helm"
)

// composeRe matches the compose file names; it runs per extracted file, not per walked path.
var composeRe = regexp.MustCompile(`(^|/)([^/]*\.)?(docker-)?compose(\.[^/]*)?\.ya?ml$`)

type dockerManager struct{}

// New returns the docker manager.
func New() manager.Manager { return dockerManager{} }

func (dockerManager) Name() string { return Name }

// Match reports whether the slash path is a Dockerfile/Containerfile or a YAML file (compose,
// workflow, action, or any other YAML scanned for Kubernetes and Helm images).
func (dockerManager) Match(p string) bool {
	return isDockerfile(p) || strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
}

// isDockerfile reports whether the base name is a Dockerfile/Containerfile name, matching
// Renovate's dockerfile managerFilePatterns: the name with any suffix (`Dockerfile`,
// `Dockerfile.dev`, `Dockerfile-prod`, `dockerfile_ci`) or with any prefix ending in a dot
// (`dev.Dockerfile`), in both the capitalized and the lowercase spelling.
// `Dockerfile.dockerignore` is an ignore file, not a Dockerfile. Matching the name directly
// keeps the walk off the regexp engine.
func isDockerfile(p string) bool {
	if strings.HasSuffix(p, "/") {
		return false
	}
	b := path.Base(p)
	for _, name := range [...]string{"Dockerfile", "dockerfile", "Containerfile", "containerfile"} {
		if strings.HasPrefix(b, name) || strings.HasSuffix(b, "."+name) {
			return !strings.HasSuffix(b, ".dockerignore")
		}
	}
	return false
}

// Extract returns every image reference in src with byte spans into src.
func (dockerManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	switch {
	case isDockerfile(file):
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: env.RegistryAliases}
		x.dockerfile()
		return x.deps, nil
	case strings.Contains(file, "compose") && composeRe.MatchString(file):
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: env.RegistryAliases}
		if err := x.yaml(env.YAML, x.compose); err != nil {
			// Not standalone YAML (e.g. an indented fragment concatenated by a script).
			slog.Debug("skipping unparsable compose file", "file", file, "err", err)
			return []dep.Dep{{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeCompose,
				File: file, Line: 1, Skip: "unparsable compose file"}}, nil
		}
		return x.deps, nil
	case actions.IsWorkflowFile(file):
		// Every reference workflow reads is under a container or image key or starts with
		// docker://; without them there is nothing to parse for (the github-actions manager
		// parses, and reports errors of, every workflow).
		if !bytes.Contains(src, []byte("container")) && !bytes.Contains(src, []byte("image")) &&
			!bytes.Contains(src, []byte("docker://")) {
			return nil, nil
		}
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: env.RegistryAliases}
		// x.deps is read after the walk filled it: in one return statement the order of the
		// operand and the call is unspecified.
		err := x.yaml(env.YAML, x.workflow)
		return x.deps, err
	}
	if !bytes.Contains(src, []byte("image:")) && !bytes.Contains(src, []byte("repository:")) {
		return nil, nil
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: env.RegistryAliases}
	x.manifests(env.YAML)
	return x.deps, nil
}

// Edits rewrites the reference: tag → tag, pinned → new digest (and tag when the version
// changed), or tag → digest by appending "@<digest>" after the reference. A replacement
// (u.NewName) also rewrites the image name.
func (dockerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewName != "" {
		ne, err := manager.NameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = ne
	}
	switch {
	case u.NewVersion == d.Version:
	case Untagged(d):
		// The tag is written after the name, before a digest: ":<tag>@<digest>".
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, New: ":" + u.NewVersion})
	default:
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a digest-pinned image")
		}
	case d.Digest != "":
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	default:
		edits = append(edits, patch.Edit{Start: d.InsertAt, End: d.InsertAt, New: "@" + u.NewDigest})
	}
	return edits, nil
}

// Rename implements manager.Renamer: name is an image name without tag or digest.
func (dockerManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	return Rename(d, name)
}

// Rename returns d as the image name names it (Name, Source), for the managers editing
// image references like the docker manager.
func Rename(d dep.Dep, name string) (dep.Dep, error) {
	r, err := imageref.Parse(name)
	switch {
	case err != nil:
		return d, err
	case r.Tag != "" || r.Digest != "":
		return d, fmt.Errorf("image name %q has a tag or digest", name)
	}
	d.Name, d.Source = r.Familiar, r.Name
	return d, nil
}

type extractor struct {
	file    string
	src     []byte
	lines   []int
	aliases map[string]string // config registryAliases
	deps    []dep.Dep
	seen    map[*yaml.Node]bool // compose: nodes already extracted (anchors)
}

// lineOf returns the 1-based line of byte offset off.
func (x *extractor) lineOf(off int) int {
	lo, hi := 0, len(x.lines)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if x.lines[m] <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// add records the image reference ref found at byte offset off (-1 = not locatable) on line.
func (x *extractor) add(depType string, line, off int, ref string) {
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: depType, File: x.file, Line: line, Name: ref}
	if depType == TypeKubernetes && strings.Contains(ref, "{{") {
		d.Skip = "template value"
	} else {
		ParseImage(&d, off, ref, x.aliases)
		if off >= 0 && d.Skip != skipVariable {
			d.Line = x.lineOf(off)
		}
	}
	x.deps = append(x.deps, d)
}

const skipVariable = "variable reference"

// implicitTag is the tag of a reference written without one: what docker pulls, and what
// Renovate looks it up as.
const implicitTag = "latest"

// ParseRef fills d from ref, which starts at byte offset off. Tags that are not versions
// (latest, bookworm) are kept: the engine refreshes or pins their digest. A reference without
// a tag (`alpine`, `alpine@sha256:…`) is the image's implicit `latest` (Untagged): its digest
// is pinned (pin: digest) or refreshed like that of `alpine:latest`.
func ParseRef(d *dep.Dep, off int, ref string) {
	r, err := imageref.Parse(ref)
	if err != nil {
		d.Skip = "invalid reference"
		return
	}
	d.Name, d.Source = r.Familiar, r.Name
	end := off + len(ref)
	if r.Digest != "" {
		d.Digest = r.Digest
		d.DigestSpan = dep.Span{Start: end - len(d.Digest), End: end}
		end -= len(d.Digest) + 1
	}
	d.InsertAt = end
	if r.Tag == "" {
		d.Version, d.RefSpan = implicitTag, dep.Span{Start: end, End: end}
		return
	}
	d.Version = r.Tag
	d.RefSpan = dep.Span{Start: end - len(d.Version), End: end}
}

// Untagged reports whether the image reference d was parsed from (ParseRef) has no tag: its
// version is the implicit `latest` and its RefSpan the empty span where `:<tag>` would go.
func Untagged(d dep.Dep) bool { return d.Version == implicitTag && d.RefSpan.Start == d.RefSpan.End }

// ParseInlineRef is ParseRef for a reference written as one token at off: one NameWhole part
// locates the image name as written, which replacements rewrite.
func ParseInlineRef(d *dep.Dep, off int, ref string) {
	ParseRef(d, off, ref)
	if d.Skip != "" {
		return
	}
	end := d.RefSpan.Start - 1 // the ':' before the tag
	if Untagged(*d) {
		end = d.RefSpan.Start
	}
	d.NameParts = []dep.NamePart{{Span: dep.Span{Start: off, End: end}, Text: ref[:end-off]}}
}

// ParseImage fills d from the image reference ref written as one token at off (-1: its bytes
// were not found) like ParseInlineRef, after resolving a registry alias (Alias). A reference
// with a variable no alias resolves is a "variable reference". An aliased reference is looked
// up as resolved, its tag and digest edited in place; its name, which the alias keeps as
// written, is not renamable.
func ParseImage(d *dep.Dep, off int, ref string, aliases map[string]string) {
	resolved, aliased := Alias(ref, aliases)
	switch {
	case strings.Contains(resolved, "$"):
		d.Skip = skipVariable
	case off < 0:
		d.Skip = "cannot locate value"
	case aliased:
		// The tag and digest end the reference in both forms: their spans count from the end.
		ParseRef(d, off+len(ref)-len(resolved), resolved)
	default:
		ParseInlineRef(d, off, ref)
	}
}

// Alias resolves a registry alias of aliases (config registryAliases, Renovate's
// registryAliases) at the start of ref: the longest key followed by "/", or for a key ending
// in "}" (`${REGISTRY}`, `${REGISTRY:-}`) the longest key, is replaced by its value ("" =
// Docker Hub; a trailing slash is ignored). ok is false, and resolved is ref, when no key
// matches.
func Alias(ref string, aliases map[string]string) (resolved string, ok bool) {
	v, rest, ok := alias(ref, aliases)
	if !ok {
		return ref, false
	}
	return v + ref[rest:], true
}

// alias is Alias: ref resolves to v+ref[rest:].
func alias(ref string, aliases map[string]string) (v string, rest int, ok bool) {
	key := ""
	for k := range aliases {
		var n int
		switch {
		case k == "" || len(k) <= len(key) || !strings.HasPrefix(ref, k):
			continue
		case len(ref) > len(k) && ref[len(k)] == '/':
			n = len(k) + 1
		case strings.HasSuffix(k, "}"):
			n = len(k)
		default:
			continue
		}
		key, rest = k, n
	}
	if key == "" {
		return "", 0, false
	}
	v = strings.TrimSuffix(aliases[key], "/")
	if v != "" {
		v += "/"
	}
	return v, rest, true
}
