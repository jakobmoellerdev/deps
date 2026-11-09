package httpx

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGzipTransport(t *testing.T) {
	const doc = `{"name":"left-pad","versions":{"1.3.0":{}}}`
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte(doc))
	_ = zw.Close()

	// The server answers /gzip compressed when asked to, /broken with a body that is not
	// gzip and /empty with no body, both labeled gzip, and everything else uncompressed.
	var gotEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		switch {
		case r.URL.Path == "/broken":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte(doc))
		case r.URL.Path == "/empty":
			w.Header().Set("Content-Encoding", "gzip")
		case r.URL.Path == "/gzip" && gotEncoding == "gzip":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(zipped.Len()))
			_, _ = w.Write(zipped.Bytes())
		default:
			_, _ = w.Write([]byte(doc))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient()

	for _, tc := range []struct {
		name, method, path string
		header             http.Header
		wantEncoding       string // Accept-Encoding the server saw
		wantBody           string
		wantErr            string // of reading the body
		uncompressed       bool
	}{
		{name: "gzip", path: "/gzip", wantEncoding: "gzip", wantBody: doc, uncompressed: true},
		{name: "server ignores it", path: "/plain", wantEncoding: "gzip", wantBody: doc},
		{name: "caller's Accept-Encoding", path: "/gzip", header: http.Header{"Accept-Encoding": {"gzip"}}, wantEncoding: "gzip", wantBody: zipped.String()},
		{name: "range", path: "/gzip", header: http.Header{"Range": {"bytes=0-"}}, wantBody: doc},
		{name: "head", method: http.MethodHead, path: "/gzip"},
		{name: "broken", path: "/broken", wantEncoding: "gzip", wantErr: "gzip: invalid header", uncompressed: true},
		{name: "empty", path: "/empty", wantEncoding: "gzip", uncompressed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+tc.path, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			maps.Copy(req.Header, tc.header)
			sent := req.Header.Clone()
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("reading the body = %v, want %s", err, tc.wantErr)
				}
				if _, again := resp.Body.Read(make([]byte, 1)); again == nil || again.Error() != tc.wantErr {
					t.Errorf("reading the body again = %v, want %s", again, tc.wantErr)
				}
			} else if err != nil || string(body) != tc.wantBody {
				t.Fatalf("body = %q, %v; want %q", body, err, tc.wantBody)
			}
			if gotEncoding != tc.wantEncoding {
				t.Errorf("server saw Accept-Encoding %q, want %q", gotEncoding, tc.wantEncoding)
			}
			if resp.Uncompressed != tc.uncompressed {
				t.Errorf("Uncompressed = %v, want %v", resp.Uncompressed, tc.uncompressed)
			}
			if tc.uncompressed && (resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Length") != "" || resp.ContentLength != -1) {
				t.Errorf("decompressed response has Content-Encoding %q, Content-Length %q, ContentLength %d",
					resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Length"), resp.ContentLength)
			}
			if resp.Request != req {
				t.Error("resp.Request is not the caller's request")
			}
			if len(req.Header) != len(sent) || req.Header.Get("Accept-Encoding") != sent.Get("Accept-Encoding") {
				t.Errorf("request headers changed to %v", req.Header)
			}
		})
	}
}

// gzipped compresses doc with the standard library, as a registry would.
func gzipped(tb testing.TB, doc []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(doc); err != nil {
		tb.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// closeBody is a body that fails reads once closed, like a response body.
type closeBody struct {
	io.Reader
	closed atomic.Bool
}

var errBodyClosed = errors.New("read on closed body")

func (c *closeBody) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, errBodyClosed
	}
	return c.Reader.Read(p)
}

func (c *closeBody) Close() error { c.closed.Store(true); return nil }

// TestGzipBodyReuse reads bodies one after another and interleaved, so pooled readers are
// reset onto new bodies, and checks a body reads its closed body's error after Close.
func TestGzipBodyReuse(t *testing.T) {
	docs := [][]byte{[]byte(`{"a":1}`), bytes.Repeat([]byte(`{"name":"x","versions":{}}`), 5000), []byte(`[]`)}
	for range 3 {
		bodies := make([]*gzipBody, len(docs))
		for i, doc := range docs {
			bodies[i] = &gzipBody{body: &closeBody{Reader: bytes.NewReader(gzipped(t, doc))}}
		}
		for i, b := range bodies {
			got, err := io.ReadAll(b)
			if err != nil || !bytes.Equal(got, docs[i]) {
				t.Fatalf("body %d = %d bytes, %v; want %d bytes", i, len(got), err, len(docs[i]))
			}
		}
		for _, b := range bodies {
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			if err := b.Close(); err != nil {
				t.Fatalf("second Close = %v", err)
			}
			if _, err := b.Read(make([]byte, 1)); !errors.Is(err, errBodyClosed) {
				t.Fatalf("Read after Close = %v, want %v", err, errBodyClosed)
			}
		}
	}
}

// TestGzipBodyConcurrentClose closes bodies while another goroutine reads them; under
// -race this catches a reader going back to the pool while a Read still uses it.
func TestGzipBodyConcurrentClose(t *testing.T) {
	zipped := gzipped(t, bytes.Repeat([]byte(`{"name":"x","versions":{}}`), 20000))
	var wg sync.WaitGroup
	for range 20 {
		b := &gzipBody{body: &closeBody{Reader: bytes.NewReader(zipped)}}
		wg.Go(func() { _, _ = io.Copy(io.Discard, b) })
		wg.Go(func() { _ = b.Close() })
	}
	wg.Wait()
}

func BenchmarkGzipBody(b *testing.B) {
	doc, err := os.ReadFile("../datasource/npm/testdata/lodash.json")
	if err != nil {
		b.Fatal(err)
	}
	zipped := gzipped(b, doc)
	b.ReportAllocs()
	for b.Loop() {
		body := &gzipBody{body: io.NopCloser(bytes.NewReader(zipped))}
		if _, err := io.Copy(io.Discard, body); err != nil {
			b.Fatal(err)
		}
		_ = body.Close()
	}
}
