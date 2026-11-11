// Package patch applies byte-span edits to a file without reformatting.
package patch

import (
	"cmp"
	"fmt"
	"slices"
)

// Edit replaces src[Start:End] (which must equal Old) with New.
type Edit struct {
	Start, End int
	Old, New   string
}

// Apply applies non-overlapping edits in a single pre-sized pass. edits is
// sorted in place by Start and then End, so that an insertion at the start of
// a replaced span is applied before it and the result does not depend on the
// order the edits came in. Identical edits (several deps sharing one version
// definition, such as a Maven property) are applied once.
func Apply(src []byte, edits []Edit) ([]byte, error) {
	slices.SortStableFunc(edits, func(a, b Edit) int {
		if a.Start != b.Start {
			return cmp.Compare(a.Start, b.Start)
		}
		return cmp.Compare(a.End, b.End)
	})
	edits = slices.Compact(edits)
	size := len(src)
	for i, e := range edits {
		if e.Start < 0 || e.End < e.Start || e.End > len(src) {
			return nil, fmt.Errorf("edit at %d: out of range", e.Start)
		}
		if i > 0 && e.Start < edits[i-1].End {
			return nil, fmt.Errorf("edits overlap at %d and %d", edits[i-1].Start, e.Start)
		}
		if got := src[e.Start:e.End]; string(got) != e.Old {
			return nil, fmt.Errorf("edit at %d: expected %q, found %q", e.Start, e.Old, got)
		}
		size += len(e.New) - (e.End - e.Start)
	}
	out := make([]byte, 0, size)
	pos := 0
	for _, e := range edits {
		out = append(out, src[pos:e.Start]...)
		out = append(out, e.New...)
		pos = e.End
	}
	return append(out, src[pos:]...), nil
}
