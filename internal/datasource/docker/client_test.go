package dockerds

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// tokenServer is a registry (the fixture's repository img) that requires a Bearer token from its
// realm /token. The realm's handler decides the token per request; "" refuses with 401.
type tokenServer struct {
	f      *fixture
	srv    *httptest.Server
	host   string
	mu     sync.Mutex
	issue  func(r *http.Request) (status int, token string)
	valid  map[string]bool // tokens the registry accepts
	deny   bool            // reject an invalid token with 400 DENIED and no challenge (ECR Public)
	tokens atomic.Int32    // token requests
	denied atomic.Int32    // 401s of registry requests
}

func newTokenServer(t *testing.T, issue func(r *http.Request) (int, string)) *tokenServer {
	t.Helper()
	ts := &tokenServer{f: newFixture(t), issue: issue, valid: map[string]bool{}}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			ts.tokens.Add(1)
			status, token := ts.issue(r)
			if status != http.StatusOK {
				registryError(w, status, "UNSUPPORTED")
				return
			}
			ts.mu.Lock()
			ts.valid[token] = true
			ts.mu.Unlock()
			fmt.Fprintf(w, `{"token":%q,"expires_in":300}`, token)
			return
		}
		ts.mu.Lock()
		ok := ts.valid[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		deny := ts.deny
		ts.mu.Unlock()
		if !ok && deny && r.Header.Get("Authorization") != "" {
			registryError(w, http.StatusBadRequest, "DENIED")
			return
		}
		if !ok {
			ts.denied.Add(1)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test-registry",scope="repository:img:pull"`, ts.srv.URL))
			registryError(w, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		ts.f.reg.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.srv.Close)
	ts.host = strings.TrimPrefix(ts.srv.URL, "http://")
	return ts
}

func (ts *tokenServer) revoke() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	clear(ts.valid)
}

// TestBearerAnonymous checks the anonymous token GET (service and scope in the query, no
// credentials) and that one token serves every request of the repository, including concurrent
// ones.
func TestBearerAnonymous(t *testing.T) {
	var query url.Values
	ts := newTokenServer(t, func(r *http.Request) (int, string) {
		if _, _, ok := r.BasicAuth(); ok || r.Method != http.MethodGet {
			t.Errorf("token request %s with basic auth %v", r.Method, ok)
		}
		query = r.URL.Query()
		return http.StatusOK, "anon"
	})
	s := newSource(ts.srv.Client(), nil, anon)
	d := dep.Dep{Name: ts.host + "/img"}
	var wg sync.WaitGroup
	for _, v := range []string{"1.0", "2.0", "latest", "1.0"} {
		wg.Go(func() {
			if _, err := s.Digest(t.Context(), d, v); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if rels, err := s.Releases(t.Context(), d, ""); err != nil || len(rels) != 3 {
		t.Fatalf("Releases = %v, %v", rels, err)
	}
	if _, err := s.ReleaseTime(t.Context(), d, "2.0"); err != nil {
		t.Fatal(err)
	}
	if n := ts.tokens.Load(); n != 1 {
		t.Errorf("token requests = %d, want 1", n)
	}
	if query.Get("service") != "test-registry" || strings.Join(query["scope"], " ") != "repository:img:pull" {
		t.Errorf("token query = %v", query)
	}
}

// TestBearerConcurrentWait checks that once the registry's scheme is known, concurrent requests
// of a repository wait for the token the first one fetches instead of each paying a 401 (on
// registries the challenger does not answer for), that a waiter gives up when its context is
// done, and that after a failed fetch the waiters get the challenge and a token themselves.
func TestBearerConcurrentWait(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			var slow atomic.Bool
			entered, release := make(chan struct{}, 1), make(chan struct{})
			ts := newTokenServer(t, func(*http.Request) (int, string) {
				if slow.CompareAndSwap(true, false) {
					entered <- struct{}{}
					<-release
					if fail {
						return http.StatusUnauthorized, ""
					}
				}
				return http.StatusOK, "t"
			})
			s := newSource(ts.srv.Client(), nil, anon)
			// Another repository's challenge makes the registry's scheme known.
			if _, err := s.manifestRequest(t.Context(), s.repository(ts.host+"/other"), http.MethodHead, "1.0"); !errors.Is(err, errNotFound) {
				t.Fatalf("other repository: %v", err)
			}
			tokens, denied := ts.tokens.Load(), ts.denied.Load()
			r := s.repository(ts.host + "/img")
			head := func(ctx context.Context) error {
				resp, err := s.manifestRequest(ctx, r, http.MethodHead, "1.0")
				if err == nil {
					resp.Body.Close()
				}
				return err
			}

			slow.Store(true)
			first := make(chan error, 1)
			go func() { first <- head(t.Context()) }()
			<-entered // the first request's token fetch is in flight
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := head(ctx); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled waiter err = %v", err)
			}
			const n = 8
			var wg sync.WaitGroup
			for range n - 1 {
				wg.Go(func() {
					if err := head(t.Context()); err != nil {
						t.Error(err)
					}
				})
			}
			time.Sleep(50 * time.Millisecond) // the others reach the wait
			close(release)
			wg.Wait()
			err := <-first

			gotTokens, gotDenied := ts.tokens.Load()-tokens, ts.denied.Load()-denied
			if fail {
				if err == nil {
					t.Error("first request succeeded despite its token refusal")
				}
				if gotTokens != 2 {
					t.Errorf("token requests = %d, want 2", gotTokens)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotTokens != 1 || gotDenied != 1 {
				t.Errorf("token requests = %d, 401s = %d; want 1 each for %d concurrent requests", gotTokens, gotDenied, n)
			}
		})
	}
}

// TestBearerBasicCredentials checks the token GET carries the username and password, and that a
// token the registry stops accepting (expired) is fetched anew once.
func TestBearerBasicCredentials(t *testing.T) {
	var n atomic.Int32
	ts := newTokenServer(t, func(r *http.Request) (int, string) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, fmt.Sprintf("t%d", n.Add(1))
	})
	s := newSource(ts.srv.Client(), nil, static(ts.host, credential{Username: "u", Password: "p"}))
	d := dep.Dep{Name: ts.host + "/img"}
	if dg, err := s.Digest(t.Context(), d, "1.0"); err != nil || dg != ts.f.imageDigest {
		t.Fatalf("Digest = %q, %v", dg, err)
	}
	ts.revoke()
	if dg, err := s.Digest(t.Context(), d, "2.0"); err != nil || dg != ts.f.indexDigest {
		t.Fatalf("Digest after expiry = %q, %v", dg, err)
	}
	if got := ts.tokens.Load(); got != 2 {
		t.Errorf("token requests = %d, want 2", got)
	}

	// Wrong credentials: the token endpoint's refusal is an HTTP error, not retried with GET.
	s = newSource(ts.srv.Client(), nil, static(ts.host, credential{Username: "u", Password: "wrong"}))
	_, err := s.Digest(t.Context(), d, "latest")
	if resp := (*errorResponse)(nil); !errors.As(err, &resp) || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong credentials err = %v", err)
	}
	if n := ts.f.count("GET /v2/img/manifests/latest"); n != 0 {
		t.Errorf("GET fallback after token refusal: %d", n)
	}
}

// A registry that rejects an expired token with 400 DENIED instead of a 401 challenge gets the
// request again without it, and the challenge it then sends a fresh token.
func TestBearerDeniedToken(t *testing.T) {
	var n atomic.Int32
	ts := newTokenServer(t, func(*http.Request) (int, string) { return http.StatusOK, fmt.Sprintf("t%d", n.Add(1)) })
	s := newSource(ts.srv.Client(), nil, anon)
	d := dep.Dep{Name: ts.host + "/img"}
	if _, err := s.Digest(t.Context(), d, "1.0"); err != nil {
		t.Fatal(err)
	}
	ts.revoke()
	ts.mu.Lock()
	ts.deny = true
	ts.mu.Unlock()
	if dg, err := s.Digest(t.Context(), d, "2.0"); err != nil || dg != ts.f.indexDigest {
		t.Fatalf("Digest after denied token = %q, %v", dg, err)
	}
	if got := ts.tokens.Load(); got != 2 {
		t.Errorf("token requests = %d, want 2", got)
	}
	ts.revoke() // a GET's 400 tells DENIED by its body
	if _, err := s.ReleaseTime(t.Context(), d, "1.0"); err != nil {
		t.Fatalf("ReleaseTime after denied token: %v", err)
	}
	if got := ts.tokens.Load(); got != 3 {
		t.Errorf("token requests = %d, want 3", got)
	}
}

// A realm on another host reached without a proxy is not resolved beforehand: a blocked address
// is refused when the token request connects, including a name that resolved to a public address
// earlier (DNS rebinding).
func TestTokenDialGuard(t *testing.T) {
	ts := newTokenServer(t, func(*http.Request) (int, string) { return http.StatusOK, "t" })
	_, port, _ := net.SplitHostPort(ts.host)
	lookups := fakeLookup(t, map[string][]net.IPAddr{"localhost": {{IP: net.ParseIP("203.0.113.7")}}})
	c := newClient(httpx.NewClient(), http.Header{}, anon, "deps")
	reg, _ := url.Parse(ts.srv.URL + "/v2/img/tags/list")
	realm := "http://localhost:" + port + "/token"
	_, err := c.distributionToken(t.Context(), reg, realm, "svc", []string{"repository:img:pull"}, credential{})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("err = %v, want the connection refused", err)
	}
	if ts.tokens.Load() != 0 {
		t.Error("token request reached the server")
	}
	if n := lookups.Load(); n != 0 {
		t.Errorf("lookups = %d, want none: the dial check applies", n)
	}
	// The registry's own host is not checked: local registries serve their own tokens.
	if _, err := c.distributionToken(t.Context(), reg, ts.srv.URL+"/token", "svc", []string{"repository:img:pull"}, credential{}); err != nil {
		t.Errorf("same-host realm: %v", err)
	}
}

// Through a proxy the connection is the proxy's, so a realm on another host is resolved
// beforehand and refused when it resolves to a blocked address.
func TestTokenProxyLookup(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	orig := proxyFor
	proxyFor = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	t.Cleanup(func() { proxyFor = orig })
	lookups := fakeLookup(t, map[string][]net.IPAddr{"metadata.test": {{IP: net.ParseIP("169.254.169.254")}}})
	c := newClient(httpx.NewClient(), http.Header{}, anon, "deps-test")
	reg, _ := url.Parse("https://registry.example/v2/img/tags/list")
	_, err := c.distributionToken(t.Context(), reg, "https://metadata.test/token", "svc", []string{"repository:img:pull"}, credential{})
	if err == nil || !strings.Contains(err.Error(), "resolves to 169.254.169.254") {
		t.Errorf("err = %v, want the realm refused by its lookup", err)
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("lookups = %d, want 1", n)
	}
	if n := proxied.Load(); n != 0 {
		t.Errorf("proxy requests = %d, want none", n)
	}
}

// TestBearerRefreshToken checks an identity token is exchanged with an OAuth2 refresh-token
// POST, and that a realm without OAuth2 support falls back to the token GET.
func TestBearerRefreshToken(t *testing.T) {
	for _, oauth2 := range []bool{true, false} {
		t.Run(fmt.Sprint("oauth2=", oauth2), func(t *testing.T) {
			var methods []string
			var mu sync.Mutex
			ts := newTokenServer(t, func(r *http.Request) (int, string) {
				mu.Lock()
				methods = append(methods, r.Method)
				mu.Unlock()
				if r.Method == http.MethodGet {
					return http.StatusOK, "from-get"
				}
				if !oauth2 {
					return http.StatusMethodNotAllowed, ""
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				want := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt"}, "service": {"test-registry"},
					"scope": {"repository:img:pull"}, "client_id": {oauth2ClientID}}
				if fmt.Sprint(r.PostForm) != fmt.Sprint(want) {
					t.Errorf("form = %v, want %v", r.PostForm, want)
				}
				return http.StatusOK, "from-post"
			})
			s := newSource(ts.srv.Client(), nil, static(ts.host, credential{RefreshToken: "rt"}))
			if dg, err := s.Digest(t.Context(), dep.Dep{Name: ts.host + "/img"}, "1.0"); err != nil || dg != ts.f.imageDigest {
				t.Fatalf("Digest = %q, %v", dg, err)
			}
			want := "[POST]"
			if !oauth2 {
				want = "[POST GET]"
			}
			if fmt.Sprint(methods) != want {
				t.Errorf("token requests = %v, want %s", methods, want)
			}
		})
	}
}

// TestBearerRegistryToken checks a registry token is used as the bearer token as is.
func TestBearerRegistryToken(t *testing.T) {
	ts := newTokenServer(t, func(*http.Request) (int, string) { return http.StatusOK, "issued" })
	ts.valid["preset"] = true
	s := newSource(ts.srv.Client(), nil, static(ts.host, credential{AccessToken: "preset"}))
	if dg, err := s.Digest(t.Context(), dep.Dep{Name: ts.host + "/img"}, "1.0"); err != nil || dg != ts.f.imageDigest {
		t.Fatalf("Digest = %q, %v", dg, err)
	}
	if n := ts.tokens.Load(); n != 0 {
		t.Errorf("token requests = %d, want 0", n)
	}
}

// TestBasicChallenge checks Basic credentials are sent after the first challenge without
// another 401, and that a credential lookup failure is reported.
func TestBasicChallenge(t *testing.T) {
	f := newFixture(t)
	f.set(func() { f.user, f.pass = "u", "p" })
	var lookups atomic.Int32
	s := newSource(f.srv.Client(), nil, func(_ context.Context, registry, path string) (credential, error) {
		lookups.Add(1)
		return static(f.host, credential{Username: "u", Password: "p"})(t.Context(), registry, path)
	})
	d := dep.Dep{Name: f.host + "/img"}
	for _, v := range []string{"1.0", "2.0", "latest"} {
		if _, err := s.Digest(t.Context(), d, v); err != nil {
			t.Fatal(err)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("credential lookups = %d, want 1", n)
	}
	if n := f.count("HEAD /v2/img/manifests/1.0"); n != 2 {
		t.Errorf("HEAD 1.0 = %d requests, want 2 (challenge, retry)", n)
	}
	if n := f.count("HEAD /v2/img/manifests/2.0"); n != 1 {
		t.Errorf("HEAD 2.0 = %d requests, want 1 (authorized up front)", n)
	}

	s = newSource(f.srv.Client(), nil, static(f.host, credential{Username: "u"}))
	if _, err := s.Digest(t.Context(), d, "1.0"); err == nil || !strings.Contains(err.Error(), "missing username or password") {
		t.Errorf("incomplete credential err = %v", err)
	}
}

// TestDigestMismatch checks fetched content is verified against its digest.
func TestDigestMismatch(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			w = wrongDigestWriter{w, f.indexDigest} // the wrong one for 1.0
		}
		f.reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s := newSource(srv.Client(), nil, anon)
	_, err := s.ReleaseTime(t.Context(), dep.Dep{Name: strings.TrimPrefix(srv.URL, "http://") + "/img"}, "1.0")
	if err == nil || !strings.Contains(err.Error(), "does not match digest") {
		t.Errorf("err = %v", err)
	}
}

// wrongDigestWriter replaces the Docker-Content-Digest of a response.
type wrongDigestWriter struct {
	http.ResponseWriter
	digest string
}

func (w wrongDigestWriter) WriteHeader(code int) {
	w.Header().Set("Docker-Content-Digest", w.digest)
	w.ResponseWriter.WriteHeader(code)
}

func TestParseChallenge(t *testing.T) {
	for _, c := range []struct {
		header string
		scheme authScheme
		params string
	}{
		{`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`, schemeBearer,
			"map[realm:https://auth.docker.io/token scope:repository:library/alpine:pull service:registry.docker.io]"},
		{`bearer Realm="https://r.example/t", service="a \"b\"",x=y`, schemeBearer, `map[realm:https://r.example/t service:a "b" x:y]`},
		{`Basic realm="Registry"`, schemeBasic, "map[]"},
		{`Negotiate`, schemeNone, "map[]"},
		{``, schemeNone, "map[]"},
		{`Bearer realm="unterminated`, schemeBearer, "map[]"},
	} {
		scheme, params := parseChallenge(c.header)
		if scheme != c.scheme || fmt.Sprint(params) != c.params {
			t.Errorf("parseChallenge(%q) = %v, %v; want %v, %s", c.header, scheme, params, c.scheme, c.params)
		}
	}
}

func TestValidateRealm(t *testing.T) {
	resolved := map[string][]net.IPAddr{
		"metadata.test": {{IP: net.ParseIP("203.0.113.7")}, {IP: net.ParseIP("169.254.169.254")}},
		"public.test":   {{IP: net.ParseIP("203.0.113.7")}},
	}
	fakeLookup(t, resolved)
	https, _ := url.Parse("https://registry.example/v2/x/tags/list")
	plain, _ := url.Parse("http://127.0.0.1:5000/v2/x/tags/list")
	for _, c := range []struct {
		realm string
		reg   *url.URL
		ok    bool
	}{
		{"https://auth.example/token", https, true},
		{"http://auth.example/token", https, false},
		{"http://127.0.0.1:5000/token", plain, true},
		{"https://169.254.169.254/latest", https, false},
		{"https://127.0.0.1/token", https, false},
		{"https://[::ffff:127.0.0.1]/token", https, false},
		{"https://[64:ff9b::a9fe:a9fe]/token", https, false}, // NAT64 of 169.254.169.254
		{"https://168.63.129.16/token", https, false},
		{"https://10.0.0.1/token", https, true}, // private token services of self-hosted registries
		{"https://metadata.test/token", https, false},
		{"https://public.test/token", https, true},
		{"https://unresolvable.test/token", https, true},
		{"ftp://auth.example/token", https, false},
		{"", https, false},
	} {
		if err := validateRealm(t.Context(), c.realm, c.reg, false); (err == nil) != c.ok {
			t.Errorf("validateRealm(%q, %s) = %v, want ok %v", c.realm, c.reg, err, c.ok)
		}
		// With a dial check only the lookup is skipped.
		if err := validateRealm(t.Context(), c.realm, c.reg, true); (err == nil) != (c.ok || c.realm == "https://metadata.test/token") {
			t.Errorf("validateRealm(%q, %s, dial checked) = %v", c.realm, c.reg, err)
		}
	}
}

// fakeLookup makes realm host lookups answer from resolved; other names fail to resolve. It
// returns the number of lookups made.
func fakeLookup(t *testing.T, resolved map[string][]net.IPAddr) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		n.Add(1)
		if addrs, ok := resolved[host]; ok {
			return addrs, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { lookupIPAddr = orig })
	return &n
}

// A token request may not be redirected to where the realm itself could not point.
func TestTokenRedirectValidated(t *testing.T) {
	fakeLookup(t, map[string][]net.IPAddr{"metadata.test": {{IP: net.ParseIP("169.254.169.254")}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://metadata.test/latest/meta-data", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.Client(), http.Header{}, anon, "deps")
	reg, _ := url.Parse(srv.URL + "/v2/x/tags/list")
	_, err := c.distributionToken(t.Context(), reg, srv.URL+"/token", "svc", []string{"repository:x:pull"}, credential{Username: "u", Password: "p"})
	if err == nil || !strings.Contains(err.Error(), "token request redirect rejected") {
		t.Errorf("err = %v, want rejected redirect", err)
	}
}

// writeHelper puts a fake docker-credential-fake on PATH: it answers reg.example with a
// username and password, Docker Hub with an identity token, and nothing else. It returns the
// file each run appends its server address to.
func writeHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
[ "$1" = get ] || exit 2
read -r addr
echo "$addr" >> '` + calls + `'
case "$addr" in
  reg.example|reg.example/team) echo '{"ServerURL":"reg.example","Username":"u","Secret":"p"}' ;;
  https://index.docker.io/v1/) echo '{"ServerURL":"https://index.docker.io/v1/","Username":"<token>","Secret":"rt"}' ;;
  broken.example) echo 'helper exploded'; exit 1 ;;
  *) echo 'credentials not found in native keychain'; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-fake"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// helperCalls returns the server addresses the fake helper was run for, in order.
func helperCalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// TestHelperCredentialMemo checks that a credential helper runs once per server address for the
// many repositories of a registry, concurrent lookups included, that a "not found" is kept too,
// and that a failed run is not.
func TestHelperCredentialMemo(t *testing.T) {
	calls := writeHelper(t)
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	config := `{"credsStore":"fake","credHelpers":{"reg.example/team":"fake"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := dockerCredentials()
	lookup := func(registry, path string) (credential, error) { return cred(t.Context(), registry, path) }

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if got, err := lookup("reg.example", fmt.Sprintf("app%d", i)); err != nil || got != (credential{Username: "u", Password: "p"}) {
				t.Errorf("reg.example/app%d = %+v, %v", i, got, err)
			}
			if got, err := lookup("reg.example", fmt.Sprintf("team/app%d", i)); err != nil || got != (credential{Username: "u", Password: "p"}) {
				t.Errorf("reg.example/team/app%d = %+v, %v", i, got, err)
			}
		})
	}
	wg.Wait()
	for range 2 {
		if got, err := lookup("ghcr.io", "org/app"); err != nil || got != (credential{}) {
			t.Errorf("ghcr.io = %+v, %v; want not found", got, err)
		}
		if _, err := lookup("broken.example", "app"); err == nil || !strings.Contains(err.Error(), "helper exploded") {
			t.Errorf("broken.example err = %v", err)
		}
	}
	got := helperCalls(t, calls)
	slices.Sort(got)
	want := []string{"broken.example", "broken.example", "ghcr.io", "reg.example", "reg.example/team"}
	if !slices.Equal(got, want) {
		t.Errorf("helper runs = %q, want %q", got, want)
	}
}

func TestDockerCredentials(t *testing.T) {
	writeHelper(t)
	for _, c := range []struct {
		name, config, registry string // registry: host, or host/path for a repository's lookup
		want                   credential
		err                    string
	}{
		{"no config", "", "ghcr.io", credential{}, ""},
		{"auth", `{"auths":{"ghcr.io":{"auth":"dTpw"}}}`, "ghcr.io", credential{Username: "u", Password: "p"}, ""},
		{"scheme and case", `{"auths":{"https://GHCR.io/":{"username":"u","password":"p"}}}`, "ghcr.io", credential{Username: "u", Password: "p"}, ""},
		{"identity token", `{"auths":{"reg.example":{"username":"<token>","identitytoken":"rt"}}}`, "reg.example", credential{Username: "<token>", RefreshToken: "rt"}, ""},
		{"registry token", `{"auths":{"reg.example":{"registrytoken":"at"}}}`, "reg.example", credential{AccessToken: "at"}, ""},
		{"hub server address", `{"auths":{"https://index.docker.io/v1/":{"auth":"dTpw"}}}`, "docker.io", credential{Username: "u", Password: "p"}, ""},
		{"hub host key", `{"auths":{"docker.io":{"auth":"dTpw"}}}`, "docker.io", credential{Username: "u", Password: "p"}, ""},
		{"other host", `{"auths":{"ghcr.io":{"auth":"dTpw"}}}`, "quay.io", credential{}, ""},
		{"bad auth", `{"auths":{"ghcr.io":{"auth":"!!"}}}`, "ghcr.io", credential{}, "decode auth"},
		{"cred helper", `{"credHelpers":{"reg.example":"fake"},"auths":{"reg.example":{"auth":"eDp5"}}}`, "reg.example", credential{Username: "u", Password: "p"}, ""},
		{"cred helper hub", `{"credHelpers":{"https://index.docker.io/v1/":"fake"}}`, "docker.io", credential{RefreshToken: "rt"}, ""},
		{"creds store", `{"credsStore":"fake"}`, "reg.example", credential{Username: "u", Password: "p"}, ""},
		{"creds store not found", `{"credsStore":"fake","auths":{"ghcr.io":{"auth":"dTpw"}}}`, "ghcr.io", credential{}, ""},
		{"helper error", `{"credsStore":"fake"}`, "broken.example", credential{}, "helper exploded"},
		{"missing helper", `{"credsStore":"absent"}`, "ghcr.io", credential{}, "docker-credential-absent"},
		{"broken config", `{`, "ghcr.io", credential{}, "docker config: "},
		{"namespace", `{"auths":{"ghcr.io/org":{"auth":"dTpw"},"ghcr.io":{"auth":"eDp5"}}}`, "ghcr.io/org/app", credential{Username: "u", Password: "p"}, ""},
		{"most specific namespace", `{"auths":{"ghcr.io/org":{"auth":"eDp5"},"GHCR.io/org/app/":{"auth":"dTpw"}}}`, "ghcr.io/org/app", credential{Username: "u", Password: "p"}, ""},
		{"other namespace", `{"auths":{"ghcr.io/org":{"auth":"eDp5"},"ghcr.io":{"auth":"dTpw"}}}`, "ghcr.io/other/app", credential{Username: "u", Password: "p"}, ""},
		{"namespace over creds store", `{"credsStore":"absent","auths":{"ghcr.io/org":{"auth":"dTpw"}}}`, "ghcr.io/org/app", credential{Username: "u", Password: "p"}, ""},
		{"namespace helper", `{"credHelpers":{"reg.example/team":"fake"}}`, "reg.example/team/app", credential{Username: "u", Password: "p"}, ""},
		{"namespace helper not found", `{"credHelpers":{"ghcr.io/org":"fake"},"auths":{"ghcr.io":{"auth":"dTpw"}}}`, "ghcr.io/org/app", credential{Username: "u", Password: "p"}, ""},
		{"hub has no namespaces", `{"auths":{"docker.io/library":{"auth":"eDp5"},"https://index.docker.io/v1/":{"auth":"dTpw"}}}`, "docker.io/library/alpine", credential{Username: "u", Password: "p"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DOCKER_CONFIG", dir)
			if c.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(c.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			registry, path, _ := strings.Cut(c.registry, "/")
			got, err := dockerCredentials()(t.Context(), registry, path)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("= %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

// TestPlanBatchSize checks planned scopes are batched within maxBatchQuery bytes of token
// request query, each scope in one batch, and that scopes planned again or alone get no batch.
func TestPlanBatchSize(t *testing.T) {
	c := newClient(nil, nil, anon, "")
	var paths []string
	for i := range 500 {
		paths = append(paths, fmt.Sprintf("org/a-repository-with-a-long-name-%03d", i))
	}
	c.plan(hubRegistry, paths)
	first := c.batches[c.tokenKey(hubRegistry, "repository:"+paths[0]+":pull")]
	c.plan(hubRegistry, paths[:10])
	c.plan("other.example", paths[:1])

	batches := map[*string][]string{}
	for _, p := range paths {
		scope := "repository:" + p + ":pull"
		b := c.batches[c.tokenKey(hubRegistry, scope)]
		if !slices.Contains(b, scope) {
			t.Fatalf("batch of %s = %v", scope, b)
		}
		batches[&b[0]] = b
	}
	if len(c.batches) != len(paths) || !slices.Equal(c.batches[c.tokenKey(hubRegistry, "repository:"+paths[0]+":pull")], first) {
		t.Errorf("planning again or alone changed the batches: %d keys", len(c.batches))
	}
	total := 0
	for _, b := range batches {
		if q := (url.Values{"scope": b}).Encode(); len(q) > maxBatchQuery {
			t.Errorf("batch query of %d scopes = %d bytes; want at most %d", len(b), len(q), maxBatchQuery)
		}
		total += len(b)
	}
	if len(batches) < 2 || total != len(paths) {
		t.Errorf("%d batches of %d scopes; want several of %d", len(batches), total, len(paths))
	}
}
