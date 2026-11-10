package config

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BranchPrefixVars are the template variables of additionalBranchPrefix, Renovate's names:
// the dep's manager, the name of the directory of its file ("" at the root) and that
// directory's path ("" at the root).
var BranchPrefixVars = []string{"manager", "parentDir", "packageFileDir"}

// branchVarRe matches a Handlebars variable, {{name}} or the unescaped {{{name}}}.
var branchVarRe = regexp.MustCompile(`\{\{\{?\s*([^{}]*?)\s*\}\}\}?`)

// CheckAdditionalBranchPrefix returns why tpl is no valid additionalBranchPrefix, or "": a
// template variable outside BranchPrefixVars, a stray brace, or literal text git does not
// accept in a branch name.
func CheckAdditionalBranchPrefix(tpl string) string {
	if tpl == "" {
		return ""
	}
	bad := ""
	lit := branchVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		if name := branchVarRe.FindStringSubmatch(m)[1]; bad == "" && !slices.Contains(BranchPrefixVars, name) {
			bad = m
		}
		return "x"
	})
	switch {
	case bad != "":
		return "unsupported template " + bad + "; supported: {{manager}}, {{parentDir}}, {{packageFileDir}}"
	case strings.ContainsAny(lit, "{}"):
		return "unbalanced template braces; supported: {{manager}}, {{parentDir}}, {{packageFileDir}}"
	case strings.HasPrefix(lit, "/"):
		return "must not start with /"
	}
	if !strings.HasSuffix(lit, "/") {
		lit += "/"
	}
	return checkBranchPrefix(lit)
}

// ExpandBranchPrefix renders the additionalBranchPrefix template tpl for d and cleans the
// result as Renovate cleans branch names: whitespace dropped, characters git rejects turned
// into '-', runs of '/' and '-' collapsed, and no component starting with '-' or '.' (so
// "{{parentDir}}-" of a file at the root expands to "").
func ExpandBranchPrefix(tpl string, d dep.Dep) string {
	if tpl == "" {
		return ""
	}
	dir := path.Dir(d.File)
	if dir == "." || dir == "/" {
		dir = ""
	}
	parent := ""
	if dir != "" {
		parent = path.Base(dir)
	}
	s := branchVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		switch branchVarRe.FindStringSubmatch(m)[1] {
		case "manager":
			return d.Manager
		case "parentDir":
			return parent
		case "packageFileDir":
			return dir
		}
		return ""
	})
	return cleanBranchPart(s)
}

// cleanBranchPart makes s safe to sit between a branch prefix ending in '/' and a slug.
func cleanBranchPart(s string) string {
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c <= ' ' || c == 0x7f:
			continue
		case strings.IndexByte("~^:?*[]\\{}", c) >= 0:
			c = '-'
		}
		last := byte('/') // the branch prefix ends in '/'
		if len(b) > 0 {
			last = b[len(b)-1]
		}
		switch {
		case c == '/' && last == '/', c == '-' && (last == '-' || last == '/'), c == '.' && (last == '.' || last == '/'):
			continue
		case c == '/' && last == '-':
			b[len(b)-1] = '/'
			continue
		}
		b = append(b, c)
	}
	return string(b)
}
