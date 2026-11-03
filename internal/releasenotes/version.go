package releasenotes

import (
	"strconv"
	"strings"
)

// version is a dotted numeric version with an optional prerelease; build metadata is dropped.
// Precision (len(parts)) matters: a bound "v7" covers every 7.x.y.
type version struct {
	parts []int
	pre   string
}

// parseVersion accepts `[v]N(.N)*[-pre][+build]`.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	s, pre, _ := strings.Cut(s, "-")
	if s == "" {
		return version{}, false
	}
	var v version
	for p := range strings.SplitSeq(s, ".") {
		if p == "" || p[0] < '0' || p[0] > '9' {
			return version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v.parts = append(v.parts, n)
	}
	v.pre = pre
	return v, true
}

// compareAt compares x with bound b at b's precision: x is equal to b when they agree on b's
// parts and x is more precise (7.0.1 is within "7"). At equal precision a prerelease sorts
// before the release.
func compareAt(x, b version) int {
	for i, bp := range b.parts {
		xp := 0
		if i < len(x.parts) {
			xp = x.parts[i]
		}
		switch {
		case xp < bp:
			return -1
		case xp > bp:
			return 1
		}
	}
	if len(x.parts) > len(b.parts) {
		return 0
	}
	switch {
	case x.pre == b.pre:
		return 0
	case x.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return strings.Compare(x.pre, b.pre)
}

// compare orders two versions completely (missing parts are 0).
func compare(x, y version) int {
	if len(x.parts) >= len(y.parts) {
		return compareAt(x, y)
	}
	return -compareAt(y, x)
}

// tagVersion returns the version of tag after one of prefixes (each tried as is, then with
// "v"): with prefixes {"", "eslint@"} both "v1.2.0" and "eslint@1.2.0" are 1.2.0.
func tagVersion(tag string, prefixes []string) (version, bool) {
	for _, p := range prefixes {
		rest, ok := strings.CutPrefix(tag, p)
		if !ok {
			continue
		}
		if v, ok := parseVersion(rest); ok {
			return v, true
		}
	}
	return version{}, false
}
