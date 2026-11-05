package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pattern"
)

// DefaultBaseBranch is the baseBranches entry naming the repository's default branch
// (Renovate's $default).
const DefaultBaseBranch = "$default"

// branchPattern is an entry of baseBranches: a branch name, or the regex form /<RE2>/ (flag i
// allowed). In rules, a leading '!' makes it an exclusion.
type branchPattern struct {
	name   string
	re     *regexp.Regexp
	negate bool
}

// parseBranchPattern parses a baseBranches entry; exclude allows a leading '!', builtin the
// entry $default.
func parseBranchPattern(s string, exclude, builtin bool) (branchPattern, error) {
	var p branchPattern
	body := s
	if exclude {
		body, p.negate = strings.CutPrefix(s, "!")
	}
	if expr, flags, ok := pattern.Regex(body); ok {
		if flags == "i" {
			expr = "(?i)" + expr
		} else if flags != "" {
			return p, fmt.Errorf("invalid regex %q: unknown flags %q (only i)", s, flags)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return p, fmt.Errorf("invalid regex %q: %w", s, err)
		}
		p.re = re
		return p, nil
	}
	if builtin && body == DefaultBaseBranch {
		p.name = body
		return p, nil
	}
	if checkBranchPrefix(body+"/") != "" || !exclude && strings.HasPrefix(body, "!") {
		if builtin {
			return p, fmt.Errorf("must be a branch name, %s or a regex /<RE2>/, got %q", DefaultBaseBranch, s)
		}
		return p, fmt.Errorf("must be a branch name or a regex /<RE2>/, got %q", s)
	}
	p.name = body
	return p, nil
}

func (p branchPattern) matches(branch string) bool {
	if p.re != nil {
		return p.re.MatchString(branch)
	}
	return p.name == branch
}

// matchBranches reports whether branch matches the rule entries ps as MatchGlobs matches
// names: no exclusion matches it and some other entry does, unless every entry is an exclusion.
func matchBranches(ps []branchPattern, branch string) bool {
	positive, hit := false, false
	for _, p := range ps {
		if p.negate {
			if p.matches(branch) {
				return false
			}
			continue
		}
		positive = true
		hit = hit || p.matches(branch)
	}
	return hit || !positive
}

// parseBranchPatterns validates the entries of field.
func parseBranchPatterns(field string, entries []string, exclude, builtin bool, add addFunc) []branchPattern {
	if len(entries) == 0 {
		return nil
	}
	ps := make([]branchPattern, 0, len(entries))
	for i, e := range entries {
		p, err := parseBranchPattern(e, exclude, builtin)
		if err != nil {
			add(fmt.Sprintf("%s[%d]", field, i), "%v", err)
			continue
		}
		ps = append(ps, p)
	}
	return ps
}

// MultiBase reports whether update branches carry their base branch: baseBranches has more than
// one entry, or its first is a regex, which can match more branches later (as Renovate
// decides). A single named base keeps the plain branch names.
func (c *Config) MultiBase() bool {
	return len(c.BaseBranches) > 1 || len(c.baseBranches) == 1 && c.baseBranches[0].re != nil
}

// ExpandBaseBranches unfolds baseBranches to branch names, in order without duplicates:
// $default is defaultBranch, a regex every branch of remote (the branches on origin) it
// matches in the order given, a name itself. Without baseBranches it is [defaultBranch].
func (c *Config) ExpandBaseBranches(defaultBranch string, remote []string) []string {
	if len(c.baseBranches) == 0 {
		return []string{defaultBranch}
	}
	var out []string
	add := func(b string) {
		if !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	for _, p := range c.baseBranches {
		switch {
		case p.re != nil:
			for _, b := range remote {
				if p.re.MatchString(b) {
					add(b)
				}
			}
		case p.name == DefaultBaseBranch:
			add(defaultBranch)
		default:
			add(p.name)
		}
	}
	return out
}

// ForBaseBranch returns a copy of c whose rules with baseBranches match against branch, the
// base branch of a run. Without it (plan, apply, explain, preview) they match nothing.
func (c *Config) ForBaseBranch(branch string) *Config {
	cp := *c
	cp.baseBranch = branch
	return &cp
}
