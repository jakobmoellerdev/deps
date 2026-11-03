// Package httpx provides the single shared HTTP client and host credentials used by every datasource.
package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Version is the deps version advertised in User-Agent headers and git agent capabilities.
// The CLI sets it once at startup, before any request is made.
var Version = "dev"

// UserAgent returns "deps/<Version>".
func UserAgent() string { return "deps/" + Version }

// NewClient returns the HTTP client shared by all packages (HTTP/2, keep-alive, proxy from
// environment). Idempotent requests are retried (see Idempotent) and rate limits waited out
// briefly; every attempt is logged at debug level.
func NewClient() *http.Client {
	return &http.Client{Transport: newRetryTransport(logTransport{&http.Transport{
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
	}})}
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
