package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestTimeout(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		switch r.URL.Path {
		case "/hang": // no response headers until the client gives up
			<-r.Context().Done()
		case "/slow-body": // headers at once, then the body never ends
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "v1.0.0\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/flaky": // a retried 503, then success
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.Transport.(timeoutTransport).next.(*retryTransport).sleep = func(context.Context, time.Duration) error { return nil }
	const budget = 200 * time.Millisecond
	get := func(path string) (string, error) {
		req, err := http.NewRequestWithContext(RequestTimeout(t.Context(), budget), http.MethodGet, srv.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	for _, path := range []string{"/hang", "/slow-body"} {
		t.Run(path, func(t *testing.T) {
			hits.Store(0)
			start := time.Now()
			_, err := get(path)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want a deadline error", err)
			}
			if d := time.Since(start); d > 5*budget {
				t.Errorf("returned after %v, budget %v", d, budget)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("%d attempts, want 1 (a timed-out attempt is not retried)", n)
			}
		})
	}

	t.Run("retries within the budget", func(t *testing.T) {
		hits.Store(0)
		if body, err := get("/flaky"); err != nil || body != "ok" || hits.Load() != 2 {
			t.Errorf("get = %q, %v after %d attempts", body, err, hits.Load())
		}
	})

	t.Run("queueing does not count", func(t *testing.T) {
		hits.Store(0)
		ctx := RequestTimeout(t.Context(), budget)
		time.Sleep(2 * budget) // e.g. waiting for a request slot
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/flaky", http.NoBody)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})
}
