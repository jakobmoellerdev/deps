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
			// One bucket, so a too-new major falls back to minors and patches.
			res, tr := resolveStub(t, "separateMajorMinor: false\n"+tc.cfg, "gomod", goDep, ds, nil)
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
			switch {
			case len(tc.rejected) == 0 && len(res.Pending) != 0:
				t.Errorf("pending = %+v, want none", res.Pending)
			case len(tc.rejected) != 0 && (len(res.Pending) != 1 || res.Pending[0].Version != "v2.0.0" || !strings.HasPrefix(res.Pending[0].Reason, "too new")):
				t.Errorf("pending = %+v, want v2.0.0 too new", res.Pending)
			}
			for v, n := range ds.asked {
				if n != 1 {
					t.Errorf("ReleaseTime(%s) called %d times", v, n)
				}
			}
		})
	}
}

// TestMinimumReleaseAgeBehaviour checks both minimumReleaseAgeBehaviour modes for versions
// whose release time is unknown: a zero time, a failed lookup, or a datasource without times.
func TestMinimumReleaseAgeBehaviour(t *testing.T) {
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	const held = "release time unknown (minimum 28 days, timestamp-required)"
	timed := func(times map[string]time.Time) Datasource {
		return &timedReleases{versions: []string{"v1.0.0", "v1.0.1", "v1.1.0"}, times: times}
	}
	zero := map[string]time.Time{"v1.0.1": daysAgo(40), "v1.1.0": {}}
	failed := map[string]time.Time{"v1.0.1": daysAgo(40)} // v1.1.0: lookup error
	tests := []struct {
		name, cfg string
		ds        Datasource
		want      string // "" = no update
		pending   string // held-back version; its reason is held
	}{
		{"optional zero time", "", timed(zero), "v1.1.0", ""},
		{"optional lookup error", "", timed(failed), "v1.1.0", ""},
		{"optional explicit", "minimumReleaseAgeBehaviour: timestamp-optional\n", timed(zero), "v1.1.0", ""},
		{"optional without timer", "", goVersions, "v1.2.0", ""},
		{"required zero time falls back", "minimumReleaseAgeBehaviour: timestamp-required\n", timed(zero), "v1.0.1", "v1.1.0"},
		{"required lookup error falls back", "minimumReleaseAgeBehaviour: timestamp-required\n", timed(failed), "v1.0.1", "v1.1.0"},
		{"required without timer", "minimumReleaseAgeBehaviour: timestamp-required\n", goVersions, "", "v1.2.0"},
		{"required by rule", "rules: [{names: [example.com/a], minimumReleaseAgeBehaviour: timestamp-required}]\n", timed(zero), "v1.0.1", "v1.1.0"},
		{"required for minors only", "rules: [{updateTypes: [minor], minimumReleaseAgeBehaviour: timestamp-required}]\n", timed(zero), "v1.0.1", "v1.1.0"},
		{"rule makes optional", "minimumReleaseAgeBehaviour: timestamp-required\nrules: [{names: [example.com/a], minimumReleaseAgeBehaviour: timestamp-optional}]\n", timed(zero), "v1.1.0", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, tr := resolveStub(t, "minimumReleaseAge: 28 days\n"+tc.cfg, "gomod", d, tc.ds, nil)
			if tc.want == "" && len(res.Updates) != 0 || tc.want != "" && (len(res.Updates) != 1 || res.Updates[0].NewVersion != tc.want) {
				t.Fatalf("updates = %+v, want %q", res.Updates, tc.want)
			}
			if tc.pending == "" {
				if len(res.Pending) != 0 || len(tr.Rejected[held]) != 0 {
					t.Errorf("pending = %+v, rejected = %v; want none", res.Pending, tr.Rejected)
				}
				return
			}
			if len(res.Pending) != 1 || res.Pending[0].Version != tc.pending || res.Pending[0].Reason != held {
				t.Errorf("pending = %+v, want %s %q", res.Pending, tc.pending, held)
			}
			if got := tr.Rejected[held]; len(got) == 0 || got[0] != tc.pending {
				t.Errorf("rejected[%q] = %v, want %s first", held, got, tc.pending)
			}
		})
	}
	// Without minimumReleaseAge, minimumReleaseAgeBehaviour has no effect.
	res, _ := resolveStub(t, "minimumReleaseAgeBehaviour: timestamp-required\n", "gomod", d, timed(zero), nil)
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" || len(res.Pending) != 0 {
		t.Errorf("no age: updates = %+v, pending = %+v; want v1.1.0", res.Updates, res.Pending)
	}
}

// TestMinimumReleaseAgeUnknownBound checks that a bucket gives up after maxUnknownTimes
// undated candidates under timestamp-required, without prefetching further times.
func TestMinimumReleaseAgeUnknownBound(t *testing.T) {
	ds := &timedReleases{versions: []string{"v1.0.0"}, times: map[string]time.Time{}}
	for i := 1; i <= 10; i++ {
		v := fmt.Sprintf("v1.0.%d", i)
		ds.versions = append(ds.versions, v)
		ds.times[v] = time.Time{}
	}
	ds.times["v1.0.1"] = daysAgo(30) // old enough, but beyond the bound
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	res, tr := resolveStub(t, "minimumReleaseAge: 28 days\nminimumReleaseAgeBehaviour: timestamp-required\n", "gomod", d, ds, nil)
	if len(res.Updates) != 0 {
		t.Fatalf("updates = %+v, want none", res.Updates)
	}
	if got := tr.Rejected["release time unknown (minimum 28 days, timestamp-required)"]; !slices.Equal(got, []string{"v1.0.10", "v1.0.9", "v1.0.8"}) {
		t.Errorf("held = %v", got)
	}
	if len(ds.asked) != maxUnknownTimes {
		t.Errorf("release times asked for %v, want %d versions", ds.asked, maxUnknownTimes)
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
// not held back by the minimum release age, also under timestamp-required without times.
func TestMinimumReleaseAgeExempt(t *testing.T) {
	img := func(digest string) []dep.Dep {
		return []dep.Dep{{Datasource: "docker", Versioning: "docker", Name: "img", Version: "1.25", Digest: digest, Line: 1}}
	}
	goDep := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	sec := &fakeSec{vulns: map[string][]Vuln{"example.com/a": {vuln("GO-1", "v1.2.0")}}}
	for _, mode := range []struct {
		cfg          string
		docker, gods func() *timedReleases
	}{
		{
			cfg: "minimumReleaseAge: 28 days\n",
			docker: func() *timedReleases {
				return &timedReleases{versions: []string{"1.25", "1.26"}, times: map[string]time.Time{"1.25": daysAgo(90), "1.26": daysAgo(1)}}
			},
			gods: goTimed,
		},
		{
			cfg:    "minimumReleaseAge: 28 days\nminimumReleaseAgeBehaviour: timestamp-required\n",
			docker: func() *timedReleases { return &timedReleases{versions: []string{"1.25", "1.26"}} },
			gods:   func() *timedReleases { ds := goTimed(); ds.times = nil; return ds },
		},
	} {
		res, _ := resolveStub(t, mode.cfg, "docker", img("sha256:old"), mode.docker(), nil)
		if len(res.Updates) != 1 || res.Updates[0].Type != dep.Digest || res.Updates[0].NewDigest != "sha256:1.25" {
			t.Errorf("%spinned: updates = %+v, want digest refresh of 1.25", mode.cfg, res.Updates)
		}
		res, _ = resolveStub(t, mode.cfg+"pin: digest\n", "docker", img(""), mode.docker(), nil)
		if len(res.Updates) != 1 || res.Updates[0].Type != dep.Pin || res.Updates[0].NewDigest != "sha256:1.25" {
			t.Errorf("%spin: updates = %+v, want pin of 1.25", mode.cfg, res.Updates)
		}
		res, _ = resolveStub(t, mode.cfg, "gomod", goDep, mode.gods(), sec)
		if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.2.0" || !slices.Equal(ids(&res.Updates[0]), []string{"GO-1"}) {
			t.Errorf("%ssecurity: updates = %+v, want v1.2.0 fixing GO-1 despite its age", mode.cfg, res.Updates)
		}
	}
}

// slowTimes reports every version as released yesterday and records how many lookups
// overlap, so the prefetch after the first too-new rejection is observable.
type slowTimes struct {
	timedReleases
	inFlight, maxInFlight int
}

func (s *slowTimes) ReleaseTime(ctx context.Context, d dep.Dep, v string) (time.Time, error) {
	s.mu.Lock()
	s.inFlight++
	s.maxInFlight = max(s.maxInFlight, s.inFlight)
	s.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return daysAgo(1), nil
}

// After one too-new rejection, the next candidates' release times are fetched concurrently
// instead of one round trip per candidate; the outcome is unchanged.
func TestMinimumReleaseAgePrefetch(t *testing.T) {
	var versions []string
	for i := range 12 {
		versions = append(versions, fmt.Sprintf("v1.%d.0", i))
	}
	ds := &slowTimes{versions: versions}
	goDep := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	res, tr := resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", goDep, ds, nil)
	if len(res.Updates) != 0 {
		t.Fatalf("updates = %+v, want none (all too new)", res.Updates)
	}
	if n := len(tr.Rejected[tooNew(1, "28 days")]); n != 11 {
		t.Errorf("too-new rejections = %d, want 11", n)
	}
	if ds.maxInFlight < 2 {
		t.Errorf("release-time lookups never overlapped (max in flight %d)", ds.maxInFlight)
	}
}

// boundedReleases is a timedReleases that also bounds every release time (ReleaseBound).
type boundedReleases struct {
	*timedReleases
	bound time.Time
}

func (b boundedReleases) ReleasedBy(dep.Dep) time.Time { return b.bound }

// TestMinimumReleaseAgeReleaseBound checks that a release bound at least minimumReleaseAge old
// spares the per-version lookups and that the chosen update never depends on it.
func TestMinimumReleaseAgeReleaseBound(t *testing.T) {
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	tests := []struct {
		name   string
		newest int // days since v1.2.0 was released
		bound  time.Time
		want   string
		asked  bool
	}{
		{"bound old enough", 30, daysAgo(30), "v1.2.0", false},
		{"bound exactly the minimum age", 28, daysAgo(28), "v1.2.0", false},
		{"bound within the window", 1, daysAgo(1), "v1.1.0", true},
		{"bound unknown", 30, time.Time{}, "v1.2.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := boundedReleases{&timedReleases{
				versions: []string{"v1.0.0", "v1.1.0", "v1.2.0"},
				times:    map[string]time.Time{"v1.0.0": daysAgo(100), "v1.1.0": daysAgo(40), "v1.2.0": daysAgo(tt.newest)},
			}, tt.bound}
			res, _ := resolveStub(t, "minimumReleaseAge: 28 days\n", "gomod", d, ds, nil)
			if len(res.Updates) != 1 || res.Updates[0].NewVersion != tt.want {
				t.Fatalf("updates = %+v, want %s", res.Updates, tt.want)
			}
			if asked := len(ds.asked) > 0; asked != tt.asked {
				t.Errorf("release times asked = %v, want %v", ds.asked, tt.asked)
			}
		})
	}
}

// A dep written without the releases' "v" (Renovate's `v?(?<currentValue>…)` over module
// versions) is aged by the release it stands for: v1.2.0 is looked up for 1.2.0.
func TestMinimumReleaseAgeVPrefixless(t *testing.T) {
	d := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "1.0.0", Line: 1}}
	res, _ := resolveStub(t, "separateMajorMinor: false\nminimumReleaseAge: 28 days\n", "custom", d, goTimed(), nil)
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "1.0.1" {
		t.Fatalf("updates = %+v, want 1.0.1 (newer ones are too new)", res.Updates)
	}
}
