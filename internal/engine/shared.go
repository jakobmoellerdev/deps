package engine

import (
	"context"
	"log/slog"
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// sharedKey identifies a shared version definition (dep.Dep.VersionRef): the bytes of one
// Maven property or Gradle version entry, which every member's update edits.
type sharedKey struct {
	manager, file string
	span          dep.Span
}

// sharedMember reports whether u edits a shared definition with a new version: lockfile-only
// updates leave the definition alone, digest refreshes and pins keep the version.
func sharedMember(u *dep.Update) bool {
	return u.Dep.VersionRef != "" && !u.Lockfile && u.NewVersion != u.Dep.Version
}

// unifyShared makes the updates of deps sharing one version definition (same Manager, File
// and RefSpan with a VersionRef) agree, once updates of at least two distinct deps edit it:
//   - per PR bucket (dep.Update.Bucket) every member takes the lowest NewVersion of the
//     bucket, the highest version all of them are known to reach (each dep's update is its
//     best accepted candidate). A lowered member gets the Type of the new version under its
//     scheme, the NewDigest and NewChecksum of the update it was lowered to, and loses its
//     advisories (the fix is no longer known to be included). Buckets only some members reach
//     are kept: one bucket is one PR, so its edits never conflict, and Local writes one
//     version per definition.
//   - every member gets one Group: the first member's policy group, else the VersionRef, so
//     the definition's updates of a bucket become one PR (security updates included, see
//     GroupUpdates). Differing policy groups are logged at debug level.
//
// ups is modified in place.
func unifyShared(ctx context.Context, ups []dep.Update) {
	var defs map[sharedKey][]int
	for i := range ups {
		u := &ups[i]
		if !sharedMember(u) {
			continue
		}
		if defs == nil {
			defs = map[sharedKey][]int{}
		}
		k := sharedKey{u.Dep.Manager, u.Dep.File, u.Dep.RefSpan}
		defs[k] = append(defs[k], i)
	}
	for k, idx := range defs {
		first := &ups[idx[0]]
		if !slices.ContainsFunc(idx, func(i int) bool { return !sameDep(ups[i].Dep, first.Dep) }) {
			continue // one dep's own buckets
		}
		group := ""
		for _, i := range idx {
			g := ups[i].Group
			switch {
			case g == "" || g == group:
			case group == "":
				group = g
			default:
				slog.DebugContext(ctx, "shared version with differing groups", "file", k.file, "ref", first.Dep.VersionRef, "group", group, "other", g)
			}
		}
		if group == "" {
			group = first.Dep.VersionRef
		}
		for _, i := range idx {
			ups[i].Group = group
		}
		s, ok := versioning.Get(first.Dep.Versioning)
		if !ok {
			continue
		}
		for n, i := range idx {
			if slices.ContainsFunc(idx[:n], func(j int) bool { return ups[j].Bucket == ups[i].Bucket }) {
				continue // bucket done
			}
			low := i
			for _, j := range idx[n+1:] {
				if ups[j].Bucket == ups[i].Bucket && s.Compare(ups[j].NewVersion, ups[low].NewVersion) < 0 {
					low = j
				}
			}
			l := ups[low]
			for _, j := range idx[n:] {
				u := &ups[j]
				if u.Bucket != l.Bucket || u.NewVersion == l.NewVersion {
					continue
				}
				u.NewVersion, u.NewDigest, u.NewChecksum, u.Advisories = l.NewVersion, l.NewDigest, l.NewChecksum, nil
				if us, ok := versioning.Get(u.Dep.Versioning); ok {
					u.Type = us.Kind(u.Dep.Version, u.NewVersion)
				} else {
					u.Type = l.Type
				}
			}
		}
	}
}

// sameDep reports whether a and b are the same dependency occurrence (dep.Dep holds slices, so
// it is not comparable): the fields that tell deps sharing one definition apart.
func sameDep(a, b dep.Dep) bool {
	return a.Manager == b.Manager && a.Name == b.Name && a.DepType == b.DepType && a.File == b.File &&
		a.Line == b.Line && a.RefSpan == b.RefSpan && a.Version == b.Version && a.Source == b.Source
}
