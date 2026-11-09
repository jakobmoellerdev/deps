package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
)

const podfile = `platform :ios, '13.0'
target 'App' do
  pod 'Alamofire', '~> 5.8'
  pod 'SwiftyJSON', '5.0.1'
  pod 'Firebase/Core', '~> 10.18.0'
  pod 'Presentr', :git => 'https://github.com/o/Presentr.git', :tag => 'v1.9.1'
end
`

const podfileLock = `PODS:
  - Alamofire (5.8.1)
  - Firebase/Core (10.18.0):
    - Firebase/CoreOnly
  - Firebase/CoreOnly (10.18.0)
  - SwiftyJSON (5.0.1)

COCOAPODS: 1.16.2
`

var podReleases = fakeGo{
	"Alamofire":     {"5.8.0", "5.8.1", "5.9.1", "6.0.0"},
	"SwiftyJSON":    {"5.0.1", "5.0.2"},
	"Firebase/Core": {"10.18.0", "10.18.1", "10.19.0"},
}

var podTags = fakeGo{"Presentr": {"v1.9.1", "v1.10.0"}}

// TestCocoaPodsRangeStrategy resolves a Podfile with its Podfile.lock under rangeStrategy auto
// (replace, as in Renovate) and update-lockfile, and builds the Podfile changes.
func TestCocoaPodsRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		lines     []string // the changed lines
	}{
		{
			name:  "auto",
			want:  []string{"Alamofire 5.8->6.0.0 major", "Firebase/Core 10.18.0->10.19.0 minor", "Presentr v1.9.1->v1.10.0 minor", "SwiftyJSON 5.0.1->5.0.2 patch"},
			lines: []string{"pod 'Alamofire', '~> 6.0'", "pod 'SwiftyJSON', '5.0.2'", "pod 'Firebase/Core', '~> 10.19.0'", "pod 'Presentr', :git => 'https://github.com/o/Presentr.git', :tag => 'v1.10.0'"},
		},
		{
			name: "update-lockfile", cfg: "rangeStrategy: update-lockfile\n",
			want: []string{
				"Alamofire 5.8->6.0.0 major", "Alamofire 5.8.1->5.9.1 minor lockfile",
				"Firebase/Core 10.18.0->10.19.0 minor", // 10.18.1 shares the non-major PR bucket
				"Presentr v1.9.1->v1.10.0 minor", "SwiftyJSON 5.0.1->5.0.2 patch",
			},
			lines: []string{"pod 'Alamofire', '~> 6.0'", "pod 'SwiftyJSON', '5.0.2'", "pod 'Firebase/Core', '~> 10.19.0'", "pod 'Presentr', :git => 'https://github.com/o/Presentr.git', :tag => 'v1.10.0'"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"ios/Podfile": podfile, "ios/Podfile.lock": podfileLock})
			o := Options{Managers: []manager.Manager{bundler.NewCocoaPods()}, Datasources: map[string]Datasource{bundler.PodDatasource: podReleases, bundler.GitDatasource: podTags}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var updates []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					updates = append(updates, u)
				}
			}
			local, _ := Local(updates)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			orig := map[string]bool{}
			for l := range strings.SplitSeq(podfile, "\n") {
				orig[l] = true
			}
			var lines []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					if !orig[l] {
						lines = append(lines, strings.TrimSpace(l))
					}
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tt.lines, "\n") {
				t.Errorf("Podfile changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.lines, "\n"))
			}
		})
	}
}
