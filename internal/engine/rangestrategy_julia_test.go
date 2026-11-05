package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
)

const juliaProject = `name = "App"
uuid = "99999999-0000-0000-0000-000000000000"

[deps]
JSON = "682c06a0-de6a-54ab-a142-c8b1cf79cde6"
Parsers = "69de0a69-1ddd-5017-9359-2bf0b02dc9f0"
HTTP = "cd3eb016-35fb-5094-929b-558a96fad6f3"

[compat]
JSON = "0.21"
Parsers = "2.5, 3"
`

const juliaManifest = `manifest_format = "2.0"

[[deps.JSON]]
uuid = "682c06a0-de6a-54ab-a142-c8b1cf79cde6"
version = "0.21.1"

[[deps.Parsers]]
uuid = "69de0a69-1ddd-5017-9359-2bf0b02dc9f0"
version = "2.8.1"

[[deps.HTTP]]
uuid = "cd3eb016-35fb-5094-929b-558a96fad6f3"
version = "1.10.0"
`

var juliaReleases = fakeGo{
	"JSON":    {"0.21.1", "0.21.4", "1.0.0", "1.2.0"},
	"Parsers": {"2.8.1", "2.8.3", "3.0.0", "4.0.1"},
	"HTTP":    {"1.10.0", "1.10.16"},
}

// TestJuliaRangeStrategy resolves a Julia project without and with its Manifest.toml: auto is
// widen for a package (no manifest), update-lockfile for an application.
func TestJuliaRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		manifest  bool
		want      []string
		compat    []string // the changed compat lines
	}{
		{
			name: "auto package",
			want: []string{"JSON 0.21->1.2.0 major", "Parsers 3->4.0.1 major"},
			// HTTP has no compat entry and no manifest version: skipped
			compat: []string{`JSON = "0.21, 1"`, `Parsers = "2.5, 3, 4"`},
		},
		{
			name: "auto application", manifest: true,
			want: []string{
				"HTTP 1.10.0->1.10.16 patch lockfile", // no compat entry: only the manifest moves
				"JSON 0.21->1.2.0 major", "JSON 0.21.1->0.21.4 patch lockfile",
				"Parsers 2.8.1->2.8.3 patch lockfile", "Parsers 3->4.0.1 major",
			},
			compat: []string{`JSON = "1.2"`, `Parsers = "4"`},
		},
		{
			name: "widen application", cfg: "rangeStrategy: widen\n", manifest: true,
			want:   []string{"JSON 0.21->1.2.0 major", "Parsers 3->4.0.1 major"},
			compat: []string{`JSON = "0.21, 1"`, `Parsers = "2.5, 3, 4"`},
		},
		{
			name: "bump package", cfg: "rangeStrategy: bump\n",
			want:   []string{"JSON 0.21->1.2.0 major", "Parsers 3->4.0.1 major"},
			compat: []string{`JSON = "1.2"`, `Parsers = "4"`},
		},
		{
			name: "pin application", cfg: "rangeStrategy: pin\n", manifest: true,
			// pin leaves the manifest alone: HTTP, without a compat entry, has nothing to pin
			want:   []string{"JSON 0.21->0.21.4 patch", "JSON 0.21->1.2.0 major", "Parsers 3->2.8.3 patch", "Parsers 3->4.0.1 major"},
			compat: []string{`JSON = "=0.21.4"`, `Parsers = "=2.8.3"`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"app/Project.toml": juliaProject}
			if tt.manifest {
				files["app/Manifest.toml"] = juliaManifest
			}
			root, cfg := setup(t, tt.cfg, files)
			o := Options{Managers: []manager.Manager{julia.New()}, Datasources: map[string]Datasource{julia.Datasource: juliaReleases}}
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
			local, _ := Local(manifest) // one version per dep, as plan writes them
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			var lines []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					if !strings.Contains(juliaProject, l+"\n") {
						lines = append(lines, l)
					}
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tt.compat, "\n") {
				t.Errorf("compat changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.compat, "\n"))
			}
		})
	}
}
