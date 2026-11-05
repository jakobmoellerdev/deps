// Package pattern matches strings against Renovate-style pattern lists: doublestar globs,
// `/RE2/` regexes and `!`-negations.
package pattern

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// List is a compiled pattern list with Renovate's matchRegexOrGlobList semantics.
type List struct{ pos, neg []matcher }

type matcher struct {
	glob string
	re   *regexp.Regexp
}

func (m matcher) match(s string) bool {
	if m.re != nil {
		return m.re.MatchString(s)
	}
	return doublestar.MatchUnvalidated(m.glob, s)
}

// Compile compiles patterns. An entry is a negation when it starts with `!`, a regex when it
// has the form `/expr/` or `/expr/i`, and a doublestar glob otherwise.
func Compile(patterns []string) (*List, error) {
	l := &List{}
	var errs []error
	for _, p := range patterns {
		body, neg := strings.CutPrefix(p, "!")
		if neg && body == "" {
			errs = append(errs, fmt.Errorf("invalid glob %q: missing pattern after %s", p, "!"))
			continue
		}
		m, err := compile(body, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
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
func compile(p, entry string) (matcher, error) {
	if expr, flags, ok := Regex(p); ok {
		switch flags {
		case "":
		case "i":
			expr = "(?i)" + expr
		default:
			return matcher{}, fmt.Errorf("invalid regex %q: unknown flags %q (only i)", entry, flags)
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
	return matcher{glob: p}, nil
}

// Match reports whether s matches: false for a nil or empty list or when any negation
// matches, true when the list has only negations, else whether any positive pattern matches.
func (l *List) Match(s string) bool {
	if l == nil || len(l.pos)+len(l.neg) == 0 {
		return false
	}
	for _, m := range l.neg {
		if m.match(s) {
			return false
		}
	}
	if len(l.pos) == 0 {
		return true
	}
	for _, m := range l.pos {
		if m.match(s) {
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
