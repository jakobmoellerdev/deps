package engine

import (
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestCurrentAgeRules checks currentAge rules end to end: the current version's release
// time is only asked when a currentAge rule can match, the policy (ignore, group) follows
// the age, and an unknown time matches no currentAge rule.
func TestCurrentAgeRules(t *testing.T) {
	const ignoredDefault = "ignored by default (indirect; enable with a rule setting ignore: false)"
	tests := []struct {
		name, cfg, version, depType string
		want, group, skip           string // want "" = no update
		asked                       int    // ReleaseTime calls for the current version
		released                    time.Time
	}{
		{name: "no currentAge rule", cfg: "rules: [{names: [example.com/a], group: g}]", version: "v1.0.0", want: "v1.2.0", group: "g"},
		{name: "other selectors do not match", cfg: "rules: [{names: [other], currentAge: '> 60 days', ignore: true}]", version: "v1.0.0", want: "v1.2.0"},
		{name: "no datasource times needed for an excluded name", cfg: "rules: [{names: ['!example.com/*'], currentAge: '> 60 days', ignore: true}]", version: "v1.0.0", want: "v1.2.0"},
		{name: "old current version ignored", cfg: "rules: [{currentAge: '> 60 days', ignore: true}]", version: "v1.0.0", skip: "ignored by rule #0", asked: 1, released: daysAgo(100)},
		{name: "recent current version kept", cfg: "rules: [{currentAge: '> 60 days', ignore: true}]", version: "v1.0.1", want: "v1.2.0", asked: 1, released: daysAgo(40)},
		{name: "unknown release time matches no currentAge rule", cfg: "rules: [{currentAge: '> 60 days', ignore: true}]", version: "v0.9.0", want: "v1.2.0", asked: 1},
		{name: "opts a default-ignored dep in", cfg: "rules: [{depTypes: [indirect], currentAge: '>= 100 days', ignore: false}]", version: "v1.0.0", depType: "indirect", want: "v1.2.0", asked: 1, released: daysAgo(100)},
		{name: "default ignore stays for a recent version", cfg: "rules: [{depTypes: [indirect], currentAge: '>= 100 days', ignore: false}]", version: "v1.0.1", depType: "indirect", skip: ignoredDefault, asked: 1, released: daysAgo(40)},
		{name: "per-kind rule groups old deps", cfg: "rules: [{updateTypes: [minor], currentAge: '> 2 months', group: stale}]", version: "v1.0.0", want: "v1.2.0", group: "stale", asked: 1, released: daysAgo(100)},
		{name: "per-kind rule skips recent deps", cfg: "rules: [{updateTypes: [minor], currentAge: '> 2 months', group: stale}]", version: "v1.1.0", want: "v1.2.0", asked: 1, released: daysAgo(10)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := goTimed()
			ds.versions = ds.versions[:4] // no major: olderThanCurrent would ask the current time too
			d := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: tc.version, DepType: tc.depType, Line: 1}}
			res, tr := resolveStub(t, "separate: none\n"+tc.cfg+"\n", "gomod", d, ds, nil)
			switch {
			case tc.want == "" && len(res.Updates) != 0:
				t.Fatalf("updates = %+v, want none", res.Updates)
			case tc.want != "" && (len(res.Updates) != 1 || res.Updates[0].NewVersion != tc.want || res.Updates[0].Group != tc.group):
				t.Fatalf("updates = %+v, want %s in group %q", res.Updates, tc.want, tc.group)
			}
			if tr.Dep.Skip != tc.skip {
				t.Errorf("skip = %q, want %q", tr.Dep.Skip, tc.skip)
			}
			if n := ds.asked[tc.version]; n != tc.asked {
				t.Errorf("ReleaseTime(%s) called %d times, want %d", tc.version, n, tc.asked)
			}
			if tr.AgeLookup != (tc.asked > 0) || !tr.Released.Equal(tc.released) {
				t.Errorf("trace age lookup %v released %v, want %v %v", tr.AgeLookup, tr.Released, tc.asked > 0, tc.released)
			}
		})
	}
}

// TestCurrentAgeSecurity checks that a dep a currentAge rule opts in gets security updates,
// and one it ignores none.
func TestCurrentAgeSecurity(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, depType, want string
	}{
		{"opted in", "rules: [{depTypes: [indirect], currentAge: '> 60 days', ignore: false, allowedUpdateTypes: [digest]}]", "indirect", "v1.0.1"},
		{"ignored", "rules: [{currentAge: '> 60 days', ignore: true}]", "direct", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := &fakeSec{vulns: map[string][]Vuln{"example.com/a": {vuln("GO-1", "v1.0.1")}}}
			d := []dep.Dep{{Datasource: "go", Versioning: "semver", Name: "example.com/a", Version: "v1.0.0", DepType: tc.depType, Line: 1}}
			res, _ := resolveStub(t, tc.cfg+"\n", "gomod", d, goTimed(), sec)
			if len(sec.asked) != 1 {
				t.Errorf("advisories asked for %v, want the dep", sec.asked)
			}
			switch {
			case tc.want == "" && len(res.Updates) != 0:
				t.Fatalf("updates = %+v, want none", res.Updates)
			case tc.want != "" && (len(res.Updates) != 1 || res.Updates[0].NewVersion != tc.want || len(res.Updates[0].Advisories) != 1):
				t.Fatalf("updates = %+v, want security update to %s", res.Updates, tc.want)
			}
		})
	}
}
