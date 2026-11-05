package engine

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/conda"
)

var condaReleases = fakeGo{
	"python":    {"3.8.5", "3.8.20", "3.12.0", "3.13.0", "3.13.9", "3.14.0rc1"},
	"numpy":     {"1.19.2", "1.19.5", "1.26.4", "2.3.0", "2.3.4"},
	"pytorch":   {"1.11.0", "2.3.0", "2.9.1"},
	"scipy":     {"1.11.4", "1.16.0", "1.16.3"},
	"openssl":   {"1.1.1v", "1.1.1w", "3.6.0"},
	"streamlit": {"0.73.1", "1.50.0"},
	"einops":    {"0.3.0", "0.8.1"},
}

func condaOptions() Options {
	return Options{
		Managers:    []manager.Manager{conda.New()},
		Datasources: map[string]Datasource{"conda": condaReleases, "pypi": condaReleases},
		Filter: func(d dep.Dep) bool {
			_, ok := condaReleases[d.Name]
			return ok
		},
	}
}

// condaEnv extends CompVis/stable-diffusion's environment.yaml (testdata of the conda
// manager) with a range and an exact pin of an openssl letter version.
func condaEnv(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../manager/conda/testdata/stable-diffusion/environment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(b), "  - numpy=1.19.2\n", "  - numpy=1.19.2\n  - scipy>=1.11,<2\n  - openssl==1.1.1v\n", 1)
	return map[string]string{"environment.yaml": src}
}

// TestCondaRangeStrategy: without a lockfile, versions a spec admits are not proposed
// (`scipy>=1.11,<2`, `streamlit>=0.73.1` under auto); a fuzzy spec moves at its precision,
// a range by the pep440 rules; conda ordering ranks 1.1.1w above 1.1.1v; the second segment
// is a minor update (python 3.8 → 3.13).
func TestCondaRangeStrategy(t *testing.T) {
	common := []string{
		"einops 0.3.0->0.8.1 minor",
		"numpy 1.19.2->1.26.4 minor",
		"numpy 1.19.2->2.3.4 major",
		"openssl 1.1.1v->1.1.1w patch",
		"openssl 1.1.1v->3.6.0 major",
		"python 3.8.5->3.13.9 minor", // 3.14.0rc1 is a prerelease
		"pytorch 1.11.0->2.9.1 major",
	}
	for _, tt := range []struct {
		name, cfg string
		want      []string
		files     []string
	}{
		{
			name: "auto", want: common,
			files: []string{"  - python=3.13.9\n", "  - numpy=1.26.4\n", "  - pytorch=2.9.1\n", "  - scipy>=1.11,<2\n", "  - openssl==1.1.1w\n", "    - einops==0.8.1\n", "    - streamlit>=0.73.1\n"},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: append(slices.Clone(common),
				"scipy 1.11->1.16 minor", // 1.16.3 is beyond the spec's precision
				"streamlit 0.73.1->1.50.0 major"),
			files: []string{"  - scipy>=1.16,<2\n", "    - streamlit>=1.50.0\n"},
		},
		{
			name: "pin", cfg: "rangeStrategy: pin\n",
			want: append(slices.Clone(common),
				"pytorch =1.11.0->1.11.0 pin", // a fuzzy spec is no exact pin
				"scipy >=1.11,<2->1.16.3 pin",
				"streamlit >=0.73.1->1.50.0 pin"),
			files: []string{"  - scipy==1.16.3\n", "    - streamlit==1.50.0\n", "  - pytorch==1.11.0\n"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, condaEnv(t))
			o := condaOptions()
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			slices.Sort(tt.want)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			local, _ := Local(res.Updates)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 || len(changes) != 1 {
				t.Fatal(errs, len(changes))
			}
			for _, w := range tt.files {
				if !strings.Contains(string(changes[0].New), w) {
					t.Errorf("environment.yaml lacks %q:\n%s", w, changes[0].New)
				}
			}
		})
	}
}
