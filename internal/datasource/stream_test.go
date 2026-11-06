package datasource

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

func TestGetStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != httpx.UserAgent() {
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
	var got string
	if err := GetStream(t.Context(), srv.Client(), sem, srv.URL, func(r io.Reader) error {
		if len(sem) != 1 {
			t.Error("read without a semaphore slot")
		}
		b, err := io.ReadAll(r)
		got = string(b)
		return err
	}); err != nil || got != "body" {
		t.Fatalf("GetStream = %q, %v", got, err)
	}
	if len(sem) != 0 {
		t.Error("semaphore slot not released")
	}

	fail := errors.New("boom")
	err := GetStream(t.Context(), srv.Client(), nil, srv.URL, func(io.Reader) error { return fail })
	if want := "GET " + srv.URL + ": boom"; err == nil || err.Error() != want || !errors.Is(err, fail) {
		t.Errorf("read error = %v, want %q wrapping it", err, want)
	}
	err = GetStream(t.Context(), srv.Client(), nil, srv.URL, func(io.Reader) error { return errors.New("mirror: HTTP 404") })
	if errors.Is(err, httpx.ErrNotFound) {
		t.Errorf("read error %v is httpx.ErrNotFound", err)
	}

	noRead := func(io.Reader) error {
		t.Error("read called")
		return nil
	}
	err = GetStream(t.Context(), srv.Client(), nil, srv.URL+"/missing", noRead)
	if want := "GET " + srv.URL + "/missing: HTTP 404"; err == nil || err.Error() != want {
		t.Errorf("status error = %v, want %q", err, want)
	}
	if se, ok := errors.AsType[*httpx.StatusError](err); !ok || se.Code != http.StatusNotFound || !errors.Is(err, httpx.ErrNotFound) {
		t.Errorf("status error = %#v, want a 404 *httpx.StatusError matching httpx.ErrNotFound", err)
	}
	if err := GetStream(t.Context(), srv.Client(), nil, "http://[::1]:namedport", noRead); err == nil {
		t.Error("bad URL: want error")
	}
	if err := GetStream(t.Context(), srv.Client(), nil, "http://127.0.0.1:1/", noRead); err == nil {
		t.Error("connect: want error")
	}
	full := make(chan struct{}, 1)
	full <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := GetStream(ctx, srv.Client(), full, srv.URL, noRead); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled while waiting for the semaphore = %v", err)
	}
}

func TestDecodeArray(t *testing.T) {
	type elem struct {
		A string `json:"a"`
		B bool   `json:"b"`
	}
	for _, tc := range []struct {
		name, body string
		want       []elem
		err        string // wanted substring of the error, empty for success
		notArray   bool
	}{
		// Every element starts from a zero T: b of the first must not leak into the second.
		{name: "elements", body: `[{"a":"x","b":true,"files":[1,2]},{"a":"y"}] trailing`, want: []elem{{"x", true}, {"y", false}}},
		{name: "empty", body: `[]`},
		{name: "object", body: `{"a":"x"}`, err: "not an array", notArray: true},
		{name: "not json", body: `<html>`, err: "invalid character", notArray: true},
		{name: "empty body", body: ``, err: "EOF", notArray: true},
		{name: "bad element", body: `[{"a":1}]`, err: "JSON number"},
		{name: "truncated", body: `[{"a":"x"},`, err: "EOF", want: []elem{{"x", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []elem
			err := DecodeArray(strings.NewReader(tc.body), func(e elem) { got = append(got, e) })
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v, want %q", err, tc.err)
			}
			if errors.Is(err, ErrNotArray) != tc.notArray {
				t.Errorf("errors.Is(%v, ErrNotArray) = %t", err, !tc.notArray)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("elements = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetStreamRedactsCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()
	cred := strings.Replace(srv.URL, "://", "://u:secretpw@", 1)
	errs := []error{
		GetStream(t.Context(), srv.Client(), nil, cred+"/missing", func(io.Reader) error { return nil }),
		GetStream(t.Context(), srv.Client(), nil, cred+"/ok", func(io.Reader) error { return errors.New("bad") }),
	}
	for _, err := range errs {
		if err == nil || strings.Contains(err.Error(), "secretpw") {
			t.Errorf("err = %v, want error without password", err)
		}
	}
}

// An error page or a stream read only up to its closing ']' must leave the keep-alive
// connection reusable, so repeated lookups against one host do not dial again each time.
func TestGetStreamReusesConnection(t *testing.T) {
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

	const n = 3
	for range n {
		if err := GetStream(t.Context(), srv.Client(), nil, srv.URL+"/missing", func(io.Reader) error { return nil }); err == nil {
			t.Fatal("404: want error")
		}
	}
	for range n {
		if err := GetStream(t.Context(), srv.Client(), nil, srv.URL, func(r io.Reader) error {
			return DecodeArray(r, func(struct{}) {})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("%d connections for %d requests, want 1", got, 2*n)
	}
}
