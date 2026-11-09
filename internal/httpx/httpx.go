// Package httpx provides the single shared HTTP client and host credentials used by every datasource.
package httpx

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Version is the deps version advertised in User-Agent headers and git agent capabilities.
// The CLI sets it once at startup, before any request is made.
var Version = "dev"

// UserAgent returns "deps/<Version>".
func UserAgent() string { return "deps/" + Version }

// NewClient returns the HTTP client shared by all packages (HTTP/2, keep-alive, proxy from
// environment). Idempotent requests are retried (see Idempotent) and rate limits waited out
// briefly; every attempt is logged at debug level. RequestTimeout bounds single requests.
// Requests to an HTTPS host wait for its first connection (see firstConnTransport).
func NewClient() *http.Client {
	return &http.Client{Transport: timeoutTransport{newRetryTransport(logTransport{&firstConnTransport{next: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}})}}
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

// StatusError is a GET answered with an unexpected status; it reads "GET <URL>: HTTP <Code>".
type StatusError struct {
	URL  string // redacted (Redact)
	Code int
}

func (e *StatusError) Error() string { return "GET " + e.URL + ": HTTP " + strconv.Itoa(e.Code) }

func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Code == http.StatusNotFound
}
