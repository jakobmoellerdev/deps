// Package imageref parses container image references the way docker does: names without a
// registry are on Docker Hub (docker.io), and single-component Docker Hub names are in its
// library namespace. Splitting and validation follow the distribution/reference grammar:
// host[:port]/path[:tag][@digest].
package imageref

import (
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
	"regexp"
	"strings"
)

const (
	dockerHub      = "docker.io"
	dockerHubIndex = "index.docker.io" // legacy alias of docker.io
	library        = "library/"
)

var (
	// domainRe is docker's registry grammar: DNS labels joined by dots or a bracketed IPv6
	// address, then an optional port.
	domainRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*|\[[a-fA-F0-9:]+\])(?::[0-9]+)?$`)
	// pathRe is the repository path: lower-case components of alphanumerics joined by '.', '_',
	// '__' or dashes, separated by '/'.
	pathRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|[-]*)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|[-]*)[a-z0-9]+)*)*$`)
	tagRe  = regexp.MustCompile(`^\w[\w.-]{0,127}$`)
)

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
	path, dg, hasDigest := strings.Cut(rest, "@")
	if hasDigest {
		if _, err := NewDigester(dg); err != nil {
			return Ref{}, fmt.Errorf("invalid reference %q: %w", ref, err)
		}
	}
	path, tag, hasTag := strings.Cut(path, ":")
	if hasTag && !tagRe.MatchString(tag) {
		return Ref{}, fmt.Errorf("invalid reference %q: invalid tag %q", ref, tag)
	}
	if !pathRe.MatchString(path) {
		return Ref{}, fmt.Errorf("invalid reference %q: invalid repository %q", ref, path)
	}
	// The registry is kept as written, as docker does.
	name := registry + "/" + path
	r := Ref{Name: name, Familiar: name, Tag: tag, Digest: dg}
	if registry == dockerHub {
		r.Familiar = path
		if short, ok := strings.CutPrefix(path, library); ok && !strings.Contains(short, "/") {
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

// NewDigester validates digest (algorithm:hex, with sha256, sha384 or sha512 and lower-case
// hex of the algorithm's length) and returns a hash of its algorithm, for verifying content
// against it.
func NewDigester(digest string) (hash.Hash, error) {
	alg, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return nil, fmt.Errorf("invalid digest %q: no algorithm", digest)
	}
	var h hash.Hash
	switch alg {
	case "sha256":
		h = sha256.New()
	case "sha384":
		h = sha512.New384()
	case "sha512":
		h = sha512.New()
	default:
		return nil, fmt.Errorf("invalid digest %q: unsupported algorithm", digest)
	}
	if len(hex) != 2*h.Size() || strings.Trim(hex, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("invalid digest %q: invalid encoded portion", digest)
	}
	return h, nil
}
