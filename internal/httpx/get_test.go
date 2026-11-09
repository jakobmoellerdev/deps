package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func TestGetterStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != UserAgent() {
			t.Errorf("User-Agent = %q", ua)
		}
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()

	sem := make(chan struct{}, 1)
	g := Getter{Client: srv.Client(), Sem: sem}
	var got string
	if err := g.Stream(t.Context(), srv.URL, func(r io.Reader) error {
		if len(sem) != 1 {
			t.Error("read without a semaphore slot")
		}
		b, err := io.ReadAll(r)
		got = string(b)
		return err
	}); err != nil || got != "body" {
		t.Fatalf("Stream = %q, %v", got, err)
	}
	if len(sem) != 0 {
		t.Error("semaphore slot not released")
	}

	g = Getter{Client: srv.Client()}
	fail := errors.New("boom")
	err := g.Stream(t.Context(), srv.URL, func(io.Reader) error { return fail })
	if want := "GET " + srv.URL + ": boom"; err == nil || err.Error() != want || !errors.Is(err, fail) {
		t.Errorf("read error = %v, want %q wrapping it", err, want)
	}
	err = g.Stream(t.Context(), srv.URL, func(io.Reader) error { return errors.New("mirror: HTTP 404") })
	if errors.Is(err, ErrNotFound) {
		t.Errorf("read error %v is ErrNotFound", err)
	}

	noRead := func(io.Reader) error {
		t.Error("read called")
		return nil
	}
	err = g.Stream(t.Context(), srv.URL+"/missing", noRead)
	if want := "GET " + srv.URL + "/missing: HTTP 404"; err == nil || err.Error() != want {
		t.Errorf("status error = %v, want %q", err, want)
	}
	if se, ok := errors.AsType[*StatusError](err); !ok || se.Code != http.StatusNotFound || !errors.Is(err, ErrNotFound) {
		t.Errorf("status error = %#v, want a 404 *StatusError matching ErrNotFound", err)
	}
	if err := g.Stream(t.Context(), "http://[::1]:namedport", noRead); err == nil {
		t.Error("bad URL: want error")
	}
	if err := g.Stream(t.Context(), "http://127.0.0.1:1/", noRead); err == nil {
		t.Error("connect: want error")
	}
	full := make(chan struct{}, 1)
	full <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (Getter{Client: srv.Client(), Sem: full}).Stream(ctx, srv.URL, noRead); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled while waiting for the semaphore = %v", err)
	}
}

func TestGetterPrepare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization")+" "+r.Header.Get("User-Agent"))
	}))
	defer srv.Close()
	sem := make(chan struct{}, 1)
	g := Getter{Client: srv.Client(), Sem: sem, Prepare: func(req *http.Request) error {
		req.Header.Set("Authorization", "Bearer tok")
		return nil
	}}
	var got []byte
	if err := g.Stream(t.Context(), srv.URL, func(r io.Reader) (err error) {
		got, err = io.ReadAll(r)
		return err
	}); err != nil || string(got) != "Bearer tok "+UserAgent() {
		t.Errorf("Stream = %q, %v; want the prepared header", got, err)
	}

	fail := errors.New("no token")
	g.Prepare = func(*http.Request) error { return fail }
	if err := g.Stream(t.Context(), srv.URL, func(io.Reader) error { return nil }); !errors.Is(err, fail) {
		t.Errorf("Prepare error = %v, want %v", err, fail)
	}
	if len(sem) != 0 {
		t.Error("semaphore slot not released after a Prepare error")
	}
}

func TestGetterOpenHoldsSlotUntilClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()
	sem := make(chan struct{}, 1)
	g := Getter{Client: srv.Client(), Sem: sem}
	resp, err := g.Open(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(sem) != 1 {
		t.Error("slot released before the body is closed")
	}
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "body" {
		t.Errorf("body = %q, %v", b, err)
	}
	if len(sem) != 1 {
		t.Error("slot released before the body is closed")
	}
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
	if len(sem) != 0 {
		t.Error("slot not released by Close")
	}
	_ = resp.Body.Close() // a second Close must not free another request's slot
	sem <- struct{}{}
	_ = resp.Body.Close()
	if len(sem) != 1 {
		t.Error("second Close released a slot")
	}
	<-sem

	_, err = g.Open(t.Context(), srv.URL+"/gone")
	if se, ok := errors.AsType[*StatusError](err); !ok || se.Code != http.StatusGone || errors.Is(err, ErrNotFound) {
		t.Errorf("410 = %v, want a 410 *StatusError", err)
	}
	if len(sem) != 0 {
		t.Error("slot not released after a status error")
	}
}

func TestGetterAccept2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNonAuthoritativeInfo)
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()
	read := func(r io.Reader) error { _, err := io.ReadAll(r); return err }
	err := Getter{Client: srv.Client()}.Stream(t.Context(), srv.URL, read)
	if se, ok := errors.AsType[*StatusError](err); !ok || se.Code != http.StatusNonAuthoritativeInfo {
		t.Errorf("203 without Accept2xx = %v, want a *StatusError", err)
	}
	if err := (Getter{Client: srv.Client(), Accept2xx: true}).Stream(t.Context(), srv.URL, read); err != nil {
		t.Errorf("203 with Accept2xx = %v", err)
	}
}

func TestGetterMaxBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 10))
	}))
	defer srv.Close()
	readAll := func(r io.Reader) error { _, err := io.ReadAll(r); return err }
	for _, tc := range []struct {
		name string
		max  int64
		read func(io.Reader) error
		want string // the error, empty for success
	}{
		{"exactly the cap", 10, readAll, ""},
		{"unbounded", -1, readAll, ""},
		{"default cap", 0, readAll, ""},
		{"over the cap", 9, readAll, "GET " + srv.URL + ": response larger than 9 bytes"},
		{"one byte at a time", 9, func(r io.Reader) error { _, err := io.ReadAll(iotest.OneByteReader(r)); return err }, "GET " + srv.URL + ": response larger than 9 bytes"},
		{"read error masked", 4, func(r io.Reader) error {
			if _, err := io.ReadAll(r); err != nil {
				return errors.New("unexpected end of JSON input")
			}
			return nil
		}, "GET " + srv.URL + ": response larger than 4 bytes"},
		{"read ignores the error", 4, func(r io.Reader) error { _, _ = io.ReadAll(r); return nil }, "GET " + srv.URL + ": response larger than 4 bytes"},
		{"read stops before the cap", 4, func(r io.Reader) error { _, err := io.ReadFull(r, make([]byte, 4)); return err }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Getter{Client: srv.Client(), MaxBody: tc.max}.Stream(t.Context(), srv.URL, tc.read)
			if got := errString(err); got != tc.want {
				t.Errorf("Stream = %q, want %q", got, tc.want)
			}
		})
	}

	resp, err := Getter{Client: srv.Client(), MaxBody: 5}.Open(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if string(b) != "xxxxx" || errString(err) != "response larger than 5 bytes" {
		t.Errorf("Open body = %q, %v; want the first 5 bytes, then the cap error", b, err)
	}
	if _, err := resp.Body.Read(make([]byte, 1)); errString(err) != "response larger than 5 bytes" {
		t.Errorf("Read after the cap = %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestGetterRedactsCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()
	cred := strings.Replace(srv.URL, "://", "://u:secretpw@", 1)
	g := Getter{Client: srv.Client(), MaxBody: 1}
	errs := []error{
		g.Stream(t.Context(), cred+"/missing", func(io.Reader) error { return nil }),
		g.Stream(t.Context(), cred+"/ok", func(io.Reader) error { return errors.New("bad") }),
		g.Stream(t.Context(), cred+"/ok", func(r io.Reader) error { _, err := io.ReadAll(r); return err }),
		g.Stream(t.Context(), "http://u:secretpw@[::1]:namedport/", func(io.Reader) error { return nil }),
		g.Stream(t.Context(), "http://u:secretpw@127.0.0.1:1/", func(io.Reader) error { return nil }),
	}
	for _, err := range errs {
		if err == nil || strings.Contains(err.Error(), "secretpw") {
			t.Errorf("err = %v, want error without password", err)
		}
	}
}

// An error page or a stream read only up to the data it needs must leave the keep-alive
// connection reusable, so repeated lookups against one host do not dial again each time.
func TestGetterReusesConnection(t *testing.T) {
	var dials atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
		}
		_, _ = io.WriteString(w, `[{"a":"x"}]`)
		w.(http.Flusher).Flush()
		// The end of the chunked body comes later than net/http waits for when it drains a
		// body closed unread in the background, so only a drain before Close keeps the
		// connection.
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, "\n")
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dials.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	g := Getter{Client: srv.Client()}
	const n = 3
	for range n {
		if err := g.Stream(t.Context(), srv.URL+"/missing", func(io.Reader) error { return nil }); err == nil {
			t.Fatal("404: want error")
		}
	}
	for range n {
		if err := g.Stream(t.Context(), srv.URL, func(r io.Reader) error {
			_, err := io.ReadFull(r, make([]byte, len(`[{"a":"x"}]`)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("%d connections for %d requests, want 1", got, 2*n)
	}
}
