package httpx

import (
	"context"
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
