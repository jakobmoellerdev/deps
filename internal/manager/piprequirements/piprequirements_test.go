package piprequirements

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

type want struct {
	line            int
	name, ver, skip string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("requirements.txt", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.DepType != DepType || d.Datasource != "pypi" || d.Versioning != "pep440" || d.File != "requirements.txt" {
			t.Errorf("%s: %+v", d.Name, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Range {
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
		if d.Line != w.line || d.Name != w.name || d.Version != w.ver || d.Skip != w.skip {
			t.Errorf("dep %d = {%d %q %q %q}, want %+v", i, d.Line, d.Name, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		"requirements.txt": true, "a/requirements-dev.txt": true, "requirements.dev.txt": true, "dev-requirements.txt": true,
		"test_requirements.txt": true, "requirements.pip": true, "requirements.in": true, "requirements/base.in": true, "requirements/base.txt": false, "myrequirements.txt.bak": false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

func TestExtract(t *testing.T) {
	src := strings.Join([]string{
		"# comment",                               // 1
		"--index-url https://example.com/simple",  // 2
		"-r base-requirements.txt",                // 3
		"requests==2.31.0  # pinned",              // 4
		"uvicorn[standard] >= 0.12.0",             // 5
		"Django~=4.2 ; python_version >= '3.10'",  // 6
		"black \\",                                // 7
		"  ==24.3.0",                              // 8
		"pkg==1.0 --hash=sha256:abc \\",           // 9
		"    --hash=sha256:def",                   // 10
		"x==1.0 \\",                               // 11
		"    --hash=sha256:abc",                   // 12
		"-e git+https://github.com/o/r.git#egg=r", // 13
		"git+https://github.com/o/r.git#egg=r",    // 14
		"https://example.com/pkg-1.0.tar.gz",      // 15
		"./local/pkg",                             // 16
		"dist/pkg-1.0-py3-none-any.whl",           // 17
		"name @ https://example.com/name.whl",     // 18
		"flask",                                   // 19
		"numpy>=1.26,<2",                          // 20
		"setuptools==69.*",                        // 21
		"${VAR}",                                  // 22
		"  # indented comment",                    // 23
		"pytest==8.0#not-a-comment",               // 24
		"trailing==1.0 \\",                        // 25
	}, "\n")
	deps := extract(t, src)
	check(t, deps, []want{
		{4, "requests", "2.31.0", ""},
		{5, "uvicorn", "0.12.0", ""},
		{6, "Django", "4.2", ""},
		{7, "black", "24.3.0", ""},
		{9, "pkg", "1.0", ""},
		{11, "x", "1.0", ""},
		{14, "git+https://github.com/o/r.git#egg=r", "", pyreq.SkipGit},
		{15, "https://example.com/pkg-1.0.tar.gz", "", pyreq.SkipURL},
		{16, "./local/pkg", "", pyreq.SkipLocal},
		{17, "dist/pkg-1.0-py3-none-any.whl", "", pyreq.SkipLocal},
		{18, "name", "", pyreq.SkipURL},
		{19, "flask", "", pyreq.SkipUnconstrained},
		{20, "numpy", "1.26", ""},
		{21, "setuptools", "69", ""},
		{24, "pytest", "==8.0#not-a-comment", pyreq.SkipRange},
		{25, "trailing", "1.0", ""},
	})
	for _, d := range deps {
		if d.Source != "https://example.com/simple" {
			t.Errorf("%s: source %q", d.Name, d.Source)
		}
	}
}

func TestIndexURL(t *testing.T) {
	for line, want := range map[string]string{
		"--index-url=https://a/simple": "https://a/simple", "-i https://b/simple": "https://b/simple",
		"--index-url": "", "--extra-index-url https://c": "", "": "", "-ihttps://x": "",
	} {
		if got := indexURL(line); got != want {
			t.Errorf("indexURL(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestSplitOptions(t *testing.T) {
	for in, want := range map[string][2]string{
		"a==1 --hash=x":                    {"a==1", "--hash=x"},
		"a==1; os_name == 'a -b' --hash=x": {"a==1; os_name == 'a -b'", "--hash=x"},
		`a==1; os_name == "a -b"`:          {`a==1; os_name == "a -b"`, ""},
		"a-b==1":                           {"a-b==1", ""},
	} {
		if r, o := splitOptions(in); r != want[0] || o != want[1] {
			t.Errorf("splitOptions(%q) = %q, %q", in, r, o)
		}
	}
}

// TestFixtures extracts real-world requirements files (sources in their first line).
func TestFixtures(t *testing.T) {
	for _, c := range []struct {
		file          string
		deps, skipped int
		first         want
	}{
		// uv pip compile and hand-maintained output with hashes.
		{"sigstore-requirements.txt", 3, 0, want{4, "annotated-types", "0.8.0", ""}},
		{"mypal68-flake8-requirements.txt", 7, 0, want{2, "flake8", "3.7.5", ""}},
		{"cpython-doc-requirements.txt", 8, 3, want{11, "sphinx", "9.1.1", ""}},
		{"home-assistant-requirements.txt", 60, 0, want{7, "aiodns", "4.0.4", ""}},
	} {
		t.Run(c.file, func(t *testing.T) {
			src, err := os.ReadFile("testdata/" + c.file)
			if err != nil {
				t.Fatal(err)
			}
			deps := extract(t, string(src))
			skipped := 0
			for _, d := range deps {
				if d.Skip != "" {
					skipped++
				}
			}
			if len(deps) != c.deps || skipped != c.skipped {
				t.Errorf("%d deps, %d skipped; want %d, %d", len(deps), skipped, c.deps, c.skipped)
			}
			check(t, deps[:1], []want{c.first})
		})
	}
}

func TestEdits(t *testing.T) {
	src := "requests==2.31.0\nuvicorn[standard] >= 0.12.0 ; python_version > '3'\n"
	deps := extract(t, src)
	var edits []patch.Edit
	for i, v := range []string{"2.32.3", "0.30.1"} {
		e, err := New().Edits(dep.Update{Dep: deps[i], NewVersion: v})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := "requests==2.32.3\nuvicorn[standard] >= 0.30.1 ; python_version > '3'\n"; string(got) != want {
		t.Errorf("got %q", got)
	}
	if New().Name() != Name {
		t.Error("Name")
	}
}

func TestRequirements(t *testing.T) {
	src, err := os.ReadFile("testdata/mypal68-flake8-requirements.txt")
	if err != nil {
		t.Fatal(err)
	}
	reqs := Requirements("requirements.txt", src)
	var got []string
	for _, r := range reqs {
		for _, s := range r.Hashes {
			if h := string(src[s.Start:s.End]); !strings.HasPrefix(h, "--hash=sha256:") || len(h) != len("--hash=sha256:")+64 {
				t.Errorf("%s: hash span %q", r.Dep.Name, h)
			}
		}
		got = append(got, fmt.Sprintf("%s %s %d", r.Dep.Name, r.Dep.Version, len(r.Hashes)))
	}
	want := "flake8 3.7.5 2|mccabe 0.6.1 2|pyflakes 2.1.0 2|pycodestyle 2.5.0 2|setuptools 40.7.1 2|autopep8 1.4.3 1|entrypoints 0.3 2"
	if strings.Join(got, "|") != want {
		t.Errorf("got %s", strings.Join(got, "|"))
	}
	reqs = Requirements("r.txt", []byte("a==1\nb==2 ; python_version > '3' --hash=sha256:x\ngit+https://x/y\n"))
	if len(reqs) != 2 || len(reqs[0].Hashes) != 0 || len(reqs[1].Hashes) != 1 {
		t.Errorf("requirements = %+v", reqs)
	}
}

func TestHashEdit(t *testing.T) {
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, tc := range []struct {
		name, src, want, err string
		hashes               []string
	}{
		{"continuation lines", "x==2 \\\n    --hash=sha256:old1 \\\n    --hash=sha256:old2\n    # via y\nz==1\n",
			"x==2 \\\n    --hash=sha256:" + a + " \\\n    --hash=sha256:" + b + " \\\n    --hash=sha256:" + c + "\n    # via y\nz==1\n", "", []string{c, a, b, a}},
		{"single hash on its own line", "autopep8==1.4.4 \\\n    --hash=sha256:old\nentrypoints==0.3\n",
			"autopep8==1.4.4 \\\n    --hash=sha256:" + a + " \\\n    --hash=sha256:" + b + "\nentrypoints==0.3\n", "", []string{b, a}},
		{"one line", "x==2 --hash=sha256:old1 --hash=sha256:old2 ; marker\n",
			"x==2 --hash=sha256:" + a + " ; marker\n", "", []string{a}},
		{"separate value", "x==2 --hash sha256:old\n", "x==2 --hash sha256:" + a + " --hash sha256:" + b + "\n", "", []string{a, b}},
		{"other option between", "x==2 --hash=sha256:old1 --config-settings=k=v --hash=sha256:old2\n", "", "other options between", []string{a}},
		{"no hashes", "x==2 --hash=sha256:old\n", "", "no hashes", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqs := Requirements("requirements.txt", []byte(tc.src))
			if len(reqs) == 0 || len(reqs[0].Hashes) == 0 {
				t.Fatalf("requirements %+v", reqs)
			}
			e, err := HashEdit([]byte(tc.src), reqs[0], tc.hashes)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := patch.Apply([]byte(tc.src), []patch.Edit{e})
			if err != nil || string(got) != tc.want {
				t.Errorf("got %q, %v\nwant %q", got, err, tc.want)
			}
		})
	}
}

func TestExtraIndexURL(t *testing.T) {
	src := "--index-url https://download.pytorch.org/whl/cpu\n--extra-index-url https://pypi.org/simple\n" +
		"torch==2.4.0\n--extra-index-url=https://extra.example/simple\n./local\n"
	deps, err := New().Extract("requirements.txt", []byte(src), manager.Env{})
	if err != nil || len(deps) != 2 {
		t.Fatalf("Extract = %d deps, %v", len(deps), err)
	}
	for _, d := range deps {
		if d.Source != "https://download.pytorch.org/whl/cpu" || !slices.Equal(d.ExtraSources, []string{"https://pypi.org/simple", "https://extra.example/simple"}) {
			t.Errorf("%s: source %q, extra %q", d.Name, d.Source, d.ExtraSources)
		}
	}
}

// BenchmarkExtract extracts a pip-compile style file of 2000 hashed requirements.
func BenchmarkExtract(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("--index-url https://pypi.org/simple\n")
	for i := range 2000 {
		fmt.Fprintf(&sb, "pkg%d==1.%d.0 \\\n", i, i)
		for h := range 3 {
			fmt.Fprintf(&sb, "    --hash=sha256:%064x \\\n", i*3+h)
		}
		fmt.Fprintf(&sb, "    --hash=sha256:%064x\n    # via app\n", i)
	}
	src := []byte(sb.String())
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Extract("requirements.txt", src, manager.Env{}); err != nil {
			b.Fatal(err)
		}
	}
}
