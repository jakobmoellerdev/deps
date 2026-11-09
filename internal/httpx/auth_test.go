package httpx

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestNewAuthStatic(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name  string
		info  platform.Info
		token string
		env   map[string]string
		host  string
		want  string // "" = no static credentials
	}{
		{name: "platform token", info: platform.Info{Host: "git.example.com"}, token: "t1", host: "git.example.com", want: "t1"},
		{name: "other host", info: platform.Info{Host: "git.example.com"}, token: "t1", host: "github.com"},
		{name: "empty token", info: platform.Info{Host: "git.example.com"}, host: "git.example.com"},
		{
			name: "github.com token on gitea", info: platform.Info{Host: "git.example.com"},
			env: map[string]string{"DEPS_GITHUB_COM_TOKEN": "gh"}, host: "github.com", want: "gh",
		},
		{
			name: "github.com token ignored on github.com", info: platform.Info{Host: "github.com"}, token: "plat",
			env: map[string]string{"DEPS_GITHUB_COM_TOKEN": "gh"}, host: "github.com", want: "plat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAuth(tt.info, tt.token, env(tt.env))
			user, pass, ok := a.Static(tt.host)
			if ok != (tt.want != "") || pass != tt.want {
				t.Fatalf("Static(%q) = %q, %q, %v; want %q", tt.host, user, pass, ok, tt.want)
			}
			if ok && user != "x-access-token" {
				t.Fatalf("user = %q, want x-access-token", user)
			}
		})
	}
	t.Run("gitlab platform token", func(t *testing.T) {
		a := NewAuth(platform.Info{Kind: platform.GitLab, Host: "gl.example"}, "t1", env(nil))
		if user, pass, ok := a.Static("gl.example"); !ok || user != "oauth2" || pass != "t1" {
			t.Fatalf("Static = %q, %q, %v; want oauth2 t1", user, pass, ok)
		}
	})
}

func TestWithTokenPrecedence(t *testing.T) {
	noEnv := func(string) string { return "" }
	tests := []struct {
		name       string
		info       platform.Info
		token      string
		opts       []AuthOption
		user, pass string // "" = no static credentials
	}{
		{
			name: "registered user", info: platform.Info{Host: "git.example.com"}, token: "plat",
			opts: []AuthOption{WithToken("gitlab.example.com", "oauth2", "gl", "test")}, user: "oauth2", pass: "gl",
		},
		{
			name: "first option wins", opts: []AuthOption{
				WithToken("gitlab.example.com", "oauth2", "gl", "test"), WithToken("gitlab.example.com", "gitlab-ci-token", "job", "test"),
			}, user: "oauth2", pass: "gl",
		},
		{
			name: "empty token skipped", opts: []AuthOption{
				WithToken("gitlab.example.com", "oauth2", "", "test"), WithToken("gitlab.example.com", "gitlab-ci-token", "job", "test"),
			}, user: "gitlab-ci-token", pass: "job",
		},
		{
			name: "platform token wins", info: platform.Info{Host: "gitlab.example.com"}, token: "plat",
			opts: []AuthOption{WithToken("gitlab.example.com", "oauth2", "gl", "test")}, user: "x-access-token", pass: "plat",
		},
		{name: "empty host", opts: []AuthOption{WithToken("", "oauth2", "gl", "test")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := "gitlab.example.com"
			user, pass, ok := NewAuth(tt.info, tt.token, noEnv, tt.opts...).Static(host)
			if ok != (tt.pass != "") || user != tt.user || pass != tt.pass {
				t.Fatalf("Static(%q) = %q, %q, %v; want %q, %q", host, user, pass, ok, tt.user, tt.pass)
			}
		})
	}
}

func TestHelperCachesPerHost(t *testing.T) {
	var calls atomic.Int32
	a := NewAuth(platform.Info{}, "", func(string) string { return "" }, WithHelper(
		func(_ context.Context, host string) (string, string, bool) {
			calls.Add(1)
			if host == "good" {
				return "u", "p", true
			}
			return "", "", false
		}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if u, p, ok := a.Helper(context.Background(), "good"); !ok || u != "u" || p != "p" {
				t.Errorf("Helper(good) = %q, %q, %v", u, p, ok)
			}
			if _, _, ok := a.Helper(context.Background(), "bad"); ok {
				t.Error("Helper(bad) ok")
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 2 {
		t.Fatalf("helper calls = %d, want 2 (one per host)", n)
	}
}

// A lookup failed by its caller's canceled context is not cached: the next caller asks again.
func TestHelperCanceledNotCached(t *testing.T) {
	calls := 0
	a := NewAuth(platform.Info{}, "", func(string) string { return "" }, WithHelper(
		func(ctx context.Context, _ string) (string, string, bool) {
			calls++
			if ctx.Err() != nil {
				return "", "", false
			}
			return "u", "p", true
		}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, ok := a.Helper(ctx, "h"); ok {
		t.Fatal("Helper(canceled) ok")
	}
	for range 2 {
		if u, p, ok := a.Helper(t.Context(), "h"); !ok || u != "u" || p != "p" {
			t.Fatalf("Helper after a canceled caller = %q, %q, %v", u, p, ok)
		}
	}
	if calls != 2 {
		t.Fatalf("helper calls = %d, want 2 (canceled, then once shared)", calls)
	}
}

func TestNilAuth(t *testing.T) {
	var a *Auth
	if _, _, ok := a.Static("x"); ok {
		t.Fatal("nil Static ok")
	}
	if _, _, ok := a.Helper(context.Background(), "x"); ok {
		t.Fatal("nil Helper ok")
	}
}

func TestParseCredential(t *testing.T) {
	tests := []struct {
		in         string
		user, pass string
		ok         bool
	}{
		{"protocol=https\nhost=h\nusername=alice\npassword=s3=cr\n", "alice", "s3=cr", true},
		{"username=alice\r\npassword=pw\r\n", "alice", "pw", true},
		{"username=alice\n", "alice", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		u, p, ok := parseCredential([]byte(tt.in))
		if u != tt.user || p != tt.pass || ok != tt.ok {
			t.Errorf("parseCredential(%q) = %q, %q, %v", tt.in, u, p, ok)
		}
	}
}

func TestSecretHook(t *testing.T) {
	var masked []string
	calls := 0
	a := NewAuth(platform.Info{}, "", func(string) string { return "" },
		WithHelper(func(_ context.Context, host string) (string, string, bool) {
			calls++
			return "u", "pw-" + host, host != "none"
		}),
		WithSecretHook(func(s string) {
			if calls == 0 {
				t.Error("hook before the helper")
			}
			masked = append(masked, s)
		}))
	for _, h := range []string{"a", "a", "none", "b"} {
		a.Helper(t.Context(), h)
	}
	if want := []string{"pw-a", "pw-b"}; !slices.Equal(masked, want) {
		t.Errorf("hook calls = %v, want %v (once per found secret)", masked, want)
	}
}

// The GitHub CLI's token is the github.com fallback: asked once, at the first github.com
// lookup, never when the platform token or DEPS_GITHUB_COM_TOKEN covers github.com, and never
// for other hosts.
func TestGHTokenFallback(t *testing.T) {
	var calls atomic.Int32
	gh := func(tok string) AuthOption {
		return func(a *Auth) { a.gh = func() string { calls.Add(1); return tok } }
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	var masked []string
	a := NewAuth(platform.Info{}, "", env(nil), WithSecretHook(func(s string) { masked = append(masked, s) }), gh("gho_cli"))
	if _, _, ok := a.Static("gitea.example"); ok || calls.Load() != 0 {
		t.Fatalf("other host: ok=%v, gh ran %d times", ok, calls.Load())
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if u, p, ok := a.Static("github.com"); !ok || u != staticUser || p != "gho_cli" {
				t.Errorf("Static(github.com) = %q, %q, %v", u, p, ok)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || !slices.Equal(masked, []string{"gho_cli"}) {
		t.Errorf("gh ran %d times, masked %q", calls.Load(), masked)
	}
	calls.Store(0)
	for _, b := range []*Auth{
		NewAuth(platform.Info{}, "", env(map[string]string{"DEPS_GITHUB_COM_TOKEN": "env"}), gh("gho_cli")),
		NewAuth(platform.Info{Kind: platform.GitHub, Host: "github.com"}, "platform", env(nil), gh("gho_cli")),
	} {
		if _, p, _ := b.Static("github.com"); p == "gho_cli" {
			t.Errorf("gh token won over a configured one")
		}
	}
	if calls.Load() != 0 {
		t.Errorf("gh ran %d times with a configured token", calls.Load())
	}
	if _, _, ok := NewAuth(platform.Info{}, "", env(nil), gh("")).Static("github.com"); ok {
		t.Error("a gh without login gave a token")
	}
}
