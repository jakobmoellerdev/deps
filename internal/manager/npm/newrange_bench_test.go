package npm

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkNewRangeCandidates rewrites ranges for 300 candidate versions (inside and
// outside each range), as the engine's accept and inRange checks do per candidate.
func BenchmarkNewRangeCandidates(b *testing.B) {
	var versions []string
	for major := range 4 {
		for minor := range 15 {
			for patch := range 5 {
				versions = append(versions, fmt.Sprintf("%d.%d.%d", major, minor, patch))
			}
		}
	}
	ranges := []string{"^1.2.3", ">=1.2.3 <2", "1.x || 2.x", "1.2.3 - 2.3.4"}
	for _, strategy := range []string{"replace", "bump", dep.RangeWiden} {
		b.Run(strategy, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, rng := range ranges {
					d := dep.Dep{Range: rng}
					for _, v := range versions {
						npmManager{}.NewRange(d, v, strategy)
					}
				}
			}
		})
	}
}
