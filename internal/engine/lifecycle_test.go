package engine

import (
	"slices"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// deprecatedReleases is a timedReleases whose registry deprecates versions (DeprecationSource).
type deprecatedReleases struct {
	*timedReleases
	why map[string]string
}

func (d deprecatedReleases) Deprecation(_ dep.Dep, v string) string { return d.why[v] }

func TestLifecycleWarnings(t *testing.T) {
	d := []dep.Dep{{Datasource: "go", Versioning: "go", Name: "example.com/a", Version: "v1.0.0", Line: 1}}
	times := map[string]time.Time{"v1.0.0": daysAgo(900), "v1.1.0": daysAgo(800), "v1.2.0-rc.1": daysAgo(10)}
	versions := []string{"v1.0.0", "v1.1.0", "v1.2.0-rc.1"}
	tests := []struct {
		name  string
		cfg   string
		ds    Datasource
		want  []Warning
		asked []string // release times looked up
	}{
		{
			name: "off by default",
			ds:   &timedReleases{versions: versions, times: times},
		},
		{
			name:  "newest stable release too old",
			cfg:   "abandonmentThreshold: 2 years\n",
			ds:    &timedReleases{versions: versions, times: times},
			want:  []Warning{{Kind: WarnAbandoned, Reason: "newest release v1.1.0 published 2024-08-25 (abandonmentThreshold 730 days)"}},
			asked: []string{"v1.1.0"},
		},
		{
			name:  "newest release recent enough",
			cfg:   "abandonmentThreshold: 3 years\n",
			ds:    &timedReleases{versions: versions, times: times},
			asked: []string{"v1.1.0"},
		},
		{
			name: "release bound old enough: no lookup",
			cfg:  "abandonmentThreshold: 18 months\n",
			ds:   boundedReleases{&timedReleases{versions: versions, times: times}, daysAgo(700)},
			want: []Warning{{Kind: WarnAbandoned, Reason: "no release since 2024-12-03 (abandonmentThreshold 540 days)"}},
		},
		{
			name:  "release bound recent: newest release looked up",
			cfg:   "abandonmentThreshold: 2 years\n",
			ds:    boundedReleases{&timedReleases{versions: versions, times: times}, daysAgo(1)},
			want:  []Warning{{Kind: WarnAbandoned, Reason: "newest release v1.1.0 published 2024-08-25 (abandonmentThreshold 730 days)"}},
			asked: []string{"v1.1.0"},
		},
		{
			name: "rule turns it off",
			cfg:  "abandonmentThreshold: 1 year\nrules: [{names: [example.com/a], abandonmentThreshold: 0 days}]\n",
			ds:   &timedReleases{versions: versions, times: times},
		},
		{
			name: "deprecated installed version",
			ds:   deprecatedReleases{&timedReleases{versions: versions, times: times}, map[string]string{"v1.0.0": "version v1.0.0 deprecated: broken"}},
			want: []Warning{{Kind: WarnDeprecated, Reason: "version v1.0.0 deprecated: broken"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := resolveStub(t, tt.cfg, "gomod", d, tt.ds, nil)
			type kindReason struct{ kind, reason string }
			var got, want []kindReason
			for _, w := range res.Warnings {
				if w.Dep.Name != "example.com/a" {
					t.Errorf("warning dep = %q", w.Dep.Name)
				}
				got = append(got, kindReason{w.Kind, w.Reason})
			}
			for _, w := range tt.want {
				want = append(want, kindReason{w.Kind, w.Reason})
			}
			if !slices.Equal(got, want) {
				t.Errorf("warnings = %+v, want %+v", res.Warnings, tt.want)
			}
			if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" {
				t.Fatalf("updates = %+v, want v1.1.0", res.Updates)
			}
			if got := res.Updates[0].Warnings; !slices.Equal(got, warningTexts(tt.want)) {
				t.Errorf("update warnings = %q, want %q", got, warningTexts(tt.want))
			}
			var asked []string
			switch ds := tt.ds.(type) {
			case *timedReleases:
				asked = keys(ds.asked)
			case boundedReleases:
				asked = keys(ds.asked)
			}
			if !slices.Equal(asked, tt.asked) {
				t.Errorf("release times asked = %v, want %v", asked, tt.asked)
			}
		})
	}
}

func keys(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
