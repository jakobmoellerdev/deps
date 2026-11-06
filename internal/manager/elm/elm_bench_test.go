package elm

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkNewRange rewrites an interval constraint for 500 candidate releases, as the
// engine does per candidate.
func BenchmarkNewRange(b *testing.B) {
	var cands []string
	for i := range 500 {
		cands = append(cands, fmt.Sprintf("%d.%d.%d", i/100, i/10%10, i%10))
	}
	m := elmManager{}
	d := dep.Dep{Range: "1.0.0 <= v < 3.0.0"}
	b.ReportAllocs()
	for b.Loop() {
		for _, v := range cands {
			m.NewRange(d, v, "bump")
		}
	}
}
