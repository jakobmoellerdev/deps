// Package pattern matches strings against Renovate-style pattern lists: doublestar globs,
// `/RE2/` regexes and `!`-negations. Globs match case-insensitively, as Renovate's minimatch
// globs (nocase); regexes are case-sensitive unless written `/expr/i`.
package pattern

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// List is a compiled pattern list with Renovate's matchRegexOrGlobList semantics.
type List struct {
	pos, neg []matcher
	nocase   bool // some matcher is a case-insensitive glob, so Match lower-cases its subject
}

type matcher struct {
	glob   string // lower-cased when nocase
	re     *regexp.Regexp
	nocase bool
}

// match reports whether m matches s; lower is s lower-cased, which Match computes once for
// the whole list.
func (m matcher) match(s, lower string) bool {
	if m.re != nil {
		return m.re.MatchString(s)
	}
	if m.nocase {
		s = lower
	}
	return doublestar.MatchUnvalidated(m.glob, s)
}

// Compile compiles patterns. An entry is a negation when it starts with `!`, a regex when it
// has the form `/expr/` or `/expr/i`, and a case-insensitive doublestar glob otherwise.
func Compile(patterns []string) (*List, error) { return compileList(patterns, true) }

// CompileCaseSensitive is Compile with case-sensitive globs, for Renovate settings matched
// with minimatch without nocase (ignorePaths).
func CompileCaseSensitive(patterns []string) (*List, error) { return compileList(patterns, false) }

func compileList(patterns []string, nocase bool) (*List, error) {
	l := &List{}
	var errs []error
	for _, p := range patterns {
		body, neg := strings.CutPrefix(p, "!")
		if neg && body == "" {
			errs = append(errs, fmt.Errorf("invalid glob %q: missing pattern after %s", p, "!"))
			continue
		}
		m, err := compile(body, p, nocase)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		l.nocase = l.nocase || m.nocase
		if neg {
			l.neg = append(l.neg, m)
		} else {
			l.pos = append(l.pos, m)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return l, nil
}

// compile compiles p; errors name the entry as written (with its "!").
func compile(p, entry string, nocase bool) (matcher, error) {
	// As in Renovate (isRegexMatch), only "/expr/" and "/expr/i" are regexes: "/.github/**/*.yml"
	// is a glob.
	if expr, flags, ok := Regex(p); ok && (flags == "" || flags == "i") {
		if flags == "i" {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return matcher{}, fmt.Errorf("invalid regex %q: %w", entry, err)
		}
		return matcher{re: re}, nil
	}
	if !doublestar.ValidatePattern(p) {
		return matcher{}, fmt.Errorf("invalid glob %q: %w", entry, doublestar.ErrBadPattern)
	}
	if nocase {
		p = strings.ToLower(p)
	}
	return matcher{glob: p, nocase: nocase}, nil
}

// Match reports whether s matches: false for a nil or empty list or when any negation
// matches, true when the list has only negations, else whether any positive pattern matches.
func (l *List) Match(s string) bool {
	if l == nil || len(l.pos)+len(l.neg) == 0 {
		return false
	}
	lower := s
	if l.nocase {
		lower = strings.ToLower(s)
	}
	for _, m := range l.neg {
		if m.match(s, lower) {
			return false
		}
	}
	if len(l.pos) == 0 {
		return true
	}
	for _, m := range l.pos {
		if m.match(s, lower) {
			return true
		}
	}
	return false
}

// Regex splits "/expr/flags"; ok is false unless s starts with "/" and has a closing "/".
func Regex(s string) (expr, flags string, ok bool) {
	if !strings.HasPrefix(s, "/") {
		return "", "", false
	}
	i := strings.LastIndexByte(s, '/')
	if i == 0 {
		return "", "", false
	}
	return s[1:i], s[i+1:], true
}
