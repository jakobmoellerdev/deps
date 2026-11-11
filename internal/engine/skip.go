package engine

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"

// SkipDep records d as skipped with reason, as Resolve records the deps it skips: the
// matching entry of r.Deps gets reason, and d is added to r.Skipped unless a dep with the
// same file, line and name is already there. Callers skip the updates of a whole run one by
// one, so both scans compare in place: a dep.Dep is hundreds of bytes, and passing every
// candidate to a predicate by value copies more than the comparison reads. The line, the
// field that differs between deps of one file, is compared first.
func (r *Result) SkipDep(d dep.Dep, reason string) {
	for i := range r.Deps {
		if o := &r.Deps[i]; o.Line == d.Line && o.Name == d.Name && o.File == d.File {
			o.Skip = reason
			break
		}
	}
	for i := range r.Skipped {
		if o := &r.Skipped[i]; o.Line == d.Line && o.Name == d.Name && o.File == d.File {
			return
		}
	}
	d.Skip = reason
	r.Skipped = append(r.Skipped, d)
}
