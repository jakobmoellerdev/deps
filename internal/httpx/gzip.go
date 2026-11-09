package httpx

import (
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzip"
)

// gzipTransport asks for gzip and decompresses it the way net/http's transparent compression
// does (its Transport has DisableCompression set), with klauspost/compress instead of
// compress/gzip: inflating registry documents (npm packuments, PyPI JSON) is a large share of
// a run's CPU, and the klauspost decoder is about a fifth faster on them. Like net/http it
// leaves requests alone that set Accept-Encoding or Range, and HEAD requests; a decompressed
// response has no Content-Encoding and Content-Length, ContentLength -1 and Uncompressed set.
type gzipTransport struct{ next http.RoundTripper }

func (t gzipTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodHead || req.Header.Get("Accept-Encoding") != "" || req.Header.Get("Range") != "" {
		return t.next.RoundTrip(req)
	}
	r := req.Clone(req.Context()) // a RoundTripper must not modify the request
	r.Header.Set("Accept-Encoding", "gzip")
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Request = req
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
		resp.Body = &gzipBody{body: resp.Body}
	}
	return resp, nil
}

// gzipBody decompresses body; it reads the gzip header on the first Read, so a broken one
// fails reading the body, not the request.
type gzipBody struct {
	body io.ReadCloser
	zr   *gzip.Reader
	err  error // sticky error of reading the header
}

func (b *gzipBody) Read(p []byte) (int, error) {
	if b.zr == nil {
		if b.err != nil {
			return 0, b.err
		}
		if b.zr, b.err = gzip.NewReader(b.body); b.err != nil {
			return 0, b.err
		}
	}
	return b.zr.Read(p)
}

func (b *gzipBody) Close() error { return b.body.Close() }
