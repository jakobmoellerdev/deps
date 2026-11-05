package engine

import (
	"context"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Phase is a stage of a run that Progress observes.
type Phase string

// Phases in run order: Resolve runs discover, extract and resolve; bot.Run builds and pushes
// the PRs.
const (
	PhaseDiscover Phase = "discover" // the file walk; no items
	PhaseExtract  Phase = "extract"  // one item per discovered file
	PhaseResolve  Phase = "resolve"  // one item per updatable dependency
	PhasePRs      Phase = "prs"      // one item per PR group (bot.Run)
)

// Progress observes the phases of a run, for a live status line or CI log groups. Start and
// End of a phase come from the goroutine running it, in order; Done and RateLimited may come
// from many goroutines at once, so implementations must be safe for concurrent use and fast.
type Progress interface {
	// Start begins phase p with total items (0 for a phase without items).
	Start(p Phase, total int)
	// Done marks one item of p as finished; err is its failure, if any.
	Done(p Phase, err error)
	// End finishes p.
	End(p Phase)
	// RateLimited reports a request waiting for host's rate limit to reset at until.
	RateLimited(host string, until time.Time)
}

// WithProgress returns ctx with p's RateLimited hooked into the HTTP retries of requests
// sent with it (httpx.WithRateLimitHook), and p or a no-op Progress when p is nil.
func WithProgress(ctx context.Context, p Progress) (context.Context, Progress) {
	if p == nil {
		return ctx, noProgress{}
	}
	return httpx.WithRateLimitHook(ctx, p.RateLimited), p
}

type noProgress struct{}

func (noProgress) Start(Phase, int)              {}
func (noProgress) Done(Phase, error)             {}
func (noProgress) End(Phase)                     {}
func (noProgress) RateLimited(string, time.Time) {}
