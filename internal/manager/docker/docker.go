// Package docker extracts container image references from Dockerfiles, compose files,
// workflows (`container:`, `services:`, `uses: docker://`), Docker actions (`runs.image`),
// Kubernetes manifests (container `image:`) and Helm values (`*image: {repository, tag}`).
package docker

import (
	"bytes"
	"errors"
	"log/slog"
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

var (
	dockerfileRe    = regexp.MustCompile(`(^|/)(Dockerfile|Containerfile)(\.[^/]*)?$`)
	dockerfileExtRe = regexp.MustCompile(`(^|/)[^/]*\.(Dockerfile|Containerfile)$`)
	composeRe       = regexp.MustCompile(`(^|/)([^/]*\.)?(docker-)?compose(\.[^/]*)?\.ya?ml$`)
)

type dockerManager struct{}

// New returns the docker manager.
func New() manager.Manager { return dockerManager{} }

func (dockerManager) Name() string { return Name }

// Match reports whether the slash path is a Dockerfile/Containerfile or a YAML file (compose,
// workflow, action, or any other YAML scanned for Kubernetes and Helm images).
func (dockerManager) Match(path string) bool {
	return isDockerfile(path) || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// isDockerfile excludes `Dockerfile.dockerignore`, which is an ignore file, not a Dockerfile.
// The name check skips the regexps for the paths that cannot match.
func isDockerfile(path string) bool {
	if !strings.Contains(path, "Dockerfile") && !strings.Contains(path, "Containerfile") {
		return false
	}
	return (dockerfileRe.MatchString(path) || dockerfileExtRe.MatchString(path)) && !strings.HasSuffix(path, ".dockerignore")
}

// Extract returns every image reference in src with byte spans into src.
func (dockerManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	switch {
	case isDockerfile(file):
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
		x.dockerfile()
		return x.deps, nil
	case composeRe.MatchString(file):
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
		if err := x.yaml(x.compose); err != nil {
			// Not standalone YAML (e.g. an indented fragment concatenated by a script).
			slog.Debug("skipping unparsable compose file", "file", file, "err", err)
			return []dep.Dep{{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeCompose,
				File: file, Line: 1, Skip: "unparsable compose file"}}, nil
		}
		return x.deps, nil
	case actions.IsWorkflowFile(file):
		x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
		return x.deps, x.yaml(x.workflow)
	}
	if !bytes.Contains(src, []byte("image:")) && !bytes.Contains(src, []byte("repository:")) {
		return nil, nil
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	x.manifests()
	return x.deps, nil
}

// Edits rewrites the reference: tag → tag, pinned → new digest (and tag when the version
// changed), or tag → digest by appending "@<digest>" after the reference.
func (dockerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != d.Version {
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

type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
	seen  map[*yaml.Node]bool // compose: nodes already extracted (anchors)
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
	switch {
	case depType == TypeKubernetes && strings.Contains(ref, "{{"):
		d.Skip = "template value"
	case strings.Contains(ref, "$"):
		d.Skip = "variable reference"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		d.Line = x.lineOf(off)
		ParseRef(&d, off, ref)
	}
	x.deps = append(x.deps, d)
}

// ParseRef fills d from ref, which starts at byte offset off. Tags that are not versions
// (latest, bookworm) are kept: the engine refreshes or pins their digest.
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
	switch {
	case r.Tag == "" && d.Digest != "":
		d.Skip = "digest without tag"
	case r.Tag == "":
		d.Skip = "no tag"
	default:
		d.Version = r.Tag
		d.RefSpan = dep.Span{Start: end - len(d.Version), End: end}
	}
}
