package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// benchManagers are the managers of the deps and rules of the large-config benchmarks.
var benchManagers = []string{"gomod", "npm", "docker", "github-actions", "helm", "cargo", "maven", "pip_requirements", "bundler", "nuget"}

// bigConfig writes a config of rules rules in the shape a translated Renovate config has:
// most rules select a manager and a name glob, some a dep type, a few nothing.
func bigConfig(b *testing.B, rules int) *Config {
	b.Helper()
	var y strings.Builder
	y.WriteString("rules:\n")
	for i := range rules {
		m := benchManagers[i%len(benchManagers)]
		switch i % 5 {
		case 0:
			fmt.Fprintf(&y, "  - managers: [%s]\n    names: [\"org%d/*\"]\n    group: g%d\n", m, i, i)
		case 1:
			fmt.Fprintf(&y, "  - managers: [%s]\n    names: [\"org%d/pkg%d\", \"com.example.org%d:*\"]\n    automerge: true\n", m, i, i, i)
		case 2:
			fmt.Fprintf(&y, "  - managers: [%s]\n    depTypes: [dependencies]\n    names: [\"@scope%d/**\"]\n    labels: [l%d]\n", m, i, i)
		case 3:
			fmt.Fprintf(&y, "  - names: [\"org%d/**\"]\n    minimumReleaseAge: 3 days\n", i)
		default:
			fmt.Fprintf(&y, "  - managers: [%s]\n    group: g%d\n", m, i)
		}
	}
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte(y.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	c, err := Load(b.Context(), dir, "", nil)
	if err != nil {
		b.Fatal(err)
	}
	return c
}

// bigDeps returns n deps spread over benchManagers, as a large repository has.
func bigDeps(n int) []dep.Dep {
	deps := make([]dep.Dep, 0, n)
	for i := range n {
		m := benchManagers[i%len(benchManagers)]
		deps = append(deps, dep.Dep{
			Manager: m, Name: fmt.Sprintf("org%d/pkg%d", i%997, i), Version: "1.2.3",
			DepType: []string{"dependencies", "direct", "devDependencies"}[i%3],
			File:    fmt.Sprintf("dir%d/file", i%11), Datasource: m,
		})
	}
	return deps
}

// BenchmarkPolicyBigConfig resolves the policy of 5000 deps against 300 rules, the per-dep
// work of a run on a large repository with a translated Renovate config.
func BenchmarkPolicyBigConfig(b *testing.B) {
	c := bigConfig(b, 300)
	deps := bigDeps(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, d := range deps {
			_ = c.Policy(d)
		}
	}
}

// BenchmarkPolicyForBigConfig adds the per-update-type policies the engine asks for.
func BenchmarkPolicyForBigConfig(b *testing.B) {
	c := bigConfig(b, 300)
	deps := bigDeps(2000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, d := range deps {
			_ = c.Policy(d)
			_ = c.PolicyFor(d, dep.Major)
			_ = c.PolicyFor(d, dep.Minor)
			_ = c.PolicyFor(d, dep.Patch)
		}
	}
}

// BenchmarkUnusedRulesBigConfig is the dependency dashboard's unused-rule scan.
func BenchmarkUnusedRulesBigConfig(b *testing.B) {
	c := bigConfig(b, 300)
	deps := bigDeps(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = c.UnusedRules(deps)
	}
}
