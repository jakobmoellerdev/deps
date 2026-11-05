package engine

import "slices"

// stableSortInPlace sorts s stably by cmp. It sorts an index permutation
// and then moves each element once, so large structs such as dep.Update
// are not copied on every swap and comparisons see pointers, not copies.
func stableSortInPlace[T any](s []T, cmp func(a, b *T) int) {
	idx := make([]int, len(s))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int {
		if c := cmp(&s[i], &s[j]); c != 0 {
			return c
		}
		return i - j
	})
	// Apply the permutation: position i receives s[idx[i]]. Follow each
	// cycle once, marking visited slots with -1.
	for i := range idx {
		if idx[i] < 0 || idx[i] == i {
			continue
		}
		tmp := s[i]
		j := i
		for {
			k := idx[j]
			idx[j] = -1
			if k == i {
				s[j] = tmp
				break
			}
			s[j] = s[k]
			j = k
		}
	}
}
