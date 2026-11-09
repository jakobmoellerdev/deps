package httpx

import (
	"io"
	"net/http"
	"strings"
	"sync"

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

// gzipReaders holds idle gzip readers. A reader carries a 32 KiB inflate window and Huffman
// tables; resetting a pooled one instead of allocating them per response is most of what
// decompressing a small registry document allocates.
var gzipReaders sync.Pool

// gzipBody decompresses body; it reads the gzip header on the first Read, so a broken one
// fails reading the body, not the request. Its reader comes from gzipReaders on the first
// Read and goes back on Close. mu keeps Close from returning the reader while a Read on
// another goroutine still uses it; Close closes body before taking mu, so it still aborts
// a Read that is blocked on the network.
type gzipBody struct {
	body   io.ReadCloser
	mu     sync.Mutex
	zr     *gzip.Reader
	err    error // sticky error of reading the header
	closed bool
}

func (b *gzipBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return b.body.Read(p) // the closed body's own error, as before pooling
	}
	if b.zr == nil {
		if b.err != nil {
			return 0, b.err
		}
		zr, _ := gzipReaders.Get().(*gzip.Reader)
		if zr == nil {
			zr = new(gzip.Reader)
		}
		if b.err = zr.Reset(b.body); b.err != nil {
			gzipReaders.Put(zr)
			return 0, b.err
		}
		b.zr = zr
	}
	return b.zr.Read(p)
}

func (b *gzipBody) Close() error {
	err := b.body.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.zr != nil {
		gzipReaders.Put(b.zr)
		b.zr = nil
	}
	return err
}
