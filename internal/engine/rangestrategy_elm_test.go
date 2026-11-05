package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/elm"
)

const elmPackage = `{
    "type": "package",
    "name": "me/pkg",
    "version": "1.0.0",
    "elm-version": "0.19.0 <= v < 0.20.0",
    "dependencies": {
        "elm/core": "1.0.0 <= v < 2.0.0",
        "elm/json": "1.0.0 <= v < 2.0.0"
    },
    "test-dependencies": {
        "elm-explorations/test": "1.2.2 <= v < 2.0.0"
    }
}
`

const elmApplication = `{
    "type": "application",
    "source-directories": ["src"],
    "elm-version": "0.19.1",
    "dependencies": {
        "direct": {"elm/json": "1.1.2"},
        "indirect": {"elm/core": "1.0.2"}
    },
    "test-dependencies": {
        "direct": {"elm-explorations/test": "1.2.2"},
        "indirect": {"elm/random": "1.0.0"}
    }
}
`

var elmReleases = fakeGo{
	"elm/compiler":          {"0.19.0", "0.19.1", "0.19.3"},
	"elm/core":              {"1.0.2", "1.0.5"},
	"elm/json":              {"1.1.2", "1.1.4"},
	"elm/random":            {"1.0.0", "1.1.0"},
	"elm-explorations/test": {"1.2.2", "2.2.0"},
}

// TestElmRangeStrategy resolves a package and an application elm.json: package constraints
// follow rangeStrategy (auto = widen), application versions are replaced and indirect ones
// ignored by default.
func TestElmRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		pkg       string // the package elm.json after the updates
	}{
		{
			name: "auto",
			want: []string{
				"elm-explorations/test 1.2.2->2.2.0 major", "elm-explorations/test 1.2.2->2.2.0 major",
				"elm/compiler 0.19.1->0.19.3 patch",
				"elm/json 1.1.2->1.1.4 patch",
			},
			pkg: strings.Replace(elmPackage, `1.2.2 <= v < 2.0.0`, `1.2.2 <= v < 3.0.0`, 1),
		},
		{
			name: "bump", cfg: "rules: [{paths: [pkg/**], rangeStrategy: bump}]\n",
			want: []string{
				"elm-explorations/test 1.2.2->2.2.0 major", "elm-explorations/test 1.2.2->2.2.0 major",
				"elm/compiler 0.19.0->0.19.3 patch", "elm/compiler 0.19.1->0.19.3 patch",
				"elm/core 1.0.0->1.0.5 patch",
				"elm/json 1.0.0->1.1.4 minor", "elm/json 1.1.2->1.1.4 patch",
			},
			pkg: strings.NewReplacer(`"0.19.0 <= v < 0.20.0"`, `"0.19.3 <= v < 0.20.0"`,
				`"elm/core": "1.0.0 <= v < 2.0.0"`, `"elm/core": "1.0.5 <= v < 2.0.0"`,
				`"elm/json": "1.0.0 <= v < 2.0.0"`, `"elm/json": "1.1.4 <= v < 2.0.0"`,
				`1.2.2 <= v < 2.0.0`, `2.2.0 <= v < 3.0.0`).Replace(elmPackage),
		},
		{
			name: "indirect opted in", cfg: "rules: [{manager: elm, depTypes: [indirect, test-indirect], ignore: false}]\n",
			want: []string{
				"elm-explorations/test 1.2.2->2.2.0 major", "elm-explorations/test 1.2.2->2.2.0 major",
				"elm/compiler 0.19.1->0.19.3 patch",
				"elm/core 1.0.2->1.0.5 patch",
				"elm/json 1.1.2->1.1.4 patch",
				"elm/random 1.0.0->1.1.0 minor",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"pkg/elm.json": elmPackage, "app/elm.json": elmApplication})
			o := Options{Managers: []manager.Manager{elm.New()}, Datasources: map[string]Datasource{elm.Datasource: elmReleases, elm.CompilerDatasource: elmReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if tt.pkg == "" {
				return
			}
			var pkg []dep.Update
			for _, u := range res.Updates {
				if u.Dep.File == "pkg/elm.json" {
					pkg = append(pkg, u)
				}
			}
			local, _ := Local(pkg)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 || len(changes) != 1 {
				t.Fatal(changes, errs)
			}
			if got := string(changes[0].New); got != tt.pkg {
				t.Errorf("pkg/elm.json =\n%s\nwant\n%s", got, tt.pkg)
			}
		})
	}
}
