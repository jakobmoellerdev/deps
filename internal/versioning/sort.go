package versioning

import (
	"slices"
	"strings"
)

// Comparator orders the versions of one slice by their index, each version parsed once
// rather than twice per comparison as Scheme.Compare does. Scheme.Order returns one.
type Comparator struct {
	// cmp orders two indices of the slice Order was given. It is the parse-once
	// comparator of the scheme, so the sorts below call it, not Compare.
	cmp func(i, j int32) int
}

// Compare orders the i-th and the j-th version of the slice Scheme.Order was given,
// exactly as Scheme.Compare orders those two strings.
func (c Comparator) Compare(i, j int) int { return c.cmp(int32(i), int32(j)) }

// parsedVersion is a version parsed up front; ok is false for one the scheme does not
// parse, which the comparator falls back to comparing bytewise, as Scheme.Compare does.
type parsedVersion[T any] struct {
	v  T
	ok bool
}

// Order parses vs once and returns the comparator over its indices.
func (s impl[T]) Order(vs []string) Comparator {
	parsed := make([]parsedVersion[T], len(vs))
	for i, raw := range vs {
		parsed[i].v, parsed[i].ok = s.parse(raw)
	}
	return Comparator{cmp: func(i, j int32) int {
		a, b := &parsed[i], &parsed[j]
		if !a.ok || !b.ok {
			return strings.Compare(vs[i], vs[j])
		}
		return s.cmp(a.v, b.v)
	}}
}

// Sort sorts vs in place, lowest version first, parsing each version once.
func (s impl[T]) Sort(vs []string) {
	if len(vs) < 2 {
		return
	}
	reorder(vs, sortIndex(len(vs), s.Order(vs).cmp))
}

// SortDesc is Sort with the highest version first.
func (s impl[T]) SortDesc(vs []string) {
	if len(vs) < 2 {
		return
	}
	c := s.Order(vs)
	reorder(vs, sortIndex(len(vs), func(i, j int32) int { return c.cmp(j, i) }))
}

// SortBy sorts items by the version of each, parsing it once rather than twice per
// comparison as a Scheme.Compare comparator does. The order is Scheme.Sort's; items whose
// versions compare equal keep their input order unless tie (may be nil) orders them.
func SortBy[T any](s Scheme, items []T, version func(T) string, tie func(a, b T) int) {
	if len(items) < 2 {
		return
	}
	vs := make([]string, len(items))
	for i := range items {
		vs[i] = version(items[i])
	}
	cmp := s.Order(vs).cmp
	if tie != nil {
		byVersion := cmp
		cmp = func(i, j int32) int {
			if r := byVersion(i, j); r != 0 {
				return r
			}
			return tie(items[i], items[j])
		}
	}
	reorder(items, sortIndex(len(items), cmp))
}

// sortIndex orders the indices 0..n-1 by cmp. The stable sort of the indices sees exactly
// the comparisons the same stable sort of the slice itself would see, so it yields the
// permutation slices.SortStableFunc would apply, ties included - but it moves 4-byte
// indices instead of the slice's elements.
func sortIndex(n int, cmp func(i, j int32) int) []int32 {
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	slices.SortStableFunc(idx, cmp)
	return idx
}

// reorder rewrites items into the order idx names: items[k] becomes the former items[idx[k]].
func reorder[T any](items []T, idx []int32) {
	old := slices.Clone(items)
	for k, i := range idx {
		items[k] = old[i]
	}
}
