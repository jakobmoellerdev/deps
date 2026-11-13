package renovate

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// benchLoad translates src from a temporary directory once per iteration.
func benchLoad(b *testing.B, src string) {
	b.Helper()
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renovate.json"), []byte(src), 0o600); err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Load(ctx, dir, "", nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadBestPractices translates the preset stack most repositories use:
// config:best-practices with config:recommended, the replacement presets and the groups.
func BenchmarkLoadBestPractices(b *testing.B) {
	benchLoad(b, `{"extends":["config:best-practices"]}`)
}

// manyRules is a repository config with n packageRules, each setting a handful of effect
// keys next to its matchers, as large monorepo configs do.
func manyRules(n int, extra map[string]any) string {
	cfg := map[string]any{"extends": []string{"config:recommended"}}
	rules := make([]map[string]any, 0, n)
	for i := range n {
		rules = append(rules, map[string]any{
			"matchManagers":     []string{"gomod"},
			"matchPackageNames": []string{fmt.Sprintf("github.com/org/pkg%d", i)},
			"groupName":         fmt.Sprintf("group %d", i),
			"automerge":         i%2 == 0,
			"minimumReleaseAge": "3 days",
			"labels":            []string{"deps", fmt.Sprintf("team-%d", i%7)},
			"reviewers":         []string{fmt.Sprintf("user%d", i%5)},
		})
	}
	cfg["packageRules"] = rules
	maps.Copy(cfg, extra)
	out, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// BenchmarkLoadManyRules translates a config with 200 packageRules of five effect keys each.
func BenchmarkLoadManyRules(b *testing.B) {
	benchLoad(b, manyRules(200, nil))
}

// BenchmarkLoadManyRulesCustomDatasources adds custom datasources, which rule validation
// needs to know about.
func BenchmarkLoadManyRulesCustomDatasources(b *testing.B) {
	cds := map[string]any{}
	for i := range 20 {
		cds[fmt.Sprintf("ds%d", i)] = map[string]any{
			"defaultRegistryUrlTemplate": fmt.Sprintf("https://example.com/%d/releases", i),
			"format":                     "json",
		}
	}
	benchLoad(b, manyRules(200, map[string]any{"customDatasources": cds}))
}
