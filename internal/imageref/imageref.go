// Package imageref parses container image references the way docker does: names without a
// registry are on Docker Hub (docker.io), and single-component Docker Hub names are in its
// library namespace. Splitting and validation are oras-go's properties.NewReference.
package imageref

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/oras-project/oras-go/v3/registry/remote/properties"
)

const (
	dockerHub      = "docker.io"
	dockerHubIndex = "index.docker.io" // legacy alias of docker.io
	library        = "library/"
)

// domainRe is docker's registry grammar (distribution/reference): DNS labels joined by dots or
// a bracketed IPv6 address, then an optional port. properties.NewReference accepts more.
var domainRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*|\[[a-fA-F0-9:]+\])(?::[0-9]+)?$`)

// Ref is a parsed image reference.
type Ref struct {
	// Name is the canonical repository: registry and path, e.g. docker.io/library/alpine.
	Name string
	// Familiar is the short name docker shows, e.g. alpine or ghcr.io/org/img.
	Familiar string
	// Tag and Digest are empty when absent.
	Tag    string
	Digest string
}

// Parse parses ref ([registry/]path[:tag][@digest]). URL schemes are rejected.
func Parse(ref string) (Ref, error) {
	if strings.Contains(ref, "://") || strings.Contains(ref, ":@") {
		return Ref{}, fmt.Errorf("invalid reference %q", ref)
	}
	registry, rest := qualify(ref)
	if !domainRe.MatchString(registry) {
		return Ref{}, fmt.Errorf("invalid reference %q: invalid registry %q", ref, registry)
	}
	p, err := properties.NewReference(registry + "/" + rest)
	if err != nil {
		return Ref{}, err
	}
	// p.Registry is lower-cased; keep the registry as written, as docker does.
	name := registry + "/" + p.Repository
	r := Ref{Name: name, Familiar: name, Tag: p.Tag, Digest: p.Digest}
	if registry == dockerHub {
		r.Familiar = p.Repository
		if short, ok := strings.CutPrefix(p.Repository, library); ok && !strings.Contains(short, "/") {
			r.Familiar = short
		}
	}
	return r, nil
}

// SplitRegistry splits an image name into its registry and the rest as docker reads it:
// docker.io and the name unchanged when its first component is not a registry (alpine,
// bitnami/nginx).
func SplitRegistry(name string) (registry, path string) {
	if r, rest, ok := strings.Cut(name, "/"); ok && isRegistry(r) {
		return r, rest
	}
	return dockerHub, name
}

// isRegistry reports whether the first component of a reference is a registry host: it has
// a '.' or ':', is localhost or is not lower case.
func isRegistry(c string) bool {
	return strings.ContainsAny(c, ".:") || c == "localhost" || strings.ToLower(c) != c
}

// qualify splits ref into registry and the rest: docker.io when its first component is not a
// registry (no '.' or ':', not localhost, lower case), with library/ added to single-component
// Docker Hub names.
func qualify(ref string) (registry, rest string) {
	registry, rest, ok := strings.Cut(ref, "/")
	if !ok || !isRegistry(registry) {
		registry, rest = dockerHub, ref
	}
	if registry == dockerHubIndex {
		registry = dockerHub
	}
	if registry == dockerHub {
		path, _, _ := strings.Cut(rest, "@")
		if path, _, _ = strings.Cut(path, ":"); !strings.Contains(path, "/") {
			rest = library + rest
		}
	}
	return registry, rest
}
