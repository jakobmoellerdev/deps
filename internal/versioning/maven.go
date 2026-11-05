package versioning

import (
	"slices"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// mavenVersion is a version ordered like Maven 3.x ComparableVersion (before the 3.9
// combination rules): 1-alpha-1 < 1-rc < 1-SNAPSHOT < 1 == 1.0 < 1-sp < 1-jre < 1-1.
type mavenVersion struct {
	items    []mavenItem // normalized top-level list
	pre      bool        // a qualifier below release (alpha, beta, milestone, rc, snapshot)
	snapshot bool
	suffix   string    // first unknown qualifier after a "-" (guava "jre"); see mavenSuffix
	ints     [3]string // leading top-level integers without leading zeros; "" = 0
	rest     string    // lowercased text after the leading integers, for semver prereleases
}

type mavenKind uint8

const (
	mavenInt mavenKind = iota
	mavenString
	mavenList
)

// mavenItem is an integer (num: digits without leading zeros, "" = 0), a qualifier (str:
// alias-resolved value, key: comparable form) or a nested list.
type mavenItem struct {
	kind     mavenKind
	num, str string
	key      string
	list     []mavenItem
}

// mavenQualifiers are the known qualifiers in order; "" is a release.
var mavenQualifiers = []string{"alpha", "beta", "milestone", "rc", "snapshot", "", "sp"}

// mavenReleaseKey is the comparable key of "".
const mavenReleaseKey = "5"

func mavenQualifierKey(s string) string {
	for i, q := range mavenQualifiers {
		if q == s {
			return strconv.Itoa(i)
		}
	}
	return strconv.Itoa(len(mavenQualifiers)) + "-" + s
}

func mavenStr(s string, followedByDigit bool) mavenItem {
	if followedByDigit && len(s) == 1 {
		switch s {
		case "a":
			s = "alpha"
		case "b":
			s = "beta"
		case "m":
			s = "milestone"
		}
	}
	switch s {
	case "ga", "final", "release":
		s = ""
	case "cr":
		s = "rc"
	}
	return mavenItem{kind: mavenString, str: s, key: mavenQualifierKey(s)}
}

func mavenParseItem(digit bool, s string) mavenItem {
	if digit {
		return mavenItem{kind: mavenInt, num: strings.TrimLeft(s, "0")}
	}
	return mavenStr(s, false)
}

func (it mavenItem) isNull() bool {
	switch it.kind {
	case mavenInt:
		return it.num == ""
	case mavenString:
		return it.str == ""
	}
	return len(it.list) == 0
}

// mavenNormalize drops trailing null items, stopping at the first non-null non-list item.
func mavenNormalize(l []mavenItem) []mavenItem {
	for i, it := range slices.Backward(l) {
		if it.isNull() {
			l = append(l[:i], l[i+1:]...) // only visited items move
		} else if it.kind != mavenList {
			break
		}
	}
	return l
}

func validMaven(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func parseMaven(s string) (mavenVersion, bool) {
	if !validMaven(s) {
		return mavenVersion{}, false
	}
	s = strings.ToLower(s)
	// stack[i] is the list being built at depth i; a finished child is attached to its
	// parent's last item when popped.
	stack := [][]mavenItem{nil}
	push := func() { stack = append(stack, nil) }
	add := func(it mavenItem) { stack[len(stack)-1] = append(stack[len(stack)-1], it) }
	digit, start := false, 0
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '.' || c == '-':
			if i == start {
				add(mavenItem{kind: mavenInt})
			} else {
				add(mavenParseItem(digit, s[start:i]))
			}
			start = i + 1
			if c == '-' {
				push()
			}
		case '0' <= c && c <= '9':
			if !digit && i > start {
				add(mavenStr(s[start:i], true))
				start = i
				push()
			}
			digit = true
		default:
			if digit && i > start {
				add(mavenParseItem(true, s[start:i]))
				start = i
				push()
			}
			digit = false
		}
	}
	if len(s) > start {
		add(mavenParseItem(digit, s[start:]))
	}
	for len(stack) > 1 {
		child := mavenNormalize(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
		add(mavenItem{kind: mavenList, list: child})
	}
	v := mavenVersion{items: mavenNormalize(stack[0])}
	var walk func([]mavenItem)
	walk = func(l []mavenItem) {
		for _, it := range l {
			switch it.kind {
			case mavenString:
				if it.key < mavenReleaseKey {
					v.pre = true
					v.snapshot = v.snapshot || it.str == "snapshot"
				}
			case mavenList:
				walk(it.list)
			}
		}
	}
	walk(v.items)
	v.suffix = mavenSuffix(s)
	for i := 0; i < len(v.items) && i < len(v.ints) && v.items[i].kind == mavenInt; i++ {
		v.ints[i] = v.items[i].num
	}
	// rest: skip the leading dot-separated digit runs of the raw string.
	i := 0
	for n := 0; n < 3 && i < len(s) && '0' <= s[i] && s[i] <= '9'; n++ {
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		if i < len(s) && s[i] == '.' && i+1 < len(s) && '0' <= s[i+1] && s[i+1] <= '9' {
			i++
		}
	}
	v.rest = strings.TrimLeft(s[i:], ".-_")
	return v, true
}

// mavenSuffix returns the first unknown qualifier that follows a "-" in the lowercased s
// (guava `33.3.1-jre` -> "jre"), or "". Words after "." or a number are not suffixes:
// Jetty `9.4.51.v20230217` and Jenkins `6.2250.va_d01d559a_209` carry dates and hashes there.
func mavenSuffix(s string) string {
	for i := 0; i < len(s); i++ { // the body advances i past each word
		if s[i] != '-' {
			continue
		}
		j := i + 1
		for j < len(s) && 'a' <= s[j] && s[j] <= 'z' {
			j++
		}
		if j == i+1 {
			continue
		}
		it := mavenStr(s[i+1:j], j < len(s) && '0' <= s[j] && s[j] <= '9')
		if len(it.key) > 1 {
			return it.str
		}
		i = j - 1
	}
	return ""
}

// compareMavenNull compares it with an absent item.
func compareMavenNull(it mavenItem) int {
	switch it.kind {
	case mavenInt:
		if it.num == "" {
			return 0
		}
		return 1
	case mavenString:
		return strings.Compare(it.key, mavenReleaseKey)
	}
	if len(it.list) == 0 {
		return 0
	}
	return compareMavenNull(it.list[0])
}

func compareMavenItem(a, b mavenItem) int {
	switch a.kind {
	case mavenInt:
		if b.kind != mavenInt {
			return 1
		}
		return compareNum(a.num, b.num)
	case mavenString:
		if b.kind != mavenString {
			return -1
		}
		return strings.Compare(a.key, b.key)
	}
	switch b.kind {
	case mavenInt:
		return -1
	case mavenString:
		return 1
	}
	return compareMavenList(a.list, b.list)
}

func compareMavenList(a, b []mavenItem) int {
	for i := range max(len(a), len(b)) {
		var c int
		switch {
		case i >= len(a):
			c = -compareMavenNull(b[i])
		case i >= len(b):
			c = compareMavenNull(a[i])
		default:
			c = compareMavenItem(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

func compareMaven(a, b mavenVersion) int { return compareMavenList(a.items, b.items) }

// mavenCompat never offers SNAPSHOTs, offers prereleases only from a prerelease, and keeps
// the unknown-qualifier suffix (-jre never moves to -android).
func mavenCompat(cur, next mavenVersion) string {
	switch {
	case next.snapshot, next.pre && !cur.pre:
		return "prerelease"
	case next.suffix != cur.suffix:
		return "suffix"
	}
	return ""
}

func mavenKindOf(cur, next mavenVersion) dep.UpdateType {
	switch {
	case cur.ints[0] != next.ints[0]:
		return dep.Major
	case cur.ints[1] != next.ints[1]:
		return dep.Minor
	}
	return dep.Patch
}

func mavenUint(s string) uint64 {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil && s != "" {
		return ^uint64(0)
	}
	return n
}

func (v mavenVersion) semver() *msemver.Version {
	var pre string
	if v.pre {
		pre = strings.ReplaceAll(v.rest, "_", "-")
	}
	return msemver.New(mavenUint(v.ints[0]), mavenUint(v.ints[1]), mavenUint(v.ints[2]), pre, "")
}

func (v mavenVersion) major() string {
	if v.ints[0] == "" {
		return "0"
	}
	return v.ints[0]
}
