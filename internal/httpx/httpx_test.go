package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// captureLog installs a default slog logger at level writing to the returned buffer.
// Not parallel-safe: it replaces the process-wide default.
func captureLog(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestClientLogging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.Transport.(timeoutTransport).next.(*retryTransport).sleep = func(context.Context, time.Duration) error { return nil }

	get := func(url string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusTeapot {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		}
		return err
	}

	t.Run("debug", func(t *testing.T) {
		buf := captureLog(t, slog.LevelDebug)
		u := strings.Replace(srv.URL, "http://", "http://user:secret@", 1) + "/path"
		if err := get(u); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		for _, want := range []string{`msg="http request"`, "method=GET", "url=http://user:xxxxx@", "/path", "status=418", "rateLimitRemaining=42"} {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "secret") {
			t.Errorf("log leaks password:\n%s", out)
		}
	})

	t.Run("failure", func(t *testing.T) {
		buf := captureLog(t, slog.LevelDebug)
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		if err := get(url); err == nil {
			t.Fatal("request to closed server succeeded")
		}
		out := buf.String()
		if !strings.Contains(out, `msg="http request failed"`) || !strings.Contains(out, "err=") {
			t.Errorf("log = %s", out)
		}
		if n := strings.Count(out, `msg="http retry"`); n != maxRetries {
			t.Errorf("logged %d retries, want %d:\n%s", n, maxRetries, out)
		}
	})

	t.Run("info", func(t *testing.T) {
		buf := captureLog(t, slog.LevelInfo)
		if err := get(srv.URL); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Errorf("logged at info level: %s", buf)
		}
	})
}

func TestClientResumesTLSSessions(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	tr := newTransport()
	tr.TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	c := newClient(tr)

	for i, want := range []bool{false, true} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.TLS.DidResume != want {
			t.Errorf("connection %d: DidResume = %v, want %v", i+1, resp.TLS.DidResume, want)
		}
		tr.CloseIdleConnections() // the next request dials again
	}
}

// TestClientRedirectDropsCredentials covers the headers a datasource sets itself: net/http
// only strips Authorization and friends when a redirect leaves the host.
func TestClientRedirectDropsCredentials(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	record := func(name string) http.HandlerFunc {
		return func(_ http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			seen[name] = r.Header.Clone()
		}
	}
	other := httptest.NewServer(record("other"))
	t.Cleanup(other.Close)
	mux := http.NewServeMux()
	mux.Handle("GET /here", record("here"))
	mux.HandleFunc("GET /to-other", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/here", http.StatusFound)
	})
	mux.HandleFunc("GET /to-here", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/here", http.StatusFound)
	})
	mux.HandleFunc("GET /loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient()
	get := func(path string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, http.NoBody)
		if err != nil {
			return err
		}
		req.Header.Set("JOB-TOKEN", "secret")
		req.Header.Set("Circle-Token", "secret")
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}

	for _, tt := range []struct {
		name, path, at string
		wantToken      bool
	}{
		{"cross host", "/to-other", "other", false},
		{"same host", "/to-here", "here", true},
	} {
		if err := get(tt.path); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		mu.Lock()
		h := seen[tt.at]
		mu.Unlock()
		for _, name := range []string{"JOB-TOKEN", "Circle-Token"} {
			if got := h.Get(name) != ""; got != tt.wantToken {
				t.Errorf("%s: %s sent = %v, want %v", tt.name, name, got, tt.wantToken)
			}
		}
		if h.Get("Accept") != "application/json" {
			t.Errorf("%s: Accept = %q", tt.name, h.Get("Accept"))
		}
	}

	if err := get("/loop"); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("redirect loop = %v, want the redirect limit", err)
	}
}

func TestUserAgent(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })
	Version = "1.2.3"
	if got := UserAgent(); got != "deps/1.2.3" {
		t.Fatalf("UserAgent() = %q", got)
	}
}

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestGitCredentialFill(t *testing.T) {
	t.Run("helper", func(t *testing.T) {
		isolateGit(t)
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
		t.Setenv("GIT_CONFIG_VALUE_0", "!f() { echo username=u; echo password=p; }; f")
		a := NewAuth(platform.Info{}, "", func(string) string { return "" })
		u, p, ok := a.Helper(t.Context(), "git.example.com")
		if !ok || u != "u" || p != "p" {
			t.Fatalf("Helper = %q, %q, %v", u, p, ok)
		}
	})
	t.Run("no helper", func(t *testing.T) {
		isolateGit(t)
		t.Setenv("GIT_CONFIG_COUNT", "0")
		if u, p, ok := gitCredentialFill(t.Context(), "git.example.com"); ok || p != "" {
			t.Fatalf("gitCredentialFill = %q, %q, %v; want no credentials", u, p, ok)
		}
	})
}

func TestRedact(t *testing.T) {
	for raw, want := range map[string]string{
		"https://u:p@h/x": "https://u:xxxxx@h/x",
		"https://h/x?a=1": "https://h/x?a=1",
		"%":               "<invalid URL>",
		"http://[::1":     "<invalid URL>",
	} {
		if got := Redact(raw); got != want {
			t.Errorf("Redact(%q) = %q, want %q", raw, got, want)
		}
	}
}
