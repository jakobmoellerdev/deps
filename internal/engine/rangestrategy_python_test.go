package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
)

// pyFixtures are real manifests and trimmed lockfiles (see their first lines): the
// fastapi-template uv workspace, rich's Poetry 1 project and pipenv's example Pipfile.
var pyFixtures = []string{
	"fastapi-template/pyproject.toml", "fastapi-template/backend/pyproject.toml", "fastapi-template/uv.lock",
	"rich/pyproject.toml", "rich/poetry.lock",
	"pipenv-examples/Pipfile", "pipenv-examples/Pipfile.lock",
}

var pyReleases = fakeGo{
	"fastapi":           {"0.141.1", "0.142.0", "1.1.0"},
	"python-multipart":  {"0.0.32", "0.0.40"},
	"pygments":          {"2.19.2", "2.20.0", "3.0.0"},
	"typing-extensions": {"4.13.2", "4.15.0", "5.1.0"},
	"requests":          {"2.33.0", "2.34.0"},
	"numpy":             {"1.26.0", "1.26.4", "2.0.0", "2.3.1"},
	"urllib3":           {"2.0.0", "2.0.7", "2.5.0"},
	"flask":             {"2.3.3", "3.0.0", "3.1.0", "4.0.0"},
}

func pyFiles(t *testing.T, locks bool) map[string]string {
	t.Helper()
	files := map[string]string{"req/requirements.txt": "numpy>=1.26,<2\nflask<3\nurllib3>=2.0.0\n"}
	for _, p := range pyFixtures {
		if !locks && strings.HasSuffix(p, ".lock") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "pytool", "testdata", filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	return files
}

func pyOptions() Options {
	return Options{
		Managers:    []manager.Manager{piprequirements.New(), pep621.New(), poetry.New(), pipenv.New()},
		Datasources: map[string]Datasource{"pypi": pyReleases},
		Filter: func(d dep.Dep) bool {
			_, ok := pyReleases[strings.ToLower(d.Name)]
			return ok
		},
	}
}

// TestPythonRangeStrategy follows Renovate's pep440 semantics: under auto (update-lockfile)
// versions the range admits move only the lockfile, others rewrite the range; bump raises the
// lower bound of every range it can.
func TestPythonRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		locks     bool
		want      []string
	}{
		{
			name: "auto", locks: true,
			want: []string{
				"fastapi 0.141.1->0.142.0 minor lockfile", // >=0.141.1,<1.0.0 admits it
				"fastapi 0.141.1->1.1.0 major",            // -> >=1.1.0,<1.2.0
				"flask 3->4 major",                        // <3 -> <5 (requirements: no lockfile)
				"numpy 1.26->2.0 major",                   // >=1.26,<2 -> >=2,<3
				"pygments 2.13.0->3.0.0 major",            // ^2.13.0 -> ^3.0.0
				"pygments 2.19.2->2.20.0 minor lockfile",
				"python-multipart 0.0.32->0.0.40 patch lockfile",
				"requests 2.33.0->2.34.0 minor lockfile", // Pipfile.lock [default]
				"typing-extensions 4.0.0->5.1.0 major",
				"typing-extensions 4.13.2->4.15.0 minor lockfile",
			},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n", locks: true,
			want: []string{
				"fastapi 0.141.1->0.142.0 minor",
				"fastapi 0.141.1->1.1.0 major",
				"flask 3->4 major",
				"numpy 1.26->2.0 major",
				"pygments 2.13.0->2.20.0 minor",
				"pygments 2.13.0->3.0.0 major",
				"python-multipart 0.0.27->0.0.40 patch",
				"requests 2.32.0->2.34.0 minor",
				"typing-extensions 4.0.0->4.15.0 minor",
				"typing-extensions 4.0.0->5.1.0 major",
				"urllib3 2.0.0->2.5.0 minor", // >=2.0.0 -> >=2.5.0
			},
		},
		{
			name: "auto without lockfiles",
			want: []string{
				"fastapi 0.141.1->1.1.0 major", "flask 3->4 major", "numpy 1.26->2.0 major", "pygments 2.13.0->3.0.0 major", "typing-extensions 4.0.0->5.1.0 major",
			},
		},
		{
			name: "widen", cfg: "rangeStrategy: widen\n", locks: true,
			want: []string{ // in-range versions are left alone (see TestPythonRangeEdits)
				"fastapi 0.141.1->1.1.0 major", "flask 3->4 major", "numpy 1.26->2.0 major", "pygments 2.13.0->3.0.0 major", "typing-extensions 4.0.0->5.1.0 major",
			},
		},
		{
			name: "in-range-only", cfg: "rangeStrategy: in-range-only\n", locks: true,
			want: []string{ // bump without the versions outside the ranges
				"fastapi 0.141.1->0.142.0 minor",
				"pygments 2.13.0->2.20.0 minor",
				"python-multipart 0.0.27->0.0.40 patch",
				"requests 2.32.0->2.34.0 minor",
				"typing-extensions 4.0.0->4.15.0 minor",
				"urllib3 2.0.0->2.5.0 minor",
			},
		},
		{
			name: "pin", cfg: "rangeStrategy: pin\n", locks: true,
			want: []string{
				"fastapi 0.141.1->0.142.0 minor", // pins the range: no separate pin update
				"fastapi 0.141.1->1.1.0 major",
				"flask 3->4.0.0 major", // ranked against the pinned 2.3.3
				"flask <3->2.3.3 pin",  // no lockfile: the newest version the range admits
				"numpy 1.26->2.3.1 major",
				"numpy >=1.26,<2->1.26.4 pin",
				"pygments 2.13.0->2.20.0 minor",
				"pygments 2.13.0->3.0.0 major",
				"python-multipart 0.0.27->0.0.40 patch",
				"requests 2.32.0->2.34.0 minor",
				"typing-extensions 4.0.0->4.15.0 minor",
				"typing-extensions 4.0.0->5.1.0 major",
				"urllib3 >=2.0.0->2.5.0 pin",
				"urllib3 >=2.8.0->2.8.0 pin", // Pipfile: the locked version
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, pyFiles(t, tt.locks))
			res, err := Resolve(t.Context(), root, cfg, env, pyOptions())
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestPythonRangeEdits builds the range rewrites of a run's manifest updates: clause by
// clause, keeping the bytes around them.
func TestPythonRangeEdits(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      map[string][]string
	}{
		{name: "auto", want: map[string][]string{
			"fastapi-template/backend/pyproject.toml": {`"fastapi[standard]>=1.1.0,<1.2.0",`},
			"rich/pyproject.toml":                     {`pygments = "^3.0.0"`, `typing-extensions = ">=5.1, <5.2"`},
			"req/requirements.txt":                    {"numpy>=2,<3\nflask<5\nurllib3>=2.0.0\n"},
		}},
		{name: "widen", cfg: "rangeStrategy: widen\n", want: map[string][]string{
			"fastapi-template/backend/pyproject.toml": {`"fastapi[standard]>=0.141.1,<1.2.0",`},
			"rich/pyproject.toml":                     {`pygments = "^2.13.0 || ^3.0.0"`, `typing-extensions = ">=4.0.0, <5.2"`},
			"req/requirements.txt":                    {"numpy>=1.26,<3\nflask<5\nurllib3>=2.0.0\n"},
		}},
		{name: "pin", cfg: "rangeStrategy: pin\n", want: map[string][]string{
			"fastapi-template/backend/pyproject.toml": {`"fastapi[standard]==0.142.0",`},
			"rich/pyproject.toml":                     {`pygments = "==2.20.0"`, `typing-extensions = "==4.15.0"`},
			"req/requirements.txt":                    {"numpy==1.26.4\nflask==2.3.3\nurllib3==2.5.0\n"},
			"pipenv-examples/Pipfile":                 {`"==2.34.0"`},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, pyFiles(t, true))
			o := pyOptions()
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil {
				t.Fatal(err)
			}
			var manifest []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					manifest = append(manifest, u)
				}
			}
			local, _ := Local(manifest) // one version per occurrence, as plan writes them
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			got := map[string]string{}
			for _, c := range changes {
				got[c.Path] = string(c.New)
			}
			for path, want := range tt.want {
				for _, w := range want {
					if !strings.Contains(got[path], w) {
						t.Errorf("%s lacks %q:\n%s", path, w, got[path])
					}
				}
			}
			if len(changes) != len(tt.want) {
				t.Errorf("changed %d files", len(changes))
				for _, c := range changes {
					t.Logf("%s:\n%s", c.Path, c.New)
				}
			}
		})
	}
}

// TestPythonRangeSecurity queries OSV with the locked version; a fix the range admits is a
// lockfile-only update with a lockfile, else raises the lower bound, and is dropped when the
// range has none (`<3`).
func TestPythonRangeSecurity(t *testing.T) {
	sec := &versionSec{vulns: map[string][]Vuln{
		"python-multipart": {vuln("GHSA-m", "0.0.40")},
		"urllib3":          {vuln("GHSA-u", "2.0.7")},
		"flask":            {vuln("GHSA-f", "2.3.3")},
	}}
	// Only majors are allowed, so the other updates come from the advisories alone.
	root, cfg := setup(t, "rules:\n  - allowedUpdateTypes: [major]\n", pyFiles(t, true))
	o := pyOptions()
	o.Security = sec
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	got := renderUpdates(res.Updates)
	for _, w := range []string{"python-multipart 0.0.32->0.0.40 patch lockfile security", "urllib3 2.0.0->2.0.7 patch security"} {
		if !strings.Contains(got, w+"\n") {
			t.Errorf("updates lack %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "flask 3->2.3.3") {
		t.Errorf("flask fix kept:\n%s", got)
	}
	slices.Sort(sec.asked)
	for _, w := range []string{"fastapi@0.141.1 locked 0.141.1", "pygments@2.13.0 locked 2.19.2", "numpy@1.26 locked "} {
		if !slices.Contains(sec.asked, w) {
			t.Errorf("asked = %v, want %q", sec.asked, w)
		}
	}
}
