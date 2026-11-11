package engine

import (
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// dispatch offers each walked path only to the managers that can match it. A manager
// declaring manager.Hints is asked about paths whose base name it named, or whose base name
// ends in one of its extensions; every other manager (custom managers, managers matching by
// content of the path) is asked about every path. Running all ~80 Match predicates on every
// path was the largest remaining cost of discovery; the hints are only a filter, Match still
// decides.
type dispatch struct {
	byName map[string][]int // base name -> manager indexes
	byExt  map[string][]int // extension with the dot, lower case -> manager indexes
	always []int            // managers without hints
	multi  bool             // an extension holds more than one dot ("x.y"), so exts need a scan
	exts   []string         // distinct extensions, longest first; only used when multi
	seen   []uint32         // per manager: the generation of the last path offered to it
	gen    uint32
	cands  []int // reused candidate buffer
}

// newDispatch indexes managers by their hints, in their own order.
func newDispatch(managers []manager.Manager) *dispatch {
	d := &dispatch{byName: map[string][]int{}, byExt: map[string][]int{}, seen: make([]uint32, len(managers))}
	for i, m := range managers {
		h, ok := m.(manager.Hinter)
		var hints manager.Hints
		if ok {
			hints = h.MatchHints()
		}
		if len(hints.Names) == 0 && len(hints.Exts) == 0 {
			d.always = append(d.always, i)
			continue
		}
		for _, n := range hints.Names {
			d.byName[n] = append(d.byName[n], i)
		}
		for _, e := range hints.Exts {
			e = strings.ToLower(e)
			if _, ok := d.byExt[e]; !ok {
				d.exts = append(d.exts, e)
			}
			d.byExt[e] = append(d.byExt[e], i)
			d.multi = d.multi || strings.Count(e, ".") > 1
		}
	}
	return d
}

// candidates returns the managers that may match the slash path, in manager order, each once.
// The slice is reused by the next call.
func (d *dispatch) candidates(path string) []int {
	name := path
	if _, after, ok := strings.CutLast(path, "/"); ok {
		name = after
	}
	d.gen++
	d.cands = d.cands[:0]
	add := func(idx []int) {
		for _, i := range idx {
			if d.seen[i] != d.gen {
				d.seen[i], d.cands = d.gen, append(d.cands, i)
			}
		}
	}
	add(d.always)
	add(d.byName[name])
	lower := name
	if hasUpper(name) {
		lower = strings.ToLower(name)
	}
	if d.multi {
		for _, e := range d.exts {
			if strings.HasSuffix(lower, e) {
				add(d.byExt[e])
			}
		}
	} else if _, after, ok := strings.CutLast(lower, "."); ok {
		add(d.byExt["."+after])
	}
	slicesSortInts(d.cands)
	return d.cands
}

// hasUpper reports whether s holds an ASCII upper-case letter; most paths hold none, so the
// lower-casing allocation is skipped.
func hasUpper(s string) bool {
	for i := range len(s) {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

// slicesSortInts sorts the small candidate list, so managers keep their order.
func slicesSortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
