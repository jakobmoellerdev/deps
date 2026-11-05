package httpx

import (
	"context"
	"io"
	"net/http"
	"time"
)

type timeoutKey struct{}

// RequestTimeout marks ctx so that each request sent with it ends after d: its attempts,
// retry waits and the response body share one deadline. The clock starts when the client
// sends the request, after the caller got its request slot, so time spent queueing does
// not count. An attempt cut off by it is not retried; the error wraps
// context.DeadlineExceeded.
func RequestTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, timeoutKey{}, d)
}

// timeoutTransport applies RequestTimeout.
type timeoutTransport struct{ next http.RoundTripper }

func (t timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d, ok := req.Context().Value(timeoutKey{}).(time.Duration)
	if !ok {
		return t.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), d)
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelBody{resp.Body, cancel}
	return resp, nil
}

// cancelBody ends the request's RequestTimeout context once the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
