package npm

// Masterminds semver constraints (Helm chart versions, Flux `spec.ref.semver`) are written in
// node-semver's grammar, plus `,` between the comparators of an alternative (`>= 1.2, < 2`):
// Renovate's helm versioning is its npm versioning. The rewrite keeps the commas.

// MastermindsAnchor is RangeAnchor for a Masterminds constraint; ok is also false when
// Masterminds rejects rng.
func MastermindsAnchor(rng string) (string, bool) {
	r, ok := compileRangeIn(rng, true)
	if !ok || r.whole == nil {
		return "", false
	}
	a := r.anchor()
	return a, a != ""
}

// NewMastermindsRange is NewRange for a Masterminds constraint.
func NewMastermindsRange(rng, v, strategy string) (string, bool) {
	r, ok := compileRangeIn(rng, true)
	if !ok {
		return "", false
	}
	return rewrite(r, v, strategy)
}
