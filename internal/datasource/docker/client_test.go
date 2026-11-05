package dockerds

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
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
		ts.mu.Unlock()
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
	s := newSource(f.srv.Client(), nil, func(_ context.Context, registry string) (credential, error) {
		lookups.Add(1)
		return static(f.host, credential{Username: "u", Password: "p"})(t.Context(), registry)
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
		{"ftp://auth.example/token", https, false},
		{"", https, false},
	} {
		if err := validateRealm(c.realm, c.reg); (err == nil) != c.ok {
			t.Errorf("validateRealm(%q, %s) = %v, want ok %v", c.realm, c.reg, err, c.ok)
		}
	}
}

// writeHelper puts a fake docker-credential-fake on PATH: it answers reg.example with a
// username and password, Docker Hub with an identity token, and nothing else.
func writeHelper(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
[ "$1" = get ] || exit 2
read -r addr
case "$addr" in
  reg.example) echo '{"ServerURL":"reg.example","Username":"u","Secret":"p"}' ;;
  https://index.docker.io/v1/) echo '{"ServerURL":"https://index.docker.io/v1/","Username":"<token>","Secret":"rt"}' ;;
  broken.example) echo 'helper exploded'; exit 1 ;;
  *) echo 'credentials not found in native keychain'; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-fake"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDockerCredentials(t *testing.T) {
	writeHelper(t)
	for _, c := range []struct {
		name, config, registry string
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
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DOCKER_CONFIG", dir)
			if c.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(c.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := dockerCredentials()(t.Context(), c.registry)
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
