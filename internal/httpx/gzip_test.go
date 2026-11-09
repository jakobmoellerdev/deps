package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
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
