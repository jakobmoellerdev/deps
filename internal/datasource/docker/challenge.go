package dockerds

import (
	"net/http"
	"strings"
	"sync"
)

// challengeHosts are the registries that answer every unauthenticated request with a bearer
// challenge, for public repositories too. Other registries (quay.io, gcr.io, registry.istio.io)
// serve some or all repositories without one, so a challenge for one repository says nothing
// about the next.
var challengeHosts = map[string]bool{
	"registry-1.docker.io": true,
	"ghcr.io":              true,
}

// challenger answers the first request to each repository of a challengeHosts registry with
// the bearer challenge the registry gave for another repository, instead of sending it.
// oras-go caches tokens per repository, so it sends the first request to a repository without
// one; the registry's 401 then costs a round trip per repository (library/python: 362 ms) for a
// challenge that differs from the last only in the repository of its scope. The answer is the
// registry's own challenge with that repository replaced, so oras-go validates the realm and
// fetches the token exactly as it does after a real 401.
//
// Lookups start together, so the first unauthenticated request of a registry is sent alone:
// the others wait for its challenge, which they would otherwise each have waited a round trip
// for themselves. When it brings none (an error, or a challenge for another scope), every
// request of the registry is sent as it is.
type challenger struct {
	next  http.RoundTripper
	hosts map[string]bool

	mu    sync.Mutex
	known map[string]*challenge // host → its first bearer challenge
}

// challenge is a registry's bearer challenge and the repository its scope names, set before
// ready is closed; empty when the registry's first request brought none.
type challenge struct {
	ready        chan struct{}
	header, repo string
}

func newChallenger(next http.RoundTripper) *challenger {
	return &challenger{next: next, hosts: challengeHosts, known: map[string]*challenge{}}
}

func (t *challenger) RoundTrip(req *http.Request) (*http.Response, error) {
	repo, ok := t.challengeable(req)
	if !ok {
		return t.next.RoundTrip(req)
	}
	host := req.URL.Host
	t.mu.Lock()
	c := t.known[host]
	if c == nil {
		c = &challenge{ready: make(chan struct{})}
		t.known[host] = c
		t.mu.Unlock()
		return t.learn(req, repo, c)
	}
	t.mu.Unlock()
	select {
	case <-c.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	if c.header == "" {
		return t.next.RoundTrip(req)
	}
	return &http.Response{
		Status:     "401 Unauthorized",
		StatusCode: http.StatusUnauthorized,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Www-Authenticate": {strings.Replace(c.header, pullScope(c.repo), pullScope(repo), 1)}},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// learn sends req, the first unauthenticated request of its host, and keeps its bearer
// challenge as c.
func (t *challenger) learn(req *http.Request, repo string, c *challenge) (*http.Response, error) {
	defer close(c.ready)
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if h := resp.Header.Get("Www-Authenticate"); strings.HasPrefix(h, "Bearer ") && strings.Count(h, pullScope(repo)) == 1 {
		c.header, c.repo = h, repo
	}
	return resp, nil
}

// challengeable returns the repository of an unauthenticated pull request (GET or HEAD of a
// manifest, blob or tag list) to a challengeHosts registry.
func (t *challenger) challengeable(req *http.Request) (string, bool) {
	if !t.hosts[req.URL.Host] || req.Header.Get("Authorization") != "" ||
		(req.Method != http.MethodGet && req.Method != http.MethodHead) {
		return "", false
	}
	return apiRepository(req.URL.Path)
}

// apiRepository returns the repository of a registry API path to a manifest, blob or tag list.
func apiRepository(path string) (string, bool) {
	path, ok := strings.CutPrefix(path, "/v2/")
	if !ok {
		return "", false
	}
	end := -1
	for _, sep := range [...]string{"/manifests/", "/blobs/", "/tags/list"} {
		end = max(end, strings.LastIndex(path, sep))
	}
	if end <= 0 {
		return "", false
	}
	return path[:end], true
}

// pullScope is the quoted scope parameter of a pull challenge for repo.
func pullScope(repo string) string {
	return `scope="repository:` + repo + `:pull"`
}
