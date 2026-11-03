// Package docker extracts container image references from Dockerfiles, compose files,
// workflows (`container:`, `services:`, `uses: docker://`) and Docker actions (`runs.image`).
package docker

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"github.com/distribution/reference"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "docker"

// Dep types.
const (
	TypeDockerfile = "dockerfile"
	TypeCompose    = "compose"
	TypeWorkflow   = "workflow"
	TypeAction     = "action"
)

var (
	dockerfileRe    = regexp.MustCompile(`(^|/)(Dockerfile|Containerfile)(\.[^/]*)?$`)
	dockerfileExtRe = regexp.MustCompile(`(^|/)[^/]*\.(Dockerfile|Containerfile)$`)
	composeRe       = regexp.MustCompile(`(^|/)(docker-)?compose(\.[^/]*)?\.ya?ml$`)
	workflowRe      = regexp.MustCompile(`(^|/)(workflow-templates|\.(github|gitea|forgejo)/(workflows|actions))/.+\.ya?ml$`)
	actionRe        = regexp.MustCompile(`(^|/)action\.ya?ml$`)
)

type dockerManager struct{}

// New returns the docker manager.
func New() manager.Manager { return dockerManager{} }

func (dockerManager) Name() string { return Name }

// Match reports whether the slash path is a Dockerfile/Containerfile, a compose file, or a
// workflow or action file.
func (dockerManager) Match(path string) bool {
	return isDockerfile(path) || composeRe.MatchString(path) || workflowRe.MatchString(path) || actionRe.MatchString(path)
}

// isDockerfile excludes `Dockerfile.dockerignore`, which is an ignore file, not a Dockerfile.
func isDockerfile(path string) bool {
	return (dockerfileRe.MatchString(path) || dockerfileExtRe.MatchString(path)) && !strings.HasSuffix(path, ".dockerignore")
}

// Extract returns every image reference in src with byte spans into src.
func (dockerManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	x := extractor{file: file, src: src, lines: lineStarts(src)}
	switch {
	case isDockerfile(file):
		x.dockerfile()
		return x.deps, nil
	case composeRe.MatchString(file):
		return x.deps, x.yaml(x.compose)
	}
	return x.deps, x.yaml(x.workflow)
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
}

func lineStarts(src []byte) []int {
	ls := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
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
	case strings.Contains(ref, "$"):
		d.Skip = "variable reference"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		d.Line = x.lineOf(off)
		parseRef(&d, off, ref)
	}
	x.deps = append(x.deps, d)
}

var dockerScheme, _ = versioning.Get("docker")

// parseRef fills d from ref, which starts at byte offset off.
func parseRef(d *dep.Dep, off int, ref string) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		d.Skip = "invalid reference"
		return
	}
	d.Name, d.Source = reference.FamiliarName(named), named.Name()
	end := off + len(ref)
	if dg, ok := named.(reference.Digested); ok {
		d.Digest = dg.Digest().String()
		d.DigestSpan = dep.Span{Start: end - len(d.Digest), End: end}
		end -= len(d.Digest) + 1
	}
	d.InsertAt = end
	t, ok := named.(reference.Tagged)
	switch {
	case !ok && d.Digest != "":
		d.Skip = "digest without tag"
	case !ok:
		d.Skip = "no tag"
	default:
		d.Version = t.Tag()
		d.RefSpan = dep.Span{Start: end - len(d.Version), End: end}
		if !dockerScheme.Valid(d.Version) {
			d.Skip = "non-version tag"
		}
	}
}
