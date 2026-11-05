package engine

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkGroupUpdatesDistinctNames groups one 1000-update policy group
// and 300 single-update groups.
func BenchmarkGroupUpdatesDistinctNames(b *testing.B) {
	var ups []dep.Update
	for i := range 1000 {
		ups = append(ups, dep.Update{Dep: dep.Dep{Manager: "gomod", Name: fmt.Sprintf("example.com/g%d", i)}, NewVersion: fmt.Sprintf("v1.%d.0", i), Group: "All"})
	}
	for i := range 300 {
		ups = append(ups, dep.Update{Dep: dep.Dep{Manager: "gomod", Name: fmt.Sprintf("example.com/s%d", i)}, NewVersion: "v2.0.0"})
	}
	b.ReportAllocs()
	for b.Loop() {
		GroupUpdates(ups, "")
	}
}
