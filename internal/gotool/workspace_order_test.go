package gotool

import (
	"slices"
	"testing"
)

func TestCompareModuleDirs(t *testing.T) {
	want := []string{"a/bb", "a/b", "ab", "a", "b", "c", "."}
	for i := range 50 {
		mods := slices.Clone(want)
		// Rotate so "." precedes the one-character dirs it ties with by length.
		mods = append(mods[i%len(mods):], mods[:i%len(mods)]...)
		slices.Reverse(mods)
		slices.SortFunc(mods, compareModuleDirs)
		if !slices.Equal(mods, want) {
			t.Fatalf("sorted = %v, want %v", mods, want)
		}
	}
}
