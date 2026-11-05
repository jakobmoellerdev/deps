package orb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// testdata/circleci-node.json is the response for circleci/node (fetched 2026-11-05, versions
// cut to the newest twelve).
func serve(t *testing.T, hits *atomic.Int32, check func(r *http.Request)) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/circleci-node.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if check != nil {
			check(r)
		}
		switch {
		case r.URL.Path != "/api/v3/orb/packages" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/"):
			http.NotFound(w, r)
		case r.URL.Query().Get("filter[name]") == "circleci/node":
			w.Write(body)
		case r.URL.Query().Get("filter[name]") == "circleci/empty":
			w.Write([]byte(`{"data":[{"attributes":{"name":"circleci/empty"},"references":{"orb_versions":[]}}]}`))
		case r.URL.Query().Get("filter[name]") == "circleci/broken":
			w.Write([]byte(`{"data":`))
		case r.URL.Query().Get("filter[name]") == "circleci/down":
			w.WriteHeader(http.StatusBadGateway)
		default:
			// The filter also matches other names; only an exact name answers.
			w.Write([]byte(`{"data":[{"attributes":{"name":"circleci/node-extra"},"references":{"orb_versions":[{"attributes":{"version":"1.0.0"}}]}}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(vals map[string]string) func(string) string { return func(k string) string { return vals[k] } }

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, func(r *http.Request) {
		if r.Header.Get("Circle-Token") != "" || r.Header.Get("Authorization") != "" {
			t.Error("token sent without DEPS_CIRCLECI_TOKEN")
		}
	})
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_CIRCLECI_URL": srv.URL + "/"}))
	d := dep.Dep{Name: "circleci/node"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"7.2.1", "7.2.0", "7.1.1", "7.1.0", "7.0.0", "6.3.0", "6.2.0", "6.1.0", "6.0.0", "5.3.0", "5.2.0", "5.1.1",
		"7.2", "7", "7.1", "7.0", "6.3", "6", "6.2", "6.1", "6.0", "5.3", "5", "5.2", "5.1"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Errorf("releases = %v\nwant %v", got, want)
	}
	for v, ts := range map[string]string{"7.2.1": "2025-10-20T17:50:48.733Z", "7": "2024-12-05T21:24:24.564Z", "6.0": "2024-08-28T16:27:15.968Z"} {
		got, err := s.ReleaseTime(t.Context(), d, v)
		want, _ := time.Parse(time.RFC3339, ts)
		if err != nil || !got.Equal(want) {
			t.Errorf("ReleaseTime(%s) = %v, %v; want %v", v, got, err, want)
		}
	}
	if got, _ := s.ReleaseTime(t.Context(), d, "9.9.9"); !got.IsZero() {
		t.Errorf("ReleaseTime(unknown) = %v", got)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}
	if _, err := s.Digest(t.Context(), d, "7.2.1"); err == nil {
		t.Error("Digest: want error")
	}
}

func TestToken(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, func(r *http.Request) {
		if r.Header.Get("Circle-Token") != "tok" || r.Header.Get("Authorization") != "tok" {
			t.Errorf("headers = %v", r.Header)
		}
	})
	s := New(srv.Client(), nil, env(map[string]string{"DEPS_CIRCLECI_URL": srv.URL, "DEPS_CIRCLECI_TOKEN": "tok"}))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "circleci/node"}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, nil)
	s := New(srv.Client(), nil, env(map[string]string{"DEPS_CIRCLECI_URL": srv.URL}))
	for name, want := range map[string]string{
		"circleci/missing": "orb circleci/missing not found",
		"circleci/empty":   "orb circleci/empty has no versions",
		"circleci/broken":  "malformed response",
		"circleci/down":    "HTTP 502",
	} {
		_, err := s.Releases(t.Context(), dep.Dep{Name: name}, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("%s: ReleaseTime: want error", name)
		}
	}
	if New(http.DefaultClient, nil, env(nil)).base != DefaultURL {
		t.Error("default base")
	}
	bad := New(srv.Client(), nil, env(map[string]string{"DEPS_CIRCLECI_URL": "http://[::1"}))
	if _, err := bad.Releases(t.Context(), dep.Dep{Name: "circleci/node"}, ""); err == nil {
		t.Error("invalid URL: want error")
	}
}
