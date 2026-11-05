package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// summary renders updates as "<version> <type> <bucket>" ("-" for the empty bucket).
func summary(ups []dep.Update) []string {
	out := make([]string, len(ups))
	for i, u := range ups {
		b := u.Bucket
		if b == "" {
			b = "-"
		}
		out[i] = u.NewVersion + " " + string(u.Type) + " " + b
	}
	return out
}

func TestBucketMatrix(t *testing.T) {
	semDep := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "1.2.3", Line: 1}}
	ds := fakeGo{"example.com/a": {"1.2.2", "1.2.4", "1.2.5", "1.3.0", "1.4.1", "2.0.0", "2.1.0", "3.0.0", "3.1.2", "4.0.0-rc.1"}}
	single := []string{"3.1.2 major -"}
	tests := []struct {
		majorMinor, multipleMajor, minorPatch bool
		want                                  []string
	}{
		{false, false, false, single},
		{false, true, false, single},
		{false, false, true, single},
		{false, true, true, single},
		{true, false, false, []string{"1.4.1 minor -", "3.1.2 major major"}},
		{true, true, false, []string{"1.4.1 minor -", "2.1.0 major v2", "3.1.2 major v3"}},
		{true, false, true, []string{"1.2.5 patch patch", "1.4.1 minor minor", "3.1.2 major major"}},
		{true, true, true, []string{"1.2.5 patch patch", "1.4.1 minor minor", "2.1.0 major v2", "3.1.2 major v3"}},
	}
	for _, tc := range tests {
		name := fmt.Sprintf("majorMinor=%v,multipleMajor=%v,minorPatch=%v", tc.majorMinor, tc.multipleMajor, tc.minorPatch)
		t.Run(name, func(t *testing.T) {
			cfg := fmt.Sprintf("separateMajorMinor: %v\nseparateMultipleMajor: %v\nseparateMinorPatch: %v\n", tc.majorMinor, tc.multipleMajor, tc.minorPatch)
			res, tr := resolveStub(t, cfg, "gomod", semDep, ds, nil)
			if got := summary(res.Updates); !slices.Equal(got, tc.want) {
				t.Errorf("updates = %q, want %q", got, tc.want)
			}
			if len(tr.Updates) != len(tc.want) {
				t.Errorf("trace updates = %+v", tr.Updates)
			}
		})
	}

	t.Run("policy per bucket", func(t *testing.T) {
		res, _ := resolveStub(t, "separateMultipleMajor: true\nrules: [{allow: '< 3'}]\n", "gomod", semDep, ds, nil)
		if got, want := summary(res.Updates), []string{"1.4.1 minor -", "2.1.0 major v2"}; !slices.Equal(got, want) {
			t.Errorf("updates = %q, want %q", got, want)
		}
	})
	t.Run("updateTypes rules decide the buckets of their types", func(t *testing.T) {
		// Majors share the non-major bucket "" (empty here); minors and patches stay apart.
		res, _ := resolveStub(t, "separateMinorPatch: true\nrules: [{updateTypes: [major], separateMajorMinor: false}]\n", "gomod", semDep, ds, nil)
		if got, want := summary(res.Updates), []string{"1.2.5 patch patch", "1.4.1 minor minor", "3.1.2 major -"}; !slices.Equal(got, want) {
			t.Errorf("updates = %q, want %q", got, want)
		}
	})
	t.Run("only a major", func(t *testing.T) {
		res, _ := resolveStub(t, "", "gomod", semDep, fakeGo{"example.com/a": {"2.0.0"}}, nil)
		if got, want := summary(res.Updates), []string{"2.0.0 major major"}; !slices.Equal(got, want) {
			t.Errorf("updates = %q, want %q", got, want)
		}
	})
}

// The release-time guards run per bucket: a too-new candidate falls back within its bucket.
func TestBucketsReleaseAge(t *testing.T) {
	ds := &timedReleases{
		versions: []string{"v1.0.0", "v1.0.1", "v1.1.0", "v1.2.0", "v2.0.0", "v2.1.0", "v3.0.0"},
		times: map[string]time.Time{
			"v1.0.0": daysAgo(100), "v1.0.1": daysAgo(40), "v1.1.0": daysAgo(10), "v1.2.0": daysAgo(1),
			"v2.0.0": daysAgo(60), "v2.1.0": daysAgo(2), "v3.0.0": daysAgo(3),
		},
	}
	semDep := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	res, tr := resolveStub(t, "minimumReleaseAge: 28 days\nseparateMultipleMajor: true\n", "gomod", semDep, ds, nil)
	if got, want := summary(res.Updates), []string{"v1.0.1 patch -", "v2.0.0 major v2"}; !slices.Equal(got, want) {
		t.Errorf("updates = %q, want %q", got, want)
	}
	var tooNew []string
	for r, vs := range tr.Rejected {
		if strings.HasPrefix(r, "too new") {
			tooNew = append(tooNew, vs...)
		}
	}
	slices.Sort(tooNew)
	if want := []string{"v1.1.0", "v1.2.0", "v2.1.0", "v3.0.0"}; !slices.Equal(tooNew, want) {
		t.Errorf("too new = %v, want %v", tooNew, want)
	}
}

func TestBucketsSecurity(t *testing.T) {
	semDep := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "1.2.3", Line: 1}}
	ds := fakeGo{"example.com/a": {"1.2.4", "1.2.5", "1.2.6", "1.3.0", "2.0.0"}}
	advised := func(ups []dep.Update) []string {
		var out []string
		for _, u := range ups {
			if len(u.Advisories) > 0 {
				out = append(out, u.NewVersion+" "+strings.Join(ids(&u), ","))
			}
		}
		return out
	}
	for _, tc := range []struct {
		name, cfg, fix string
		want           []string
		advised        []string
	}{
		{"the lowest update at or above the fix carries the advisories", "separateMinorPatch: true\n", "1.2.5",
			[]string{"1.2.6 patch patch", "1.3.0 minor minor", "2.0.0 major major"}, []string{"1.2.6 GO-1"}},
		{"a minimal fix replaces its bucket's update", "separateMinorPatch: true\nrules: [{allow: '< 1.2.5'}]\n", "1.2.5",
			[]string{"1.2.5 patch patch"}, []string{"1.2.5 GO-1"}},
		{"an update above the fix carries the advisories, whatever its type", "rules: [{allowedUpdateTypes: [major]}]\n", "1.2.5",
			[]string{"2.0.0 major major"}, []string{"2.0.0 GO-1"}},
		{"a fix above every update joins in its own bucket", "separateMinorPatch: true\nrules: [{allow: '< 1.3'}]\n", "1.3.0",
			[]string{"1.2.6 patch patch", "1.3.0 minor minor"}, []string{"1.3.0 GO-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := &fakeSec{vulns: map[string][]Vuln{"example.com/a": {vuln("GO-1", tc.fix)}}}
			res, _ := resolveStub(t, tc.cfg, "gomod", semDep, ds, sec)
			if got := summary(res.Updates); !slices.Equal(got, tc.want) {
				t.Errorf("updates = %q, want %q", got, tc.want)
			}
			if got := advised(res.Updates); !slices.Equal(got, tc.advised) {
				t.Errorf("advised = %q, want %q", got, tc.advised)
			}
		})
	}
}

// staticReleases serves fixed versions per dep name with digest "sha256:<version>".
type staticReleases map[string][]string

func (s staticReleases) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	out := make([]Release, len(s[d.Name]))
	for i, v := range s[d.Name] {
		out[i] = Release{Version: v, Digest: "sha256:" + v}
	}
	return out, nil
}

func (staticReleases) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	return "sha256:" + v, nil
}

// The open-component-model rules: cosign under regex versioning in one group, the Garden
// Linux FIPS image under loose versioning limited to <major>.<patch> tags, with Renovate's
// separateMinorPatch and separateMultipleMajor.
func TestOCMVersioningRules(t *testing.T) {
	const cfg = `separateMinorPatch: true
separateMultipleMajor: true
minimumReleaseAge: 28 days
rules:
  - datasources: [docker]
    names: ['ghcr.io/gardenlinux/gardenlinux/fips']
    allow: '/^\d+\.\d+$/'
    versioning: loose
  - names: ['open-component-model/cosign', 'ghcr.io/open-component-model/cosign']
    group: OCM cosign
    versioning: 'regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$'
    minimumReleaseAge: 0 days
`
	deps := []dep.Dep{
		{Datasource: "docker", Versioning: "docker", Name: "ghcr.io/gardenlinux/gardenlinux/fips", Version: "1877.25", Digest: "sha256:1877.25", Line: 1},
		{Datasource: "docker", Versioning: "docker", Name: "ghcr.io/open-component-model/cosign", Version: "v3.1.4-ocm.6", Digest: "sha256:v3.1.4-ocm.6", Line: 2},
		{Datasource: "docker", Versioning: "semver", Name: "open-component-model/cosign", Version: "v3.1.4+ocm.6", Line: 3},
	}
	ds := staticReleases{
		"ghcr.io/gardenlinux/gardenlinux/fips": {"1592.14", "1877.25", "1877.28", "1877.28.1", "amd64-1877.28", "1877.29-rc1", "latest"},
		"ghcr.io/open-component-model/cosign":  {"v3.1.4-ocm.6", "v3.1.4-ocm.5", "v3.1.5-ocm.1", "v3.1.5", "latest"},
		"open-component-model/cosign":          {"v3.1.4+ocm.6", "v3.1.5+ocm.1", "v3.1.5", "v3.2.0-rc.1"},
	}
	root, c := setup(t, cfg, map[string]string{"stub.txt": "x\n"})
	traces := map[int]Trace{}
	res, err := Resolve(t.Context(), root, c, env, Options{
		Managers:    []manager.Manager{stubManager{name: "docker", deps: deps}},
		Datasources: map[string]Datasource{"docker": ds},
		Trace:       func(x Trace) { traces[x.Dep.Line] = x },
	})
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		from, to := u.FromTo()
		got = append(got, fmt.Sprintf("%s %s -> %s %s %s group=%q versioning=%s", u.Dep.Name, from, to, u.Type, u.Bucket, u.Group, u.Dep.Versioning[:min(5, len(u.Dep.Versioning))]))
	}
	want := []string{
		`ghcr.io/gardenlinux/gardenlinux/fips 1877.25@1877.25 -> 1877.28@1877.28 minor minor group="" versioning=loose`,
		`ghcr.io/open-component-model/cosign v3.1.4-ocm.6@v3.1.4- -> v3.1.5-ocm.1@v3.1.5- patch patch group="OCM cosign" versioning=regex`,
		`open-component-model/cosign v3.1.4+ocm.6 -> v3.1.5+ocm.1 patch patch group="OCM cosign" versioning=regex`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	gl := traces[1]
	if r := gl.Rejected[`policy: outside allow /^\d+\.\d+$/`]; !slices.Equal(r, []string{"1877.28.1"}) {
		t.Errorf("gardenlinux rejected = %v", gl.Rejected)
	}
	if gl.Dep.Versioning != "loose" || gl.Policy.Versioning != "loose" {
		t.Errorf("gardenlinux trace versioning %q / %q", gl.Dep.Versioning, gl.Policy.Versioning)
	}
	var branches []string
	for _, g := range GroupUpdates(res.Updates, "renovate/") {
		branches = append(branches, g.Branch+" "+g.Title)
	}
	if want := []string{
		"renovate/docker/ghcr.io-gardenlinux-gardenlinux-fips-minor chore(deps): update ghcr.io/gardenlinux/gardenlinux/fips to 1877.28",
		"renovate/group/ocm-cosign-patch chore(deps): update OCM cosign (2 dependencies)",
	}; !slices.Equal(branches, want) {
		t.Errorf("groups =\n%s\nwant\n%s", strings.Join(branches, "\n"), strings.Join(want, "\n"))
	}
}

// A rule's versioning applies before the current value is validated: a value the manager's
// scheme cannot parse is updatable under the rule's scheme, and vice versa.
func TestRuleVersioningValidatesCurrent(t *testing.T) {
	deps := []dep.Dep{
		{Datasource: "go", Versioning: "semver", Name: "four", Version: "1.2.3.4", Line: 1},
		{Datasource: "go", Versioning: "semver", Name: "plain", Version: "1.2.3", Line: 2},
	}
	cfg := "rules:\n  - names: [four]\n    versioning: loose\n  - names: [plain]\n    versioning: 'regex:^v(?<major>\\d+)$'\n"
	ds := fakeGo{"four": {"1.2.3.5"}, "plain": {"1.2.4"}}
	root, c := setup(t, cfg, map[string]string{"stub.txt": "x\n"})
	traces := map[string]Trace{}
	res, err := Resolve(t.Context(), root, c, env, Options{
		Managers:    []manager.Manager{stubManager{name: "gomod", deps: deps}},
		Datasources: map[string]Datasource{"go": ds},
		Trace:       func(x Trace) { traces[x.Dep.Name] = x },
	})
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	if got := summary(res.Updates); !slices.Equal(got, []string{"1.2.3.5 patch -"}) {
		t.Errorf("updates = %q", got)
	}
	if p := traces["plain"]; len(res.Skipped) != 1 || p.Dep.Skip != `non-version value "1.2.3"` || !slices.Equal(p.Rules, []int{1}) || p.Policy.Versioning == "" {
		t.Errorf("plain: skipped %+v trace %+v", res.Skipped, p)
	}
}

func TestLocal(t *testing.T) {
	a := dep.Dep{File: "go.mod", Line: 3, RefSpan: dep.Span{Start: 10, End: 16}}
	b := dep.Dep{File: "go.mod", Line: 4, RefSpan: dep.Span{Start: 30, End: 36}}
	c := dep.Dep{File: "x.yml", Line: 1, RefSpan: dep.Span{Start: 5, End: 7}}
	ups := []dep.Update{
		{Dep: a, Type: dep.Patch, NewVersion: "1.2.5", Bucket: "patch"},
		{Dep: a, Type: dep.Minor, NewVersion: "1.4.1", Bucket: "minor"},
		{Dep: a, Type: dep.Major, NewVersion: "2.1.0", Bucket: "v2"},
		{Dep: b, Type: dep.Major, NewVersion: "2.0.0", Bucket: "v2"},
		{Dep: b, Type: dep.Major, NewVersion: "3.0.0", Bucket: "v3"},
		{Dep: c, Type: dep.Digest, NewVersion: "v4"},
	}
	local, separate := Local(ups)
	if got, want := summary(local), []string{"1.4.1 minor minor", "2.0.0 major v2", "v4 digest -"}; !slices.Equal(got, want) {
		t.Errorf("local = %q, want %q", got, want)
	}
	if want := []bool{true, false, true, false, true, false}; !slices.Equal(separate, want) {
		t.Errorf("separate = %v, want %v", separate, want)
	}
	if local, separate := Local(ups[5:]); len(local) != 1 || !slices.Equal(separate, []bool{false}) {
		t.Errorf("one update: %v %v", local, separate)
	}
}

func TestGroupBucketSuffixes(t *testing.T) {
	a := dep.Dep{Manager: "gomod", Name: "example.com/A"}
	b := dep.Dep{Manager: "gomod", Name: "example.com/b"}
	ups := []dep.Update{
		{Dep: a, Type: dep.Minor, NewVersion: "v1.4.0"},
		{Dep: a, Type: dep.Major, NewVersion: "v2.0.0", Bucket: "v2"},
		{Dep: b, Type: dep.Patch, NewVersion: "v1.0.1", Bucket: "patch", Group: "Go deps"},
		{Dep: b, Type: dep.Minor, NewVersion: "v1.1.0", Bucket: "minor", Group: "Go deps"},
		{Dep: a, Type: dep.Major, NewVersion: "v3.0.0", Bucket: "major", Group: "Go deps", Advisories: []dep.Advisory{{ID: "GO-1"}}},
	}
	var got []string
	for _, g := range GroupUpdates(ups, "") {
		got = append(got, g.Branch+" "+g.Title)
	}
	want := []string{
		"deps/gomod/example.com-a chore(deps): update example.com/A to v1.4.0",
		"deps/gomod/example.com-a-major fix(deps): update example.com/A to v3.0.0 [security]",
		"deps/gomod/example.com-a-v2 chore(deps): update example.com/A to v2.0.0",
		"deps/group/go-deps-minor chore(deps): update example.com/b to v1.1.0",
		"deps/group/go-deps-patch chore(deps): update example.com/b to v1.0.1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("groups =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
