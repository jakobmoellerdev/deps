package npm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The helpers below give managers that embed npm package specs in their own files (pre-commit
// `additional_dependencies`) the dependency-section semantics of Extract and Edits.

// SetRange sets d's Version, Range and RefSpan from the dependency-section range raw whose bytes
// are span in the file (a zero span: not locatable), or its skip reason, as Extract does for a
// `dependencies` entry (section): Version is the bare version (`^1.2.3` → 1.2.3) or the range's
// anchor.
func SetRange(d *dep.Dep, raw string, span dep.Span) {
	(*extractor)(nil).setRange(d, raw, span, 0)
}

// InRange reports whether d's Range admits version v, as the engine matches npm ranges (a
// Masterminds constraint).
func InRange(d dep.Dep, v string) bool {
	cr, ok := compileRange(d.Range)
	if !ok || cr.whole == nil {
		return false
	}
	sv, err := newVersion(v)
	return err == nil && cr.whole.Check(sv)
}
