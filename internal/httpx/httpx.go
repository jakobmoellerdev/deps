// Package httpx provides the single shared HTTP client and host credentials used by every datasource.
package httpx

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/klauspost/compress/gzhttp"
)

// Version is the deps version advertised in User-Agent headers and git agent capabilities.
// The CLI sets it once at startup, before any request is made.
var Version = "dev"

// UserAgent returns "deps/<Version>".
func UserAgent() string { return "deps/" + Version }

// NewClient returns the HTTP client shared by all packages (HTTP/2, keep-alive, proxy from
// environment). Idempotent requests are retried (see Idempotent) and rate limits waited out
// briefly; every attempt is logged at debug level. RequestTimeout bounds single requests.
// Requests to an HTTPS host wait for its first connection (see firstConnTransport); gzip and
// zstd responses are decompressed with klauspost/compress (see newClient). A redirect to
// another host loses the request's credentials (see checkRedirect).
func NewClient() *http.Client { return newClient(newTransport()) }

// newClient wraps base in the shared transport chain. gzhttp.Transport asks for zstd and gzip
// and decompresses responses itself (net/http's own decompression is off): inflating registry
// documents (npm packuments, PyPI JSON) is a large share of a run's CPU and klauspost's gzip
// decoder is about a fifth faster on them, while zstd decodes several times faster still. No
// public registry deps talks to serves zstd today (they answer gzip, which stays the common
// case); a self-hosted registry, proxy or custom datasource that does gets the faster path
// for free. Both decoders come from pools, so a response costs no window allocation.
// Tests build a client over their own base transport.
func newClient(base *http.Transport) *http.Client {
	return &http.Client{
		CheckRedirect: checkRedirect,
		Transport:     timeoutTransport{newRetryTransport(logTransport{&firstConnTransport{next: gzhttp.Transport(base)}})},
	}
}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Resumed TLS sessions skip the certificate verification (P-384 ECDSA for Maven
		// Central and the Gradle plugin portal) that dominates a full handshake's CPU: the
		// HTTP/1.1 connections after the first one to a host resume its session.
		TLSClientConfig:       &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(0)},
		ForceAttemptHTTP2:     true,
		DisableCompression:    true, // gzhttp.Transport
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// credentialHeaders are the request headers of a datasource that carry a secret the way
// Authorization does: GitLab's CI job token (forge, gitlabpackages) and CircleCI's API token
// (orb). net/http only knows about Authorization, Www-Authenticate, Cookie and Cookie2.
var credentialHeaders = [...]string{"JOB-TOKEN", "Circle-Token"}

// checkRedirect keeps net/http's limit of ten redirects and drops the credential headers a
// datasource set itself once a redirect leaves the host they were meant for, so a registry
// that redirects a download to a CDN or a mirror cannot collect them. Headers a package
// takes from its configuration are its own business (see maven's stripHeadersOnRedirect).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		for _, h := range credentialHeaders {
			req.Header.Del(h)
		}
	}
	return nil
}

// logTransport logs method, URL (without credentials), status, time to first response byte and
// X-RateLimit-Remaining when the server sends it.
type logTransport struct{ next http.RoundTripper }

func (t logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return t.next.RoundTrip(req)
	}
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	attrs := []any{"method", req.Method, "url", req.URL.Redacted(), "duration", time.Since(start)}
	if err != nil {
		slog.DebugContext(ctx, "http request failed", append(attrs, "err", err)...)
		return resp, err
	}
	attrs = append(attrs, "status", resp.StatusCode, "proto", resp.Proto)
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		attrs = append(attrs, "rateLimitRemaining", v)
	}
	slog.DebugContext(ctx, "http request", attrs...)
	return resp, nil
}

// Redact returns raw with the password of its userinfo masked (url.URL.Redacted), for
// errors and logs; unparsable input becomes "<invalid URL>" so it cannot leak a secret.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	return u.Redacted()
}

// DrainBody reads and discards up to 4 KiB of resp's body. Closing a body that was not read
// to EOF (an error page, a stream abandoned after the data it needed) leaves net/http only a
// background drain with a short deadline, which the next request to the host races, so it
// often dials a new connection; draining first makes Close return the connection to the
// pool. Callers close the body themselves, in their own package, so bodyclose sees it.
func DrainBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
}

// ErrNotFound matches (errors.Is) a *StatusError for HTTP 404.
var ErrNotFound = errors.New("not found")

// StatusError is a GET answered with an unexpected status; it reads "GET <URL>: HTTP <Code>",
// followed by ": <Limit>" for a rate limit.
type StatusError struct {
	URL   string // redacted (Redact)
	Code  int
	Limit error // LimitError of the response: wraps ErrRateLimited; nil = no rate limit
}

func (e *StatusError) Error() string {
	s := "GET " + e.URL + ": HTTP " + strconv.Itoa(e.Code)
	if e.Limit != nil {
		s += ": " + e.Limit.Error()
	}
	return s
}

func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Code == http.StatusNotFound
}

// Unwrap returns Limit, so that a rate-limited response matches ErrRateLimited.
func (e *StatusError) Unwrap() error { return e.Limit }
