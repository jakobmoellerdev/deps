package toolversions

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"

// validUnder reports whether v is a version of the versioning scheme name (a tool's
// versioning, see tool).
func validUnder(name, v string) bool {
	s, ok := versioning.Get(name)
	return ok && s.Valid(v)
}
