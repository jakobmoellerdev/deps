package engine

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Group is the updates that become one branch and one pull request: all updates of one
// dependency name in one PR bucket, across managers and files (Kind ""), or all non-security
// updates sharing a policy group and bucket (Kind "group"), or all security updates sharing a
// security group and bucket (Kind "group", branch suffix "-security"), or all updates of a
// shared version definition and bucket (Kind "group", named after the dep.Dep.VersionRef
// unless a policy group applies), or every lock file maintenance update (Kind
// MaintenanceBranch). Updates with different dep.Update.BranchTopicPrefix never share one.
type Group struct {
	Kind, Name, Branch, Title string
	// Header and Footer are the rendered prHeader and prFooter of the PR body, "" = none.
	Header, Footer string
	Updates        []dep.Update
	// Automerge is set when every update automerges; AutomergeStrategy is the first update's.
	// AutomergeBranch is set when every update also automerges with automergeType branch: the
	// branch is merged into the base without a PR while its checks pass.
	Automerge         bool
	AutomergeStrategy string
	AutomergeBranch   bool
	// DashboardApproval is set when any update needs approval on the dependency dashboard
	// before its PR (or automerge branch) is created.
	DashboardApproval bool
	// Draft opens the group's new PR as a draft: some update asks for one (draftPR) and the
	// group does not automerge (forges do not merge drafts). PRCreation is when the PR of
	// the pushed branch is opened (config.PRCreations): the most waiting of its updates' modes.
	Draft      bool
	PRCreation string
	// Labels, Reviewers and Assignees are the union over the updates, in first-seen order.
	Labels, Reviewers, Assignees []string
}

// GroupKind is the Group.Kind of policy groups, security groups and shared versions.
const GroupKind = "group"

// MaintenanceBranch is the branch name (after the branch prefix) of the lock file maintenance
// PR, which holds every dep.LockFileMaintenance update; it is Renovate's.
const MaintenanceBranch = "lock-file-maintenance"

// GroupUpdates groups updates by policy group name, or by dependency name for ungrouped and
// security updates (security updates are never batched into a policy group) across managers
// and files, by PR bucket (dep.Update.Bucket) and by dep.Update.BranchTopicPrefix (the
// expanded additionalBranchPrefix). Security updates with a dep.Update.SecurityGroup share a
// group of that name apart from the policy group of the same name. Updates of a shared version
// definition carry the group Resolve gave them (unifyShared), security updates included: they
// edit the same bytes. Branches are <branchPrefix><topicPrefix><branchSlug(name)> and
// <branchPrefix><topicPrefix>group/<branchSlug(group)>, with "-<bucket>" appended for a
// non-empty bucket, then "-security" for a security group; "" means
// config.DefaultBranchPrefix. A branch a group already holds gets a short hash of the later
// group's key appended, since two groups must never share one. Lock file maintenance updates
// share one group (Kind and Name MaintenanceBranch) on <branchPrefix>lock-file-maintenance.
// Sorted by Kind, Name, then Branch.
func GroupUpdates(updates []dep.Update, branchPrefix string) []Group {
	branchPrefix = cmp.Or(branchPrefix, config.DefaultBranchPrefix)
	type key struct {
		topicPrefix, kind, name, bucket string
		security                        bool
	}
	idx := map[key]int{}
	// Different groups can render into one branch: "@types/node" and "types/node" slug alike,
	// and so do the dep "react" in bucket "major" and the dep "react-major". Their two PRs
	// would push over each other, so a branch is handed out once.
	taken := map[string]bool{}
	var groups []Group
	for _, u := range updates {
		k := key{topicPrefix: u.BranchTopicPrefix, name: u.Dep.Name, bucket: u.Bucket}
		switch {
		case u.Maintenance():
			k = key{kind: MaintenanceBranch, name: MaintenanceBranch}
		case u.Group != "" && (len(u.Advisories) == 0 || u.Dep.VersionRef != ""):
			k.kind, k.name = GroupKind, u.Group
		case u.SecurityGroup != "" && len(u.Advisories) > 0:
			k.kind, k.name, k.security = GroupKind, u.SecurityGroup, true
		}
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			var branch string
			switch k.kind {
			case MaintenanceBranch:
				branch = branchPrefix + MaintenanceBranch
			case GroupKind:
				branch = branchPrefix + k.topicPrefix + GroupKind + "/" + branchSlug(k.name)
			default:
				branch = branchPrefix + k.topicPrefix + branchSlug(k.name)
			}
			if k.bucket != "" {
				branch += "-" + k.bucket
			}
			if k.security {
				branch += "-security"
			}
			for taken[branch] {
				branch += "-" + nameHash(k.topicPrefix, k.kind, k.name, k.bucket, strconv.FormatBool(k.security))
			}
			taken[branch] = true
			groups = append(groups, Group{Kind: k.kind, Name: k.name, Branch: branch})
		}
		groups[i].Updates = append(groups[i].Updates, u)
	}
	for i := range groups {
		g := &groups[i]
		g.Title, g.Header, g.Footer = texts(*g)
		g.Automerge = !slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return !u.Automerge })
		if g.Automerge {
			g.AutomergeStrategy = g.Updates[0].AutomergeStrategy
			g.AutomergeBranch = !slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return u.AutomergeType != config.AutomergeTypeBranch })
		}
		g.DashboardApproval = slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return u.DashboardApproval })
		g.Draft = !g.Automerge && slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return u.DraftPR })
		g.PRCreation = config.PRCreationImmediate
		for _, u := range g.Updates {
			if slices.Index(config.PRCreations, u.PRCreation) > slices.Index(config.PRCreations, g.PRCreation) {
				g.PRCreation = u.PRCreation
			}
			g.Labels = union(g.Labels, u.Labels)
			g.Reviewers = union(g.Reviewers, u.Reviewers)
			g.Assignees = union(g.Assignees, u.Assignees)
		}
	}
	stableSortInPlace(groups, func(a, b *Group) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name), strings.Compare(a.Branch, b.Branch))
	})
	return groups
}

// union appends the entries of add missing from dst.
func union(dst, add []string) []string {
	for _, s := range add {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}

// occ identifies the dependency occurrence an update edits, see occurrence.
type occ struct {
	file        string
	ref, digest dep.Span
	ident       string
}

// occurrence is the occurrence u edits: its file and the spans it writes, so commit-only deps
// without a RefSpan, such as a cargo git `rev` or a kustomize commit ref or untagged digest,
// stay apart. A dep without any span is written by identity rather than by offset (an
// unpinned nix flake input refreshes its lockfile entry, a submodule without a branch its
// gitlink), so such deps are told apart by the dep itself: its gitlink, else its name and dep
// type. Keying them by their empty spans would collapse every one of them in a file into a
// single local update and report the rest as separate PRs.
func occurrence(u dep.Update) occ {
	k := occ{file: u.Dep.File, ref: u.Dep.RefSpan, digest: u.Dep.DigestSpan}
	if k.ref == (dep.Span{}) && k.digest == (dep.Span{}) {
		if k.ident = u.Dep.Gitlink; k.ident == "" {
			k.ident = u.Dep.Name + "\x00" + u.Dep.DepType
		}
	}
	return k
}

// Local splits updates into the ones plan and apply write and the ones only `run` proposes
// (in their own PRs): a dependency occurrence (see occurrence) gets one local version, since
// the updates of its PR buckets edit the same bytes. The newest non-major update is kept, else
// the lowest major one (Resolve orders a dep's updates by version; deps sharing a version
// definition are compared by version). Every update of a shared definition (dep.Dep.VersionRef)
// with the kept edit stays local too; patch.Apply merges their identical edits. Lock file
// maintenance updates are always left out: they refresh a lockfile apart from any update.
// local keeps the order of updates; separate[i] reports whether updates[i] was left out.
func Local(updates []dep.Update) (local []dep.Update, separate []bool) {
	chosen := make(map[occ]int, len(updates))
	for i, u := range updates {
		if u.Maintenance() {
			continue // run only: its own PR
		}
		k := occurrence(u)
		j, ok := chosen[k]
		if !ok {
			chosen[k] = i
			continue
		}
		c := updates[j]
		if u.Type == dep.Major && c.Type != dep.Major {
			continue
		}
		newer := i > j
		if u.Dep.VersionRef != "" {
			if s, ok := versioning.Get(u.Dep.Versioning); ok {
				newer = s.Compare(u.NewVersion, c.NewVersion) > 0
			}
		}
		switch {
		case u.Type != dep.Major && (c.Type == dep.Major || newer):
			chosen[k] = i // a (newer) non-major wins over a major
		case u.Type == dep.Major && u.Dep.VersionRef != "" && !newer && u.NewVersion != c.NewVersion:
			chosen[k] = i // the lowest major of a shared definition
		}
	}
	separate = make([]bool, len(updates))
	if len(chosen) == len(updates) {
		return updates, separate
	}
	local = make([]dep.Update, 0, len(chosen))
	for i, u := range updates {
		if j, ok := chosen[occurrence(u)]; !u.Maintenance() && ok && (j == i || sameSharedEdit(u, updates[j])) {
			local = append(local, u)
		} else {
			separate[i] = true
		}
	}
	return local, separate
}

// sameSharedEdit reports whether u and c are updates of distinct deps sharing a version
// definition that write the same bytes: the same version, and the same digest and checksum
// where they share the span (custom deps of one version each write a checksum of their own).
func sameSharedEdit(u, c dep.Update) bool {
	return u.Dep.VersionRef != "" && !sameDep(u.Dep, c.Dep) && u.NewVersion == c.NewVersion &&
		(u.NewDigest == c.NewDigest || u.Dep.DigestSpan != c.Dep.DigestSpan) &&
		(u.NewChecksum == c.NewChecksum || u.Dep.ChecksumSpan != c.Dep.ChecksumSpan) &&
		u.NewName == c.NewName && u.Lockfile == c.Lockfile && u.RangeStrategy == c.RangeStrategy
}

// SecuritySuffix ends the title of a PR with security updates.
const SecuritySuffix = " [security]"

// commitType returns the conventional-commit type and scope of a title. Security groups are
// always fix; otherwise fix if any member is fix, else the first member's type. The scope is
// the first member's. Updates without a policy type count as chore(deps).
func commitType(ups []dep.Update, security bool) (typ, scope string) {
	typ, scope = ups[0].CommitType, ups[0].CommitScope
	if typ == "" {
		typ, scope = config.CommitChore, config.DefaultScope
	}
	if security || slices.ContainsFunc(ups, func(u dep.Update) bool { return u.CommitType == config.CommitFix }) {
		typ = config.CommitFix
	}
	return typ, scope
}

// commitPrefix renders the conventional-commit header of a title ("chore(deps): ").
func commitPrefix(ups []dep.Update, security bool) string {
	typ, scope := commitType(ups, security)
	if scope == "" {
		return typ + ": "
	}
	return typ + "(" + scope + "): "
}

// texts renders the title, header and footer of g with the templates of its first update
// (dep.Update.Templates); nil ones render the built-in title (groupTitle), or no header or
// footer. The lock file maintenance title is always the built-in one. A templated security
// title ends with SecuritySuffix like the built-in ones. A template that fails to execute
// (Config.Validate executed it on sample data) is logged and renders as nil.
func texts(g Group) (title, header, footer string) {
	tpl := g.Updates[0].Templates
	d := templateData(g)
	render := func(t *prtemplate.Template, key string) string {
		if t == nil {
			return ""
		}
		s, err := t.Execute(d)
		if err != nil {
			slog.Warn("PR template failed, using the default", "template", key, "branch", g.Branch, "err", err)
		}
		return s
	}
	if g.Kind != MaintenanceBranch {
		title = render(tpl.Title, "prTitle")
	}
	switch {
	case title == "":
		title = groupTitle(g)
	case d.IsSecurity && !strings.HasSuffix(title, SecuritySuffix):
		title += SecuritySuffix
	}
	return title, render(tpl.Header, "prHeader"), render(tpl.Footer, "prFooter")
}

// templateData is what the PR templates of g see: Name is the dependency, or g's name when it
// updates several ("lock file maintenance" for that PR); From, To and UpdateType are "" unless
// every update has the same.
func templateData(g Group) prtemplate.Data {
	security := slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return len(u.Advisories) > 0 })
	typ, scope := commitType(g.Updates, security)
	first, count := distinctNames(g.Updates)
	d := prtemplate.Data{
		Type: typ, Scope: scope, Name: g.Name, Count: count, IsSecurity: security,
		From: shared(g.Updates, func(u dep.Update) string {
			if u.Lockfile {
				return u.Dep.Locked
			}
			return u.Dep.Version
		}),
		To:         shared(g.Updates, func(u dep.Update) string { return u.NewVersion }),
		UpdateType: shared(g.Updates, func(u dep.Update) string { return string(u.Type) }),
	}
	switch {
	case g.Kind == MaintenanceBranch:
		d.Name = "lock file maintenance"
	case count == 1:
		d.Name = first
	}
	if g.Kind == GroupKind {
		d.Group = g.Name
	}
	return d
}

// shared returns f of every update when it is the same for all, else "".
func shared(ups []dep.Update, f func(dep.Update) string) string {
	v := f(ups[0])
	for _, u := range ups[1:] {
		if f(u) != v {
			return ""
		}
	}
	return v
}

// groupTitle renders the built-in PR title of g. A group with security updates (a security
// group, or a shared version definition with a security member) gets the security prefix and
// SecuritySuffix.
func groupTitle(g Group) string {
	if g.Kind == MaintenanceBranch {
		return commitPrefix(g.Updates, false) + "lock file maintenance"
	}
	name := g.Name
	security := slices.ContainsFunc(g.Updates, func(u dep.Update) bool { return len(u.Advisories) > 0 })
	if g.Kind == GroupKind {
		first, count := distinctNames(g.Updates)
		if count > 1 {
			pre, suffix := commitPrefix(g.Updates, security), ""
			if security {
				suffix = SecuritySuffix
			}
			if v, ok := sameVersion(g.Updates); ok {
				return pre + "update " + g.Name + " to " + v + suffix
			}
			return fmt.Sprintf("%supdate %s (%d dependencies)%s", pre, g.Name, count, suffix)
		}
		name = first
	}
	if security {
		pre := commitPrefix(g.Updates, true)
		if v, ok := sameVersion(g.Updates); ok {
			return pre + "update " + name + " to " + v + SecuritySuffix
		}
		return pre + "update " + name + SecuritySuffix
	}
	return title(name, g.Updates)
}

// distinctNames returns the first update's dependency name and the number
// of distinct dependency names in ups. ups must not be empty.
func distinctNames(ups []dep.Update) (first string, n int) {
	first = ups[0].Dep.Name
	i := slices.IndexFunc(ups, func(u dep.Update) bool { return u.Dep.Name != first })
	if i < 0 {
		return first, 1
	}
	seen := make(map[string]struct{}, len(ups)-i+1)
	seen[first] = struct{}{}
	for _, u := range ups[i:] {
		seen[u.Dep.Name] = struct{}{}
	}
	return first, len(seen)
}

// sameVersion returns the NewVersion shared by all updates (the first update's form, see
// sameV).
func sameVersion(ups []dep.Update) (string, bool) {
	v := ups[0].NewVersion
	for _, u := range ups[1:] {
		if !sameV(u.NewVersion, v) {
			return "", false
		}
	}
	return v, true
}

// sameV reports whether versions a and b are the same for a title: equal up to a leading "v",
// so a dependency updated to v0.7.0 by github-actions and to 0.7.0 by homebrew moves to one
// version.
func sameV(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
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
	case same(func(u dep.Update) bool {
		return u.Type == dep.Pin && u.NewDigest == "" && u.NewVersion == first.NewVersion
	}):
		return pre + "pin " + name + " to " + first.NewVersion
	case same(func(u dep.Update) bool {
		return u.Type == dep.Replacement && u.NewName == first.NewName && u.NewVersion == first.NewVersion
	}):
		return pre + "replace " + name + " with " + first.NewName + " " + first.NewVersion
	case same(func(u dep.Update) bool { return u.Type == dep.Pin && u.NewDigest == first.NewDigest }):
		return pre + "pin " + name + " to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return u.Type == dep.Digest && u.NewDigest == first.NewDigest }):
		return pre + "update " + name + " digest to " + short(first.NewDigest)
	case same(func(u dep.Update) bool { return sameV(u.NewVersion, first.NewVersion) }):
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

// branchSlug is slug(s) as a branch may carry it: git rejects a ref whose last component is
// empty, begins with '.', ends with '.' or ".lock", or holds "..", and such a name would cost
// the whole run its PR. A name slug leaves nothing of (one of non-ASCII bytes only) becomes
// its hash, so two of them stay apart.
func branchSlug(s string) string {
	b := strings.Trim(strings.ReplaceAll(slug(s), "..", "-"), ".-")
	if rest, ok := strings.CutSuffix(b, ".lock"); ok {
		b = rest + "-lock"
	}
	if b == "" {
		return nameHash(s)
	}
	return b
}

// nameHash is a short stable hash of parts, which a branch carries where the names it is made
// of do not tell two groups apart.
func nameHash(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}
