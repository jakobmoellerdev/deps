package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/beam"
)

const mixProject = `defmodule App.MixProject do
  use Mix.Project

  defp deps do
    [
      {:jason, "~> 1.3"},
      {:plug, ">= 1.0.0 and < 2.0.0"},
      {:mint, "== 1.5.0", only: :test}
    ]
  end
end
`

const mixLock = `%{
  "jason": {:hex, :jason, "1.3.0", "x", [:mix], [], "hexpm", "y"},
  "plug": {:hex, :plug, "1.10.0", "x", [:mix], [], "hexpm", "y"},
  "mint": {:hex, :mint, "1.5.0", "x", [:mix], [], "hexpm", "y"},
}
`

const gleamProject = `name = "app"

[dependencies]
gleam_json = ">= 1.0.0 and < 2.0.0"
argv = "~> 1.0"
`

const gleamManifest = `packages = [
  { name = "argv", version = "1.0.0", build_tools = ["gleam"], requirements = [], otp_app = "argv", source = "hex", outer_checksum = "A" },
  { name = "gleam_json", version = "1.0.0", build_tools = ["gleam"], requirements = [], otp_app = "gleam_json", source = "hex", outer_checksum = "B" },
]

[requirements]
argv = { version = "~> 1.0" }
gleam_json = { version = ">= 1.0.0 and < 2.0.0" }
`

var hexReleases = fakeGo{
	"jason":      {"1.3.0", "1.4.4", "2.0.0"},
	"plug":       {"1.10.0", "1.16.0", "2.1.0"},
	"mint":       {"1.5.0", "1.6.2"},
	"gleam_json": {"1.0.0", "1.0.1", "2.0.0"},
	"argv":       {"1.0.0", "1.0.2", "2.0.0"},
}

// TestBeamRangeStrategy resolves a mix and a gleam project with their lockfiles: auto is
// update-lockfile for mix (widen for requirements with several comparators) and widen for
// gleam, as Renovate's getRangeStrategy of both managers; bump of a requirement with several
// comparators widens too.
func TestBeamRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		reqs      []string // the changed requirement lines
	}{
		{
			name: "auto",
			want: []string{
				"argv 1.0.0->2.0.0 major", "gleam_json 1.0.0->2.0.0 major",
				"jason 1.3.0->1.4.4 minor lockfile", "jason 1.3.0->2.0.0 major",
				"mint 1.5.0->1.6.2 minor", "plug 1.0.0->2.1.0 major", // widen ranks from the range
			},
			reqs: []string{`gleam_json = ">= 1.0.0 and < 3.0.0"`, `argv = "~> 1.0 or ~> 2.0"`, `      {:jason, "~> 2.0"},`, `      {:plug, ">= 1.0.0 and < 3.0.0"},`, `      {:mint, "== 1.6.2", only: :test}`},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: []string{
				"argv 1.0.0->2.0.0 major", "gleam_json 1.0.0->2.0.0 major",
				"jason 1.3.0->1.4.4 minor", "jason 1.3.0->2.0.0 major",
				"mint 1.5.0->1.6.2 minor", "plug 1.0.0->2.1.0 major", // widen ranks from the range
			},
			// `~> 1.0` cannot be raised for 1.0.2 at its precision, so 1.0.2 is left alone.
			reqs: []string{`gleam_json = ">= 1.0.0 and < 3.0.0"`, `argv = "~> 2.0"`, `      {:jason, "~> 1.4"},`, `      {:plug, ">= 1.0.0 and < 3.0.0"},`, `      {:mint, "== 1.6.2", only: :test}`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"app/mix.exs": mixProject, "app/mix.lock": mixLock, "lib/gleam.toml": gleamProject, "lib/manifest.toml": gleamManifest})
			o := Options{Managers: []manager.Manager{beam.NewMix(), beam.NewGleam()}, Datasources: map[string]Datasource{beam.Datasource: hexReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var manifest []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					manifest = append(manifest, u)
				}
			}
			local, _ := Local(manifest)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			var lines []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					orig := mixProject + gleamProject
					if !strings.Contains(orig, l+"\n") {
						lines = append(lines, l)
					}
				}
			}
			slices.Sort(lines)
			want := slices.Sorted(slices.Values(tt.reqs))
			if strings.Join(lines, "\n") != strings.Join(want, "\n") {
				t.Errorf("requirement changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
