package setupcfg

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

type want struct {
	line                     int
	depType, name, ver, skip string
}

func extract(t *testing.T, file string) (string, []dep.Dep) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	deps, err := New().Extract("setup.cfg", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.Datasource != "pypi" || d.Versioning != "pep440" {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Range {
			t.Errorf("%s: span %q != range %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Range)
		}
	}
	return src, deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Errorf("got %d deps, want %d", len(deps), len(wants))
	}
	for i := range min(len(deps), len(wants)) {
		d, w := deps[i], wants[i]
		if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Skip}); got != w {
			t.Errorf("dep %d = %+v, want %+v", i, got, w)
		}
	}
}

// TestExtractRenovateFixture uses Renovate's setup-cfg fixture
// (lib/modules/manager/setup-cfg/__fixtures__/setup-cfg-1.txt): the same deps;
// the unconstrained ones Renovate reports without version are skipped.
func TestExtractRenovateFixture(t *testing.T) {
	_, deps := extract(t, "testdata/renovate-setup-cfg-1.cfg")
	u := pyreq.SkipUnconstrained
	check(t, deps, []want{
		{7, DepTypeInstall, "coloredlogs", "14.0", ""},
		{8, DepTypeInstall, "first", "1.0", ""},
		{8, DepTypeInstall, "second", "2.2", ""},
		{8, DepTypeInstall, "third", "3.0", ""},
		{10, DepTypeInstall, "quux", "5.5.5", ""},
		{11, DepTypeInstall, "python-dateutil", "2.1", ""},
		{12, DepTypeInstall, "foo", "1.1.1", ""},
		{13, DepTypeInstall, "baz", ">=3.3.3.", pyreq.SkipRange},
		{14, DepTypeInstall, "docopt", "0.4", ""},
		{15, DepTypeInstall, "fs", "2.1", ""},
		{16, DepTypeInstall, "nmspc.pkg", "1.0", ""},
		{18, DepTypeInstall, "requests", "2.18", ""},
		{19, DepTypeInstall, "compact", "1.2.3", ""},
		{21, DepTypeInstall, "responses", "2.27.0", ""},
		{24, DepTypeSetup, "six", "1.4", ""},
		{25, DepTypeSetup, "tqdm", "4.19", ""},
		{26, DepTypeSetup, "tenacity", "6.0", ""},
		{28, DepTypeTest, "typing", "3.6", ""},
		{29, DepTypeTest, "verboselogs", "1.7", ""},
		{30, DepTypeTest, "instalooter", "", u},
		{36, DepTypeExtra, "piexif", "", u},
		{37, DepTypeExtra, "Pillow", "", u},
		{42, DepTypeExtra, "bar", "2.2.2", ""},
		{43, DepTypeExtra, "qux", "4.4.4", ""},
		{46, DepTypeExtra, "contexter", "0.1", ""},
		{47, DepTypeExtra, "mock", "2.0", ""},
		{48, DepTypeExtra, "parameterized", "0.6", ""},
		{49, DepTypeExtra, "green", "2.12", ""},
		{53, DepTypeExtra, "coverage", "", u},
		{54, DepTypeExtra, "codecov", "", u},
		{55, DepTypeExtra, "codacy-coverage", "", u},
		{57, DepTypeExtra, "sphinx", "1.7", ""},
		{59, DepTypeExtra, "sphinx-bootstrap-theme", "0.6", ""},
		{60, DepTypeExtra, "semantic-version", "2.6", ""},
		{63, DepTypeExtra, "docutils", "", u},
		{64, DepTypeExtra, "Pygments", "", u},
		{72, DepTypeInstall, "aiortc", "0.9", ""},
		{73, DepTypeInstall, "websockets", "8.1", ""},
		{74, DepTypeInstall, "aiohttp", "3.6", ""},
		{75, DepTypeInstall, "pyee", "6.0", ""},
		{76, DepTypeInstall, "websockets", "8.1", ""},
		{77, DepTypeInstall, "dataclasses_json", "0.3", ""},
		{78, DepTypeInstall, "coloredlogs", "10.0", ""},
		{80, DepTypeInstall, "av", "8.0.0", ""},
	})
	// The last python_requires wins, as setuptools reads the file.
	if deps[0].PythonConstraint != ">=3.7" {
		t.Errorf("python %q", deps[0].PythonConstraint)
	}
}

// TestExtractPytest uses pytest 7.0.0's setup.cfg.
func TestExtractPytest(t *testing.T) {
	src, deps := extract(t, "testdata/pytest-7.0.0.cfg")
	var names []string
	for _, d := range deps {
		names = append(names, d.DepType+":"+d.Name+"@"+d.Version)
		if d.PythonConstraint != ">=3.6" {
			t.Errorf("%s: python %q", d.Name, d.PythonConstraint)
		}
	}
	if len(deps) != 18 || deps[0].Name != "attrs" || deps[3].Range != ">=0.12,<2.0" || deps[9].Name != "setuptools" || deps[11].DepType != DepTypeExtra {
		t.Fatalf("%v", names)
	}
	edits, err := New().Edits(dep.Update{Dep: deps[3], NewVersion: "3.0.0", RangeStrategy: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := New().Extract("setup.cfg", out, manager.Env{}); after[3].Range != ">=3,<3.1" {
		t.Errorf("rewritten range %q", after[3].Range)
	}
}

func TestExtractInline(t *testing.T) {
	src := "[metadata]\nname = x\ninstall_requires = nope>=1\n[options]\ninstall_requires = file: requirements.txt\nsetup_requires: a>=1; python_version<'3.8'; b @ https://x/b.whl\n\t# c>=1\n; d>=1\nother =\n    e>=1\n"
	deps, err := New().Extract("setup.cfg", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, deps, []want{
		{6, DepTypeSetup, "a", "1", ""},
		{6, DepTypeSetup, "b", "", pyreq.SkipURL},
	})
}

func TestManager(t *testing.T) {
	m := New()
	if !m.Match("pkg/setup.cfg") || m.Match("xsetup.cfg") || m.Name() != Name {
		t.Error("match")
	}
	d := dep.Dep{Range: ">=1.0", Version: "1.0", Versioning: "pep440"}
	if !m.(manager.RangeUpdater).InRange(d, "1.5") {
		t.Error("InRange")
	}
	if r, ok := m.(manager.RangeUpdater).NewRange(d, "2.0.0", "bump"); !ok || r != ">=2.0.0" {
		t.Errorf("NewRange = %q %v", r, ok)
	}
}
