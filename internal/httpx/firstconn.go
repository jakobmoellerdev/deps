package httpx

import (
	"net/http"
	"net/http/httptrace"
	"sync"
)

// firstConnTransport holds back the requests to an HTTPS host while the first connection to
// it is being set up. net/http only learns that a host speaks HTTP/2 once a TLS handshake
// negotiated it, so a burst of requests to a new host (a full npm or Maven pool of them)
// dials and verifies one connection per request, then multiplexes over one of them: on
// backstage 112 TLS handshakes for 9 hosts. Released once the first request got its
// connection, the others share it when it is HTTP/2, or dial their own (as before, one
// handshake later) when it is HTTP/1.1.
type firstConnTransport struct {
	next http.RoundTripper

	mu    sync.Mutex
	hosts map[string]chan struct{} // closed once the host's first request has a connection
}

func (t *firstConnTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return t.next.RoundTrip(req)
	}
	key := req.URL.Host
	t.mu.Lock()
	ready, seen := t.hosts[key]
	if !seen {
		if t.hosts == nil {
			t.hosts = make(map[string]chan struct{})
		}
		ready = make(chan struct{})
		t.hosts[key] = ready
	}
	t.mu.Unlock()
	if seen {
		select {
		case <-ready:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return t.next.RoundTrip(req)
	}
	release := sync.OnceFunc(func() { close(ready) })
	defer release() // the request failed before it got a connection
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { release() }}
	return t.next.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
}
