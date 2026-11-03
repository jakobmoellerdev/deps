// Package versioning parses, compares and selects versions per versioning scheme
// (Actions tags, Go modules, Docker tags, semver).
package versioning

import (
	"regexp"
	"strconv"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

var tagRe = regexp.MustCompile(`^(v?)(\d+)(?:\.(\d+))?(?:\.(\d+))?$`)

// Tag is a 1–3 part numeric version with an optional "v" prefix.
type Tag struct {
	Prefix string
	Parts  []int
}

// ParseTag parses s; suffixed and prerelease tags never parse.
func ParseTag(s string) (Tag, bool) {
	m := tagRe.FindStringSubmatch(s)
	if m == nil {
		return Tag{}, false
	}
	t := Tag{Prefix: m[1], Parts: make([]int, 0, 3)}
	for _, p := range m[2:] {
		if p == "" {
			break
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Tag{}, false
		}
		t.Parts = append(t.Parts, n)
	}
	return t, true
}

// CompareTag compares parts lexicographically.
func CompareTag(a, b Tag) int {
	for i := 0; i < len(a.Parts) && i < len(b.Parts); i++ {
		if a.Parts[i] != b.Parts[i] {
			if a.Parts[i] < b.Parts[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Parts) < len(b.Parts):
		return -1
	case len(a.Parts) > len(b.Parts):
		return 1
	}
	return 0
}

// TagKind classifies cur → next.
func TagKind(cur, next Tag) dep.UpdateType {
	if part(cur, 0) != part(next, 0) {
		return dep.Major
	}
	if part(cur, 1) != part(next, 1) {
		return dep.Minor
	}
	return dep.Patch
}

func part(t Tag, i int) int {
	if i < len(t.Parts) {
		return t.Parts[i]
	}
	return 0
}

// Semver returns the tag as a semver version, padding missing parts with 0.
func (t Tag) Semver() *semver.Version {
	return semver.New(uint64(part(t, 0)), uint64(part(t, 1)), uint64(part(t, 2)), "", "")
}
