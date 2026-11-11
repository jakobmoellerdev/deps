package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkPolicySharedUpdates resolves policies against ~30 rules for deps of three
// managers, the per-dep work newPolicies and policies.of do in the engine.
func BenchmarkPolicySharedUpdates(b *testing.B) {
	var y strings.Builder
	y.WriteString("rules:\n")
	for i := range 10 {
		fmt.Fprintf(&y, "  - managers: [npm]\n    names: [\"pkg-%d*\"]\n    group: g%d\n", i, i)
		fmt.Fprintf(&y, "  - managers: [gomod]\n    names: [\"example.com/m%d/*\"]\n    allowedUpdateTypes: [minor, patch]\n", i)
		fmt.Fprintf(&y, "  - managers: [docker]\n    names: [\"img%d\"]\n    updateTypes: [major]\n    ignore: true\n", i)
	}
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte(y.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	c, err := Load(b.Context(), dir, "", nil)
	if err != nil {
		b.Fatal(err)
	}
	deps := []dep.Dep{
		{Manager: "npm", Name: "pkg-3-core", Version: "1.2.3"},
		{Manager: "npm", Name: "other", Version: "1.2.3"},
		{Manager: "gomod", Name: "example.com/m4/x", Version: "v1.2.3"},
		{Manager: "gomod", Name: "example.com/z", Version: "v1.2.3", DepType: "indirect"},
		{Manager: "docker", Name: "img5", Version: "1.2"},
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, d := range deps {
			_ = c.Policy(d)
			_ = c.PolicyFor(d, dep.Major)
		}
	}
}

// BenchmarkPolicyCurrentVersion resolves policies against rules whose currentVersion is a
// constraint: each of them converts the dep's current version with the versioning scheme the
// dep is resolved with, which the rules setting versioning decide.
func BenchmarkPolicyCurrentVersion(b *testing.B) {
	var y strings.Builder
	y.WriteString("rules:\n")
	for i := range 8 {
		fmt.Fprintf(&y, "  - managers: [npm]\n    currentVersion: \"< %d\"\n    group: g%d\n", i+2, i)
		fmt.Fprintf(&y, "  - managers: [gomod]\n    currentVersion: \">= 1.%d\"\n    allowedUpdateTypes: [minor, patch]\n", i)
	}
	for i := range 6 {
		fmt.Fprintf(&y, "  - names: [\"@scope-%d/*\", \"example.com/m%d/**\"]\n    versioning: loose\n", i, i)
	}
	y.WriteString("  - managers: [docker]\n    versioning: docker\n")
	y.WriteString("  - managers: [npm]\n    names: [\"pkg-*\"]\n    versioning: semver\n")
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte(y.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	c, err := Load(b.Context(), dir, "", nil)
	if err != nil {
		b.Fatal(err)
	}
	deps := []dep.Dep{
		{Manager: "npm", Name: "pkg-3-core", Version: "1.2.3", Versioning: "semver"},
		{Manager: "gomod", Name: "example.com/m4/x", Version: "v1.2.3", Versioning: "semver"},
		{Manager: "docker", Name: "img5", Version: "1.2", Versioning: "docker"},
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, d := range deps {
			_ = c.Policy(d)
			for _, k := range dep.UpdateTypes {
				_ = c.PolicyFor(d, k)
			}
		}
	}
}
