package main

import (
	"fmt"
	"io"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// benchSummary is a large run: 500 updates in 500 PRs, one bot action each, and 200
// skipped deps.
func benchSummary() *summary {
	res := &engine.Result{}
	for i := range 500 {
		d := dep.Dep{Manager: "gomod", File: "go.mod", Line: i + 3, Name: fmt.Sprintf("example.com/mod%d", i), Version: "v1.0.0"}
		res.Deps = append(res.Deps, d)
		res.Updates = append(res.Updates, dep.Update{Dep: d, Type: dep.Minor, NewVersion: "v1.1.0"})
	}
	for i := range 200 {
		d := dep.Dep{File: "go.mod", Line: 600 + i, Name: fmt.Sprintf("example.com/ind%d", i), Skip: "ignored by default (indirect; enable with a rule setting ignore: false)"}
		res.Deps = append(res.Deps, d)
		res.Skipped = append(res.Skipped, d)
	}
	groups := engine.GroupUpdates(res.Updates, "")
	actions := make([]bot.Action, len(groups))
	for i, g := range groups {
		actions[i] = bot.Action{Branch: g.Branch, Title: g.Title, Status: bot.StatusUpToDate, URL: fmt.Sprintf("https://x/pulls/%d", i), Number: i}
	}
	return &summary{Command: "run", Run: true, Res: res, Groups: groups, Actions: actions, Elapsed: time.Second}
}

func BenchmarkSummary(b *testing.B) {
	base := benchSummary()
	b.ReportAllocs()
	for b.Loop() {
		s := *base // a fresh summary each round, as one CLI run renders one
		s.writeText(io.Discard)
		_ = s.markdown()
	}
}
