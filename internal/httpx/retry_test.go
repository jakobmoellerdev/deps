package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var clockStart = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// fakeClock records sleeps and advances time by them.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

// fakeRetry wraps next with a retryTransport on a fake clock; jitter returns its maximum.
func fakeRetry(next http.RoundTripper) (*http.Client, *fakeClock) {
	clk := &fakeClock{t: clockStart}
	rt := newRetryTransport(logTransport{next})
	rt.now, rt.sleep = clk.now, clk.sleep
	rt.jitter = func(d time.Duration) time.Duration { return d }
	return &http.Client{Transport: rt}, clk
}

type reply struct {
	code int
	hdr  map[string]string
}

// replay serves replies in order, repeating the last one, and records the request bodies.
type replay struct {
	mu      sync.Mutex
	replies []reply
	bodies  []string
}

func (s *replay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	n := len(s.bodies)
	s.bodies = append(s.bodies, string(b))
	rp := s.replies[min(n, len(s.replies)-1)]
	s.mu.Unlock()
	for k, v := range rp.hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rp.code)
	_, _ = io.WriteString(w, "body "+strconv.Itoa(n))
}

func newReplay(t *testing.T, replies ...reply) (*replay, *httptest.Server) {
	t.Helper()
	s := &replay{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func epoch(d time.Duration) string { return strconv.FormatInt(clockStart.Add(d).Unix(), 10) }

func stamp(d time.Duration) string { return clockStart.Add(d).Format(time.RFC3339) }

func TestRetryStatuses(t *testing.T) {
	const s = time.Second
	backoff := []time.Duration{500 * time.Millisecond, s, 2 * s}
	limited := func(reset string) map[string]string {
		return map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}
	}
	tests := []struct {
		name     string
		replies  []reply
		attempts int
		sleeps   []time.Duration
		status   int
		err      string // substring; "" = no error
	}{
		{name: "success", replies: []reply{{code: 200}}, attempts: 1, status: 200},
		{name: "404", replies: []reply{{code: 404}}, attempts: 1, status: 404},
		{name: "501", replies: []reply{{code: 501}}, attempts: 1, status: 501},
		{name: "plain 403", replies: []reply{{code: 403}}, attempts: 1, status: 403},
		{name: "429", replies: []reply{{code: 429}}, attempts: 4, sleeps: backoff, status: 429},
		{name: "500", replies: []reply{{code: 500}}, attempts: 4, sleeps: backoff, status: 500},
		{name: "502", replies: []reply{{code: 502}}, attempts: 4, sleeps: backoff, status: 502},
		{name: "503", replies: []reply{{code: 503}}, attempts: 4, sleeps: backoff, status: 503},
		{name: "504", replies: []reply{{code: 504}}, attempts: 4, sleeps: backoff, status: 504},
		{name: "recovers", replies: []reply{{code: 503}, {code: 502}, {code: 200}}, attempts: 3, sleeps: backoff[:2], status: 200},
		{name: "no retry after success", replies: []reply{{code: 200}, {code: 503}}, attempts: 1, status: 200},
		{
			name:     "retry-after seconds",
			replies:  []reply{{code: 503, hdr: map[string]string{"Retry-After": "7"}}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{7 * s}, status: 200,
		},
		{
			name:     "retry-after date",
			replies:  []reply{{code: 429, hdr: map[string]string{"Retry-After": clockStart.Add(30 * s).Format(http.TimeFormat)}}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{30 * s}, status: 200,
		},
		{
			name:     "retry-after zero",
			replies:  []reply{{code: 500, hdr: map[string]string{"Retry-After": "0"}}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{0}, status: 200,
		},
		{
			name:     "retry-after invalid uses backoff",
			replies:  []reply{{code: 503, hdr: map[string]string{"Retry-After": "soon"}}, {code: 200}},
			attempts: 2, sleeps: backoff[:1], status: 200,
		},
		{
			name:     "retry-after repeated until exhausted",
			replies:  []reply{{code: 503, hdr: map[string]string{"Retry-After": "1"}}},
			attempts: 4, sleeps: []time.Duration{s, s, s}, status: 503,
		},
		{
			name:     "retry-after beyond budget returns response",
			replies:  []reply{{code: 503, hdr: map[string]string{"Retry-After": "120"}}},
			attempts: 1, status: 503,
		},
		{
			name:     "rate limit short wait",
			replies:  []reply{{code: 403, hdr: limited(epoch(20 * s))}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{20 * s}, status: 200,
		},
		{
			name:     "429 with reset",
			replies:  []reply{{code: 429, hdr: limited(epoch(60 * s))}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{60 * s}, status: 200,
		},
		{
			name:     "rate limit reset passed",
			replies:  []reply{{code: 403, hdr: limited(epoch(-10 * s))}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{0}, status: 200,
		},
		{
			// GitHub restores the quota up to a second after its reset: wait 1s, then 2s.
			name:     "rate limit still exhausted after reset",
			replies:  []reply{{code: 403, hdr: limited(epoch(20 * s))}, {code: 403, hdr: limited(epoch(20 * s))}, {code: 403, hdr: limited(epoch(20 * s))}, {code: 200}},
			attempts: 4, sleeps: []time.Duration{20 * s, s, 2 * s}, status: 200,
		},
		{
			name:     "rate limit exhausted after every retry returns the response",
			replies:  []reply{{code: 403, hdr: limited(epoch(20 * s))}},
			attempts: 4, sleeps: []time.Duration{20 * s, s, 2 * s}, status: 403,
		},
		{
			name:     "403 retry-after (secondary limit)",
			replies:  []reply{{code: 403, hdr: map[string]string{"Retry-After": "5"}}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{5 * s}, status: 200,
		},
		{
			name:     "rate limit fail fast",
			replies:  []reply{{code: 403, hdr: limited(epoch(time.Hour))}},
			attempts: 1, err: "rate limited by 127.0.0.1 until " + stamp(time.Hour),
		},
		{
			name:     "429 retry-after fail fast",
			replies:  []reply{{code: 429, hdr: map[string]string{"Retry-After": "61"}}},
			attempts: 1, err: "rate limited by 127.0.0.1 until " + stamp(61*s),
		},
		{
			name:     "403 exhausted without reset",
			replies:  []reply{{code: 403, hdr: limited("soon")}},
			attempts: 1, status: 403,
		},
		{
			// Docker Hub: no Retry-After or reset, only the exhausted quota and its window.
			name:     "429 quota window fail fast",
			replies:  []reply{{code: 429, hdr: map[string]string{"Ratelimit-Remaining": "0;w=3600", "X-Ratelimit-Remaining": "0;w=3600"}}},
			attempts: 1, err: "rate limited by 127.0.0.1 until " + stamp(time.Hour),
		},
		{
			name:     "429 short quota window waits",
			replies:  []reply{{code: 429, hdr: map[string]string{"Ratelimit-Remaining": "0;w=30"}}, {code: 200}},
			attempts: 2, sleeps: []time.Duration{30 * s}, status: 200,
		},
		{
			name:     "403 quota window fail fast",
			replies:  []reply{{code: 403, hdr: map[string]string{"Ratelimit-Remaining": "0;w=21600"}}},
			attempts: 1, err: "rate limited by 127.0.0.1 until " + stamp(6*time.Hour),
		},
		{
			name:     "429 with quota left uses backoff",
			replies:  []reply{{code: 429, hdr: map[string]string{"Ratelimit-Remaining": "5;w=3600"}}},
			attempts: 4, sleeps: backoff, status: 429,
		},
		{
			name:     "429 quota without window uses backoff",
			replies:  []reply{{code: 429, hdr: map[string]string{"Ratelimit-Remaining": "0;w=soon"}}},
			attempts: 4, sleeps: backoff, status: 429,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp, srv := newReplay(t, tt.replies...)
			c, clk := fakeRetry(srv.Client().Transport)
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
			resp, err := c.Do(req)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !errors.Is(err, ErrRateLimited) {
					t.Fatalf("err = %v, want ErrRateLimited containing %q", err, tt.err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != tt.status || string(b) != "body "+strconv.Itoa(tt.attempts-1) {
					t.Errorf("got %d %q, want %d from attempt %d", resp.StatusCode, b, tt.status, tt.attempts)
				}
			}
			if len(rp.bodies) != tt.attempts {
				t.Errorf("attempts = %d, want %d", len(rp.bodies), tt.attempts)
			}
			if !reflect.DeepEqual(clk.sleeps, tt.sleeps) {
				t.Errorf("sleeps = %v, want %v", clk.sleeps, tt.sleeps)
			}
		})
	}
}

// Responses still rate limited once the retries gave up wrap ErrRateLimited, so that callers
// can tell them from other failures.
func TestResponseErrorRateLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://api.github.com/repos/o/r/releases", http.NoBody)
	for _, tt := range []struct {
		name string
		code int
		hdr  map[string]string
		want string
	}{
		{"exhausted 403", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1793707200"}, "HTTP 403: rate limited by api.github.com until 2026-11-03T12:00:00Z"},
		{"secondary 403", 403, map[string]string{"Retry-After": "60"}, "HTTP 403: rate limited by api.github.com until "},
		{"429 without reset", 429, nil, "HTTP 429: rate limited by api.github.com"},
		{"plain 403", 403, nil, "HTTP 403"},
		{"exhausted 500", 500, map[string]string{"X-RateLimit-Remaining": "0"}, "HTTP 500"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.code, Header: http.Header{}, Request: req}
			for k, v := range tt.hdr {
				resp.Header.Set(k, v)
			}
			limited := strings.Contains(tt.want, "rate limited")
			if err := ResponseError(resp); !strings.HasPrefix(err.Error(), tt.want) || errors.Is(err, ErrRateLimited) != limited {
				t.Errorf("ResponseError = %v, want %q (rate limited: %t)", err, tt.want, limited)
			}
			se := &StatusError{URL: "https://api.github.com/x", Code: tt.code, Limit: LimitError(resp)}
			if errors.Is(se, ErrRateLimited) != limited || errors.Is(se, ErrNotFound) || !strings.HasPrefix(se.Error(), "GET https://api.github.com/x: "+tt.want) {
				t.Errorf("StatusError = %v", se)
			}
		})
	}
}

func TestRetryMethods(t *testing.T) {
	const payload = `{"queries":[1,2,3]}`
	tests := []struct {
		name     string
		method   string
		mark     bool
		body     func() io.Reader
		reply    reply
		attempts int
		err      bool
	}{
		{name: "HEAD", method: http.MethodHead, attempts: 4},
		{name: "OPTIONS", method: http.MethodOptions, attempts: 4},
		{name: "POST", method: http.MethodPost, body: func() io.Reader { return strings.NewReader(payload) }, attempts: 1},
		{name: "PATCH", method: http.MethodPatch, body: func() io.Reader { return strings.NewReader(payload) }, attempts: 1},
		{name: "PUT", method: http.MethodPut, attempts: 1},
		{name: "marked POST", method: http.MethodPost, mark: true, body: func() io.Reader { return strings.NewReader(payload) }, attempts: 4},
		{name: "marked POST without body", method: http.MethodPost, mark: true, attempts: 4},
		{
			name: "marked POST without GetBody", method: http.MethodPost, mark: true, attempts: 1,
			body: func() io.Reader { return io.MultiReader(strings.NewReader(payload)) },
		},
		{
			name: "POST rate limit fails fast", method: http.MethodPost, attempts: 1, err: true,
			reply: reply{code: 429, hdr: map[string]string{"Retry-After": "3600"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.reply.code == 0 {
				tt.reply.code = http.StatusServiceUnavailable
			}
			rp, srv := newReplay(t, tt.reply)
			c, _ := fakeRetry(srv.Client().Transport)
			ctx := t.Context()
			if tt.mark {
				ctx = Idempotent(ctx)
			}
			var body io.Reader
			if tt.body != nil {
				body = tt.body()
			}
			req, _ := http.NewRequestWithContext(ctx, tt.method, srv.URL, body)
			resp, err := c.Do(req)
			if tt.err != (err != nil) {
				t.Fatalf("err = %v", err)
			}
			if err == nil {
				resp.Body.Close()
			}
			if len(rp.bodies) != tt.attempts {
				t.Errorf("attempts = %d, want %d", len(rp.bodies), tt.attempts)
			}
			want := ""
			if tt.body != nil {
				want = payload
			}
			for i, b := range rp.bodies {
				if b != want {
					t.Errorf("attempt %d body = %q, want %q", i+1, b, want)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// trackedBody records whether it was read to the end and closed.
type trackedBody struct {
	io.Reader
	mu          sync.Mutex
	eof, closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.mu.Lock()
		b.eof = true
		b.mu.Unlock()
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func TestRetryConnectionErrors(t *testing.T) {
	t.Run("fake errors then success", func(t *testing.T) {
		boom := errors.New("connection reset")
		discarded := &trackedBody{Reader: strings.NewReader("discard me")}
		var calls int
		c, clk := fakeRetry(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				return nil, boom
			case 2:
				return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}, Body: discarded, Request: r}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		}))
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/x", http.NoBody)
		resp, err := c.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("Do = %v, %v", resp, err)
		}
		resp.Body.Close()
		if calls != 3 || len(clk.sleeps) != 2 {
			t.Errorf("calls = %d, sleeps = %v", calls, clk.sleeps)
		}
		if !discarded.eof || !discarded.closed {
			t.Errorf("discarded body drained=%v closed=%v", discarded.eof, discarded.closed)
		}
	})

	t.Run("closed server", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		var calls int
		next := srv.Client().Transport
		c, clk := fakeRetry(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return next.RoundTrip(r)
		}))
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		if _, err := c.Do(req); err == nil {
			t.Fatal("want error")
		}
		if calls != 4 || len(clk.sleeps) != 3 {
			t.Errorf("calls = %d, sleeps = %v", calls, clk.sleeps)
		}
	})

	t.Run("certificate error not retried", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)
		var calls int
		next := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		c, _ := fakeRetry(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return next.RoundTrip(r)
		}))
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		var cert *tls.CertificateVerificationError
		if _, err := c.Do(req); !errors.As(err, &cert) {
			t.Fatalf("err = %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("canceled not retried", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls int
		c, _ := fakeRetry(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			cancel()
			return nil, context.Canceled
		}))
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/", http.NoBody)
		if _, err := c.Do(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("GetBody error", func(t *testing.T) {
		bad := errors.New("no replay")
		c, _ := fakeRetry(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_, _ = io.Copy(io.Discard, r.Body)
			return nil, errors.New("reset")
		}))
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, "https://example.test/", strings.NewReader("x"))
		req = req.WithContext(Idempotent(req.Context()))
		req.GetBody = func() (io.ReadCloser, error) { return nil, bad }
		if _, err := c.Do(req); !errors.Is(err, bad) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestRetryWaitCanceled cancels the context while the transport sleeps (real timer).
func TestRetryWaitCanceled(t *testing.T) {
	for name, rp := range map[string]reply{
		"backoff":    {code: 503},
		"rate limit": {code: 403, hdr: map[string]string{"X-RateLimit-Remaining": "0", "Retry-After": "30"}},
	} {
		t.Run(name, func(t *testing.T) {
			hit := make(chan struct{}, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range rp.hdr {
					w.Header().Set(k, v)
				}
				w.WriteHeader(rp.code)
				hit <- struct{}{}
			}))
			t.Cleanup(srv.Close)
			rt := newRetryTransport(srv.Client().Transport)
			rt.jitter = func(time.Duration) time.Duration { return time.Hour }
			ctx, cancel := context.WithCancel(t.Context())
			go func() {
				<-hit
				cancel()
			}()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
			start := time.Now()
			if _, err := (&http.Client{Transport: rt}).Do(req); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("returned after %v", d)
			}
			if len(hit) != 0 {
				t.Errorf("%d extra attempts", len(hit))
			}
		})
	}
}

func TestRetryLogging(t *testing.T) {
	buf := captureLog(t, slog.LevelDebug)
	_, srv := newReplay(t,
		reply{code: 503},
		reply{code: 429, hdr: map[string]string{"Retry-After": "2", "X-RateLimit-Remaining": "0"}},
		reply{code: 200})
	c, _ := fakeRetry(srv.Client().Transport)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/p", http.NoBody)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	out := buf.String()
	for _, want := range []string{
		`level=DEBUG msg="http retry" method=GET url=` + srv.URL + `/p attempt=1 wait=500ms status=503`,
		`level=INFO msg="rate limited, waiting" host=127.0.0.1 wait=2s until=` + stamp(2500*time.Millisecond),
		"rateLimitRemaining=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestSleepAndJitter(t *testing.T) {
	if err := sleepCtx(t.Context(), 0); err != nil {
		t.Errorf("sleep 0: %v", err)
	}
	if err := sleepCtx(t.Context(), time.Millisecond); err != nil {
		t.Errorf("sleep 1ms: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepCtx(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled sleep 0: %v", err)
	}
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled sleep: %v", err)
	}
	if d := fullJitter(0); d != 0 {
		t.Errorf("fullJitter(0) = %v", d)
	}
	for range 100 {
		if d := fullJitter(time.Second); d < 0 || d > time.Second {
			t.Fatalf("fullJitter(1s) = %v", d)
		}
	}
	rt := newRetryTransport(nil)
	rt.jitter = func(d time.Duration) time.Duration { return d }
	if d := rt.backoff(10); d != backoffCap {
		t.Errorf("backoff(10) = %v, want cap %v", d, backoffCap)
	}
}

func TestRateLimitHook(t *testing.T) {
	_, srv := newReplay(t,
		reply{code: 503},
		reply{code: 429, hdr: map[string]string{"Retry-After": "2"}},
		reply{code: 200})
	c, _ := fakeRetry(srv.Client().Transport)
	type wait struct {
		host  string
		until time.Time
	}
	var got []wait
	ctx := WithRateLimitHook(t.Context(), func(host string, until time.Time) { got = append(got, wait{host, until}) })
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// The 503 backoff is no rate limit; the 429 waits 2s after the 500ms backoff.
	if want := []wait{{"127.0.0.1", clockStart.Add(2500 * time.Millisecond)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("hook calls = %v, want %v", got, want)
	}
}
