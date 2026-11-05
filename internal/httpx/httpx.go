// Package httpx provides the single shared HTTP client and host credentials used by every datasource.
package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Version is the deps version advertised in User-Agent headers and git agent capabilities.
// The CLI sets it once at startup, before any request is made.
var Version = "dev"

// UserAgent returns "deps/<Version>".
func UserAgent() string { return "deps/" + Version }

// NewClient returns the HTTP client shared by all packages (HTTP/2, keep-alive, proxy from
// environment). Idempotent requests are retried (see Idempotent) and rate limits waited out
// briefly; every attempt is logged at debug level. RequestTimeout bounds single requests. A
// request whose context carries WithDialCheck connects only to addresses the check allows.
func NewClient() *http.Client {
	return &http.Client{Transport: timeoutTransport{newRetryTransport(logTransport{&http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:        30 * time.Second,
			KeepAlive:      30 * time.Second,
			ControlContext: dialControl,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}})}}
}

type dialCheckKey struct{}

// WithDialCheck returns ctx with check run on the address every connection of a request with
// that context is about to connect to, after name resolution: an error refuses the connection.
// Unlike a check of the resolved name beforehand, it holds when the name resolves differently
// at dial time (DNS rebinding). Through a proxy the address is the proxy's, so callers attach
// a check only to requests that go direct. An idle connection the transport reuses was checked
// when it was dialed, by the check of the request that dialed it, if any.
func WithDialCheck(ctx context.Context, check func(netip.Addr) error) context.Context {
	return context.WithValue(ctx, dialCheckKey{}, check)
}

// dialControl runs the WithDialCheck check of ctx on address.
func dialControl(ctx context.Context, _, address string, _ syscall.RawConn) error {
	check, ok := ctx.Value(dialCheckKey{}).(func(netip.Addr) error)
	if !ok {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	return check(ap.Addr())
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
