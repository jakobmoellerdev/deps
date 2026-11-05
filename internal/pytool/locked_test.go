package pytool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestReadLocks reads the real lockfiles in testdata (see their first lines): a uv workspace
// member locked by the root uv.lock, a PEP 621 project and a Poetry 1 project locked by
// poetry.lock, and a Pipfile.lock by category.
func TestReadLocks(t *testing.T) {
	d := func(file, depType, name string) dep.Dep {
		return dep.Dep{File: file, DepType: depType, Name: name, Range: ">=0"}
	}
	files := [][]dep.Dep{
		{
			d("fastapi-template/backend/pyproject.toml", "project.dependencies", "fastapi"),
			d("fastapi-template/backend/pyproject.toml", "project.dependencies", "python-multipart"),
			d("fastapi-template/backend/pyproject.toml", "project.dependencies", "psycopg"), // trimmed from the lock
			d("fastapi-template/backend/pyproject.toml", "dependency-groups", "Ruff"),
			{File: "fastapi-template/backend/pyproject.toml", Name: "skipped", Skip: "x", Range: ">=0"},
		},
		{d("fastapi-template/pyproject.toml", "dependency-groups", "zizmor")},
		{d("poetry/pyproject.toml", "project.dependencies", "requests"), d("poetry/pyproject.toml", "dependency-groups", "dulwich")},
		{d("rich/pyproject.toml", "dependencies", "Pygments"), d("rich/pyproject.toml", "dev-dependencies", "typing_extensions")},
		{
			// Pipfile deps are looked up in the section of the table their spec is in.
			pipfileDep(t, "requests", `">=2.32.0"`),
			pipfileDep(t, "pytest", `">=7.4.0"`),
			pipfileDep(t, "packaging", `">=2.32.0"`), // locked in develop only
			pipfileDep(t, "packaging", `">=7.4.0"`),
		},
		{d("nolock/pyproject.toml", "project.dependencies", "x")},
	}
	if errs := ReadLocks("testdata", files); len(errs) != 0 {
		t.Fatal(errs)
	}
	var got []string
	for _, f := range files {
		for _, d := range f {
			got = append(got, d.Name+"="+d.Locked)
		}
	}
	want := "fastapi=0.141.1 python-multipart=0.0.32 psycopg= Ruff=0.16.9 skipped= zizmor= requests=2.34.2 dulwich=1.2.17 " +
		"Pygments=2.19.2 typing_extensions=4.13.2 requests=2.33.0 pytest=9.0.3 packaging= packaging=26.0 x="
	if strings.Join(got, " ") != want {
		t.Errorf("locked =\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
}

// pipfileDep is a dep of testdata/pipenv-examples/Pipfile whose spec is quoted.
func pipfileDep(t *testing.T, name, quoted string) dep.Dep {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "pipenv-examples", Pipfile))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(src, []byte(quoted)) + 1
	return dep.Dep{File: "pipenv-examples/Pipfile", Name: name, Range: quoted[1 : len(quoted)-1], RefSpan: dep.Span{Start: i, End: i + len(quoted) - 2}}
}

func TestReadLocksErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"a/pyproject.toml": "", "a/uv.lock": "[[package]\n",
		"b/Pipfile": "", "b/Pipfile.lock": `{"default": []}`,
		"c/Pipfile": "", "c/Pipfile.lock": "{",
		// A name locked at two versions (a uv fork) is ambiguous.
		"d/pyproject.toml": "", "d/uv.lock": "[[package]]\nname = \"x\"\nversion = \"1.0\"\n\n[[package]]\nname = \"x\"\nversion = \"2.0\"\n\n[[package]]\nname = \"y\"\nversion = \"1.0\"\n\n[[package]]\nname = \"y\"\nversion = \"1.0\"\n",
	})
	files := [][]dep.Dep{
		{{File: "a/pyproject.toml", Name: "x", Range: ">=1"}, {File: "a/pyproject.toml", Name: "y", Range: ">=1"}},
		{{File: "b/Pipfile", DepType: "packages", Name: "x", Range: ">=1"}},
		{{File: "c/Pipfile", DepType: "packages", Name: "x", Range: ">=1"}},
		{{File: "d/pyproject.toml", Name: "x", Range: ">=1"}, {File: "d/pyproject.toml", Name: "y", Range: ">=1"}},
	}
	errs := ReadLocks(root, files)
	if len(errs) != 3 || !strings.HasPrefix(errs[0].Error(), "a/uv.lock: ") || !strings.HasPrefix(errs[1].Error(), "b/Pipfile.lock: default: ") ||
		!strings.HasPrefix(errs[2].Error(), "c/Pipfile.lock: ") {
		t.Errorf("errs = %v", errs)
	}
	if files[3][0].Locked != "" || files[3][1].Locked != "1.0" {
		t.Errorf("forks: %+v", files[3])
	}
}

// BenchmarkReadLocksPipfile reads the locks of a Pipfile with 100 deps in two categories.
func BenchmarkReadLocksPipfile(b *testing.B) {
	var pipfile strings.Builder
	var deps []dep.Dep
	lock := map[string]map[string]map[string]string{}
	for _, sec := range [][2]string{{"packages", "default"}, {"dev-packages", "develop"}} {
		fmt.Fprintf(&pipfile, "[%s]\n", sec[0])
		lock[sec[1]] = map[string]map[string]string{}
		for i := range 50 {
			name, spec := fmt.Sprintf("%s-%d", sec[1], i), fmt.Sprintf(">=1.%d", i)
			fmt.Fprintf(&pipfile, "%s = \"", name)
			start := pipfile.Len()
			fmt.Fprintf(&pipfile, "%s\"\n", spec)
			deps = append(deps, dep.Dep{File: Pipfile, Name: name, Range: spec, RefSpan: dep.Span{Start: start, End: start + len(spec)}})
			lock[sec[1]][name] = map[string]string{"version": fmt.Sprintf("==1.%d.0", i)}
		}
	}
	lockSrc, err := json.Marshal(lock)
	if err != nil {
		b.Fatal(err)
	}
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, Pipfile), []byte(pipfile.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PipfileLock), lockSrc, 0o600); err != nil {
		b.Fatal(err)
	}
	if errs := ReadLocks(root, [][]dep.Dep{deps}); len(errs) != 0 || deps[0].Locked != "1.0.0" || deps[99].Locked != "1.49.0" {
		b.Fatal(errs, deps[0], deps[99])
	}
	for b.Loop() {
		ReadLocks(root, [][]dep.Dep{deps})
	}
}

func lockUp(manifest, name, version string, at dep.Span) map[string][]dep.Update {
	return map[string][]dep.Update{manifest: {{Dep: dep.Dep{Name: name, RefSpan: at}, NewVersion: version, Lockfile: true}}}
}

const testPipfile = "[packages]\nrequests = \">=2.32\"\nidna = { version = \">=3\" }\n\n[dev-packages]\npytest = \">=9\"\n\n[docs]\nsphinx = \">=8\"\n"

// span is the span of the first s in src.
func span(src, s string) dep.Span {
	i := strings.Index(src, s)
	return dep.Span{Start: i, End: i + len(s)}
}

func TestProcessLockedCommands(t *testing.T) {
	for _, c := range []struct {
		name     string
		tool     string
		manifest string
		lock     string
		locks    map[string][]dep.Update
		argv     []string
	}{
		{"uv", "uv", Pyproject, UVLock, lockUp(Pyproject, "httpx", "0.28.1", dep.Span{}),
			[]string{"uv lock --no-progress --upgrade-package httpx==0.28.1"}},
		{"poetry", "poetry", Pyproject, PoetryLock, lockUp(Pyproject, "requests", "2.34.2", dep.Span{}),
			[]string{"poetry update --lock requests"}},
		{"pdm", "pdm", Pyproject, PDMLock, map[string][]dep.Update{Pyproject: {
			{Dep: dep.Dep{Name: "six", DepType: "project.dependencies"}, NewVersion: "1.17.0", Lockfile: true},
			{Dep: dep.Dep{Name: "IDNA", DepType: "tool.pdm.dev-dependencies"}, NewVersion: "3.20", Lockfile: true},
			{Dep: dep.Dep{Name: "unlocked", DepType: "project.dependencies"}, NewVersion: "1.0", Lockfile: true},
		}}, []string{
			"pdm update --update-reuse --no-sync -G default six unlocked",
			"pdm update --update-reuse --no-sync -G dev IDNA",
		}},
		{"pipenv", "pipenv", Pipfile, PipfileLock, map[string][]dep.Update{Pipfile: {
			{Dep: dep.Dep{Name: "requests", RefSpan: span(testPipfile, `>=2.32`)}, NewVersion: "2.34.0", Lockfile: true},
			{Dep: dep.Dep{Name: "pytest", RefSpan: span(testPipfile, `>=9`)}, NewVersion: "9.1.1", Lockfile: true},
			{Dep: dep.Dep{Name: "idna", RefSpan: span(testPipfile, `>=3`)}, NewVersion: "3.16", Lockfile: true},
			{Dep: dep.Dep{Name: "sphinx", RefSpan: span(testPipfile, `>=8`)}, NewVersion: "8.2.3", Lockfile: true},
		}}, []string{
			"pipenv upgrade --lock-only --categories dev-packages pytest==9.1.1",
			"pipenv upgrade --lock-only --categories docs sphinx==8.2.3",
			"pipenv upgrade --lock-only --categories packages requests==2.34.0 idna==3.16",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := stubs(t, c.tool)
			dir := t.TempDir()
			lock := "v1\n"
			if c.lock == PDMLock {
				lock = "[[package]]\nname = \"six\"\ngroups = [\"dev\", \"default\"]\n\n[[package]]\nname = \"idna\"\ngroups = [\"dev\"]\n"
			}
			old := "old"
			if c.manifest == Pipfile {
				old = testPipfile
			}
			write(t, dir, map[string]string{c.manifest: old, c.lock: lock})
			files := map[string][]byte{c.lock: []byte(lock)}
			// The manifest is unchanged: only the lockfile-only commands run.
			out, err := Locker{}.ProcessLocked(t.Context(), dir, map[string][]byte{c.manifest: []byte(old)}, files, c.locks)
			if err != nil || len(out) != 1 {
				t.Fatalf("out = %q, err = %v", out, err)
			}
			if got, want := argvs(readLog(t, log)), strings.Join(c.argv, "\n"); got != want {
				t.Errorf("argv =\n%s\nwant\n%s", got, want)
			}
			// A patched manifest is locked first.
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if _, err := (Locker{}).ProcessLocked(t.Context(), dir, map[string][]byte{c.manifest: []byte("new")}, files, c.locks); err != nil {
				t.Fatal(err)
			}
			if got := argvs(readLog(t, log)); len(strings.Split(got, "\n")) != len(c.argv)+1 || !strings.HasSuffix(got, strings.Join(c.argv, "\n")) {
				t.Errorf("patched argv =\n%s", got)
			}
		})
	}
	if _, err := (Locker{}).ProcessLocked(t.Context(), t.TempDir(), nil, nil, lockUp(Pyproject, "x", "1", dep.Span{})); err == nil {
		t.Error("no lockfile: no error")
	}
}

// argvs returns the argv lines of a stub log, without the "argv: " prefix.
func argvs(log string) string {
	var out []string
	for line := range strings.SplitSeq(log, "\n") {
		if a, ok := strings.CutPrefix(line, "argv: "); ok {
			out = append(out, a)
		}
	}
	return strings.Join(out, "\n")
}

// TestProcessLockedReal moves a locked version inside the manifest's range with the real
// tools (network; uvx for poetry, pipenv and pdm when not in PATH): the lockfile pins the new
// version and the manifest is not touched.
func TestProcessLockedReal(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	pyproject := func(spec string) string {
		return "[project]\nname = \"demo\"\nversion = \"1.0.0\"\nrequires-python = \">=3.9\"\ndependencies = [\"six" + spec + "\"]\n"
	}
	pipfile := func(spec string) string {
		return "[[source]]\nurl = \"https://pypi.org/simple\"\nverify_ssl = true\nname = \"pypi\"\n\n[packages]\nsix = \"" + spec + "\"\n"
	}
	for _, c := range []struct {
		name, manifest, lock, pin string
		src                       func(string) string
		cmd                       cmdSpec
	}{
		{"uv", Pyproject, UVLock, `name = "six"` + "\nversion = \"VER\"", pyproject, uvCmd()},
		{"poetry", Pyproject, PoetryLock, `name = "six"` + "\nversion = \"VER\"", pyproject, poetryCmd(t.Context())},
		{"pdm", Pyproject, PDMLock, `name = "six"` + "\nversion = \"VER\"", func(s string) string { return pyproject(s) + "\n[tool.pdm]\ndistribution = false\n" }, pdmCmd()},
		{"pipenv", Pipfile, PipfileLock, `"version": "==VER"`, pipfile, pipenvCmd()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.cmd.argv(); err != nil {
				t.Skipf("neither %s nor uvx in PATH", c.cmd.tool)
			}
			t.Setenv("UV_CACHE_DIR", t.TempDir())
			// Lock at 1.16.0 with a pin, then widen the manifest to a range admitting 1.17.0.
			dir := t.TempDir()
			write(t, dir, map[string]string{c.manifest: c.src("==1.16.0")})
			if err := c.cmd.run(t.Context(), dir); err != nil {
				t.Skipf("%s unavailable here: %v", c.cmd.tool, err)
			}
			src := c.src(">=1.16,<2")
			write(t, dir, map[string]string{c.manifest: src})
			lock, err := os.ReadFile(filepath.Join(dir, c.lock))
			pin := func(v string) string { return strings.ReplaceAll(c.pin, "VER", v) }
			if err != nil || !strings.Contains(string(lock), pin("1.16.0")) {
				t.Fatalf("lock = %s, %v", lock, err)
			}
			if c.lock != PipfileLock {
				// Poetry and pdm record the manifest's hash: re-lock the widened range first.
				if err := c.cmd.run(t.Context(), dir); err != nil {
					t.Fatal(err)
				}
				if lock, err = os.ReadFile(filepath.Join(dir, c.lock)); err != nil || !strings.Contains(string(lock), pin("1.16.0")) {
					t.Fatalf("relocked = %s, %v", lock, err)
				}
			}
			out, err := Locker{}.ProcessLocked(t.Context(), dir, map[string][]byte{c.manifest: []byte(src)}, map[string][]byte{c.lock: lock},
				lockUp(c.manifest, "six", "1.17.0", span(src, ">=1.16,<2")))
			if err != nil || !strings.Contains(string(out[c.lock]), pin("1.17.0")) {
				t.Errorf("out = %s, err = %v", out[c.lock], err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, c.manifest)); string(b) != src {
				t.Errorf("working tree touched: %s", b)
			}
		})
	}
}
