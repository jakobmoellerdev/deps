package diff

import (
	"fmt"
	"strings"
	"testing"
)

// lockfile returns an n-line lockfile-like text and a copy with every step-th line changed.
func lockfile(n, step int) (old, updated string) {
	var a, b strings.Builder
	for i := range n {
		line := fmt.Sprintf("  \"node_modules/pkg-%d\": { \"version\": \"1.%d.0\" },\n", i, i%7)
		a.WriteString(line)
		if i%step == 0 {
			line = fmt.Sprintf("  \"node_modules/pkg-%d\": { \"version\": \"2.%d.0\" },\n", i, i%7)
		}
		b.WriteString(line)
	}
	return a.String(), b.String()
}

func BenchmarkUnified(b *testing.B) {
	for _, c := range []struct {
		name    string
		n, step int
	}{{"small", 40, 10}, {"lockfile", 30000, 60}} {
		old, updated := lockfile(c.n, c.step)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Unified("a", "b", old, updated)
			}
		})
	}
}
