package nodetool

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runs splits a stub log into its runs: argv, env and the sorted files of the working
// directory.
func runs(t *testing.T, log string) (argv, env []string, files [][]string) {
	t.Helper()
	for _, block := range strings.Split(readLog(t, log), "argv: ")[1:] {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		argv = append(argv, lines[0])
		env = append(env, strings.TrimPrefix(lines[1], "env: "))
		var fs []string
		for _, l := range lines[2:] {
			fs = append(fs, strings.TrimPrefix(l, "file: ./"))
		}
		slices.Sort(fs)
		files = append(files, fs)
	}
	return argv, env, files
}

func TestMaintainCommands(t *testing.T) {
	const rootPkg = `{"name":"ws","private":true,"workspaces":["packages/*"]}`
	npmArgv := "npm install --package-lock-only --ignore-scripts --no-audit --no-fund"
	pnpmArgv := "pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile"
	for _, tt := range []struct {
		name      string
		tools     []string
		files     map[string][]byte // siblings
		rootPkg   string
		lock      string
		create    bool     // the stub creates the lockfile (it is not copied)
		wantArgv  []string // one per run
		wantEnv   string
		wantFiles [][]string // per run, besides package.json and the members
		wantLock  string
	}{
		{
			name: "npm installs twice without the lockfile", tools: []string{"npm", "pnpm", "yarn"},
			files: map[string][]byte{PackageLock: []byte("{}\n"), npmrc: []byte("x=y\n")},
			lock:  PackageLock, create: true,
			wantArgv:  []string{npmArgv, npmArgv},
			wantEnv:   "scripts= immutable=",
			wantFiles: [][]string{{npmrc}, {npmrc, PackageLock}},
			wantLock:  "created\nmodified\nmodified\n",
		},
		{
			name: "pnpm without the lockfile", tools: []string{"npm", "pnpm", "yarn"},
			files:   map[string][]byte{PnpmLock: []byte("lockfileVersion: '9.0'\n"), pnpmWorkspace: []byte("packages: [packages/*]\n")},
			rootPkg: `{"name":"ws","private":true}`,
			lock:    PnpmLock, create: true,
			wantArgv:  []string{pnpmArgv},
			wantEnv:   "scripts= immutable=",
			wantFiles: [][]string{{pnpmWorkspace}},
			wantLock:  "created\nmodified\n",
		},
		{
			name: "npx at the packageManager version", tools: []string{"npx"},
			files:   map[string][]byte{PackageLock: []byte("{}\n"), PnpmLock: []byte("lockfileVersion: '9.0'\n")},
			rootPkg: `{"name":"ws","packageManager":"pnpm@9.1.0","workspaces":["packages/*"]}`,
			lock:    PnpmLock, create: true,
			wantArgv:  []string{"npx --yes pnpm@9.1.0 install --lockfile-only --ignore-scripts --no-frozen-lockfile"},
			wantEnv:   "scripts= immutable=",
			wantFiles: [][]string{nil},
			wantLock:  "created\nmodified\n",
		},
		{
			name: "yarn berry with an empty lockfile", tools: []string{"yarn"},
			files:     map[string][]byte{YarnLock: []byte("__metadata:\n  version: 8\n"), yarnrc: []byte("enableTelemetry: false\n")},
			lock:      YarnLock,
			wantArgv:  []string{"yarn install --mode=update-lockfile"},
			wantEnv:   "scripts=0 immutable=0",
			wantFiles: [][]string{{yarnrc, YarnLock}},
			wantLock:  "modified\n",
		},
		{
			name: "yarn classic with an empty lockfile", tools: []string{"yarn"},
			files:     map[string][]byte{YarnLock: []byte("# yarn lockfile v1\n")},
			lock:      YarnLock,
			wantArgv:  []string{"yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress"},
			wantEnv:   "scripts= immutable=",
			wantFiles: [][]string{{YarnLock}},
			wantLock:  "modified\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := stubs(t, tt.tools...)
			if tt.create {
				t.Setenv("STUB_CREATE", tt.lock)
			}
			dir := t.TempDir()
			root := rootPkg
			if tt.rootPkg != "" {
				root = tt.rootPkg
			}
			disk := map[string]string{"package.json": root, "packages/a/package.json": `{"name":"a"}`, "packages/b/package.json": `{"name":"b"}`}
			write(t, dir, disk)

			out, err := Locker{}.Maintain(t.Context(), dir, tt.files)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 || string(out[tt.lock]) != tt.wantLock {
				t.Errorf("out = %q, want only %s = %q", out, tt.lock, tt.wantLock)
			}
			argv, env, files := runs(t, log)
			if !slices.Equal(argv, tt.wantArgv) {
				t.Errorf("argv = %q, want %q", argv, tt.wantArgv)
			}
			for i := range files {
				if env[i] != tt.wantEnv {
					t.Errorf("run %d env = %s, want %s", i, env[i], tt.wantEnv)
				}
				want := append([]string{"package.json", "packages/a/package.json", "packages/b/package.json"}, tt.wantFiles[i]...)
				slices.Sort(want)
				if !slices.Equal(files[i], want) {
					t.Errorf("run %d files = %v, want %v", i, files[i], want)
				}
			}
			for p, c := range disk {
				if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); string(b) != c {
					t.Errorf("%s changed: %s", p, b)
				}
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 2 {
				t.Errorf("working tree touched: %v", entries)
			}
		})
	}
}

func TestMaintainEdgeCases(t *testing.T) {
	log := stubs(t, "npm")
	dir := t.TempDir()
	write(t, dir, map[string]string{"package.json": string(manifest("^1.0.0"))})

	// No lockfile: nothing to do, the tool is not run.
	if out, err := (Locker{}).Maintain(t.Context(), dir, map[string][]byte{npmrc: []byte("x=y\n")}); err != nil || out != nil {
		t.Errorf("no lockfile: out = %v, err = %v", out, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("tool ran without a lockfile: %v", err)
	}
	// The regenerated lockfile is the one in the tree.
	t.Setenv("STUB_CREATE", PackageLock)
	t.Setenv("STUB_KEEP", "1")
	if out, err := (Locker{}).Maintain(t.Context(), dir, map[string][]byte{PackageLock: []byte("created\n")}); err != nil || out != nil {
		t.Errorf("unchanged: out = %v, err = %v", out, err)
	}
	// Tool failure: the tail of stdout and stderr.
	t.Setenv("STUB_FAIL", "1")
	if _, err := (Locker{}).Maintain(t.Context(), dir, map[string][]byte{PackageLock: []byte("{}\n")}); err == nil || err.Error() != "npm install in "+dir+": progress\nERR something broke" {
		t.Errorf("failure: err = %v", err)
	}
	// Without the root package.json.
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), map[string][]byte{PackageLock: []byte("{}\n")}); !os.IsNotExist(err) {
		t.Errorf("missing package.json: err = %v", err)
	}
}

// TestMaintainRealNpm refreshes a workspace package-lock.json with the real npm against a local
// registry: tiny, locked at 1.0.0 under the member's ^1.0.0, moves to 1.1.0; the range stays.
func TestMaintainRealNpm(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH")
	}
	isolate(t)
	srv := registry(t)
	dir := t.TempDir()
	member := `{"name":"a","version":"1.0.0","dependencies":{"tiny":"^1.0.0"}}` + "\n"
	disk := map[string]string{"package.json": `{"name":"ws","private":true,"workspaces":["packages/*"]}`, "packages/a/package.json": member}
	write(t, dir, disk)
	files := map[string][]byte{npmrc: []byte("registry=" + srv.URL + "/\n"), PackageLock: []byte(`{"name":"ws","lockfileVersion":3,"requires":true,"packages":{}}`)}
	// npm locks the exact 1.0.0; with the range put back, the lock is in sync with ^1.0.0.
	out, err := Locker{}.Process(t.Context(), dir, pkgs("packages/a/package.json", []byte(strings.Replace(member, "^1.0.0", "1.0.0", 1))), files)
	if err != nil {
		t.Fatal(err)
	}
	before := strings.Replace(string(out[PackageLock]), `"tiny": "1.0.0"`, `"tiny": "^1.0.0"`, 1)
	if !strings.Contains(before, "tiny-1.0.0.tgz") || !strings.Contains(before, `"tiny": "^1.0.0"`) {
		t.Fatalf("initial lock = %s", before)
	}
	files[PackageLock] = []byte(before)

	out, err = Locker{}.Maintain(t.Context(), dir, files)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out[PackageLock])
	if len(out) != 1 || !strings.Contains(got, "tiny-1.1.0.tgz") || strings.Contains(got, "tiny-1.0.0.tgz") || !strings.Contains(got, `"tiny": "^1.0.0"`) || !strings.Contains(got, `"packages/a"`) {
		t.Errorf("maintained lock = %s", got)
	}
	for p, c := range disk {
		if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); string(b) != c {
			t.Errorf("%s changed: %s", p, b)
		}
	}

	// Nothing newer: nothing to write.
	files[PackageLock] = out[PackageLock]
	if out, err := (Locker{}).Maintain(t.Context(), dir, files); err != nil || out != nil {
		t.Errorf("up to date: out = %q, err = %v", out, err)
	}
}

// realTool puts the package manager bin on PATH: the one there, else pkg installed from the
// public npm registry. Without npm, in -short mode or offline the test is skipped.
func realTool(t *testing.T, bin, pkg string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err == nil {
		return
	}
	if testing.Short() {
		t.Skip(bin + " not in PATH; installing it needs the npm registry")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH")
	}
	prefix := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "npm", "install", "--prefix", prefix, "--ignore-scripts", "--no-audit", "--no-fund", pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("installing %s: %v\n%s", pkg, err, out)
	}
	t.Setenv("PATH", filepath.Join(prefix, "node_modules", ".bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestMaintainRealPnpmYarn refreshes pnpm-lock.yaml and yarn.lock (classic and berry) of a
// workspace with the real tools against a local registry: tiny, locked at 1.0.0 under the
// member's ^1.0.0, moves to 1.1.0.
func TestMaintainRealPnpmYarn(t *testing.T) {
	for _, tt := range []struct {
		name, bin, pkg string
		lock           string
		files          func(url string) map[string][]byte // siblings besides the lockfile
		rootPkg        string
		pin            [2]string // locked at 1.0.0 -> under ^1.0.0
		want, gone     string
	}{
		{
			name: "pnpm", bin: "pnpm", pkg: "pnpm@10", lock: PnpmLock,
			files: func(url string) map[string][]byte {
				return map[string][]byte{npmrc: []byte("registry=" + url + "/\n"), pnpmWorkspace: []byte("packages:\n  - packages/*\n")}
			},
			rootPkg: `{"name":"ws","private":true}`,
			pin:     [2]string{"specifier: 1.0.0", "specifier: ^1.0.0"},
			want:    "tiny@1.1.0", gone: "tiny@1.0.0",
		},
		{
			name: "yarn classic", bin: "yarn", pkg: "yarn@1", lock: YarnLock,
			files:   func(url string) map[string][]byte { return map[string][]byte{npmrc: []byte("registry=" + url + "/\n")} },
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			pin:     [2]string{"tiny@1.0.0:", "tiny@^1.0.0:"},
			want:    `version "1.1.0"`, gone: `version "1.0.0"`,
		},
		{
			name: "yarn berry", bin: "yarn", pkg: "@yarnpkg/cli-dist@4", lock: YarnLock,
			// The local registry has no publish times, which yarn's npmMinimalAgeGate rejects.
			files: func(url string) map[string][]byte {
				return map[string][]byte{yarnrc: []byte("npmRegistryServer: \"" + url + "\"\nunsafeHttpWhitelist: [\"127.0.0.1\"]\nenableTelemetry: false\nnpmMinimalAgeGate: 0\n")}
			},
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			pin:     [2]string{`"tiny@npm:1.0.0":`, `"tiny@npm:^1.0.0":`},
			want:    `resolution: "tiny@npm:1.1.0"`, gone: `resolution: "tiny@npm:1.0.0"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			realTool(t, tt.bin, tt.pkg)
			srv := registry(t)
			dir := t.TempDir()
			member := `{"name":"a","version":"1.0.0","dependencies":{"tiny":"^1.0.0"}}` + "\n"
			disk := map[string]string{"package.json": tt.rootPkg, "packages/a/package.json": member}
			write(t, dir, disk)
			files := tt.files(srv.URL)
			files[tt.lock] = []byte{}
			if tt.lock == PnpmLock {
				files[tt.lock] = []byte("lockfileVersion: '9.0'\n")
			}
			out, err := Locker{}.Process(t.Context(), dir, pkgs("packages/a/package.json", []byte(strings.Replace(member, "^1.0.0", "1.0.0", 1))), files)
			if err != nil {
				t.Fatal(err)
			}
			before := string(out[tt.lock])
			if !strings.Contains(before, tt.pin[0]) || !strings.Contains(before, tt.gone) {
				t.Fatalf("initial lock = %s", before)
			}
			files[tt.lock] = []byte(strings.ReplaceAll(before, tt.pin[0], tt.pin[1]))

			out, err = Locker{}.Maintain(t.Context(), dir, files)
			if err != nil {
				t.Fatal(err)
			}
			got := string(out[tt.lock])
			if len(out) != 1 || !strings.Contains(got, tt.want) || strings.Contains(got, tt.gone) || !strings.Contains(got, tt.pin[1]) {
				t.Errorf("maintained lock = %s", got)
			}
			for p, c := range disk {
				if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); string(b) != c {
					t.Errorf("%s changed: %s", p, b)
				}
			}
		})
	}
}
