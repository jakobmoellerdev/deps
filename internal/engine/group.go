package engine

import (
	"cmp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Group is all updates of one dependency; it becomes one branch and one pull request.
type Group struct {
	Manager, Name, Branch, Title string
	Updates                      []dep.Update
}

// GroupUpdates groups updates by (Manager, Name), sorted by Manager then Name.
func GroupUpdates(updates []dep.Update) []Group {
	type key struct{ manager, name string }
	idx := map[key]int{}
	var groups []Group
	for _, u := range updates {
		k := key{u.Dep.Manager, u.Dep.Name}
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			groups = append(groups, Group{Manager: k.manager, Name: k.name, Branch: "deps/" + k.manager + "/" + slug(k.name)})
		}
		groups[i].Updates = append(groups[i].Updates, u)
	}
	for i := range groups {
		groups[i].Title = title(groups[i].Name, groups[i].Updates)
	}
	slices.SortFunc(groups, func(a, b Group) int {
		return cmp.Or(strings.Compare(a.Manager, b.Manager), strings.Compare(a.Name, b.Name))
	})
	return groups
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
	switch {
	case same(func(u dep.Update) bool { return u.Type == dep.Pin && u.NewDigest == first.NewDigest }):
		return "chore(deps): pin " + name + " to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return u.Type == dep.Digest && u.NewDigest == first.NewDigest }):
		return "chore(deps): update " + name + " digest to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return u.NewVersion == first.NewVersion }):
		return "chore(deps): update " + name + " to " + first.NewVersion
	}
	return "chore(deps): update " + name
}

func short(sha string) string { return sha[:min(7, len(sha))] }

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
