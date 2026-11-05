package engine

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// sortIdxUpdates builds updates from four managers, each listing its
// files in order, so the combined slice interleaves files like Resolve's.
func sortIdxUpdates(perManager int) []dep.Update {
	var ups []dep.Update
	for m := range 4 {
		for i := range perManager {
			ups = append(ups, dep.Update{Dep: dep.Dep{
				Manager: fmt.Sprint("m", m),
				Name:    fmt.Sprint("dep", i),
				File:    fmt.Sprintf("dir%03d/manifest%d", i%100, m%2),
				Line:    i % 7,
			}})
		}
	}
	return ups
}

func TestStableSortInPlaceMatchesSortStableFunc(t *testing.T) {
	cmpUp := func(a, b dep.Update) int {
		if c := strings.Compare(a.Dep.File, b.Dep.File); c != 0 {
			return c
		}
		return a.Dep.Line - b.Dep.Line
	}
	r := rand.New(rand.NewPCG(1, 2))
	for n := range 80 {
		ups := make([]dep.Update, n)
		for i := range ups {
			// Few distinct keys force many ties, so stability is exercised.
			ups[i] = dep.Update{Dep: dep.Dep{Name: fmt.Sprint(i), File: fmt.Sprint("f", r.IntN(4)), Line: r.IntN(3)}}
		}
		want := slices.Clone(ups)
		slices.SortStableFunc(want, cmpUp)
		sortUpdates(ups)
		for i := range ups {
			if ups[i].Dep.Name != want[i].Dep.Name {
				t.Fatalf("n=%d: position %d = %s, want %s", n, i, ups[i].Dep.Name, want[i].Dep.Name)
			}
		}
	}
}

func BenchmarkSortIdxSortUpdates(b *testing.B) {
	base := sortIdxUpdates(500)
	buf := make([]dep.Update, len(base))
	for b.Loop() {
		copy(buf, base)
		sortUpdates(buf)
	}
}

func BenchmarkSortIdxGroupUpdates(b *testing.B) {
	ups := sortIdxUpdates(500)
	for b.Loop() {
		GroupUpdates(ups, "deps/")
	}
}
