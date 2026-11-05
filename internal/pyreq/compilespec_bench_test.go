package pyreq

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// compileSpecCandidates are 3000 PyPI-style releases, as the engine checks for one dep.
func compileSpecCandidates() []string {
	versions := make([]string, 0, 3000)
	for i := range 3000 {
		versions = append(versions, fmt.Sprintf("%d.%d.%d", i/600, i/30%20, i%30))
	}
	return versions
}

// compileSpecDeps are deps with the ranges of the benchmarks.
var compileSpecDeps = []dep.Dep{
	{Range: ">=1.0,<3", Manager: "pip_requirements"},
	{Range: "~=2.1", Manager: "pip_requirements"},
	{Range: "^1.2 || ^2.0", Manager: poetryManager},
}

// BenchmarkCompileSpecInRange checks every candidate against each range, as the engine's
// accept does for the candidates newer than the current version.
func BenchmarkCompileSpecInRange(b *testing.B) {
	versions := compileSpecCandidates()
	b.ReportAllocs()
	for b.Loop() {
		for _, d := range compileSpecDeps {
			for _, v := range versions {
				InRange(d, v)
			}
		}
	}
}

// BenchmarkCompileSpecNewRange rewrites each range for every candidate, as the engine does
// under bump for every candidate in range (bump rewrites as replace does).
func BenchmarkCompileSpecNewRange(b *testing.B) {
	versions := compileSpecCandidates()
	for _, strategy := range []string{"replace", dep.RangeWiden} {
		b.Run(strategy, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, d := range compileSpecDeps {
					for _, v := range versions {
						NewRange(d, v, strategy)
					}
				}
			}
		})
	}
}
