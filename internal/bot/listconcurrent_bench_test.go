package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// listLatency is the injected round-trip time of each listing call.
const listLatency = 20 * time.Millisecond

type slowListGit struct{ fakeGit }

func (g *slowListGit) LsRemote(ctx context.Context, pattern string) (map[string]string, error) {
	if strings.HasSuffix(pattern, "*") {
		time.Sleep(listLatency)
	}
	return g.fakeGit.LsRemote(ctx, pattern)
}

type slowListPC struct{ fakePC }

func (c *slowListPC) ListPRs(ctx context.Context, state string, n int) ([]platform.PR, error) {
	time.Sleep(listLatency)
	return c.fakePC.ListPRs(ctx, state, n)
}

func (c *slowListPC) ListIssues(ctx context.Context, maxPages int, f platform.IssueFilter) (platform.IssueList, error) {
	time.Sleep(listLatency)
	return c.fakePC.ListIssues(ctx, maxPages, f)
}

// BenchmarkRunListLatency runs the bot with 20 ms on each of the four state
// reads (update branches, open PRs, closed PRs, dashboard issues).
func BenchmarkRunListLatency(b *testing.B) {
	root, res := fixture(b)
	o := Options{MaxNewPRs: 10, Managers: managers, Dashboard: &DashboardOptions{Title: "Dependency Dashboard"}}
	for b.Loop() {
		g := &slowListGit{}
		pc := &slowListPC{}
		if _, errs := Run(b.Context(), o, []Base{{Root: root, Res: res, Git: g}}, nil, pc); len(errs) != 0 {
			b.Fatal(errs)
		}
	}
}
