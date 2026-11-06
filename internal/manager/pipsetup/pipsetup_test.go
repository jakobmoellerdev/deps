package pipsetup

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
	depType, name, rng, skip string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("setup.py", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.Datasource != "pypi" || d.Versioning != "pep440" {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Range != "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Range {
			t.Errorf("%s: span %q != range %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Range)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Errorf("got %d deps, want %d", len(deps), len(wants))
	}
	for i := range min(len(deps), len(wants)) {
		d, w := deps[i], wants[i]
		if got := (want{d.Line, d.DepType, d.Name, d.Range, d.Skip}); got != w {
			t.Errorf("dep %d = %+v, want %+v", i, got, w)
		}
	}
}

func read(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExtractRenovateFixture uses Renovate's pip_setup fixture
// (lib/modules/manager/pip_setup/__fixtures__/setup.py) and finds the deps of its snapshot.
func TestExtractRenovateFixture(t *testing.T) {
	check(t, extract(t, read(t, "testdata/renovate-setup.py")), []want{
		{50, DepTypeExtra, "celery", ">=3.1.13.0,<5.0", ""},
		{53, DepTypeExtra, "logging_tree", ">=1.7", ""},
		{54, DepTypeExtra, "pygments", ">=2.2", ""},
		{55, DepTypeExtra, "psutil", ">=5.0", ""},
		{56, DepTypeExtra, "objgraph", ">=3.0", ""},
		{59, DepTypeExtra, "django", ">=1.11.23,<2.0", ""},
		{62, DepTypeExtra, "flask", ">=0.11,<2.0", ""},
		{63, DepTypeExtra, "blinker", ">=1.4,<2.0", ""},
		{64, DepTypeExtra, "flask2", "==1.2.3", ""},
		{76, DepTypeInstall, "gunicorn", ">=19.7.0,<20.0", ""},
		{77, DepTypeInstall, "Werkzeug", ">=0.15.3,<0.16", ""},
		{77, DepTypeInstall, "statsd", ">=3.2.1,<4.0", ""},
		{78, DepTypeInstall, "requests", ">=2.10.0,<3.0", SkipIgnored},
		{79, DepTypeInstall, "raven", ">=5.27.1,<7.0", ""},
		{80, DepTypeInstall, "future", ">=0.15.2,<0.17", ""},
		{81, DepTypeInstall, "ipaddress", ">=1.0.16,<2.0", ""},
		{82, DepTypeInstall, "zope.interface", ">=5.5.2,<6.0.0", ""},
	})
}

// TestExtractBlack uses black 22.1.0's setup.py and rewrites one requirement.
func TestExtractBlack(t *testing.T) {
	src := read(t, "testdata/black-22.1.0.py")
	deps := extract(t, src)
	check(t, deps, []want{
		{100, DepTypeInstall, "click", ">=8.0.0", ""},
		{101, DepTypeInstall, "platformdirs", ">=2", ""},
		{102, DepTypeInstall, "tomli", ">=1.1.0", ""},
		{103, DepTypeInstall, "typed-ast", ">=1.4.2", ""},
		{104, DepTypeInstall, "pathspec", ">=0.9.0", ""},
		{105, DepTypeInstall, "dataclasses", ">=0.6", ""},
		{106, DepTypeInstall, "typing_extensions", ">=3.10.0.0", ""},
		{107, DepTypeInstall, "mypy_extensions", ">=0.4.3", ""},
		{110, DepTypeExtra, "aiohttp", ">=3.7.4", ""},
		{111, DepTypeExtra, "colorama", ">=0.4.3", ""},
		{112, DepTypeExtra, "uvloop", ">=0.15.2", ""},
		{113, DepTypeExtra, "ipython", ">=7.8.0", ""},
		{113, DepTypeExtra, "tokenize-rt", ">=3.2.0", ""},
	})
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "9.0.0", RangeStrategy: "bump"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if got := extract(t, string(out))[0].Range; got != ">=9.0.0" {
		t.Errorf("rewritten range %q", got)
	}
}

func TestExtractLexing(t *testing.T) {
	src := `REQUIRES = ["a>=1", r'b~=2.0', u"c==3"]
"""
"d>=1"
"""
x = f"e>={v}"
y = b"f>=1"
z = "g>=" + V
w = "h>={}".format(V)
q = "i>=1\n"
p = "j>=1" * 2
s = "k @ https://x/k.whl"
t = "l"
o = '''m>=1'''
# "n>=1"
setup(setup_requires=["setuptools>=40", "wheel!=0.1"], tests_require=("pytest>=7",), version="1.0")
if x in "o>=1": pass
"unterminated>=1
`
	check(t, extract(t, src), []want{
		{1, DepTypeOther, "a", ">=1", ""},
		{1, DepTypeOther, "b", "~=2.0", ""},
		{1, DepTypeOther, "c", "==3", ""},
		{15, DepTypeSetup, "setuptools", ">=40", ""},
		{15, DepTypeSetup, "wheel", "", pyreq.SkipUnconstrained},
		{15, DepTypeTest, "pytest", ">=7", ""},
		{16, DepTypeOther, "o", ">=1", ""},
	})
}

// TestExtractKeywordScope checks that a keyword's dep type ends with its statement or argument:
// a module-level `tests_require = [...]` does not type the statements after it, nor does a
// keyword whose value ends without a comma before another `name=`.
func TestExtractKeywordScope(t *testing.T) {
	src := `tests_require = ["pytest>=6"]
EXTRA = ["sphinx>=3"]
install_requires = \
    ["requests>=2"]; OTHER = ["six>=1"]
setup(name="x", docs=["wheel>=0.3"], tests_require=[
    "mock>=4",
], extras_require={"a": ["b>=1"]} if X else {}
, py=["c>=1"])
`
	check(t, extract(t, src), []want{
		{1, DepTypeTest, "pytest", ">=6", ""},
		{2, DepTypeOther, "sphinx", ">=3", ""},
		{4, DepTypeInstall, "requests", ">=2", ""},
		{4, DepTypeOther, "six", ">=1", ""},
		{5, DepTypeOther, "wheel", ">=0.3", ""},
		{6, DepTypeTest, "mock", ">=4", ""},
		{7, DepTypeExtra, "b", ">=1", ""},
		{8, DepTypeOther, "c", ">=1", ""},
	})
}

func TestManager(t *testing.T) {
	m := New()
	if !m.Match("pkg/setup.py") || m.Match("mysetup.py") || m.Name() != Name {
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
