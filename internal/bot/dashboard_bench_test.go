package bot

import (
	"fmt"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func BenchmarkRenderDashboard(b *testing.B) {
	var actions []Action
	for i := range 200 {
		actions = append(actions, Action{Branch: fmt.Sprintf("deps/gomod/mod%d", i), Title: fmt.Sprintf("chore(deps): update mod%d to v1.1.0", i), Status: StatusUpToDate, Number: i + 1, URL: fmt.Sprintf("https://x/pulls/%d", i+1)})
	}
	var deps []dep.Dep
	for i := range 3000 {
		deps = append(deps, dep.Dep{Manager: []string{"gomod", "npm", "docker"}[i%3], File: fmt.Sprintf("dir%d/file", i%40), Line: i, Name: fmt.Sprintf("example.com/mod%d", i), Version: "v1.0.0"})
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = renderDashboard(actions, nil, deps)
	}
}
