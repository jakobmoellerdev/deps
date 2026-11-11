package config

//go:generate go run ./internal/genreplacements

import (
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Replacement is a built-in package move: deps matching it get a replacement update (see
// dep.Replacement) unless replacements is false or a rule sets another replacementName. The
// entries (Replacements, generated) are Renovate's replacements presets, which its
// config:recommended enables, as far as deps can apply them.
type Replacement struct {
	Name     string   // Renovate's preset name (replacements:<Name>)
	Managers []string // managers whose deps can match
	Names    []string // doublestar globs on Dep.Name
	Patterns []string // RE2 regexps on Dep.Name (Renovate's /…/ package names)
	// CurrentVersion is a Masterminds constraint or the regex form /…/ on the current version
	// (as rules' currentVersion); "" = any.
	CurrentVersion string
	// To is the new name; with Prefix set, it replaces Prefix at the start of the old name
	// (a registry move).
	Prefix, To string
	Version    string // the new version; "" = the current one

	current *Allowed // parsed CurrentVersion
}

// NewName returns the name Replacement r moves the dep name to, "" when the name does not
// start with its Prefix.
func (r *Replacement) NewName(name string) string {
	if r.Prefix == "" {
		return r.To
	}
	rest, ok := strings.CutPrefix(name, r.Prefix)
	if !ok {
		return ""
	}
	return r.To + rest
}

// replacementMatcher is a compiled replacement name matcher: a glob with its literal prefix
// (like monorepoMatcher), or a regexp.
type replacementMatcher struct {
	r      *Replacement
	prefix string
	glob   string
	re     *regexp.Regexp
}

func (m replacementMatcher) match(name string) bool {
	if m.re != nil {
		return m.re.MatchString(name)
	}
	if !strings.HasPrefix(name, m.prefix) {
		return false
	}
	ok, _ := doublestar.Match(m.glob, name)
	return ok
}

// replacementIndex maps a manager to the matchers of the Replacements that apply to it.
var replacementIndex = func() map[string][]replacementMatcher {
	idx := map[string][]replacementMatcher{}
	for i := range Replacements {
		r := &Replacements[i]
		if r.CurrentVersion != "" {
			a, err := ParseAllowed(r.CurrentVersion)
			if err != nil {
				panic("replacement " + r.Name + ": " + err.Error())
			}
			r.current = a
		}
		var ms []replacementMatcher
		for _, g := range r.Names {
			lit := g
			if j := strings.IndexAny(g, `*?[{\`); j >= 0 {
				lit = g[:j]
			}
			ms = append(ms, replacementMatcher{r: r, prefix: lit, glob: g})
		}
		for _, p := range r.Patterns {
			ms = append(ms, replacementMatcher{r: r, re: regexp.MustCompile(p)})
		}
		for _, mgr := range r.Managers {
			idx[mgr] = append(idx[mgr], ms...)
		}
	}
	return idx
}()

// replacementOf returns the first built-in replacement matching d that changes its name, or
// nil. Entries match the dep's packageName.
func (c *Config) replacementOf(d dep.Dep, vc *vcache) *Replacement {
	name := packageName(&d)
	for _, m := range replacementIndex[d.Manager] {
		if !m.match(name) {
			continue
		}
		if n := m.r.NewName(name); n != "" && n != name && (m.r.current == nil || c.currentMatches(m.r.current, d, vc)) {
			return m.r
		}
	}
	return nil
}

// packageName is the name Renovate's package rules see: Dep.Name, but for a helm chart from
// an OCI registry the full reference without the scheme (ghcr.io/org/charts/cilium), which
// helm replacements also accept as the new name.
func packageName(d *dep.Dep) string {
	if repo, ok := strings.CutPrefix(d.Source, "oci://"); ok && d.Manager == "helm" {
		return strings.TrimSuffix(repo, "/") + "/" + d.Name
	}
	return d.Name
}
