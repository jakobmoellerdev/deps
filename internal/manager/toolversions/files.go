package toolversions

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// extractFirst returns the version of a single-version file (`.nvmrc`, `.node-version`,
// `.ruby-version`) as a dep of t: the first token of the first line that is neither blank nor
// a `#` comment.
func extractFirst(mgr, depType string, t tool, file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	eachLine(src, func(l []byte, n, off int) bool {
		f, k := fields(l)
		if k == 0 {
			return true
		}
		deps = append(deps, newDep(mgr, depType, file, n, t, string(l[f[0][0]:f[0][1]]), off+f[0][0]))
		return false
	})
	return deps
}

// extractAsdf returns one dep per `<tool> <version> [fallback versions…]` line of a
// `.tool-versions` file; only the first version is a dep. Comments start at `#`.
func extractAsdf(file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	eachLine(src, func(l []byte, n, off int) bool {
		f, k := fields(l)
		if k < 2 {
			return true
		}
		t := lookup(string(l[f[0][0]:f[0][1]]))
		deps = append(deps, newDep(NameAsdf, DepTypeTool, file, n, t, string(l[f[1][0]:f[1][1]]), off+f[1][0]))
		return true
	})
	return deps
}
