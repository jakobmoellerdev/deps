package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrRateLimited is wrapped by the error returned when a server's rate limit resets later than
// deps is willing to wait, and by the errors of responses still rate limited once the retries
// gave up (LimitError).
var ErrRateLimited = errors.New("rate limited")

type idempotentKey struct{}

// Idempotent marks ctx so that requests sent with it are retried like GET requests although
// their method is not idempotent, e.g. a read-only query sent as POST. The request body must be
// replayable: http.NewRequest sets GetBody for bytes and strings readers.
func Idempotent(ctx context.Context) context.Context {
	return context.WithValue(ctx, idempotentKey{}, true)
}

type rateLimitHookKey struct{}

// WithRateLimitHook returns ctx with fn called whenever a request sent with it waits out a
// rate limit (429 or an exhausted quota with a reset deps is willing to wait for): host is the
// limited server, until the time the request is sent again. fn must be safe for concurrent use.
func WithRateLimitHook(ctx context.Context, fn func(host string, until time.Time)) context.Context {
	return context.WithValue(ctx, rateLimitHookKey{}, fn)
}

const (
	maxRetries       = 3 // 4 attempts
	backoffBase      = 500 * time.Millisecond
	backoffCap       = 10 * time.Second
	maxRateLimitWait = 60 * time.Second
	// resetLag is the first wait when a rate limit is still exhausted after its reset (GitHub
	// restores the quota up to about a second late); it doubles per further attempt.
	resetLag   = time.Second
	drainLimit = 64 << 10 // bytes read from a discarded body so the connection can be reused
)

// retryTransport retries idempotent requests on connection errors and on 429, 500, 502, 503
// and 504 with exponential backoff and full jitter, honoring Retry-After. Rate limits (429, or
// 403 with an exhausted X-RateLimit-Remaining or Retry-After) are waited out when they reset
// within maxWait; later resets fail fast with ErrRateLimited. A limit still exhausted after its
// reset is retried after resetLag, doubled per attempt.
type retryTransport struct {
	next    http.RoundTripper
	maxWait time.Duration
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	jitter  func(d time.Duration) time.Duration // uniform in [0, d]
}

func newRetryTransport(next http.RoundTripper) *retryTransport {
	return &retryTransport{next: next, maxWait: maxRateLimitWait, now: time.Now, sleep: sleepCtx, jitter: fullJitter}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	hasBody := req.Body != nil && req.Body != http.NoBody
	retryable := idempotent(req) && (!hasBody || req.GetBody != nil)
	r := req
	for attempt := 1; ; attempt++ {
		resp, err := t.next.RoundTrip(r)
		var wait time.Duration
		if err != nil {
			if !retryable || attempt > maxRetries || !transient(ctx, err) {
				return nil, err
			}
			wait = t.backoff(attempt)
			slog.DebugContext(ctx, "http retry", "method", req.Method, "url", req.URL.Redacted(), "attempt", attempt, "wait", wait, "err", err)
		} else {
			h := resp.Header
			limited := rateLimited(resp.StatusCode, h)
			if !limited && !retryStatus(resp.StatusCode) {
				return resp, nil
			}
			until, hinted := t.hint(h, limited)
			wait = max(until.Sub(t.now()), 0)
			if hinted && wait > t.maxWait {
				if !limited {
					return resp, nil
				}
				discard(resp)
				return nil, fmt.Errorf("%w by %s until %s", ErrRateLimited, req.URL.Hostname(), until.UTC().Format(time.RFC3339))
			}
			if !retryable || attempt > maxRetries || resp.StatusCode == http.StatusForbidden && !hinted {
				return resp, nil
			}
			discard(resp)
			if limited && hinted {
				if attempt > 1 && wait < resetLag {
					wait = min(resetLag<<(attempt-2), t.maxWait)
				}
				slog.InfoContext(ctx, "rate limited, waiting", "host", req.URL.Hostname(), "wait", wait, "until", until.UTC().Format(time.RFC3339))
				if fn, ok := ctx.Value(rateLimitHookKey{}).(func(string, time.Time)); ok {
					fn(req.URL.Hostname(), t.now().Add(wait))
				}
			} else {
				if !hinted {
					wait = t.backoff(attempt)
				}
				slog.DebugContext(ctx, "http retry", "method", req.Method, "url", req.URL.Redacted(), "attempt", attempt, "wait", wait, "status", resp.StatusCode)
			}
		}
		if err := t.sleep(ctx, wait); err != nil {
			return nil, err
		}
		if hasBody {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			c := *req
			c.Body = body
			r = &c
		}
	}
}

// backoff is the full-jitter exponential delay before retry number attempt (1-based).
func (t *retryTransport) backoff(attempt int) time.Duration {
	return t.jitter(min(backoffCap, backoffBase<<(attempt-1)))
}

// hint returns when the server asks to be retried (retryHint at t's clock).
func (t *retryTransport) hint(h http.Header, limited bool) (time.Time, bool) {
	return retryHint(h, limited, t.now())
}

// retryHint returns when the server asks to be retried: Retry-After (seconds or HTTP date), or
// for rate-limited responses X-RateLimit-Reset (epoch seconds), else the end of the quota
// window of an exhausted `RateLimit-Remaining: 0;w=<seconds>` (Docker Hub sends no reset time,
// so the window length is the latest the quota can recover).
func retryHint(h http.Header, limited bool, now time.Time) (time.Time, bool) {
	if v := h.Get("Retry-After"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec >= 0 {
			return now.Add(time.Duration(sec) * time.Second), true
		}
		if at, err := http.ParseTime(v); err == nil {
			return at, true
		}
	}
	if !limited {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
		return time.Unix(sec, 0), true
	}
	if left, window := quota(h); left == "0" && window > 0 {
		return now.Add(time.Duration(window) * time.Second), true
	}
	return time.Time{}, false
}

// rateLimited reports whether a response with status code and header h is a rate limit: a 429,
// or a 403 with an exhausted quota or a Retry-After (GitHub's secondary limits).
func rateLimited(code int, h http.Header) bool {
	return code == http.StatusTooManyRequests || code == http.StatusForbidden && (exhausted(h) || h.Get("Retry-After") != "")
}

// LimitError returns the error of resp when it is a rate limit (a 429, or a 403 with an
// exhausted quota or Retry-After) that the retries could not wait out: "rate limited by <host>
// until <reset>" wrapping ErrRateLimited, without "by" when resp has no request and without
// "until" when the server names no reset. It is nil for any other response.
func LimitError(resp *http.Response) error {
	if !rateLimited(resp.StatusCode, resp.Header) {
		return nil
	}
	by := ""
	if resp.Request != nil && resp.Request.URL != nil {
		by = " by " + resp.Request.URL.Hostname()
	}
	until, ok := retryHint(resp.Header, true, time.Now())
	if !ok {
		return fmt.Errorf("%w%s", ErrRateLimited, by)
	}
	return fmt.Errorf("%w%s until %s", ErrRateLimited, by, until.UTC().Format(time.RFC3339))
}

// ResponseError describes a response with an unexpected status: "HTTP <code>", followed for a
// rate limit by ": " and its LimitError, which it wraps.
func ResponseError(resp *http.Response) error {
	if err := LimitError(resp); err != nil {
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// quota parses the requests left from X-RateLimit-Remaining or RateLimit-Remaining: GitHub
// sends `0`, Docker Hub `0;w=3600` with the quota window in seconds (window 0 = none sent).
func quota(h http.Header) (left string, window int64) {
	v := h.Get("X-RateLimit-Remaining")
	if v == "" {
		v = h.Get("RateLimit-Remaining")
	}
	left, params, _ := strings.Cut(v, ";")
	if w, ok := strings.CutPrefix(strings.TrimSpace(params), "w="); ok {
		window, _ = strconv.ParseInt(w, 10, 64)
	}
	return strings.TrimSpace(left), window
}

// exhausted reports whether the remaining-requests header says none are left.
func exhausted(h http.Header) bool {
	left, _ := quota(h)
	return left == "0"
}

func idempotent(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	marked, _ := req.Context().Value(idempotentKey{}).(bool)
	return marked
}

func retryStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// transient reports whether a transport error may succeed on retry: not a cancellation and
// not a certificate rejection.
func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var cert *tls.CertificateVerificationError
	return !errors.As(err, &cert)
}

func discard(resp *http.Response) {
	_, _ = io.CopyN(io.Discard, resp.Body, drainLimit)
	_ = resp.Body.Close()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fullJitter(d time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(d) + 1))
}
