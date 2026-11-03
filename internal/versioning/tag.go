// Package versioning parses, compares and selects versions per versioning scheme
// (Actions tags, Go modules, Docker tags, semver).
package versioning

import (
	"strconv"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Tag is a 1–3 part numeric version with an optional "v" prefix.
type Tag struct {
	Prefix string
	Parts  []int
}

// ParseTag parses `^(v?)(\d+)(?:\.(\d+))?(?:\.(\d+))?$`; suffixed and prerelease tags never
// parse. It is a hand-written scanner: it runs for every candidate of every dependency, and
// invalid input allocates nothing.
func ParseTag(s string) (Tag, bool) {
	var t Tag
	if s != "" && s[0] == 'v' {
		t.Prefix, s = "v", s[1:]
	}
	var parts [3]int
	n := 0
	for {
		i := 0
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		if i == 0 || n == len(parts) {
			return Tag{}, false
		}
		v, err := strconv.Atoi(s[:i])
		if err != nil {
			return Tag{}, false // overflow
		}
		parts[n], n, s = v, n+1, s[i:]
		if s == "" {
			break
		}
		if s[0] != '.' {
			return Tag{}, false
		}
		s = s[1:]
	}
	t.Parts = append(make([]int, 0, n), parts[:n]...)
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
