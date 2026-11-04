package customds

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// envtestYAML has the shape of controller-tools' envtest-releases.yaml.
const envtestYAML = `releases:
  v1.36.0:
    envtest-v1.36.0-linux-amd64.tar.gz:
      hash: aa
  v1.35.2:
    envtest-v1.35.2-linux-amd64.tar.gz: {hash: bb}
  v1.37.0-rc.1: {}
  1.20: {}
`

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != "/doc" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReleases(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		spec Spec
		want []string
	}{
		{"envtest keys", envtestYAML, Spec{Format: "yaml", Versions: ".releases | keys", VersionRegex: `^v?(?P<version>\d+\.\d+\.\d+)$`},
			[]string{"1.35.2", "1.36.0"}},
		{"yaml scalars keep their text", envtestYAML, Spec{Format: "yaml", Versions: ".releases|keys"},
			[]string{"1.20", "v1.35.2", "v1.36.0", "v1.37.0-rc.1"}},
		{"default selector", `{"releases":[{"version":"1.0.0"},{"version":"1.1.0"},{"version":null},{"other":1},{"version":"1.0.0"}]}`, Spec{},
			[]string{"1.0.0", "1.1.0"}},
		{"github releases", `[{"tag_name":"v2.0.0","draft":false},{"tag_name":"v1.9.0"}]`, Spec{Format: "json", Versions: "[].tag_name"},
			[]string{"v2.0.0", "v1.9.0"}},
		{"nested fields and numbers keep their text", `{"a":{"b":[{"v":1.50},{"v":2}]}}`, Spec{Versions: ".a.b[].v"},
			[]string{"1.50", "2"}},
		{"object values", `{"x":{"a":"1.0.0"}}`, Spec{Versions: ".x[]"},
			[]string{"1.0.0"}},
		{"identity", `"1.0.0"`, Spec{Versions: "."},
			[]string{"1.0.0"}},
		{"regex without version group", `["release-1.2", "1.3", "junk"]`, Spec{Versions: ".[]", VersionRegex: `\d+\.\d+`},
			[]string{"1.2", "1.3"}},
		{"yaml aliases and nulls", "base: &b ['1.0.0', ~]\nall: [*b, ['2.0.0']]\n", Spec{Format: "yaml", Versions: ".all[][]"},
			[]string{"1.0.0", "2.0.0"}},
		{"plain", "# versions\n1.0.0\n\n  1.1.0  \r\n1.0.0\n", Spec{Format: "plain"},
			[]string{"1.0.0", "1.1.0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := serve(t, c.body, &hits)
			c.spec.URL = srv.URL + "/doc"
			s, err := New(srv.Client(), make(chan struct{}, 1), "x", c.spec)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				rs, err := s.Releases(t.Context(), dep.Dep{Name: "x"}, "")
				if err != nil {
					t.Fatal(err)
				}
				if got := versions(rs); !slices.Equal(got, c.want) {
					t.Fatalf("versions = %v, want %v", got, c.want)
				}
			}
			if hits.Load() != 1 {
				t.Errorf("%d requests, want 1 per run", hits.Load())
			}
		})
	}
}

func TestNewErrors(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Format: "toml"}, `custom.x: format: must be one of json, yaml, plain, got "toml"`},
		{Spec{Format: "plain", Versions: ".a"}, "custom.x: versions: not allowed with format plain"},
		{Spec{Versions: "a"}, `custom.x: versions: selector "a": stage "a" must be keys or start with . or []`},
		{Spec{VersionRegex: "("}, "custom.x: versionRegex: error parsing regexp"},
	} {
		if _, err := New(nil, nil, "x", c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%+v) = %v, want %q", c.spec, err, c.want)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	for _, c := range []struct {
		name, body string
		spec       Spec
		want       string
	}{
		{"bad json", `{`, Spec{}, "unexpected EOF"},
		{"bad yaml", "a: [", Spec{Format: "yaml"}, "yaml:"},
		{"wrong type", `{"releases":"1.0.0"}`, Spec{}, `cannot iterate over a string`},
		{"object result", `{"releases":[{"version":{"a":1}}]}`, Spec{}, "yields an object, want strings"},
		{"no versions", `{"releases":[]}`, Spec{}, "no versions"},
		{"too large", strings.Repeat("1\n", MaxBody/2+1), Spec{Format: "plain"}, "larger than 10 MiB"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := serve(t, c.body, nil)
			c.spec.URL = srv.URL + "/doc"
			s, err := New(srv.Client(), nil, "x", c.spec)
			if err != nil {
				t.Fatal(err)
			}
			rs, err := s.Releases(t.Context(), dep.Dep{}, "")
			if err == nil || rs != nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "custom.x: ") {
				t.Errorf("Releases = %v, %v; want error containing %q", rs, err, c.want)
			}
		})
	}
	srv := serve(t, "", nil)
	s, _ := New(srv.Client(), nil, "x", Spec{URL: srv.URL + "/missing"})
	if _, err := s.Releases(t.Context(), dep.Dep{}, ""); err == nil || !strings.Contains(err.Error(), "GET "+srv.URL+"/missing: HTTP 404") {
		t.Errorf("404: %v", err)
	}
	s, _ = New(srv.Client(), nil, "x", Spec{URL: "http://[::1]:namedport/doc"})
	if _, err := s.Releases(t.Context(), dep.Dep{}, ""); err == nil {
		t.Error("bad URL: want error")
	}
	s, _ = New(&http.Client{Transport: failing{}}, nil, "x", Spec{URL: "http://example.invalid/doc"})
	if _, err := s.Releases(t.Context(), dep.Dep{}, ""); err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Errorf("transport error: %v", err)
	}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, _ = New(srv.Client(), sem, "x", Spec{URL: srv.URL + "/doc"})
	if _, err := s.Releases(ctx, dep.Dep{}, ""); err == nil {
		t.Error("canceled while waiting for the semaphore: want error")
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "envtest"}, "1.0.0"); err == nil || !strings.Contains(err.Error(), "custom.x: digests are not supported") {
		t.Errorf("Digest: %v", err)
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, io.ErrClosedPipe }
