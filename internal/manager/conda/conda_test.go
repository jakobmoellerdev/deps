package conda

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

type want struct {
	line                                  int
	depType, name, ver, rng, skip, source string
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, src, manager.Env{YAML: &yamlspan.Parsed{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		ds, vs := "conda", "conda"
		if d.DepType == DepTypePip {
			ds, vs = "pypi", "pep440"
		}
		if d.Datasource == pyreq.GitTags {
			ds, vs = pyreq.GitTags, "tag"
		}
		if d.Manager != Name || d.Datasource != ds || d.Versioning != vs || d.File != file {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Skip == "" && d.Range != "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Range {
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
		if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Range, d.Skip, d.Source}); got != w {
			t.Errorf("dep %d = %+v, want %+v", i, got, w)
		}
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestMatch(t *testing.T) {
	m := New()
	for p, ok := range map[string]bool{
		"environment.yml": true, "environment.yaml": true, "ci/requirements/environment.yml": true,
		"my-environment.yml": false, "environment.yml.bak": false, "environment.json": false, "env.yml": false,
	} {
		if m.Match(p) != ok {
			t.Errorf("Match(%q) = %v", p, !ok)
		}
	}
	if m.Name() != "conda" {
		t.Error(m.Name())
	}
}

// testdata/stable-diffusion/environment.yaml is CompVis/stable-diffusion's environment.yaml
// at 21f890f9 verbatim: `defaults` and `pytorch` channels, fuzzy specs and a pip list with
// editable VCS installs.
func TestExtractStableDiffusion(t *testing.T) {
	deps := extract(t, "environment.yaml", read(t, "testdata/stable-diffusion/environment.yaml"))
	const chans = "pytorch anaconda"
	check(t, deps, []want{
		{6, "dependencies", "python", "3.8.5", "=3.8.5", "", chans},
		{7, "dependencies", "pip", "20.3", "=20.3", "", chans},
		{8, "dependencies", "cudatoolkit", "11.3", "=11.3", "", chans},
		{9, "dependencies", "pytorch", "1.11.0", "=1.11.0", "", chans},
		{10, "dependencies", "torchvision", "0.12.0", "=0.12.0", "", chans},
		{11, "dependencies", "numpy", "1.19.2", "=1.19.2", "", chans},
		{13, "pip", "albumentations", "0.4.3", "==0.4.3", "", ""},
		{14, "pip", "diffusers", "", "", "unconstrained", ""},
		{15, "pip", "opencv-python", "4.1.2.30", "==4.1.2.30", "", ""},
		{16, "pip", "pudb", "2019.2", "==2019.2", "", ""},
		{17, "pip", "invisible-watermark", "", "", "unconstrained", ""},
		{18, "pip", "imageio", "2.9.0", "==2.9.0", "", ""},
		{19, "pip", "imageio-ffmpeg", "0.4.2", "==0.4.2", "", ""},
		{20, "pip", "pytorch-lightning", "1.4.2", "==1.4.2", "", ""},
		{21, "pip", "omegaconf", "2.1.1", "==2.1.1", "", ""},
		{22, "pip", "test-tube", "0.7.5", ">=0.7.5", "", ""},
		{23, "pip", "streamlit", "0.73.1", ">=0.73.1", "", ""},
		{24, "pip", "einops", "0.3.0", "==0.3.0", "", ""},
		{25, "pip", "torch-fidelity", "0.3.0", "==0.3.0", "", ""},
		{26, "pip", "transformers", "4.19.2", "==4.19.2", "", ""},
		{27, "pip", "torchmetrics", "0.6.0", "==0.6.0", "", ""},
		{28, "pip", "kornia", "0.6", "==0.6", "", ""},
	})
	for _, d := range deps {
		if d.DepType == DepTypePip && d.PythonConstraint != "==3.8.5.*" {
			t.Errorf("%s: python constraint %q", d.Name, d.PythonConstraint)
		}
	}
}

// testdata/pandas/environment.yml is pandas-dev/pandas's environment.yml at 249a3e48 verbatim,
// testdata/xarray/environment.yml pydata/xarray's ci/requirements/environment.yml at b5b5e7af.
func TestExtractRealWorld(t *testing.T) {
	for _, tt := range []struct {
		file           string
		deps, unpinned int
		picks          map[int]want
	}{
		{"testdata/pandas/environment.yml", 87, 38, map[int]want{
			0:  {6, "dependencies", "python", "3.11", "=3.11", "", "conda-forge"},
			3:  {11, "dependencies", "cython", "3.1.0", ">=3.1.0,<4", "", "conda-forge"},
			14: {26, "dependencies", "numpy", "3", "<3", "", "conda-forge"},
			27: {42, "dependencies", "numexpr", "2.11.0", ">=2.11.0,!=2.14.1", "", "conda-forge"},
			31: {47, "dependencies", "pyarrow", "16.0.0", ">=16.0.0,!=24.*", "", "conda-forge"},
			86: {128, "pip", "tzdata", "2023.3", ">=2023.3", "", ""},
		}},
		{"testdata/xarray/environment.yml", 60, 57, map[int]want{
			17: {23, "dependencies", "mypy", "1.18.1", "==1.18.1", "", "conda-forge"},
			21: {27, "dependencies", "numbagg", "", "", "unconstrained", "conda-forge"},
			23: {29, "dependencies", "numpy", "2.2", ">=2.2", "", "conda-forge"},
			27: {33, "dependencies", "pandas-stubs", "2.2.3.241126", "<=2.2.3.241126", "", "conda-forge"},
			57: {65, "pip", "jax", "", "", "unconstrained", ""},
		}},
	} {
		deps := extract(t, "environment.yml", read(t, tt.file))
		unpinned := 0
		for _, d := range deps {
			if d.Skip == pyreq.SkipUnconstrained {
				unpinned++
			} else if d.Skip != "" {
				t.Errorf("%s: %s skipped: %s", tt.file, d.Name, d.Skip)
			}
		}
		if len(deps) != tt.deps || unpinned != tt.unpinned {
			t.Errorf("%s: %d deps, %d unconstrained, want %d, %d", tt.file, len(deps), unpinned, tt.deps, tt.unpinned)
		}
		for i, w := range tt.picks {
			d := deps[i]
			if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Range, d.Skip, d.Source}); got != w {
				t.Errorf("%s: dep %d = %+v, want %+v", tt.file, i, got, w)
			}
		}
	}
}

const edge = `name: edge
channels:
  - https://conda.anaconda.org/conda-forge
  - bioconda
  - nodefaults
dependencies:
  - python>=3.10,<3.13
  - pytorch::pytorch==2.3.0
  - conda-forge/linux-64::libblas 3.9.0.*
  - numpy=1.26.4=py312h8753938_0
  - scipy 1.11.4 py312_0
  - pandas[version='>=2']
  - openssl=1.1.1|3.0
  - samtools >=1.19, <2
  - "zlib==1.3.1"
  - ? weird
  - https://prefix.dev/conda-forge::rich=13
  - tzdata=2024a
  - bad=latest
  - =1.0
  - "\x6dpi4py=3.1"
  - pip:
      - --index-url https://pypi.example/simple
      - --extra-index-url=https://extra.example/simple
      - -r requirements.txt
      - -e .
      - requests[socks]>=2.31 ; python_version >= "3.10"
      - "black ==24.4.2"
      - git+https://github.com/o/r.git@main#egg=r
      - ./local-pkg
      - mylib @ https://example.com/mylib-1.0.whl
      - not a requirement!
      - tool @ git+https://github.com/org/tool@v1.4.0
  - pip:
  - 7
`

func TestExtractEdgeCases(t *testing.T) {
	deps := extract(t, "environment.yml", []byte(edge))
	const chans = "conda-forge bioconda"
	check(t, deps, []want{
		{7, "dependencies", "python", "3.10", ">=3.10,<3.13", "", chans},
		{8, "dependencies", "pytorch", "2.3.0", "==2.3.0", "", "pytorch"},
		{9, "dependencies", "libblas", "3.9.0", "3.9.0.*", "", "conda-forge"},
		{10, "dependencies", "numpy", "", "", SkipBuild, chans},
		{11, "dependencies", "scipy", "", "", SkipBuild, chans},
		{12, "dependencies", "pandas", "", "", SkipMatchSpec, chans},
		{13, "dependencies", "openssl", "", "", SkipMatchSpec, chans},
		{14, "dependencies", "samtools", "1.19", ">=1.19, <2", "", chans},
		{15, "dependencies", "zlib", "1.3.1", "==1.3.1", "", chans},
		{17, "dependencies", "rich", "", "", SkipChannel + " https://prefix.dev/conda-forge", ""},
		{18, "dependencies", "tzdata", "2024a", "=2024a", "", chans},
		{19, "dependencies", "bad", "", "", pyreq.SkipRange, chans},
		{21, "dependencies", "mpi4py", "", "", pyreq.SkipLocate, chans},
		{27, "pip", "requests", "2.31", ">=2.31", "", "https://pypi.example/simple"},
		{28, "pip", "black", "24.4.2", "==24.4.2", "", "https://pypi.example/simple"},
		{29, "pip", "r", "", "", pyreq.SkipGit, "https://pypi.example/simple"},
		{30, "pip", "./local-pkg", "", "", pyreq.SkipLocal, "https://pypi.example/simple"},
		{31, "pip", "mylib", "", "", pyreq.SkipURL, "https://pypi.example/simple"},
		{32, "pip", "not a requirement!", "", "", pyreq.SkipRange, "https://pypi.example/simple"},
		{33, "pip", "tool", "v1.4.0", "", "", "https://github.com/org/tool"},
	})
	if d := deps[len(deps)-1]; d.Datasource != pyreq.GitTags || edge[d.RefSpan.Start:d.RefSpan.End] != "v1.4.0" {
		t.Errorf("git dep = %+v", d)
	} else if edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "v1.5.0"}); err != nil || len(edits) != 1 || edits[0].New != "v1.5.0" {
		t.Errorf("edits = %+v, %v", edits, err)
	}
	for _, d := range deps[13 : len(deps)-1] {
		if d.PythonConstraint != ">=3.10,<3.13" || len(d.ExtraSources) != 1 || d.ExtraSources[0] != "https://extra.example/simple" {
			t.Errorf("%s: python %q, extra %v", d.Name, d.PythonConstraint, d.ExtraSources)
		}
	}
}

func TestExtractChannels(t *testing.T) {
	for src, w := range map[string]want{
		"dependencies: [numpy=1.26]":                                       {1, "dependencies", "numpy", "1.26", "=1.26", "", ""},
		"channels: [defaults]\ndependencies: [numpy=1.26]":                 {2, "dependencies", "numpy", "1.26", "=1.26", "", "anaconda"},
		"channels: [https://prefix.dev/x, ./local]\ndependencies: [numpy]": {2, "dependencies", "numpy", "", "", SkipChannel + " https://prefix.dev/x", ""},
		"channels: [nodefaults]\ndependencies: [numpy=1.26]":               {2, "dependencies", "numpy", "1.26", "=1.26", "", ""},
	} {
		deps := extract(t, "environment.yml", []byte(src))
		if len(deps) != 1 {
			t.Fatalf("%q: %d deps", src, len(deps))
		}
		d := deps[0]
		if got := (want{d.Line, d.DepType, d.Name, d.Version, d.Range, d.Skip, d.Source}); got != w {
			t.Errorf("%q: %+v, want %+v", src, got, w)
		}
	}
	for _, src := range []string{"", "name: x\n", "dependencies: numpy\n", "- a\n- b\n"} {
		if deps := extract(t, "environment.yml", []byte(src)); len(deps) != 0 {
			t.Errorf("%q: %v", src, deps)
		}
	}
	if _, err := New().Extract("environment.yml", []byte("dependencies: [\n"), manager.Env{}); err == nil {
		t.Error("invalid YAML accepted")
	}
}

func TestAnchorAndPEP440(t *testing.T) {
	for spec, w := range map[string][2]string{
		">=1.2,<2": {"1.2", ""}, "<3": {"3", ""}, "<=2,<3": {"2", ""}, "!=1.0": {"", pyreq.SkipUnconstrained},
		"!=1.0,>1": {"1", ""}, ">=1.*": {"", pyreq.SkipRange},
	} {
		if v, skip := anchor(spec); v != w[0] || skip != w[1] {
			t.Errorf("anchor(%q) = %q, %q, want %q", spec, v, skip, w)
		}
	}
	for spec, w := range map[string]string{
		"=3.11": "==3.11.*", "3.11.4": "==3.11.4", "3.11.*": "==3.11.*", ">=3.10,<3.13": ">=3.10,<3.13", "!=3.9.*": "!=3.9.*",
		"=1.1.1w": "", "latest": "",
	} {
		if got := pep440(spec); got != w {
			t.Errorf("pep440(%q) = %q, want %q", spec, got, w)
		}
	}
}

func TestNewRange(t *testing.T) {
	m := New().(manager.RangeUpdater)
	for _, tt := range []struct {
		rng, v, strategy, want string
	}{
		{"=1.26", "2.3", "", "=2.3"},
		{"=1.26", "2.3.4", "replace", "=2.3"},
		{"=1.26", "1.26.4", "bump", ""}, // admitted at the spec's precision: nothing to raise
		{"1.26.*", "2.0.0", "", "2.0.*"},
		{"=1.11.0", "2.5.1", "", "=2.5.1"},
		{"==1.18.1", "1.19.0", "", "==1.19.0"},
		{"1.18.1", "1.19.0", "", "1.19.0"},
		{"==1.18.1", "1.18.1", "", ""},
		{"=1.1.1v", "1.1.1w", "", "=1.1.1w"},
		{"=2024a", "2025b", "", "=2025b"},
		{">=1.2,<2", "3.1", "replace", ">=3.1,<4"},
		{">=1.2, <2", "3.1", "widen", ">=1.2, <4"},
		{">=2.2", "2.3", "bump", ">=2.3"},
		{"<=2.2.3.241126", "2.3.3.250101", "replace", "<=2.3.3.250101"},
		{">=16.0.0,!=24.*", "25.0.0", "bump", ">=25.0.0,!=24.*"},
		{"=1.26", "2.3.4", dep.RangePin, "==2.3.4"},
		{">=1.2", "1.5", dep.RangePin, "==1.5"},
		{"==1.2", "1.5", dep.RangePin, "==1.5"},
		{">=1.1.1w", "3.0", "replace", ""}, // no PEP 440 equivalent
		{"=1.2,>=1.2.1", "2.0", "replace", ""},
		{"1.2*,<2", "3.0", "replace", ""},
		{">=1.2,!=3.0", "3.0", "replace", ""}, // the rewrite would not admit 3.0
		{"bad spec", "1.0", "", ""},
		{"=1.2", "x", "", ""},
	} {
		got, ok := m.NewRange(dep.Dep{Range: tt.rng, Datasource: "conda"}, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("NewRange(%q, %s, %q) = %q, %v, want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	if got, ok := m.NewRange(dep.Dep{Range: ">=2.31", Datasource: "pypi", Manager: Name}, "3.0.0", "replace"); !ok || got != ">=3.0.0" {
		t.Errorf("pip NewRange = %q, %v", got, ok)
	}
}

func TestInRange(t *testing.T) {
	m := New().(manager.RangeUpdater)
	for _, tt := range []struct {
		rng, v string
		ds     string
		want   bool
	}{
		{"=1.26", "1.26.4", "conda", true}, {"=1.26", "1.27.0", "conda", false}, {">=1.2,<2", "1.9", "conda", true},
		{"bad spec", "1.0", "conda", false}, {"bad spec", "1.0", "conda", false}, {">=2.31", "2.32.3", "pypi", true}, {"==2.31", "2.32", "pypi", false},
	} {
		if got := m.InRange(dep.Dep{Range: tt.rng, Datasource: tt.ds}, tt.v); got != tt.want {
			t.Errorf("InRange(%q, %s) = %v", tt.rng, tt.v, got)
		}
	}
}

func TestEdits(t *testing.T) {
	src := read(t, "testdata/stable-diffusion/environment.yaml")
	deps := extract(t, "environment.yaml", src)
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[5], NewVersion: "2.3"},                        // numpy=1.19.2 → candidates are cut to its precision
		{Dep: deps[0], NewVersion: "3.12.11"},                    // python=3.8.5
		{Dep: deps[6], NewVersion: "1.4.24"},                     // albumentations==0.4.3 (pip)
		{Dep: deps[16], NewVersion: "1.50.0", RangeStrategy: ""}, // streamlit>=0.73.1 (pip)
	} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatalf("%s: %v", u.Dep.Name, err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"  - numpy=2.3\n", "  - python=3.12.11\n", "    - albumentations==1.4.24\n", "    - streamlit>=1.50.0\n"} {
		if !strings.Contains(string(out), line) {
			t.Errorf("missing %q in\n%s", line, out)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "3.8.5"}); err == nil {
		t.Error("unchanged edit accepted")
	}
}
