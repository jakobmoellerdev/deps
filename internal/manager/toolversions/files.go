package toolversions

import (
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// extractFirst returns the version of a single-version file (`.nvmrc`, `.node-version`,
// `.ruby-version`, `.python-version`, `.bun-version`) as a dep of t: the first token of the first line that is
// neither blank nor a `#` comment. An operator-led token followed by another (`>=20 <22`) is
// a compound range, kept whole so it is skipped.
func extractFirst(mgr, depType string, t tool, file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	eachLine(src, func(l []byte, n, off int) bool {
		f, k := fields(l)
		if k == 0 {
			return true
		}
		end := f[0][1]
		if k > 1 && strings.IndexByte("<>=^~", l[f[0][0]]) >= 0 {
			end = f[1][1]
		}
		deps = append(deps, newDep(mgr, depType, file, n, t, string(l[f[0][0]:end]), off+f[0][0]))
		return false
	})
	return deps
}

// runtimePrefix starts the Python version of a runtime.txt (`python-3.13.1`).
const runtimePrefix = "python-"

// extractRuntime returns the Python version of a runtime.txt: the first token of the first
// line that is neither blank nor a `#` comment, when it is `python-<version>`. Other runtimes
// (Binder's `r-4.4-2024-10-01`) have no dep.
func extractRuntime(file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	eachLine(src, func(l []byte, n, off int) bool {
		f, k := fields(l)
		if k == 0 {
			return true
		}
		if raw, ok := strings.CutPrefix(string(l[f[0][0]:f[0][1]]), runtimePrefix); ok {
			deps = append(deps, newDep(NameRuntimeVersion, DepTypePython, file, n, pythonTool, raw, off+f[0][0]+len(runtimePrefix)))
		}
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
