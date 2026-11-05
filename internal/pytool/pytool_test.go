package pytool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const workspace = "[project]\nname = \"root\"\n[tool.uv.workspace]\nmembers = [\"packages/*\", \"libs/a\"]\nexclude = [\"packages/skip\"]\n"

func TestScope(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"pyproject.toml":               workspace,
		"uv.lock":                      "",
		"packages/one/pyproject.toml":  "",
		"packages/skip/pyproject.toml": "",
		"libs/a/pyproject.toml":        "",
		"poetry/pyproject.toml":        "",
		"poetry/poetry.lock":           "",
		"pdm/pyproject.toml":           "",
		"pdm/pdm.lock":                 "",
		"pipenv/Pipfile":               "",
		"pipenv/Pipfile.lock":          "",
		"nolock/Pipfile":               "",
		"sub/uvlock/uv.lock":           "",
		"sub/uvlock/x/pyproject.toml":  "",
		"badws/uv.lock":                "",
		"badws/m/pyproject.toml":       "",
	})
	for _, c := range []struct {
		manifest, dir string
		ok            bool
	}{
		{"pyproject.toml", ".", true},
		{"packages/one/pyproject.toml", ".", true},
		{"libs/a/pyproject.toml", ".", true},
		{"packages/skip/pyproject.toml", ".", false},
		{"poetry/pyproject.toml", "poetry", true},
		{"pdm/pyproject.toml", "pdm", true},
		{"pipenv/Pipfile", "pipenv", true},
		{"nolock/Pipfile", "nolock", false},
		{"sub/uvlock/x/pyproject.toml", "", false}, // sub/uvlock has no pyproject.toml
		{"badws/m/pyproject.toml", "", false},
		{"requirements.txt", "", false},
	} {
		dir, ok := Locker{}.Scope(root, c.manifest)
		if ok != c.ok || ok && dir != c.dir {
			t.Errorf("Scope(%s) = %q, %v; want %q, %v", c.manifest, dir, ok, c.dir, c.ok)
		}
	}
	// Outside any lock.
	if _, ok := (Locker{}).Scope(t.TempDir(), "a/pyproject.toml"); ok {
		t.Error("scope without lockfile")
	}
}

// stubs puts shell scripts named tools first in PATH; each logs its name, argv, PIPENV/POETRY
// env and the files of its working directory, then appends "locked" to every lockfile there
// unless STUB_KEEP is set; STUB_FAIL makes it fail.
func stubs(t *testing.T, tools ...string) (log string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	find, err := exec.LookPath("find")
	if err != nil {
		t.Skip("find not in PATH")
	}
	sort, err := exec.LookPath("sort")
	if err != nil {
		t.Skip("sort not in PATH")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!` + sh + `
[ "$1" = "--version" ] && { echo "Poetry (version ${STUB_POETRY:-2.1.3})"; exit 0; }
{
  printf 'argv:'
  printf ' %s' "${0##*/}" "$@"
  printf '\nenv: %s %s\n' "$POETRY_NO_INTERACTION" "$PIPENV_VENV_IN_PROJECT"
  '` + find + `' . -type f | LC_ALL=C '` + sort + `' | while read -r f; do printf 'file: %s\n' "$f"; done
} >> '` + log + `'
if [ -n "$STUB_FAIL" ]; then echo "progress" >&2; echo "${STUB_FAIL_MSG:-ERR something broke}"; exit 3; fi
[ -n "$STUB_KEEP" ] && exit 0
for f in uv.lock poetry.lock pdm.lock Pipfile.lock; do
  [ -f "$f" ] && echo locked >> "$f"
done
exit 0
`
	for _, tool := range tools {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_KEEP", "")
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_FAIL_MSG", "")
	t.Setenv("STUB_POETRY", "")
	return log
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProcess(t *testing.T) {
	for _, c := range []struct {
		name      string
		tools     []string
		poetryV   string
		manifests map[string]string
		files     map[string]string // siblings
		disk      map[string]string // the scope directory besides the siblings
		lock      string
		argv      string
		wantFiles []string
	}{
		{
			name: "uv workspace", tools: []string{"uv"},
			manifests: map[string]string{"pyproject.toml": workspace, "packages/one/pyproject.toml": "patched"},
			files:     map[string]string{UVLock: "v1\n"},
			disk: map[string]string{"pyproject.toml": workspace, "README.md": "readme", "packages/one/pyproject.toml": "old", "packages/two/pyproject.toml": "two",
				"packages/skip/pyproject.toml": "skip", "packages/nopy/x.py": "", "libs/a/pyproject.toml": "a", "src/x.py": ""},
			lock: UVLock, argv: "uv lock --no-progress",
			wantFiles: []string{"./README.md", "./libs/a/pyproject.toml", "./packages/one/pyproject.toml", "./packages/two/pyproject.toml", "./pyproject.toml", "./uv.lock"},
		},
		{
			name: "poetry 2", tools: []string{"poetry"},
			manifests: map[string]string{"pyproject.toml": "[tool.poetry]\n"},
			files:     map[string]string{PoetryLock: "v1\n"},
			lock:      PoetryLock, argv: "poetry lock", wantFiles: []string{"./poetry.lock", "./pyproject.toml"},
		},
		{
			name: "poetry 1", tools: []string{"poetry"}, poetryV: "1.8.3",
			manifests: map[string]string{"pyproject.toml": "[tool.poetry]\n"},
			files:     map[string]string{PoetryLock: "v1\n"},
			lock:      PoetryLock, argv: "poetry lock --no-update", wantFiles: []string{"./poetry.lock", "./pyproject.toml"},
		},
		{
			name: "poetry via uvx", tools: []string{"uvx"},
			manifests: map[string]string{"pyproject.toml": "[tool.poetry]\n"},
			files:     map[string]string{PoetryLock: "v1\n"},
			lock:      PoetryLock, argv: "uvx --from poetry poetry lock", wantFiles: []string{"./poetry.lock", "./pyproject.toml"},
		},
		{
			name: "pdm", tools: []string{"pdm"},
			manifests: map[string]string{"pyproject.toml": "[project]\n"},
			files:     map[string]string{PDMLock: "v1\n"},
			lock:      PDMLock, argv: "pdm lock --update-reuse", wantFiles: []string{"./pdm.lock", "./pyproject.toml"},
		},
		{
			name: "pdm via uvx", tools: []string{"uvx"},
			manifests: map[string]string{"pyproject.toml": "[project]\n"},
			files:     map[string]string{PDMLock: "v1\n"},
			lock:      PDMLock, argv: "uvx --from pdm pdm lock --update-reuse", wantFiles: []string{"./pdm.lock", "./pyproject.toml"},
		},
		{
			name: "pipenv", tools: []string{"pipenv", "uv"},
			manifests: map[string]string{"Pipfile": "[packages]\n"},
			files:     map[string]string{PipfileLock: "{}\n", UVLock: "v1\n"},
			lock:      PipfileLock, argv: "pipenv lock", wantFiles: []string{"./Pipfile", "./Pipfile.lock", "./uv.lock"},
		},
		{
			name: "pipenv via uvx", tools: []string{"uvx"},
			manifests: map[string]string{"Pipfile": "[packages]\n"},
			files:     map[string]string{PipfileLock: "{}\n"},
			lock:      PipfileLock, argv: "uvx --from pipenv pipenv lock", wantFiles: []string{"./Pipfile", "./Pipfile.lock"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := stubs(t, c.tools...)
			t.Setenv("STUB_POETRY", c.poetryV)
			dir := t.TempDir()
			write(t, dir, c.disk)
			write(t, dir, c.files)
			manifests, files := bytesOf(c.manifests), bytesOf(c.files)
			out, err := Locker{}.Process(t.Context(), dir, manifests, files)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 || string(out[c.lock]) != c.files[c.lock]+"locked\n" {
				t.Errorf("out = %q", out)
			}
			got := readLog(t, log)
			if !strings.Contains(got, "argv: "+c.argv+"\n") {
				t.Errorf("log = %s, want argv %q", got, c.argv)
			}
			var listed []string
			for line := range strings.SplitSeq(got, "\n") {
				if f, ok := strings.CutPrefix(line, "file: "); ok {
					listed = append(listed, f)
				}
			}
			if strings.Join(listed, ",") != strings.Join(c.wantFiles, ",") {
				t.Errorf("files = %v, want %v", listed, c.wantFiles)
			}
			for p, want := range c.manifests {
				if b, _ := os.ReadFile(filepath.Join(dir, p)); p == "packages/one/pyproject.toml" && string(b) != "old" {
					t.Errorf("working tree touched: %s = %q (patched %q)", p, b, want)
				}
			}
		})
	}
}

func bytesOf(m map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = []byte(v)
	}
	return out
}

func TestProcessEdgeCases(t *testing.T) {
	dir := t.TempDir()
	manifests := bytesOf(map[string]string{"pyproject.toml": "x"})
	t.Run("no lockfile", func(t *testing.T) {
		if out, err := (Locker{}).Process(t.Context(), dir, manifests, nil); out != nil || err != nil {
			t.Errorf("out = %v, err = %v", out, err)
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		stubs(t, "uv")
		t.Setenv("STUB_KEEP", "1")
		if out, err := (Locker{}).Process(t.Context(), dir, manifests, bytesOf(map[string]string{UVLock: "v1"})); out != nil || err != nil {
			t.Errorf("out = %v, err = %v", out, err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		stubs(t, "uv")
		t.Setenv("STUB_FAIL", "1")
		_, err := Locker{}.Process(t.Context(), dir, manifests, bytesOf(map[string]string{UVLock: "v1"}))
		if err == nil || err.Error() != "uv lock --no-progress in "+dir+": progress\nERR something broke" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		stubs(t)
		for lock, tool := range map[string]string{UVLock: "uv", PoetryLock: "poetry", PDMLock: "pdm", PipfileLock: "pipenv"} {
			m := manifests
			if lock == PipfileLock {
				m = bytesOf(map[string]string{Pipfile: "x"})
			}
			_, err := Locker{}.Process(t.Context(), dir, m, bytesOf(map[string]string{lock: "v1"}))
			if err == nil || err.Error() != tool+" not found (needed to update "+lock+")" {
				t.Errorf("%s: err = %v", lock, err)
			}
		}
	})
	t.Run("lock removed", func(t *testing.T) {
		stubs(t, "uv")
		bin := filepath.SplitList(os.Getenv("PATH"))[0]
		write(t, bin, map[string]string{"uv": "#!/bin/sh\nrm uv.lock\n"})
		if err := os.Chmod(filepath.Join(bin, "uv"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := (Locker{}).Process(t.Context(), dir, manifests, bytesOf(map[string]string{UVLock: "v1"})); err == nil {
			t.Error("missing lockfile accepted")
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		if _, err := (Locker{}).Process(t.Context(), filepath.Join(dir, "nope"), manifests, bytesOf(map[string]string{UVLock: "v1"})); err == nil {
			t.Error("missing scope directory accepted")
		}
	})
	t.Run("bad member glob", func(t *testing.T) {
		stubs(t, "uv")
		d := t.TempDir()
		write(t, d, map[string]string{"pyproject.toml": "[tool.uv.workspace]\nmembers = [\"[\"]\n"})
		if _, err := (Locker{}).Process(t.Context(), d, nil, bytesOf(map[string]string{UVLock: "v1"})); err == nil {
			t.Error("invalid glob accepted")
		}
	})
}

func TestPoetryV1(t *testing.T) {
	for out, want := range map[string]bool{"Poetry (version 1.8.3)\n": true, "Poetry (version 2.1.3)": false, "garbage": false} {
		if got := poetryV1(out); got != want {
			t.Errorf("poetryV1(%q) = %v", out, got)
		}
	}
}

// TestProcessRealUV locks a dependency-free project with the real uv, offline.
func TestProcessRealUV(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH")
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	t.Setenv("UV_OFFLINE", "1")
	t.Setenv("UV_NO_CONFIG", "1")
	dir := t.TempDir()
	pyproject := func(v string) []byte {
		return []byte("[project]\nname = \"demo\"\nversion = \"" + v + "\"\nrequires-python = \">=3.8\"\ndependencies = []\n")
	}
	// Create the lockfile at 1.0.0 with uv itself, so the format is uv's.
	first := t.TempDir()
	write(t, first, map[string]string{Pyproject: string(pyproject("1.0.0"))})
	cmd := exec.CommandContext(t.Context(), "uv", "lock", "--no-progress")
	cmd.Dir = first
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("uv lock unavailable here: %v: %s", err, b)
	}
	lock, err := os.ReadFile(filepath.Join(first, UVLock))
	if err != nil || !strings.Contains(string(lock), `name = "demo"`) || !strings.Contains(string(lock), `version = "1.0.0"`) {
		t.Fatalf("lock = %s, %v", lock, err)
	}
	if out, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{Pyproject: pyproject("1.0.0")}, map[string][]byte{UVLock: lock}); err != nil || out != nil {
		t.Errorf("unchanged: out = %q, err = %v", out, err)
	}
	out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{Pyproject: pyproject("1.1.0")}, map[string][]byte{UVLock: lock})
	if err != nil || !strings.Contains(string(out[UVLock]), `version = "1.1.0"`) {
		t.Errorf("updated: out = %q, err = %v", out, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("working tree touched: %v", entries)
	}
}

// realLock creates the first lockfile of files with the tool (from PATH, else uvx; network),
// then checks that Process leaves it unchanged and, after the manifest's `==old` becomes
// `==next`, rewrites it to pin next (lockPin with VER replaced); it is skipped without the
// tool or uvx, or when the first lock fails. It runs in parallel with the other network tests.
func realLock(t *testing.T, manifest, lock string, files map[string]string, old, next, lockPin string, c cmdSpec) {
	t.Helper()
	pin := func(v string) string { return strings.ReplaceAll(lockPin, "VER", v) }
	if testing.Short() {
		t.Skip("network")
	}
	t.Parallel()
	if _, err := c.argv(); err != nil {
		t.Skipf("neither %s nor uvx in PATH", c.tool)
	}
	first := t.TempDir()
	write(t, first, files)
	if err := c.run(t.Context(), first); err != nil {
		t.Skipf("%s unavailable here: %v", c.tool, err)
	}
	locked, err := os.ReadFile(filepath.Join(first, lock))
	if err != nil || !strings.Contains(string(locked), pin(old)) {
		t.Fatalf("lock = %s, %v", locked, err)
	}
	dir := t.TempDir()
	write(t, dir, files)
	src := []byte(files[manifest])
	if out, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{manifest: src}, map[string][]byte{lock: locked}); err != nil || out != nil {
		t.Errorf("unchanged: out = %q, err = %v", out, err)
	}
	patched := []byte(strings.ReplaceAll(files[manifest], "=="+old, "=="+next))
	out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{manifest: patched}, map[string][]byte{lock: locked})
	if err != nil || !strings.Contains(string(out[lock]), pin(next)) || strings.Contains(string(out[lock]), pin(old)) {
		t.Errorf("updated: out = %s, err = %v", out[lock], err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, manifest)); string(b) != files[manifest] {
		t.Errorf("working tree touched: %s", b)
	}
}

// TestProcessRealPipenv locks a one-package Pipfile with the real pipenv.
func TestProcessRealPipenv(t *testing.T) {
	pipfile := "[[source]]\nurl = \"https://pypi.org/simple\"\nverify_ssl = true\nname = \"pypi\"\n\n[packages]\nsix = \"==1.16.0\"\n\n[docs]\nidna = \"==3.7\"\n"
	realLock(t, Pipfile, PipfileLock, map[string]string{Pipfile: pipfile}, "1.16.0", "1.17.0", `"version": "==VER"`, pipenvCmd())
}

// TestProcessRealPDM locks a one-package pyproject.toml with the real pdm.
func TestProcessRealPDM(t *testing.T) {
	pyproject := "[project]\nname = \"demo\"\nversion = \"1.0.0\"\nrequires-python = \">=3.9\"\ndependencies = [\"six==1.16.0\"]\n\n[tool.pdm]\ndistribution = false\n\n[tool.pdm.dev-dependencies]\ndev = [\"idna==3.7\"]\n"
	realLock(t, Pyproject, PDMLock, map[string]string{Pyproject: pyproject}, "1.16.0", "1.17.0", `version = "VER"`, pdmCmd())
}
