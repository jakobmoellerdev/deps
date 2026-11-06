package nuget

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkRangeHooks checks and rewrites an interval range for 500 candidate releases, as
// the engine does per candidate.
func BenchmarkRangeHooks(b *testing.B) {
	var cands []string
	for i := range 500 {
		cands = append(cands, fmt.Sprintf("%d.%d.%d", i/100, i/10%10, i%10))
	}
	m := &nugetManager{}
	d := dep.Dep{Range: "[1.0,3.0)"}
	b.ReportAllocs()
	for b.Loop() {
		for _, v := range cands {
			m.InRange(d, v)
			m.NewRange(d, v, "bump")
		}
	}
}
