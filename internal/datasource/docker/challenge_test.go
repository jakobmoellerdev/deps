package dockerds

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oras-project/oras-go/v3/registry/remote/auth"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// tokenRegistry is a memRegistry behind the distribution token flow, as on Docker Hub: every
// request without the token for its repository's pull scope gets a bearer challenge.
type tokenRegistry struct {
	*memRegistry
	srv  *httptest.Server
	host string

	mu   sync.Mutex
	hits map[string]int // "401", "token" or "METHOD path" of an authorized request
}

func newTokenRegistry(t *testing.T) *tokenRegistry {
	t.Helper()
	r := &tokenRegistry{memRegistry: newMemRegistry(), hits: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			r.hit("token")
			if req.URL.Query().Get("service") != "test" {
				http.Error(w, "bad service", http.StatusBadRequest)
				return
			}
			_ = json.MarshalWrite(w, map[string]string{"token": "t " + req.URL.Query().Get("scope")})
			return
		}
		repo, _ := apiRepository(req.URL.Path)
		if req.Header.Get("Authorization") != "Bearer t "+"repository:"+repo+":pull" {
			r.hit("401")
			w.Header().Set("Www-Authenticate", `Bearer realm="`+r.srv.URL+`/token",service="test",`+pullScope(repo))
			registryError(w, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		r.hit(req.Method + " " + req.URL.Path)
		r.ServeHTTP(w, req)
	}))
	t.Cleanup(r.srv.Close)
	r.host = strings.TrimPrefix(r.srv.URL, "http://")
	return r
}

func (r *tokenRegistry) hit(k string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits[k]++
}

func (r *tokenRegistry) count(k string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[k]
}

// TestChallengeOncePerRegistry: on a registry that challenges every unauthenticated request,
// only the first request pays the 401, also when lookups start together; the others get the
// registry's challenge with their own scope, so each token is the one the registry would have
// asked for.
func TestChallengeOncePerRegistry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		challenges bool // the registry is in challengeHosts
		want401    int
	}{
		{"challenge reused", true, 1},
		{"other registry", false, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTokenRegistry(t)
			img := r.image(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), nil)
			repos := []string{"library/alpine", "org/one", "org/two", "org/three"}
			for _, repo := range repos {
				r.tag(repo, "1.0", img)
				r.tag(repo, "latest", img)
			}
			s := newSource(r.srv.Client(), nil, anon)
			if tc.challenges {
				s.challenger.hosts = map[string]bool{r.host: true}
			}
			dp := func(repo string) dep.Dep { return dep.Dep{Name: r.host + "/" + repo} }

			var wg sync.WaitGroup
			for _, repo := range repos[:3] {
				wg.Go(func() {
					if rels, err := s.Releases(t.Context(), dp(repo), ""); err != nil || len(rels) != 2 {
						t.Errorf("Releases %s = %v, %v", repo, rels, err)
					}
				})
			}
			// A digest of a repository that was never listed (latest is resolved without a listing).
			wg.Go(func() {
				if got, err := s.Digest(t.Context(), dp(repos[3]), "latest"); err != nil || got != img.Digest.String() {
					t.Errorf("Digest = %q, %v; want %s", got, err, img.Digest)
				}
			})
			wg.Wait()
			// A listed repository reuses its cached token.
			if got, err := s.ReleaseTime(t.Context(), dp(repos[1]), "1.0"); err != nil || got.IsZero() {
				t.Errorf("ReleaseTime = %v, %v", got, err)
			}

			if got := r.count("401"); got != tc.want401 {
				t.Errorf("401 challenges = %d; want %d", got, tc.want401)
			}
			if got := r.count("token"); got != len(repos) {
				t.Errorf("token requests = %d; want %d", got, len(repos))
			}
			for _, repo := range repos[:3] {
				if got := r.count("GET /v2/" + repo + "/tags/list"); got != 1 {
					t.Errorf("tags/list of %s = %d; want 1", repo, got)
				}
			}
			if got := r.count("HEAD /v2/" + repos[3] + "/manifests/latest"); got != 1 {
				t.Errorf("HEAD latest = %d; want 1", got)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestChallengerPassesThrough: requests that carry credentials, write, or are no registry pull
// (the token realm on the registry host) are sent as they are, and a first challenge whose
// scope is not the request's repository pull scope leaves every request to be sent.
func TestChallengerPassesThrough(t *testing.T) {
	var sent int
	newCh := func() *challenger {
		ch := newChallenger(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			sent++
			h := http.Header{}
			if repo, ok := apiRepository(req.URL.Path); ok {
				if repo == "x" {
					repo = "other"
				}
				h.Set("Www-Authenticate", `Bearer realm="https://reg/token",service="reg",`+pullScope(repo))
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: h, Body: http.NoBody, Request: req}, nil
		}))
		ch.hosts = map[string]bool{"reg": true}
		return ch
	}
	do := func(ch *challenger, method, path, authz string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, "https://reg"+path, http.NoBody)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := ch.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	ch := newCh()
	do(ch, http.MethodGet, "/v2/x/tags/list", "") // the challenge names another repository
	do(ch, http.MethodGet, "/v2/b/tags/list", "")
	if sent != 2 {
		t.Fatalf("sent %d; want 2", sent)
	}

	sent, ch = 0, newCh()
	do(ch, http.MethodGet, "/token", "")
	do(ch, http.MethodGet, "/v2/", "")
	do(ch, http.MethodGet, "/v2/b/tags/list", "Bearer x")
	do(ch, http.MethodPut, "/v2/b/manifests/1", "")
	if sent != 4 || len(ch.known) != 0 {
		t.Fatalf("sent %d, known %v; want 4 sent, none known", sent, ch.known)
	}
	do(ch, http.MethodGet, "/v2/a/tags/list", "")
	resp := do(ch, http.MethodHead, "/v2/c/d/manifests/latest", "")
	if sent != 5 || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD = %d (sent %d); want a local 401", resp.StatusCode, sent)
	}
	if got, want := resp.Header.Get("Www-Authenticate"), `Bearer realm="https://reg/token",service="reg",scope="repository:c/d:pull"`; got != want {
		t.Errorf("challenge = %s; want %s", got, want)
	}
}

// TestRegistryScopes: on a registry whose challenge asks for a scope of its own instead of the
// repository's (public.ecr.aws: `aws`), the token is fetched once and, with the registry's
// scope hinted, every later request of the repository (the second tags/list page, HEAD,
// manifest and blob GETs) carries it; without the hint each of them is first answered with a
// 401.
func TestRegistryScopes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hinted  bool
		want401 int
	}{
		{"hinted", true, 1},
		{"not hinted", false, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newMemRegistry()
			img := reg.image(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), nil)
			reg.tag("lambda/python", "3.12", img)
			var mu sync.Mutex
			hits := map[string]int{}
			hit := func(k string) {
				mu.Lock()
				defer mu.Unlock()
				hits[k]++
			}
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/token" {
					hit("token")
					_ = json.MarshalWrite(w, map[string]string{"token": "t " + strings.Join(req.URL.Query()["scope"], " ")})
					return
				}
				if req.Header.Get("Authorization") != "Bearer t aws repository:lambda/python:pull" {
					hit("401")
					w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test",scope="aws"`)
					registryError(w, http.StatusUnauthorized, "DENIED")
					return
				}
				hit(req.Method + " " + req.URL.Path)
				if strings.HasSuffix(req.URL.Path, "/tags/list") && req.URL.Query().Get("last") == "" {
					w.Header().Set("Link", `</v2/lambda/python/tags/list?last=3.11>; rel="next"`)
					_ = json.MarshalWrite(w, map[string]any{"tags": []string{"3.11"}})
					return
				}
				reg.ServeHTTP(w, req) // the last page: 3.12
			}))
			t.Cleanup(srv.Close)
			host := strings.TrimPrefix(srv.URL, "http://")
			s := newSource(srv.Client(), nil, anon)
			if tc.hinted {
				s.scopes = map[string][]auth.Scope{host: {{ResourceType: "aws"}}}
			}
			d := dep.Dep{Name: host + "/lambda/python"}

			if rels, err := s.Releases(t.Context(), d, ""); err != nil || len(rels) != 2 {
				t.Fatalf("Releases = %v, %v", rels, err)
			}
			if got, err := s.Digest(t.Context(), d, "3.12"); err != nil || got != img.Digest.String() {
				t.Errorf("Digest = %q, %v; want %s", got, err, img.Digest)
			}
			if got, err := s.ReleaseTime(t.Context(), d, "3.12"); err != nil || got.IsZero() {
				t.Errorf("ReleaseTime = %v, %v", got, err)
			}

			mu.Lock()
			defer mu.Unlock()
			if hits["401"] != tc.want401 || hits["token"] != 1 {
				t.Errorf("401 challenges = %d, token requests = %d; want %d, 1", hits["401"], hits["token"], tc.want401)
			}
			if got := hits["GET /v2/lambda/python/tags/list"]; got != 2 {
				t.Errorf("tags/list pages = %d; want 2", got)
			}
		})
	}
}
