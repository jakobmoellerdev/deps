package gradleversion

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// index is a trimmed https://services.gradle.org/versions/all (fetched 2026-11-04; fields
// cut to the ones relevant here): newest first, with a nightly, a release nightly, an rc, a
// milestone and a broken release.
const index = `[
{"version": "9.10.0-20261104002831+0000", "buildTime": "20261104002831+0000", "current": false, "snapshot": true, "nightly": true, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions-snapshots/gradle-9.10.0-20261104002831+0000-bin.zip"},
{"version": "9.9.0-20261103010224+0000", "buildTime": "20261103010224+0000", "current": false, "snapshot": true, "nightly": false, "releaseNightly": true, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions-snapshots/gradle-9.9.0-20261103010224+0000-bin.zip"},
{"version": "9.9.0-rc-1", "buildTime": "20261030141744+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": true, "rcFor": "9.9.0", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-9.9.0-rc-1-bin.zip"},
{"version": "9.9.0-milestone-2", "buildTime": "20260918110406+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "9.9.0", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-9.9.0-milestone-2-bin.zip"},
{"version": "9.8.2", "buildTime": "20261019162926+0000", "current": true, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-9.8.2-bin.zip"},
{"version": "9.8.1", "buildTime": "20261007165746+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-9.8.1-bin.zip"},
{"version": "9.0.0", "buildTime": "20250731163512+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-9.0.0-bin.zip"},
{"version": "8.14.3", "buildTime": "20250704131544+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-8.14.3-bin.zip"},
{"version": "8.11-rc-1", "buildTime": "20241017104024+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "8.11", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-8.11-rc-1-bin.zip"},
{"version": "8.10.2", "buildTime": "20240923212839+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-8.10.2-bin.zip"},
{"version": "8.10", "buildTime": "20240814110745+0000", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-8.10-bin.zip"},
{"version": "7.6.4", "buildTime": "bad", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-7.6.4-bin.zip"},
{"version": "1.0-milestone-4", "buildTime": "20110728103822+0200", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": true, "downloadUrl": "https://services.gradle.org/distributions/gradle-1.0-milestone-4-bin.zip"},
{"version": "0.7", "buildTime": "20090720085013+0200", "current": false, "snapshot": false, "nightly": false, "releaseNightly": false, "activeRc": false, "rcFor": "", "milestoneFor": "", "broken": false, "downloadUrl": "https://services.gradle.org/distributions/gradle-0.7-bin.zip"}
]`

func serve(t *testing.T, status int, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/versions/all" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_GRADLE_VERSIONS_URL" {
			return url
		}
		return ""
	}
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, http.StatusOK, index, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/versions/all"))
	d := dep.Dep{Name: "gradle"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"9.9.0-rc-1", "9.9.0-milestone-2", "9.8.2", "9.8.1", "9.0.0", "8.14.3", "8.11-rc-1", "8.10.2", "8.10", "7.6.4", "0.7"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	if _, err := s.Releases(t.Context(), d, ""); err != nil || hits.Load() != 1 {
		t.Errorf("second call: err %v, %d requests", err, hits.Load())
	}
	for v, want := range map[string]string{
		"8.10.2": "2024-09-23T21:28:39Z", "0.7": "2009-07-20T06:50:13Z", "7.6.4": "", "1.0-milestone-4": "", "5.0": "",
	} {
		got, err := s.ReleaseTime(t.Context(), d, v)
		if err != nil {
			t.Fatal(err)
		}
		if f := got.UTC().Format(time.RFC3339); want == "" && !got.IsZero() || want != "" && f != want {
			t.Errorf("ReleaseTime(%s) = %v, want %q", v, got, want)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
	if _, err := s.Digest(t.Context(), d, "8.10.2"); err == nil {
		t.Error("Digest: want error")
	}
}

func TestDefaultURL(t *testing.T) {
	if s := New(http.DefaultClient, nil, func(string) string { return "" }); s.url != DefaultURL {
		t.Errorf("url = %q", s.url)
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http error", http.StatusBadGateway, "", "HTTP 502"},
		{"not an array", http.StatusOK, `{"version":"1"}`, "malformed index"},
		{"truncated", http.StatusOK, `[{"version":"8.10.2"`, "GET"},
		{"bad element", http.StatusOK, `[{"version":1}]`, "GET"},
		{"only snapshots", http.StatusOK, `[{"version":"9.10.0-20261104002831+0000","snapshot":true}]`, "no releases"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := serve(t, tc.status, tc.body, &hits)
			s := New(srv.Client(), nil, env(srv.URL+"/versions/all"))
			d := dep.Dep{Name: "gradle"}
			if _, err := s.Releases(t.Context(), d, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if _, err := s.ReleaseTime(t.Context(), d, "8.10.2"); err == nil {
				t.Error("ReleaseTime: want error")
			}
		})
	}
}
