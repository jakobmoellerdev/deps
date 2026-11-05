package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
)

const bundlerGemfile = `source "https://rubygems.org"
gemspec
gem "rails", "~> 7.1"
gem "rack", ">= 2.2", "< 3"
gem "puma", "6.4.2"
gem "rake"
`

const bundlerGemspec = `Gem::Specification.new do |s|
  s.add_dependency "nokogiri", "~> 1.16"
  s.add_development_dependency "rspec", "~> 3.12"
end
`

const bundlerLock = `GEM
  remote: https://rubygems.org/
  specs:
    nokogiri (1.16.0)
    puma (6.4.2)
    rack (2.2.8)
    rails (7.1.3)
    rake (13.0.6)

PLATFORMS
  ruby

DEPENDENCIES
  nokogiri (~> 1.16)
  puma (= 6.4.2)
  rack (>= 2.2, < 3)
  rails (~> 7.1)
  rake

BUNDLED WITH
   2.5.22
`

var bundlerReleases = fakeGo{
	"rails":    {"7.1.3", "7.2.1", "8.0.1"},
	"rack":     {"2.2.8", "2.2.9", "3.1.0"},
	"puma":     {"6.4.2", "6.5.0"},
	"rake":     {"13.0.6", "13.2.1"},
	"nokogiri": {"1.16.0", "1.18.2", "2.0.0"},
	"rspec":    {"3.12.0", "3.13.0", "4.0.0"},
}

// TestBundlerRangeStrategy resolves a Gemfile with its Gemfile.lock and a gemspec under each
// rangeStrategy and builds the manifest changes: auto is update-lockfile for the Gemfile and
// replace for the gemspec.
func TestBundlerRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		gemfile   []string // the changed requirement lines
		gemspec   []string
	}{
		{
			name: "auto",
			want: []string{
				"nokogiri 1.16->2.0.0 major", // the gemspec replaces: 1.18.2 is admitted
				"puma 6.4.2->6.5.0 minor",
				"rack 2.2->3.1.0 major", "rack 2.2.8->2.2.9 patch lockfile",
				"rails 7.1->8.0.1 major", "rails 7.1.3->7.2.1 minor lockfile",
				"rake 13.0.6->13.2.1 minor lockfile",
				"rspec 3.12->4.0.0 major",
			},
			gemfile: []string{`gem "rails", "~> 8.0"`, `gem "rack", ">= 3.1", "< 4"`, `gem "puma", "6.5.0"`},
			gemspec: []string{`s.add_dependency "nokogiri", "~> 2.0"`, `s.add_development_dependency "rspec", "~> 4.0"`},
		},
		{
			name: "update-lockfile", cfg: "rangeStrategy: update-lockfile\n",
			want: []string{
				"nokogiri 1.16->2.0.0 major", "nokogiri 1.16.0->1.18.2 minor lockfile",
				"puma 6.4.2->6.5.0 minor",
				"rack 2.2->3.1.0 major", "rack 2.2.8->2.2.9 patch lockfile",
				"rails 7.1->8.0.1 major", "rails 7.1.3->7.2.1 minor lockfile",
				"rake 13.0.6->13.2.1 minor lockfile",
				"rspec 3.12->4.0.0 major", // not locked: admitted versions are not proposed
			},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: []string{
				"nokogiri 1.16->1.18.2 minor", "nokogiri 1.16->2.0.0 major",
				"puma 6.4.2->6.5.0 minor",
				"rack 2.2->2.2.9 patch", "rack 2.2->3.1.0 major",
				"rails 7.1->7.2.1 minor", "rails 7.1->8.0.1 major",
				"rake 13.0.6->13.2.1 minor lockfile", // no requirement to raise
				"rspec 3.12->3.13.0 minor", "rspec 3.12->4.0.0 major",
			},
			gemfile: []string{`gem "rails", "~> 7.2", ">= 7.2.1"`, `gem "rack", ">= 2.2.9", "< 3"`, `gem "puma", "6.5.0"`},
			gemspec: []string{`s.add_dependency "nokogiri", "~> 1.18", ">= 1.18.2"`, `s.add_development_dependency "rspec", "~> 3.13"`},
		},
		{
			name: "widen", cfg: "rangeStrategy: widen\n",
			want: []string{
				"nokogiri 1.16->2.0.0 major",
				"puma 6.4.2->6.5.0 minor",
				"rack 2.2->3.1.0 major",
				"rails 7.1->8.0.1 major",
				"rspec 3.12->4.0.0 major",
			},
			gemfile: []string{`gem "rails", ">= 7.1", "< 9"`, `gem "rack", ">= 2.2", "< 4"`, `gem "puma", "6.5.0"`},
			gemspec: []string{`s.add_dependency "nokogiri", ">= 1.16", "< 3"`, `s.add_development_dependency "rspec", ">= 3.12", "< 5"`},
		},
		{
			name: "pin", cfg: "rangeStrategy: pin\n",
			want: []string{
				"nokogiri 1.16->1.18.2 minor", "nokogiri 1.16->2.0.0 major",
				"puma 6.4.2->6.5.0 minor",
				"rack 2.2->2.2.9 patch", "rack 2.2->3.1.0 major",
				"rails 7.1->7.2.1 minor", "rails 7.1->8.0.1 major",
				"rspec \"~> 3.12\"->3.13.0 pin", "rspec 3.12->4.0.0 major", // rake: no requirement to pin, and pin leaves the lock alone
			},
			gemfile: []string{`gem "rails", "= 7.2.1"`, `gem "rack", "= 2.2.9"`, `gem "puma", "6.5.0"`},
			gemspec: []string{`s.add_dependency "nokogiri", "= 1.18.2"`, `s.add_development_dependency "rspec", "= 3.13.0"`},
		},
		{
			name: "in-range-only", cfg: "rangeStrategy: in-range-only\n",
			want: []string{
				"nokogiri 1.16->1.18.2 minor",
				"rack 2.2->2.2.9 patch",
				"rails 7.1->7.2.1 minor",
				"rake 13.0.6->13.2.1 minor lockfile",
				"rspec 3.12->3.13.0 minor",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"app/Gemfile": bundlerGemfile, "app/app.gemspec": bundlerGemspec, "app/Gemfile.lock": bundlerLock})
			o := Options{Managers: []manager.Manager{bundler.New()}, Datasources: map[string]Datasource{bundler.Datasource: bundlerReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if tt.gemfile == nil && tt.gemspec == nil {
				return
			}
			var manifest []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					manifest = append(manifest, u)
				}
			}
			local, _ := Local(manifest) // one version per dep, as plan writes them
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			sources := map[string]string{"app/Gemfile": bundlerGemfile, "app/app.gemspec": bundlerGemspec}
			files := maps.Clone(sources)
			for _, c := range changes {
				files[c.Path] = string(c.New)
			}
			for path, want := range map[string][]string{"app/Gemfile": tt.gemfile, "app/app.gemspec": tt.gemspec} {
				orig := map[string]bool{}
				for l := range strings.SplitSeq(sources[path], "\n") {
					orig[l] = true
				}
				var lines []string
				for l := range strings.SplitSeq(files[path], "\n") {
					if !orig[l] {
						lines = append(lines, strings.TrimSpace(l))
					}
				}
				if strings.Join(lines, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s changes\n%s\nwant\n%s", path, strings.Join(lines, "\n"), strings.Join(want, "\n"))
				}
			}
		})
	}
}
