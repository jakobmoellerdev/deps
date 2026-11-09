package engine

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// BenchmarkApplySecurity raises a dep at 1.0.0 with 10 advisories (three fixed versions each)
// against 500 releases: with a policy update above the fix, and without one, where the
// fixing release is searched among all releases.
func BenchmarkApplySecurity(b *testing.B) {
	s, ok := versioning.Get("semver")
	if !ok {
		b.Fatal("no semver scheme")
	}
	var names []string
	for major := range 5 {
		for minor := range 10 {
			for patch := range 10 {
				names = append(names, fmt.Sprintf("%d.%d.%d", major, minor, patch))
			}
		}
	}
	vulns := make([]Vuln, 10)
	for i := range vulns {
		vulns[i] = Vuln{Advisory: dep.Advisory{ID: fmt.Sprintf("GHSA-%d", i)}, Fixed: []string{fmt.Sprintf("1.%d.1", i), fmt.Sprintf("2.%d.0", i), "3.0.0"}}
	}
	_, cfg := setup(b, "", nil)
	d := dep.Dep{Manager: "npm", Datasource: "npm", Name: "x", Version: "1.0.0"}
	r := &resolver{d: d, cur: d.Version, pol: newPolicies(cfg, d, false)}
	for _, bc := range []struct {
		name string
		ups  []dep.Update
	}{
		{"update", []dep.Update{{Dep: d, Type: dep.Minor, NewVersion: "1.9.9"}}},
		{"fallback", []dep.Update{{Dep: d, Type: dep.Patch, NewVersion: "1.0.9"}}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ups := r.applySecurity(append([]dep.Update(nil), bc.ups...), s, names, vulns)
				if len(ups) != 1 || len(ups[0].Advisories) != len(vulns) {
					b.Fatalf("ups = %+v", ups)
				}
			}
		})
	}
}
