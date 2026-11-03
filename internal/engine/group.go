package engine

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Group is the updates that become one branch and one pull request: all updates of one
// dependency, or all non-security updates sharing a policy group (Manager "group").
type Group struct {
	Manager, Name, Branch, Title string
	Updates                      []dep.Update
}

// GroupManager is the Group.Manager of policy groups.
const GroupManager = "group"

// GroupUpdates groups updates by policy group name, or by (Manager, Name) for ungrouped and
// security updates (security updates are never batched into a policy group). Branches are
// <branchPrefix><manager>/<slug> and <branchPrefix>group/<slug>; "" means
// config.DefaultBranchPrefix. Sorted by Manager then Name.
func GroupUpdates(updates []dep.Update, branchPrefix string) []Group {
	branchPrefix = cmp.Or(branchPrefix, config.DefaultBranchPrefix)
	type key struct{ manager, name string }
	idx := map[key]int{}
	var groups []Group
	for _, u := range updates {
		k := key{u.Dep.Manager, u.Dep.Name}
		if u.Group != "" && len(u.Advisories) == 0 {
			k = key{GroupManager, u.Group}
		}
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			groups = append(groups, Group{Manager: k.manager, Name: k.name, Branch: branchPrefix + k.manager + "/" + slug(k.name)})
		}
		groups[i].Updates = append(groups[i].Updates, u)
	}
	for i := range groups {
		groups[i].Title = groupTitle(groups[i])
	}
	slices.SortFunc(groups, func(a, b Group) int {
		return cmp.Or(strings.Compare(a.Manager, b.Manager), strings.Compare(a.Name, b.Name))
	})
	return groups
}

// commitPrefix renders the conventional-commit header of a title ("chore(deps): ").
// Security groups are always fix; otherwise fix if any member is fix, else the first
// member's type. The scope is the first member's. Updates without a policy type count as
// chore(deps).
func commitPrefix(ups []dep.Update, security bool) string {
	typ, scope := ups[0].CommitType, ups[0].CommitScope
	if typ == "" {
		typ, scope = config.CommitChore, config.DefaultScope
	}
	if security || slices.ContainsFunc(ups, func(u dep.Update) bool { return u.CommitType == config.CommitFix }) {
		typ = config.CommitFix
	}
	if scope == "" {
		return typ + ": "
	}
	return typ + "(" + scope + "): "
}

func groupTitle(g Group) string {
	if g.Manager == GroupManager {
		names := map[string]bool{}
		for _, u := range g.Updates {
			names[u.Dep.Name] = true
		}
		if len(names) > 1 {
			pre := commitPrefix(g.Updates, false)
			if v, ok := sameVersion(g.Updates); ok {
				return pre + "update " + g.Name + " to " + v
			}
			return fmt.Sprintf("%supdate %s (%d dependencies)", pre, g.Name, len(names))
		}
		return title(g.Updates[0].Dep.Name, g.Updates)
	}
	if slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return len(u.Advisories) > 0 }) {
		pre := commitPrefix(g.Updates, true)
		if v, ok := sameVersion(g.Updates); ok {
			return pre + "update " + g.Name + " to " + v + " [security]"
		}
		return pre + "update " + g.Name + " [security]"
	}
	return title(g.Name, g.Updates)
}

// sameVersion returns the NewVersion shared by all updates.
func sameVersion(ups []dep.Update) (string, bool) {
	v := ups[0].NewVersion
	for _, u := range ups[1:] {
		if u.NewVersion != v {
			return "", false
		}
	}
	return v, true
}

func title(name string, ups []dep.Update) string {
	same := func(f func(dep.Update) bool) bool {
		for _, u := range ups {
			if !f(u) {
				return false
			}
		}
		return true
	}
	first := ups[0]
	pre := commitPrefix(ups, false)
	switch {
	case same(func(u dep.Update) bool { return u.Type == dep.Pin && u.NewDigest == first.NewDigest }):
		return pre + "pin " + name + " to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return u.Type == dep.Digest && u.NewDigest == first.NewDigest }):
		return pre + "update " + name + " digest to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return u.NewVersion == first.NewVersion }):
		return pre + "update " + name + " to " + first.NewVersion
	}
	return pre + "update " + name
}

func short(digest string) string { return dep.ShortDigest(digest) }

// slug lowercases s, maps bytes outside [a-z0-9._-] to '-', collapses runs of '-' and trims '-'.
func slug(s string) string {
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-') {
			c = '-'
		}
		if c == '-' && len(b) > 0 && b[len(b)-1] == '-' {
			continue
		}
		b = append(b, c)
	}
	return strings.Trim(string(b), "-")
}
