package engine

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// npmPkg is a package of npmRegistry: its versions with their release times, `dist-tags.latest`
// and `modified`.
type npmPkg struct {
	latest   string
	modified time.Time
	times    map[string]time.Time
}

// npmRegistry serves the abbreviated metadata and the full packument (the only one with
// `time`) of pkgs and counts the full packument requests.
func npmRegistry(t *testing.T, pkgs map[string]npmPkg) (string, *atomic.Int32) {
	t.Helper()
	full := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pkgs[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var versions, times []string
		for _, v := range slices.Sorted(maps.Keys(p.times)) {
			versions = append(versions, fmt.Sprintf("%q:{}", v))
			times = append(times, fmt.Sprintf("%q:%q", v, p.times[v].Format(time.RFC3339)))
		}
		vs := "{" + strings.Join(versions, ",") + "}"
		if r.Header.Get("Accept") == "application/json" {
			full.Add(1)
			fmt.Fprintf(w, `{"versions":%s,"time":{%s}}`, vs, strings.Join(times, ","))
			return
		}
		fmt.Fprintf(w, `{"dist-tags":{"latest":%q},"versions":%s,"modified":%q}`, p.latest, vs, p.modified.Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, full
}

// TestNpmMajorsByLatest checks that npm's majors are judged by `dist-tags.latest` from the
// abbreviated metadata: a line above latest's major is rejected and latest's major proposed
// without the full packument, which is only fetched when the current version is above latest
// or a minimumReleaseAge needs release times.
func TestNpmMajorsByLatest(t *testing.T) {
	pkgs := map[string]npmPkg{
		// A mistaken or abandoned line above latest (@stoplight/types 9999.9999.9999).
		"legacy": {latest: "14.1.0", modified: daysAgo(200), times: map[string]time.Time{
			"14.0.0": daysAgo(300), "14.1.0": daysAgo(200), "9999.9999.9998": daysAgo(2000), "9999.9999.9999": daysAgo(2000),
		}},
		// The current release of a maintained old line is newer than latest (node-fetch 2.7.0
		// versus 3.3.2): latest's major is still an upgrade.
		"backport": {latest: "3.3.2", modified: daysAgo(1), times: map[string]time.Time{
			"2.6.0": daysAgo(900), "2.7.0": daysAgo(400), "3.3.2": daysAgo(500),
		}},
		// The current version is above latest: the release times decide.
		"ahead": {latest: "1.5.0", modified: daysAgo(40), times: map[string]time.Time{
			"1.5.0": daysAgo(50), "2.0.0": daysAgo(40), "3.0.0": daysAgo(1000),
		}},
	}
	tests := []struct {
		name, cfg, pkg, version string
		want                    []string
		rejected                map[string][]string
		full                    int32 // full packument requests
	}{
		{"legacy line above latest", "", "legacy", "14.0.0", []string{"14.1.0"},
			map[string][]string{reasonAboveLatest: {"9999.9999.9999", "9999.9999.9998"}}, 0},
		{"major of latest", "", "backport", "2.7.0", []string{"3.3.2"}, nil, 0},
		{"current above latest", "", "ahead", "2.0.0", nil, map[string][]string{reasonOlder: {"3.0.0"}}, 1},
		{"minimum release age within modified", "minimumReleaseAge: 3 days\n", "backport", "2.7.0", []string{"3.3.2"}, nil, 1},
		{"minimum release age before modified", "minimumReleaseAge: 3 days\n", "legacy", "14.0.0", []string{"14.1.0"},
			map[string][]string{reasonAboveLatest: {"9999.9999.9999", "9999.9999.9998"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, full := npmRegistry(t, pkgs)
			ds := npmds.New(http.DefaultClient, nil, func(k string) string {
				if k == "NPM_CONFIG_REGISTRY" {
					return url
				}
				return ""
			})
			d := []dep.Dep{{Datasource: "npm", Versioning: "semver", Name: tt.pkg, Version: tt.version, Line: 1}}
			res, tr := resolveStub(t, tt.cfg, "npm", d, ds, nil)
			var got []string
			for _, u := range res.Updates {
				got = append(got, u.NewVersion)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("updates = %v, want %v", got, tt.want)
			}
			for _, reason := range []string{reasonAboveLatest, reasonOlder} {
				if !slices.Equal(tr.Rejected[reason], tt.rejected[reason]) {
					t.Errorf("rejected[%q] = %v, want %v", reason, tr.Rejected[reason], tt.rejected[reason])
				}
			}
			if n := full.Load(); n != tt.full {
				t.Errorf("full packument requests = %d, want %d", n, tt.full)
			}
		})
	}
}
