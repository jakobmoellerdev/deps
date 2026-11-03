package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

var ageNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return ageNow.Add(-time.Duration(n) * 24 * time.Hour) }

// timedReleases lists versions released at times (zero = unknown; missing = lookup error)
// and counts ReleaseTime calls per version. Digests are "sha256:<version>".
type timedReleases struct {
	versions []string
	times    map[string]time.Time

	mu    sync.Mutex
	asked map[string]int
}

func (f *timedReleases) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	out := make([]Release, len(f.versions))
	for i, v := range f.versions {
		out[i] = Release{Version: v}
	}
	return out, nil
}

func (*timedReleases) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	return "sha256:" + v, nil
}

func (f *timedReleases) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.asked == nil {
		f.asked = map[string]int{}
	}
	f.asked[v]++
	t, ok := f.times[v]
	if !ok {
		return time.Time{}, errors.New("no time for " + v)
	}
	return t, nil
}

func goTimed() *timedReleases {
	return &timedReleases{
		versions: []string{"v1.0.0", "v1.0.1", "v1.1.0", "v1.2.0", "v2.0.0"},
		times: map[string]time.Time{
			"v1.0.0": daysAgo(100), "v1.0.1": daysAgo(40), "v1.1.0": daysAgo(10), "v1.2.0": daysAgo(1), "v2.0.0": daysAgo(2),
		},
	}
}

// resolveStub resolves the deps of a stub manager against ds under cfgYAML at ageNow.
func resolveStub(t *testing.T, cfgYAML, mgr string, deps []dep.Dep, ds Datasource, sec SecuritySource) (*Result, Trace) {
	t.Helper()
	root, cfg := setup(t, cfgYAML, map[string]string{"stub.txt": "x\n"})
	var tr Trace
	res, err := Resolve(t.Context(), root, cfg, env, Options{
		Managers:    []manager.Manager{stubManager{name: mgr, deps: deps}},
		Datasources: map[string]Datasource{deps[0].Datasource: ds},
		Security:    sec,
		Trace:       func(x Trace) { tr = x },
		Now:         ageNow,
	})
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	return res, tr
}

func tooNew(days int, age string) string {
	return fmt.Sprintf("too new (released %s, minimum %s)", daysAgo(days).Format(time.DateOnly), age)
}

func TestMinimumReleaseAge(t *testing.T) {
	// semver versioning, so the major v2.0.0 is a candidate (go versioning needs /v2).
	goDep := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	tests := []struct {
		name     string
		cfg      string
		want     string // "" = no update
		kind     dep.UpdateType
		rejected map[string][]string // too-new reasons expected in the trace
	}{
		{"off", "", "v2.0.0", dep.Major, nil},
		{"fallback chain to patch", "minimumReleaseAge: 28 days\n", "v1.0.1", dep.Patch, map[string][]string{
			tooNew(2, "28 days"): {"v2.0.0"}, tooNew(1, "28 days"): {"v1.2.0"}, tooNew(10, "28 days"): {"v1.1.0"},
		}},
		{"next minor old enough", "minimumReleaseAge: 5d\n", "v1.1.0", dep.Minor, map[string][]string{
			tooNew(2, "5 days"): {"v2.0.0"}, tooNew(1, "5 days"): {"v1.2.0"},
		}},
		{"hours", "minimumReleaseAge: 36h\n", "v2.0.0", dep.Major, nil},
		{"all too new", "minimumReleaseAge: 2 weeks\nrules: [{updates: [major, minor]}]\n", "", "", map[string][]string{
			tooNew(2, "14 days"): {"v2.0.0"}, tooNew(1, "14 days"): {"v1.2.0"}, tooNew(10, "14 days"): {"v1.1.0"},
		}},
		{"rule disables with 0 days", "minimumReleaseAge: 28 days\nrules: [{names: [example.com/a], minimumReleaseAge: 0 days}]\n", "v2.0.0", dep.Major, nil},
		{"rule sets age", "rules: [{manager: gomod, minimumReleaseAge: '3 days'}]\n", "v1.1.0", dep.Minor, map[string][]string{
			tooNew(2, "3 days"): {"v2.0.0"}, tooNew(1, "3 days"): {"v1.2.0"},
		}},
		{"per-kind override exempts majors", "minimumReleaseAge: 28 days\nrules: [{updateTypes: [major], minimumReleaseAge: '0'}]\n", "v2.0.0", dep.Major, nil},
		{"per-kind age on major and minor only", "rules: [{updateTypes: [major, minor], minimumReleaseAge: 28 days}]\n", "v1.0.1", dep.Patch, map[string][]string{
			tooNew(2, "28 days"): {"v2.0.0"}, tooNew(1, "28 days"): {"v1.2.0"}, tooNew(10, "28 days"): {"v1.1.0"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := goTimed()
			res, tr := resolveStub(t, tc.cfg, "gomod", goDep, ds, nil)
			switch {
			case tc.want == "" && len(res.Updates) != 0:
				t.Fatalf("updates = %+v, want none", res.Updates)
			case tc.want != "" && (len(res.Updates) != 1 || res.Updates[0].NewVersion != tc.want || res.Updates[0].Type != tc.kind):
				t.Fatalf("updates = %+v, want %s (%s)", res.Updates, tc.want, tc.kind)
			}
			got := map[string][]string{}
			for r, vs := range tr.Rejected {
				if strings.HasPrefix(r, "too new") {
					got[r] = vs
				}
			}
			if len(got) != len(tc.rejected) {
				t.Errorf("too-new rejections = %v, want %v", got, tc.rejected)
			}
			for r, vs := range tc.rejected {
				if !slices.Equal(got[r], vs) {
					t.Errorf("rejected[%q] = %v, want %v", r, got[r], vs)
				}
			}
			for v, n := range ds.asked {
				if n != 1 {
					t.Errorf("ReleaseTime(%s) called %d times", v, n)
				}
			}
		})
	}
}

func TestMinimumReleaseAgeUnknownTime(t *testing.T) {
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	for name, times := range map[string]map[string]time.Time{
		"zero time":    {"v1.1.0": {}},
		"lookup error": {},
	} {
		t.Run(name, func(t *testing.T) {
			ds := &timedReleases{versions: []string{"v1.0.0", "v1.1.0"}, times: times}
			res, _ := resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", d, ds, nil)
			if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" {
				t.Fatalf("updates = %+v, want v1.1.0 (unknown release time is allowed)", res.Updates)
			}
		})
	}
	// Without a Timer the age cannot be checked: the update is proposed.
	res, _ := resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", d, goVersions, nil)
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.2.0" {
		t.Fatalf("no timer: updates = %+v, want v1.2.0", res.Updates)
	}
}

// TestMinimumReleaseAgeBound checks that best gives up after maxRetries too-new candidates.
func TestMinimumReleaseAgeBound(t *testing.T) {
	ds := &timedReleases{versions: []string{"v1.0.0"}, times: map[string]time.Time{}}
	for i := 1; i <= maxRetries+10; i++ {
		v := fmt.Sprintf("v1.0.%d", i)
		ds.versions = append(ds.versions, v)
		ds.times[v] = daysAgo(0)
	}
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	ds.times["v1.0.1"] = daysAgo(30) // old enough, but beyond the retry bound
	res, tr := resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", d, ds, nil)
	if len(res.Updates) != 0 {
		t.Fatalf("updates = %+v, want none after %d retries", res.Updates, maxRetries)
	}
	if n := len(tr.Rejected[tooNew(0, "28 days")]); n != maxRetries {
		t.Errorf("too-new rejections = %d, want %d", n, maxRetries)
	}
	if len(ds.asked) != maxRetries {
		t.Errorf("release times asked for %d versions, want %d", len(ds.asked), maxRetries)
	}
}

// TestMinimumReleaseAgeExempt checks that digest refreshes, pins and security updates are
// not held back by the minimum release age.
func TestMinimumReleaseAgeExempt(t *testing.T) {
	img := func(digest string) []dep.Dep {
		return []dep.Dep{{Datasource: "docker", Versioning: "docker", Name: "img", Version: "1.25", Digest: digest, Line: 1}}
	}
	ds := func() *timedReleases {
		return &timedReleases{versions: []string{"1.25", "1.26"}, times: map[string]time.Time{"1.25": daysAgo(90), "1.26": daysAgo(1)}}
	}
	res, _ := resolveStub(t, "minimumReleaseAge: 28 days\n", "docker", img("sha256:old"), ds(), nil)
	if len(res.Updates) != 1 || res.Updates[0].Type != dep.Digest || res.Updates[0].NewDigest != "sha256:1.25" {
		t.Errorf("pinned: updates = %+v, want digest refresh of 1.25", res.Updates)
	}
	res, _ = resolveStub(t, "minimumReleaseAge: 28 days\npin: digest\n", "docker", img(""), ds(), nil)
	if len(res.Updates) != 1 || res.Updates[0].Type != dep.Pin || res.Updates[0].NewDigest != "sha256:1.25" {
		t.Errorf("pin: updates = %+v, want pin of 1.25", res.Updates)
	}
	sec := &fakeSec{vulns: map[string][]Vuln{"example.com/a": {vuln("GO-1", "v1.2.0")}}}
	goDep := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	res, _ = resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", goDep, goTimed(), sec)
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.2.0" || !slices.Equal(ids(&res.Updates[0]), []string{"GO-1"}) {
		t.Errorf("security: updates = %+v, want v1.2.0 fixing GO-1 despite its age", res.Updates)
	}
}
