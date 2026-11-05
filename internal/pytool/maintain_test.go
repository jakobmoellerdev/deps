package pytool

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestMaintainCommands(t *testing.T) {
	for _, c := range []struct {
		name  string
		tools []string
		disk  map[string]string // the lock directory
		lock  string
		argv  string
		env   string
		file  string // a file the copy must hold
	}{
		{"uv", []string{"uv"}, map[string]string{Pyproject: workspace, "packages/one/pyproject.toml": "m"}, UVLock,
			"uv lock --no-progress --upgrade", "env:  ", "./packages/one/pyproject.toml"},
		{"poetry", []string{"poetry"}, map[string]string{Pyproject: "p", "README.md": "r"}, PoetryLock,
			"poetry update --lock", "env: 1 ", "./README.md"},
		{"poetry uvx", []string{"uvx"}, map[string]string{Pyproject: "p"}, PoetryLock,
			"uvx --from poetry poetry update --lock", "env: 1 ", "./pyproject.toml"},
		{"pdm", []string{"pdm"}, map[string]string{Pyproject: "p"}, PDMLock,
			"pdm update --no-sync --update-all", "env:  ", "./pyproject.toml"},
		{"pipenv", []string{"pipenv"}, map[string]string{Pipfile: "p"}, PipfileLock,
			"pipenv lock", "env:  1", "./Pipfile"},
		// uv.lock wins over poetry.lock and, next to a pyproject.toml, over Pipfile.lock.
		{"uv first", []string{"uv"}, map[string]string{Pyproject: "p", PoetryLock: "l", Pipfile: "p", PipfileLock: "l"}, UVLock,
			"uv lock --no-progress --upgrade", "env:  ", "./Pipfile.lock"},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := stubs(t, c.tools...)
			t.Setenv("POETRY_NO_INTERACTION", "")
			t.Setenv("PIPENV_VENV_IN_PROJECT", "")
			dir := t.TempDir()
			write(t, dir, c.disk)
			write(t, dir, map[string]string{c.lock: "v1\n"})
			files := map[string][]byte{c.lock: []byte("v1\n")}
			for _, n := range (Locker{}).Siblings() {
				if b, ok := c.disk[n]; ok {
					files[n] = []byte(b)
				}
			}
			out, err := Locker{}.Maintain(t.Context(), dir, files)
			if err != nil || len(out) != 1 || string(out[c.lock]) != "v1\nlocked\n" {
				t.Fatalf("out = %q, err = %v", out, err)
			}
			got := readLog(t, log)
			if a := argvs(got); a != c.argv {
				t.Errorf("argv = %s, want %s", a, c.argv)
			}
			if !strings.Contains(got, "\n"+c.env+"\n") || !strings.Contains(got, "file: "+c.file+"\n") {
				t.Errorf("log = %s", got)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, c.lock)); string(b) != "v1\n" {
				t.Errorf("working tree touched: %q", b)
			}
			// Nothing changed.
			t.Setenv("STUB_KEEP", "1")
			if out, err := (Locker{}).Maintain(t.Context(), dir, files); err != nil || out != nil {
				t.Errorf("unchanged: out = %q, err = %v", out, err)
			}
			t.Setenv("STUB_KEEP", "")
			t.Setenv("STUB_FAIL", "1")
			if _, err := (Locker{}).Maintain(t.Context(), dir, files); err == nil || !strings.Contains(err.Error(), " in "+dir+": progress\nERR something broke") {
				t.Errorf("failure: err = %v", err)
			}
		})
	}
	t.Run("no lockfile", func(t *testing.T) {
		log := stubs(t, "uv", "poetry", "pdm", "pipenv")
		for _, disk := range []map[string]string{{Pyproject: "p"}, {Pipfile: "p"}, {}} {
			dir := t.TempDir()
			write(t, dir, disk)
			if out, err := (Locker{}).Maintain(t.Context(), dir, nil); err != nil || out != nil {
				t.Errorf("%v: out = %q, err = %v", disk, out, err)
			}
		}
		if _, err := os.Stat(log); err == nil {
			t.Errorf("a tool ran: %s", readLog(t, log))
		}
	})
	t.Run("tool not found", func(t *testing.T) {
		stubs(t)
		dir := t.TempDir()
		write(t, dir, map[string]string{Pyproject: "p", UVLock: "v1\n"})
		_, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{UVLock: []byte("v1\n")})
		if err == nil || err.Error() != "uv not found (needed to update uv.lock)" {
			t.Errorf("err = %v", err)
		}
	})
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"pyproject.toml":              workspace,
		"uv.lock":                     "",
		"packages/one/pyproject.toml": "",
		"libs/a/pyproject.toml":       "",
		"poetry/pyproject.toml":       "",
		"poetry/poetry.lock":          "",
		"pdm/pyproject.toml":          "",
		"pdm/pdm.lock":                "",
		"both/pyproject.toml":         "",
		"both/uv.lock":                "",
		"both/poetry.lock":            "",
		"pipenv/Pipfile":              "",
		"pipenv/Pipfile.lock":         "",
		"mixed/Pipfile":               "",
		"mixed/Pipfile.lock":          "",
		"mixed/pyproject.toml":        "",
		"mixed/pdm.lock":              "",
		"nolock/pyproject.toml":       "",
		"nolock/Pipfile":              "",
	})
	pyprojects := []string{"libs/a/pyproject.toml", "packages/one/pyproject.toml", "pyproject.toml", "poetry/pyproject.toml",
		"pdm/pyproject.toml", "both/pyproject.toml", "mixed/pyproject.toml", "nolock/pyproject.toml"}
	pipfiles := []string{"pipenv/Pipfile", "mixed/Pipfile", "nolock/Pipfile"}
	lr := func(m, l string) manager.LockRoot { return manager.LockRoot{Manifest: m, Lockfile: l} }
	want := map[string][]manager.LockRoot{
		"pep621": {lr("both/pyproject.toml", "both/uv.lock"), lr("mixed/pyproject.toml", "mixed/pdm.lock"),
			lr("pdm/pyproject.toml", "pdm/pdm.lock"), lr("pyproject.toml", "uv.lock")},
		"poetry": {lr("poetry/pyproject.toml", "poetry/poetry.lock")},
		"pipenv": {lr("pipenv/Pipfile", "pipenv/Pipfile.lock")},
	}
	got := map[string][]manager.LockRoot{
		"pep621": LockRoots(root, pyprojects, UVLock, PDMLock),
		"poetry": LockRoots(root, pyprojects, PoetryLock),
		"pipenv": LockRoots(root, pipfiles, PipfileLock),
	}
	seen := map[string]bool{}
	for m, w := range want {
		if !slices.Equal(got[m], w) {
			t.Errorf("%s: LockRoots = %v, want %v", m, got[m], w)
		}
		for _, r := range got[m] {
			if seen[r.Lockfile] {
				t.Errorf("%s claimed twice", r.Lockfile)
			}
			seen[r.Lockfile] = true
		}
	}
	if got := LockRoots(root, nil, UVLock); got != nil {
		t.Errorf("no files: %v", got)
	}
}

// TestMaintainReal moves a locked version up inside the manifest's range with the real tools
// (network; uvx for poetry, pipenv and pdm when not in PATH); the manifest is not touched.
func TestMaintainReal(t *testing.T) {
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
			pin := func(v string) string { return strings.ReplaceAll(c.pin, "VER", v) }
			// Lock at 1.16.0 with a pin, then widen the manifest to a range admitting 1.17.0.
			dir := t.TempDir()
			write(t, dir, map[string]string{c.manifest: c.src("==1.16.0")})
			if err := c.cmd.run(t.Context(), dir); err != nil {
				t.Skipf("%s unavailable here: %v", c.cmd.tool, err)
			}
			src := c.src(">=1.16,<2")
			write(t, dir, map[string]string{c.manifest: src})
			if c.lock != PipfileLock {
				// The lock matches the widened manifest and keeps 1.16.0 (pipenv lock would not).
				if err := c.cmd.run(t.Context(), dir); err != nil {
					t.Fatal(err)
				}
			}
			lock, err := os.ReadFile(filepath.Join(dir, c.lock))
			if err != nil || !strings.Contains(string(lock), pin("1.16.0")) {
				t.Fatalf("lock = %s, %v", lock, err)
			}
			out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{c.lock: lock})
			if err != nil || !strings.Contains(string(out[c.lock]), pin("1.17.0")) {
				t.Errorf("out = %s, err = %v", out[c.lock], err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, c.manifest)); string(b) != src {
				t.Errorf("manifest touched: %s", b)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, c.lock)); !bytes.Equal(b, lock) {
				t.Errorf("lockfile touched: %s", b)
			}
		})
	}
}
