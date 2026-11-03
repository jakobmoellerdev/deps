package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Allowed is a parsed allow value: a Masterminds semver constraint, or Renovate's
// allowedVersions regex form `/<RE2>/` (only matching versions are allowed) or `!/<RE2>/`
// (matching versions are rejected), optionally with the `i` flag (`/<RE2>/i`). The regex is
// matched against the candidate version as written in its source (tag, image tag, module
// version).
type Allowed struct {
	constraint *semver.Constraints
	re         *regexp.Regexp
	negate     bool
	src        string // regex form as written
}

// ParseAllowed parses an allow value.
func ParseAllowed(s string) (*Allowed, error) {
	body, negate := s, false
	if rest, ok := strings.CutPrefix(body, "!"); ok && strings.HasPrefix(rest, "/") {
		body, negate = rest, true
	}
	if expr, flags, ok := regexForm(body); ok {
		if flags == "i" {
			expr = "(?i)" + expr
		} else if flags != "" {
			return nil, fmt.Errorf("invalid regex %q: unknown flags %q (only i)", s, flags)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", s, err)
		}
		return &Allowed{re: re, negate: negate, src: s}, nil
	}
	if negate {
		return nil, fmt.Errorf("invalid regex %q: missing closing /", s)
	}
	cs, err := semver.NewConstraint(s)
	if err != nil {
		return nil, fmt.Errorf("invalid constraint %q: %w", s, err)
	}
	return &Allowed{constraint: cs}, nil
}

// regexForm splits "/expr/flags"; ok is false unless s starts with "/" and has a closing "/".
func regexForm(s string) (expr, flags string, ok bool) {
	if !strings.HasPrefix(s, "/") {
		return "", "", false
	}
	i := strings.LastIndexByte(s, '/')
	if i == 0 {
		return "", "", false
	}
	return s[1:i], s[i+1:], true
}

// String renders the constraint normalized, or the regex form as written.
func (a *Allowed) String() string {
	if a.re != nil {
		return a.src
	}
	return a.constraint.String()
}

// Check reports whether version v is allowed. A constraint needs v as a Masterminds version:
// semverOf converts it and is only called for constraints; a nil func or result is never
// allowed.
func (a *Allowed) Check(v string, semverOf func(string) *semver.Version) bool {
	if a.re != nil {
		return a.re.MatchString(v) != a.negate
	}
	if semverOf == nil {
		return false
	}
	sv := semverOf(v)
	return sv != nil && a.constraint.Check(sv)
}
