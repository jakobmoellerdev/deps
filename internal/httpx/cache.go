package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Cache is a shared store of registry metadata responses. `deps serve` backs it with the
// Valkey every replica talks to: without it a fleet scanning 100k repositories makes some
// 350 upstream requests per second, and several registries (Docker Hub's 100 manifest reads
// per hour and IP) would refuse long before that. A nil Cache leaves NewClient uncached,
// which is what the CLI uses.
type Cache interface {
	// Get returns the stored entry; ok is false on a miss and on any failure, so a cache
	// outage degrades to misses, never to errors.
	Get(ctx context.Context, key string) (entry []byte, ok bool)
	Put(ctx context.Context, key string, entry []byte, ttl time.Duration)
}

// CacheOutcome is how a request was served, for the metric.
type CacheOutcome string

const (
	CacheHit         CacheOutcome = "hit"
	CacheMiss        CacheOutcome = "miss"
	CacheRevalidated CacheOutcome = "revalidated"
	CacheUncacheable CacheOutcome = "uncacheable"
)

// NewCachingClient is NewClient with c in front of the retries: a hit costs no retry
// bookkeeping and no rate-limit accounting, and a miss behaves exactly as an uncached
// request. scope separates tenants: it is part of every key, so two deployments with
// different private-registry credentials cannot read each other's entries. It must name the
// credential (its host and user name), never the secret. observe may be nil.
func NewCachingClient(c Cache, scope string, observe func(host string, outcome CacheOutcome)) *http.Client {
	client := newClient(newTransport())
	client.Transport = &cacheTransport{next: client.Transport, cache: c, scope: scope, observe: observe}
	return client
}

// maxCacheBody is the largest body that is stored. Larger documents stream through uncached,
// so the Helm index.yaml scanner (tens of megabytes, read as a stream) is unaffected.
const maxCacheBody = 1 << 20

// cacheTTL classes. Artifacts addressed by an immutable coordinate never change; listings do
// and are refreshed often enough that a cycle sees a release the same day.
const (
	ttlImmutable = 30 * 24 * time.Hour
	ttlListing   = 10 * time.Minute
	ttlAdvisory  = 24 * time.Hour
)

// cacheTransport serves cacheable GETs from the Cache.
type cacheTransport struct {
	next    http.RoundTripper
	cache   Cache
	scope   string
	observe func(host string, outcome CacheOutcome)
}

func (t *cacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ttl := cacheTTL(req)
	if t.cache == nil || ttl == 0 {
		t.note(req, CacheUncacheable)
		return t.next.RoundTrip(req)
	}
	ctx := req.Context()
	key := t.key(req)
	if raw, ok := t.cache.Get(ctx, key); ok {
		if e, err := decodeEntry(raw); err == nil {
			if time.Since(e.stored) < ttl {
				t.note(req, CacheHit)
				return e.response(req), nil
			}
			if e.etag != "" || e.lastModified != "" {
				return t.revalidate(req, key, e, ttl)
			}
		}
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	t.note(req, CacheMiss)
	t.store(ctx, key, resp, ttl)
	return resp, nil
}

// revalidate asks the server whether the stored entry is still current; a 304 refreshes it
// without transferring the body again.
func (t *cacheTransport) revalidate(req *http.Request, key string, e entry, ttl time.Duration) (*http.Response, error) {
	cond := req.Clone(req.Context())
	if e.etag != "" {
		cond.Header.Set("If-None-Match", e.etag)
	}
	if e.lastModified != "" {
		cond.Header.Set("If-Modified-Since", e.lastModified)
	}
	resp, err := t.next.RoundTrip(cond)
	if err != nil {
		return resp, err
	}
	if resp.StatusCode == http.StatusNotModified {
		DrainBody(resp)
		_ = resp.Body.Close()
		e.stored = time.Now()
		t.cache.Put(req.Context(), key, e.encode(), ttl)
		t.note(req, CacheRevalidated)
		return e.response(req), nil
	}
	t.note(req, CacheMiss)
	t.store(req.Context(), key, resp, ttl)
	return resp, nil
}

func (t *cacheTransport) note(req *http.Request, o CacheOutcome) {
	if t.observe != nil {
		t.observe(req.URL.Host, o)
	}
}

// store reads the response body, keeps a copy in the cache and hands the caller an
// equivalent body. A body over maxCacheBody is streamed through untouched.
func (t *cacheTransport) store(ctx context.Context, key string, resp *http.Response, ttl time.Duration) {
	if !cacheableStatus(resp.StatusCode) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCacheBody+1))
	if err != nil {
		// The caller still needs a readable body; give it what was read plus the rest.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return
	}
	if len(body) > maxCacheBody {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	e := entry{
		status:       resp.StatusCode,
		stored:       time.Now(),
		etag:         resp.Header.Get("ETag"),
		lastModified: resp.Header.Get("Last-Modified"),
		headers:      retainedHeaders(resp.Header),
		body:         body,
	}
	t.cache.Put(ctx, key, e.encode(), ttl)
}

func cacheableStatus(code int) bool {
	// 404 and 410 are cached on purpose: the Go major-path probes are mostly misses, and one
	// of them used to cost a ten second timeout on every run.
	return code == http.StatusOK || code == http.StatusNotFound || code == http.StatusGone
}

// retainedHeaders are the response headers deps' callers read.
var retainedHeaders = func(h http.Header) map[string]string {
	keep := []string{"ETag", "Last-Modified", "Content-Type", "Docker-Content-Digest", "Content-Disposition"}
	out := map[string]string{}
	for _, k := range keep {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	for k, v := range h {
		if strings.HasPrefix(k, "X-Checksum-") && len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// key is the cache key of a request: method, URL, the Accept header (registries serve
// different documents for different Accept values) and the tenant scope.
func (t *cacheTransport) key(req *http.Request) string {
	h := sha256.New()
	for _, part := range []string{req.Method, req.URL.String(), req.Header.Get("Accept"), t.scope} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cacheTTL classifies a request; 0 means it is not cached at all. Forge API calls, OSV batch
// queries and every non-GET are never cached: their answers are the state deps acts on.
func cacheTTL(req *http.Request) time.Duration {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return 0
	}
	u := req.URL
	p := u.Path
	switch {
	case forgeAPI(u):
		return 0
	case strings.Contains(p, "/@v/") && (strings.HasSuffix(p, ".info") || strings.HasSuffix(p, ".mod") || strings.HasSuffix(p, ".zip")):
		return ttlImmutable
	case strings.Contains(p, "/@v/list"), strings.HasSuffix(p, "/@latest"):
		return ttlListing
	case strings.Contains(p, "/blobs/sha256:"), strings.Contains(p, "/manifests/sha256:"):
		return ttlImmutable
	case strings.Contains(p, "/manifests/"), strings.HasSuffix(p, "/tags/list"):
		return ttlListing
	case strings.HasSuffix(p, "maven-metadata.xml"), strings.HasSuffix(p, "index.yaml"):
		return ttlListing
	case strings.Contains(p, "/vulns/"):
		return ttlAdvisory
	case strings.HasSuffix(p, ".jar"), strings.HasSuffix(p, ".pom"), strings.HasSuffix(p, ".tgz"),
		strings.HasSuffix(p, ".whl"), strings.HasSuffix(p, ".crate"):
		return ttlImmutable
	}
	return ttlListing
}

// forgeAPI reports whether the request reads or writes forge state, which is never cached.
func forgeAPI(u *url.URL) bool {
	p := u.Path
	switch {
	case strings.Contains(p, "/pulls"), strings.Contains(p, "/merge_requests"),
		strings.Contains(p, "/issues"), strings.HasSuffix(p, "/graphql"),
		strings.Contains(p, "/statuses/"), strings.Contains(p, "/check-runs"),
		strings.Contains(p, "/querybatch"), strings.Contains(p, "/installation"):
		return true
	}
	return false
}

// entry is a stored response.
type entry struct {
	status       int
	stored       time.Time
	etag         string
	lastModified string
	headers      map[string]string
	body         []byte
}

var (
	// EncodeAll keeps one history buffer per concurrent call, sized by the window, for the life
	// of the encoder: at GOMAXPROCS of them that is hundreds of megabytes the daemon never gives
	// back (184 MB, 86 % of the live heap, measured over one pass of an organization). Bodies
	// here are at most maxCacheBody, so a 128 KiB window leaves the ratio alone (0.1435 against
	// 0.1434 on a 200 KB registry document) and four encoder states are plenty.
	zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithWindowSize(1<<17), zstd.WithEncoderConcurrency(4))
	zstdDec, _ = zstd.NewReader(nil)
)

// encode renders the entry: a small header, the retained headers, then the zstd-compressed
// body. Registry documents are JSON and XML, which compress to a fraction of their size, so
// the cache holds many more of them per gigabyte.
func (e entry) encode() []byte {
	var b bytes.Buffer
	b.WriteByte(1) // format version
	var num [8]byte
	binary.BigEndian.PutUint16(num[:2], uint16(e.status)) //nolint:gosec // HTTP status codes fit
	b.Write(num[:2])
	binary.BigEndian.PutUint64(num[:], uint64(e.stored.UnixMilli())) //nolint:gosec // time after 1970
	b.Write(num[:])
	writeString(&b, e.etag)
	writeString(&b, e.lastModified)
	binary.BigEndian.PutUint16(num[:2], uint16(len(e.headers))) //nolint:gosec // a handful of headers
	b.Write(num[:2])
	for k, v := range e.headers {
		writeString(&b, k)
		writeString(&b, v)
	}
	b.Write(zstdEnc.EncodeAll(e.body, nil))
	return b.Bytes()
}

func writeString(b *bytes.Buffer, s string) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s))) //nolint:gosec // header values are short
	b.Write(n[:])
	b.WriteString(s)
}

var errEntry = errors.New("malformed cache entry")

func decodeEntry(raw []byte) (entry, error) {
	r := bytes.NewReader(raw)
	version, err := r.ReadByte()
	if err != nil || version != 1 {
		return entry{}, errEntry
	}
	var e entry
	var num [8]byte
	if _, err := io.ReadFull(r, num[:2]); err != nil {
		return entry{}, errEntry
	}
	e.status = int(binary.BigEndian.Uint16(num[:2]))
	if _, err := io.ReadFull(r, num[:]); err != nil {
		return entry{}, errEntry
	}
	e.stored = time.UnixMilli(int64(binary.BigEndian.Uint64(num[:]))) //nolint:gosec // written from a time
	if e.etag, err = readString(r); err != nil {
		return entry{}, errEntry
	}
	if e.lastModified, err = readString(r); err != nil {
		return entry{}, errEntry
	}
	if _, err := io.ReadFull(r, num[:2]); err != nil {
		return entry{}, errEntry
	}
	n := int(binary.BigEndian.Uint16(num[:2]))
	e.headers = make(map[string]string, n)
	for range n {
		k, err := readString(r)
		if err != nil {
			return entry{}, errEntry
		}
		v, err := readString(r)
		if err != nil {
			return entry{}, errEntry
		}
		e.headers[k] = v
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		return entry{}, errEntry
	}
	if e.body, err = zstdDec.DecodeAll(rest, nil); err != nil {
		return entry{}, errEntry
	}
	return e, nil
}

func readString(r *bytes.Reader) (string, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", err
	}
	size := int(binary.BigEndian.Uint32(n[:]))
	if size > r.Len() {
		return "", errEntry
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// response builds the http.Response a stored entry stands for.
func (e entry) response(req *http.Request) *http.Response {
	h := make(http.Header, len(e.headers))
	for k, v := range e.headers {
		h.Set(k, v)
	}
	return &http.Response{
		Status:        strconv.Itoa(e.status) + " " + http.StatusText(e.status),
		StatusCode:    e.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// LogCacheError reports a cache failure at debug level; it is never fatal.
func LogCacheError(ctx context.Context, op string, err error) {
	slog.DebugContext(ctx, "metadata cache", "op", op, "err", err)
}
