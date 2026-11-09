package httpx

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// burst sends n concurrent GETs to url through rt and returns the first error.
func burst(t *testing.T, rt http.RoundTripper, url string, n int) error {
	t.Helper()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
			if err != nil {
				errs <- err
				return
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				errs <- err
				return
			}
			_ = resp.Body.Close()
		})
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func TestFirstConnTransport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
		max   int64 // connections the burst may open
	}{
		{"HTTP/2 burst shares one connection", true, 1},
		{"HTTP/1.1 burst still dials in parallel", false, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var conns atomic.Int64
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			srv.EnableHTTP2 = tc.http2
			srv.Config.ErrorLog = log.New(io.Discard, "", 0) // dials the client closes unused
			srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			srv.StartTLS()
			t.Cleanup(srv.Close)
			tr := &http.Transport{TLSClientConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig, ForceAttemptHTTP2: true}
			t.Cleanup(tr.CloseIdleConnections)

			if err := burst(t, &firstConnTransport{next: tr}, srv.URL, 32); err != nil {
				t.Fatal(err)
			}
			if n := conns.Load(); n < 1 || n > tc.max {
				t.Errorf("burst of 32 requests opened %d connections, want 1..%d", n, tc.max)
			}
		})
	}
}

func TestFirstConnTransportWaiting(t *testing.T) {
	ok := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
	// next holds a request to /slow until unblock is closed and fails /fail.
	unblock := make(chan struct{})
	entered := make(chan struct{}, 1)
	var sent atomic.Int64
	ft := &firstConnTransport{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent.Add(1)
		switch r.URL.Path {
		case "/slow":
			entered <- struct{}{}
			<-unblock
		case "/fail":
			return nil, errors.New("dial failed")
		}
		return ok, nil
	})}
	get := func(ctx context.Context, url string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			return err
		}
		_, err = ft.RoundTrip(req)
		return err
	}

	// The first request of a host is connecting: the next one waits for it, until its
	// context ends.
	first := make(chan error, 1)
	go func() { first <- get(t.Context(), "https://a.example/slow") }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := get(ctx, "https://a.example/x"); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting request with a canceled context = %v, want context.Canceled", err)
	}
	// Plain HTTP and other hosts don't wait.
	if err := get(t.Context(), "http://a.example/x"); err != nil {
		t.Errorf("plain HTTP request = %v", err)
	}
	if err := get(t.Context(), "https://b.example/x"); err != nil {
		t.Errorf("request to another host = %v", err)
	}
	close(unblock)
	if err := <-first; err != nil {
		t.Errorf("first request = %v", err)
	}
	if err := get(t.Context(), "https://a.example/x"); err != nil {
		t.Errorf("request after the first one = %v", err)
	}

	// A first request that fails releases the waiting ones too.
	if err := get(t.Context(), "https://c.example/fail"); err == nil {
		t.Error("failing first request succeeded")
	}
	if err := get(t.Context(), "https://c.example/x"); err != nil {
		t.Errorf("request after a failed first one = %v", err)
	}
	if n := sent.Load(); n != 6 {
		t.Errorf("next got %d requests, want 6 (all but the canceled one)", n)
	}
}
