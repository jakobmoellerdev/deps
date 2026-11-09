package npm

// RangeAnchor returns the version that stands for the npm range rng (dep.Dep.Version): `^1.2`
// → 1.2.0, `1.x` → 1.0.0, `<2` → 2.0.0. ok is false when rng is outside node-semver's grammar
// or has no version (`*`). Other managers whose ranges are npm's share it.
func RangeAnchor(rng string) (string, bool) {
	r, ok := compileRange(rng)
	if !ok {
		return "", false
	}
	a := r.anchor()
	return a, a != ""
}

// NewRange returns the npm range rng rewritten for version v under rangeStrategy strategy, as
// manager.RangeRewriter.NewRange: false when that leaves rng unchanged or does not admit v.
func NewRange(rng, v, strategy string) (string, bool) {
	r, ok := compileRange(rng)
	if !ok {
		return "", false
	}
	return rewrite(r, v, strategy)
}

// rewrite is NewRange for the compiled range r.
func rewrite(r *compiledRange, v, strategy string) (string, bool) {
	nv, err := newVersion(v)
	if err != nil {
		return "", false
	}
	out := r.newRange(nv, strategy)
	if out == r.raw || !admits(out, nv) {
		return "", false
	}
	return out, true
}
