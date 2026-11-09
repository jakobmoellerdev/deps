package httpx

import (
	"cmp"
	"io"
	"net/http"
	"slices"
	"sync"
)

// HostUse is the request count of one host: Open requests in flight, Done finished ones
// (read to the end, or failed). Open+Done were sent.
type HostUse struct {
	Host       string
	Open, Done int
}

// HostCounter counts the requests per host of the clients it wraps: a request is open from
// the request until its response body is closed (or the request fails), retries included,
// and done afterwards.
type HostCounter struct {
	mu sync.Mutex
	n  map[string]*HostUse
}

// Wrap returns a copy of c whose requests h counts; c is unchanged.
func (h *HostCounter) Wrap(c *http.Client) *http.Client {
	w := *c
	w.Transport = countTransport{h, cmp.Or[http.RoundTripper](c.Transport, http.DefaultTransport)}
	return &w
}

// Hosts returns every host requests were sent to, those with open requests first, busiest
// first (ties by name).
func (h *HostCounter) Hosts() []HostUse {
	h.mu.Lock()
	out := make([]HostUse, 0, len(h.n))
	for _, u := range h.n {
		out = append(out, *u)
	}
	h.mu.Unlock()
	slices.SortFunc(out, func(a, b HostUse) int { return cmp.Or(b.Open-a.Open, cmp.Compare(a.Host, b.Host)) })
	return out
}

// open counts a new request to host; done ends it.
func (h *HostCounter) open(host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n == nil {
		h.n = map[string]*HostUse{}
	}
	u := h.n[host]
	if u == nil {
		u = &HostUse{Host: host}
		h.n[host] = u
	}
	u.Open++
}

func (h *HostCounter) done(host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u := h.n[host]
	u.Open--
	u.Done++
}

type countTransport struct {
	h    *HostCounter
	next http.RoundTripper
}

func (t countTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	t.h.open(host)
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.Body == nil {
		t.h.done(host)
		return resp, err
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, done: func() { t.h.done(host) }}
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
