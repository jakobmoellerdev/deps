package httpx

import (
	"cmp"
	"io"
	"net/http"
	"slices"
	"sync"
)

// HostUse is the number of requests in flight to one host.
type HostUse struct {
	Host string
	N    int
}

// HostCounter counts the requests in flight per host of the clients it wraps, from the
// request until its response body is closed (or the request fails), retries included.
type HostCounter struct {
	mu sync.Mutex
	n  map[string]int
}

// Wrap returns a copy of c whose requests h counts; c is unchanged.
func (h *HostCounter) Wrap(c *http.Client) *http.Client {
	w := *c
	w.Transport = countTransport{h, cmp.Or[http.RoundTripper](c.Transport, http.DefaultTransport)}
	return &w
}

// InFlight returns the hosts with requests in flight, busiest first (ties by name).
func (h *HostCounter) InFlight() []HostUse {
	h.mu.Lock()
	out := make([]HostUse, 0, len(h.n))
	for host, n := range h.n {
		out = append(out, HostUse{host, n})
	}
	h.mu.Unlock()
	slices.SortFunc(out, func(a, b HostUse) int { return cmp.Or(b.N-a.N, cmp.Compare(a.Host, b.Host)) })
	return out
}

func (h *HostCounter) add(host string, d int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n == nil {
		h.n = map[string]int{}
	}
	if h.n[host] += d; h.n[host] <= 0 {
		delete(h.n, host)
	}
}

type countTransport struct {
	h    *HostCounter
	next http.RoundTripper
}

func (t countTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	t.h.add(host, 1)
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.Body == nil {
		t.h.add(host, -1)
		return resp, err
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, done: func() { t.h.add(host, -1) }}
	return resp, nil
}

// countedBody ends the count of its request on the first Close.
type countedBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *countedBody) Close() error {
	b.once.Do(b.done)
	return b.ReadCloser.Close()
}
