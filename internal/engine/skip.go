package engine

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// SkipDep records d as skipped with reason, as Resolve records the deps it skips: the
// matching entry of r.Deps gets reason, and d is added to r.Skipped unless a dep with the
// same file, line and name is already there.
func (r *Result) SkipDep(d dep.Dep, reason string) {
	same := func(o dep.Dep) bool { return o.File == d.File && o.Line == d.Line && o.Name == d.Name }
	if i := slices.IndexFunc(r.Deps, same); i >= 0 {
		r.Deps[i].Skip = reason
	}
	if !slices.ContainsFunc(r.Skipped, same) {
		d.Skip = reason
		r.Skipped = append(r.Skipped, d)
	}
}
