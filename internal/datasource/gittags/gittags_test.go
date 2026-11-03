package gittags

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func pkts(lines ...string) string {
	var b []byte
	for _, l := range lines {
		switch l {
		case "0000", "0001", "0002":
			b = append(b, l...)
		default:
			b = appendPkt(b, l)
		}
	}
	return string(b)
}

const serviceHeader = "# service=git-upload-pack\n"

func noEnv(string) string { return "" }

func newLister(t *testing.T, srv *httptest.Server, auth *httpx.Auth) *Lister {
	t.Helper()
	return New(srv.Client(), auth, make(chan struct{}, 1))
}

func TestTagsV2(t *testing.T) {
	tests := []struct {
		name     string
		caps     []string
		wantBody string
	}{
		{
			name: "agent and object-format advertised",
			caps: []string{"agent=git/2.45.0\n", "ls-refs=unborn\n", "fetch=shallow\n", "object-format=sha1\n"},
			wantBody: pkts("command=ls-refs\n", "agent=deps/dev\n", "object-format=sha1\n", "0001",
				"peel\n", "ref-prefix refs/tags/\n", "0000"),
		},
		{
			name:     "minimal capabilities",
			caps:     []string{"ls-refs\n"},
			wantBody: pkts("command=ls-refs\n", "0001", "peel\n", "ref-prefix refs/tags/\n", "0000"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Git-Protocol") != "version=2" {
					t.Errorf("%s %s: Git-Protocol = %q", r.Method, r.URL, r.Header.Get("Git-Protocol"))
				}
				if ua := r.Header.Get("User-Agent"); ua != "deps/dev" {
					t.Errorf("User-Agent = %q", ua)
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/org/repo/info/refs":
					gets.Add(1)
					if s := r.URL.Query().Get("service"); s != "git-upload-pack" {
						t.Errorf("service = %q", s)
					}
					io.WriteString(w, pkts(append(append([]string{serviceHeader, "0000", "version 2\n"}, tt.caps...), "0000")...))
				case r.Method == http.MethodPost && r.URL.Path == "/org/repo/git-upload-pack":
					if ct := r.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-request" {
						t.Errorf("Content-Type = %q", ct)
					}
					if a := r.Header.Get("Accept"); a != "application/x-git-upload-pack-result" {
						t.Errorf("Accept = %q", a)
					}
					body, _ := io.ReadAll(r.Body)
					if string(body) != tt.wantBody {
						t.Errorf("ls-refs body:\n got %q\nwant %q", body, tt.wantBody)
					}
					io.WriteString(w, pkts(
						"1111111111111111111111111111111111111111 refs/tags/v1.0.0\n",
						"2222222222222222222222222222222222222222 refs/tags/v2 peeled:3333333333333333333333333333333333333333\n",
						"4444444444444444444444444444444444444444 refs/tags/v3",
						"0000"))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			l := newLister(t, srv, nil)
			want := []Tag{
				{Name: "v1.0.0", Commit: "1111111111111111111111111111111111111111"},
				{Name: "v2", Commit: "3333333333333333333333333333333333333333"},
				{Name: "v3", Commit: "4444444444444444444444444444444444444444"},
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					got, err := l.Tags(context.Background(), srv.URL+"/org/repo/")
					if err != nil {
						t.Error(err)
						return
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("Tags = %+v, want %+v", got, want)
					}
				})
			}
			wg.Wait()
			if n := gets.Load(); n != 1 {
				t.Fatalf("info/refs requests = %d, want 1 (deduped)", n)
			}
		})
	}
}

func TestTagsV0Peel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			return
		}
		io.WriteString(w, pkts(serviceHeader, "0000",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa HEAD\x00multi_ack side-band-64k agent=git/2.1\n",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa refs/heads/main\n",
			"1111111111111111111111111111111111111111 refs/tags/v1\n",
			"9999999999999999999999999999999999999999 refs/tags/v1^{}\n",
			"2222222222222222222222222222222222222222 refs/tags/v2.0.0\n",
			"0000"))
	}))
	defer srv.Close()

	got, err := newLister(t, srv, nil).Tags(context.Background(), srv.URL+"/org/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []Tag{
		{Name: "v1", Commit: "9999999999999999999999999999999999999999"},
		{Name: "v2.0.0", Commit: "2222222222222222222222222222222222222222"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tags = %+v, want %+v", got, want)
	}
}

// authServer serves a v2 repository only to Basic user:pass and counts unauthenticated requests.
func authServer(t *testing.T, user, pass string, anon *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			anon.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			io.WriteString(w, pkts(serviceHeader, "0000", "version 2\n", "ls-refs\n", "0000"))
			return
		}
		io.WriteString(w, pkts("1111111111111111111111111111111111111111 refs/tags/v1\n", "0000"))
	}))
}

func TestTagsHelperRetry(t *testing.T) {
	var anon, helperCalls atomic.Int32
	srv := authServer(t, "alice", "secret", &anon)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	auth := httpx.NewAuth(platform.Info{Host: "other.example"}, "tok", noEnv, httpx.WithHelper(
		func(_ context.Context, h string) (string, string, bool) {
			helperCalls.Add(1)
			if h != host {
				t.Errorf("helper host = %q, want %q", h, host)
			}
			return "alice", "secret", true
		}))
	l := newLister(t, srv, auth)
	for _, repo := range []string{"/a/one", "/a/two"} {
		got, err := l.Tags(context.Background(), srv.URL+repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "v1" {
			t.Fatalf("Tags(%s) = %+v", repo, got)
		}
	}
	if n := anon.Load(); n != 2 {
		t.Errorf("unauthenticated requests = %d, want 2 (one GET per repo, POST reuses creds)", n)
	}
	if n := helperCalls.Load(); n != 1 {
		t.Errorf("helper calls = %d, want 1 (cached per host)", n)
	}
}

func TestTagsHelperNoCredentials(t *testing.T) {
	var anon atomic.Int32
	srv := authServer(t, "alice", "secret", &anon)
	defer srv.Close()
	auth := httpx.NewAuth(platform.Info{}, "", noEnv, httpx.WithHelper(
		func(context.Context, string) (string, string, bool) { return "", "", false }))
	_, err := newLister(t, srv, auth).Tags(context.Background(), srv.URL+"/a/b")
	if want := "ls-refs " + srv.URL + "/a/b: HTTP 401"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

func TestTagsStaticAuth(t *testing.T) {
	var anon atomic.Int32
	srv := authServer(t, "x-access-token", "tok", &anon)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	auth := httpx.NewAuth(platform.Info{Host: u.Hostname()}, "tok", noEnv, httpx.WithHelper(
		func(context.Context, string) (string, string, bool) {
			t.Error("helper called despite static credentials")
			return "", "", false
		}))
	if _, err := newLister(t, srv, auth).Tags(context.Background(), srv.URL+"/a/b"); err != nil {
		t.Fatal(err)
	}
	if n := anon.Load(); n != 0 {
		t.Fatalf("unauthenticated requests = %d, want 0", n)
	}
}

func TestTagsErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string // suffix after "ls-refs <url>: "
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "HTTP 404"},
		{"bad length", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "zzzz") }, "malformed pkt-line"},
		{"truncated", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "0040short") }, "malformed pkt-line"},
		{"missing flush", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, pkts(serviceHeader, "0000", "1111111111111111111111111111111111111111 refs/tags/v1\n"))
		}, "malformed pkt-line"},
		{"ls-refs failure", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				io.WriteString(w, pkts(serviceHeader, "0000", "version 2\n", "0000"))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}, "HTTP 500"},
		{"server ERR", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, pkts("ERR access denied\n"))
		}, "access denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, err := newLister(t, srv, nil).Tags(context.Background(), srv.URL+"/a/b")
			if want := "ls-refs " + srv.URL + "/a/b: " + tt.want; err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %s", err, want)
			}
		})
	}
}
