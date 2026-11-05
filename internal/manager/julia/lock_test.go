package julia

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLockedPackages(t *testing.T) {
	pkgs, err := lockedPackages(read(t, "testdata/packageanalyzer/Manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if pkgs["c3e4b0f8-55cb-11ea-2926-15256bba5781"] != "0.19.27" || pkgs["0dad84c5-d112-42e6-8d28-ef12dabb789f"] != "1.1.1" {
		t.Errorf("Pluto/ArgTools = %v", pkgs)
	}
	if _, ok := pkgs["56f22d72-fd6d-98f1-02f0-08ddc0907c33"]; ok { // Artifacts: a stdlib without version
		t.Error("an entry without version is locked")
	}
	ws, err := lockedPackages(read(t, "testdata/revise/Manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if ws["aa1ae85d-cabe-5617-a682-6adf51b2e16a"] != "0.12.2" || ws["295af30f-e4ad-537b-8983-00126c2a3abe"] != "" {
		t.Errorf("JuliaInterpreter / Revise (path) = %q / %q", ws["aa1ae85d-cabe-5617-a682-6adf51b2e16a"], ws["295af30f-e4ad-537b-8983-00126c2a3abe"])
	}
	// Format 1 (Julia < 1.7): the packages are the top-level arrays.
	v1 := "[[JSON]]\ndeps = [\"Dates\"]\ngit-tree-sha1 = \"x\"\nuuid = \"682C06A0-de6a-54ab-a142-c8b1cf79cde6\"\nversion = \"0.21.4\"\n\n" +
		"[[Dev]]\npath = \"dev/Dev\"\nuuid = \"11111111-de6a-54ab-a142-c8b1cf79cde6\"\nversion = \"0.1.0\"\n\n" +
		"[[Git]]\nrepo-url = \"https://github.com/o/Git.jl\"\nuuid = \"22222222-de6a-54ab-a142-c8b1cf79cde6\"\nversion = \"0.1.0\"\n"
	if p, err := lockedPackages([]byte(v1)); err != nil || len(p) != 1 || p["682c06a0-de6a-54ab-a142-c8b1cf79cde6"] != "0.21.4" {
		t.Errorf("format 1 = %v, %v", p, err)
	}
	for in, msg := range map[string]string{
		"manifest_format = \"3.0\"\n":           "unsupported manifest_format",
		"manifest_format = \"2.0\"\ndeps = 1\n": "deps is not a table",
		"[[x":                                   "toml",
	} {
		if _, err := lockedPackages([]byte(in)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("lockedPackages(%q) = %v, want %s", in, err, msg)
		}
	}
	if p, err := lockedPackages([]byte("manifest_format = \"2.0\"\n")); err != nil || len(p) != 0 {
		t.Errorf("empty manifest = %v, %v", p, err)
	}
}

// repo writes a repository of the testdata directories below root (with a .git, so the mirror
// stops there) and returns root.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files[".git/HEAD"] = ""
	write(t, root, files)
	return root
}

func testdata(t *testing.T, dir string, files map[string]string) map[string]string {
	t.Helper()
	err := filepath.WalkDir(filepath.Join("testdata", dir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel("testdata", p)
		files[filepath.ToSlash(rel)] = string(read(t, p))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLockPath(t *testing.T) {
	root := repo(t, testdata(t, "cuda", testdata(t, "revise", map[string]string{
		"cuda/Manifest.toml": "", "revise/test/x.jl": "",
		"app/Manifest-v1.10.toml": "", "app/Manifest-v1.11.toml": "", "app/JuliaManifest-v1.9.toml": "", "app/Project.toml": "",
		"plain/Manifest-v1.11.toml": "", "plain/Manifest.toml": "", "plain/Project.toml": "",
		"jp/JuliaProject.toml": "", "jp/JuliaManifest.toml": "", "jp/sub/Project.toml": "",
		"none/Project.toml": "", "outside/Project.toml": "",
	})))
	for project, want := range map[string]string{
		"revise/Project.toml":               "revise/Manifest.toml",
		"revise/test/Project.toml":          "revise/Manifest.toml",
		"revise/docs/Project.toml":          "revise/Manifest.toml",
		"cuda/lib/cusolver/Project.toml":    "cuda/Manifest.toml",
		"cuda/lib/cublas/Project.toml":      "cuda/Manifest.toml",
		"cuda/lib/cublas/test/Project.toml": "cuda/Manifest.toml", // nested: cublas lists test, the root lists lib/cublas
		"app/Project.toml":                  "app/Manifest-v1.11.toml",
		"plain/Project.toml":                "plain/Manifest.toml",
		"jp/JuliaProject.toml":              "jp/JuliaManifest.toml",
		"jp/sub/Project.toml":               "", // not listed in a workspace
		"none/Project.toml":                 "",
	} {
		if got := lockPath(root, project); got != want {
			t.Errorf("lockPath(%s) = %q, want %q", project, got, want)
		}
	}
	if dir, ok := (Locker{}).Scope(t.Context(), root, "revise/test/Project.toml"); !ok || dir != "revise" {
		t.Errorf("Scope = %s, %v", dir, ok)
	}
	if _, ok := (Locker{}).Scope(t.Context(), root, "none/Project.toml"); ok {
		t.Error("Scope without a manifest")
	}
	if got := (Locker{}).ScopeSiblings(filepath.Join(root, "app")); !slices.Equal(got, []string{"Manifest-v1.11.toml"}) {
		t.Errorf("ScopeSiblings = %v", got)
	}
	if got := (Locker{}).ScopeSiblings(filepath.Join(root, "none")); got != nil {
		t.Errorf("ScopeSiblings(none) = %v", got)
	}
	if !slices.Equal((Locker{}).Siblings(), []string{"JuliaManifest.toml", "Manifest.toml"}) {
		t.Error("Siblings")
	}
	roots := New().(manager.LockMaintainer).LockRoots(root, []string{"revise/test/Project.toml", "revise/Project.toml", "none/Project.toml", "plain/Project.toml"})
	if want := []manager.LockRoot{{Manifest: "plain/Project.toml", Lockfile: "plain/Manifest.toml"}, {Manifest: "revise/Project.toml", Lockfile: "revise/Manifest.toml"}}; !slices.Equal(roots, want) {
		t.Errorf("LockRoots = %v", roots)
	}
}

func extractAll(t *testing.T, root string, files ...string) [][]dep.Dep {
	t.Helper()
	var out [][]dep.Dep
	for _, f := range files {
		deps, err := New().Extract(f, read(t, filepath.Join(root, filepath.FromSlash(f))), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, deps)
	}
	return out
}

func find(files [][]dep.Dep, file, name string) dep.Dep {
	for _, f := range files {
		for _, d := range f {
			if d.File == file && d.Name == name {
				return d
			}
		}
	}
	return dep.Dep{}
}

func TestReadLocks(t *testing.T) {
	root := repo(t, testdata(t, "revise", testdata(t, "packageanalyzer", map[string]string{
		"broken/Project.toml":  "[deps]\nJSON = \"682c06a0-de6a-54ab-a142-c8b1cf79cde6\"\n[compat]\nJSON = \"0.21\"\n",
		"broken/Manifest.toml": "[[x",
		"lib/Project.toml":     "[deps]\nJSON = \"682c06a0-de6a-54ab-a142-c8b1cf79cde6\"\nX = \"11111111-de6a-54ab-a142-c8b1cf79cde6\"\n[compat]\nJSON = \"< 2\"\n",
	})))
	files := extractAll(t, root, "packageanalyzer/Project.toml", "revise/Project.toml", "revise/test/Project.toml", "revise/docs/Project.toml", "broken/Project.toml", "lib/Project.toml")
	errs := New().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "broken/Manifest.toml: toml") {
		t.Errorf("errs = %v", errs)
	}
	for _, tt := range []struct {
		file, name, version, locked, skip string
		unread                            bool
	}{
		{"packageanalyzer/Project.toml", "Pluto", "0.19.27", "0.19.27", "", false},
		{"revise/Project.toml", "JuliaInterpreter", "0.12", "0.12.2", "", false},
		{"revise/Project.toml", "Distributed", "1", "", skipStdlib, false},
		{"revise/test/Project.toml", "Requires", "1.1.1", "1.3.1", "", false},
		{"revise/test/Project.toml", "Example", "0.5.5", "0.5.5", "", false},
		{"revise/docs/Project.toml", "Revise", "", "", skipNoLock, false}, // the workspace's own package (path)
		{"broken/Project.toml", "JSON", "0.21", "", "", true},
		{"lib/Project.toml", "JSON", "", "", skipNoLock, false},
		{"lib/Project.toml", "X", "", "", skipNoLock, false},
	} {
		d := find(files, tt.file, tt.name)
		if d.Version != tt.version || d.Locked != tt.locked || d.Skip != tt.skip || d.LockUnread != tt.unread {
			t.Errorf("%s %s: version %q locked %q skip %q unread %v", tt.file, tt.name, d.Version, d.Locked, d.Skip, d.LockUnread)
		}
	}
}

// stub installs a fake julia on PATH (only it: it uses shell builtins) that logs its
// arguments relative to the mirror, which project and manifest files the mirror holds as
// copies, and appends a comment to the mirror's Manifest.toml; with STUB_FAIL set it fails,
// with STUB_NOOP it changes nothing.
func stub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  for a in "$@"; do echo "arg: ${a#"$PWD"}"; done
  [ "$JULIA_PKG_PRECOMPILE_AUTO" = 0 ] || echo "precompiling"
  for f in Project.toml test/Project.toml docs/Project.toml Manifest.toml JuliaManifest.toml Manifest-v1.11.toml; do
    if [ -L "$f" ]; then echo "$f: symlink"; elif [ -f "$f" ]; then echo "$f: copy"; fi
  done
  [ -e "$PWD/../shared/Shared.jl" ] && echo "sibling visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "ERROR: Unsatisfiable requirements detected for package JSON in $PWD"
  echo "Stacktrace:"
  echo " [1] pkgerror"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "# resolved" >> Manifest.toml
`
	if err := os.WriteFile(filepath.Join(bin, "julia"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	return log
}

func upd(file, name, uuid, depType, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, File: file, Name: name, Source: uuid, DepType: depType}, NewVersion: version}
}

const (
	interpreterUUID = "aa1ae85d-cabe-5617-a682-6adf51b2e16a"
	requiresUUID    = "ae029012-a4dd-5104-9daa-d747884805df"
	documenterUUID  = "e30172f5-a6a5-5a46-863b-614d45cd2de4"
)

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	root := repo(t, testdata(t, "revise", map[string]string{"shared/Shared.jl": "", "revise/Manifest-v1.11.toml": "old"}))
	dir := filepath.Join(root, "revise")
	manifest := read(t, filepath.Join(dir, "Manifest.toml"))
	patched := strings.Replace(string(read(t, filepath.Join(dir, "Project.toml"))), `JuliaInterpreter = "0.11, 0.12"`, `JuliaInterpreter = "0.11, 0.12, 1"`, 1)
	ups := map[string][]dep.Update{"Project.toml": {
		upd("revise/Project.toml", "JuliaInterpreter", interpreterUUID, DepTypeDeps, "1.0.1"),
		upd("revise/Project.toml", "Distributed", "8ba89e20-285c-5b6f-9357-94700520ee1b", DepTypeWeakdeps, "1.0.0"), // not locked: resolved anyway
	}}
	locks := map[string][]dep.Update{
		"test/Project.toml": {upd("revise/test/Project.toml", "Requires", requiresUUID, DepTypeDeps, "1.3.2+0")},
		"docs/Project.toml": {upd("revise/docs/Project.toml", "Documenter", documenterUUID, DepTypeDeps, "1.17.0")},
	}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"Project.toml": []byte(patched)}, map[string][]byte{"Manifest.toml": manifest}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["Manifest.toml"]); got != string(manifest)+"# resolved\n" {
		t.Errorf("Manifest.toml ends %q", got[max(0, len(got)-40):])
	}
	b := string(read(t, log))
	for _, want := range []string{
		"arg: --startup-file=no\n",
		"Pkg.activate(ARGS[1])\nPkg.add([Pkg.PackageSpec(name=\"JuliaInterpreter\", uuid=\"" + interpreterUUID + "\", version=\"1.0.1\")])\n" +
			"Pkg.activate(ARGS[2])\nPkg.add([Pkg.PackageSpec(name=\"Documenter\", uuid=\"" + documenterUUID + "\", version=\"1.17.0\")])\n" +
			"Pkg.activate(ARGS[3])\nPkg.add([Pkg.PackageSpec(name=\"Requires\", uuid=\"" + requiresUUID + "\", version=\"1.3.2\")])\n",
		"isempty(Pkg.Registry.reachable_registries()) && Pkg.Registry.add(\"General\")\n",
		"arg: \narg: /docs\narg: /test\n",
		"Project.toml: copy\ntest/Project.toml: copy\ndocs/Project.toml: copy\nManifest.toml: copy\nsibling visible\n",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("log lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Manifest-v1.11.toml") || strings.Contains(b, "precompiling") {
		t.Errorf("versioned manifest mirrored or precompiling:\n%s", b)
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "Manifest.toml")), manifest) || strings.Contains(string(read(t, filepath.Join(dir, "Project.toml"))), "0.12, 1") {
		t.Error("the working tree was modified")
	}
}

func TestProcessResolve(t *testing.T) {
	log := stub(t)
	root := repo(t, testdata(t, "revise", map[string]string{}))
	dir := filepath.Join(root, "revise")
	manifest := read(t, filepath.Join(dir, "Manifest.toml"))
	src := string(read(t, filepath.Join(dir, "Project.toml")))
	patched := strings.Replace(src, `Distributed = "1"`, `Distributed = "1, 2"`, 1)
	if _, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{"Project.toml": []byte(patched)}, map[string][]byte{"Manifest.toml": manifest}); err != nil {
		t.Fatal(err)
	}
	if b := string(read(t, log)); !strings.Contains(b, "Pkg.activate(ARGS[1])\nPkg.resolve()\n") || !strings.Contains(b, "\narg: \n") {
		t.Errorf("log:\n%s", b)
	}
}

func TestProcessOutcomes(t *testing.T) {
	stub(t)
	root := repo(t, testdata(t, "packageanalyzer", map[string]string{"nolock/Project.toml": ""}))
	dir := filepath.Join(root, "packageanalyzer")
	manifest := read(t, filepath.Join(dir, "Manifest.toml"))
	files := map[string][]byte{"Manifest.toml": manifest}
	locks := map[string][]dep.Update{"Project.toml": {upd("packageanalyzer/Project.toml", "Pluto", "c3e4b0f8-55cb-11ea-2926-15256bba5781", DepTypeDeps, "0.19.47")}}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, files, nil, locks); err != nil || out != nil {
		t.Errorf("unchanged manifest = %v, %v", out, err)
	}
	t.Setenv("STUB_NOOP", "")
	t.Setenv("STUB_FAIL", "1")
	_, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, files, nil, locks)
	if err == nil || err.Error() != "julia Pkg in "+dir+": ERROR: Unsatisfiable requirements detected for package JSON in "+dir {
		t.Errorf("failure = %v", err)
	}
	bad := map[string][]dep.Update{"Project.toml": {upd("packageanalyzer/Project.toml", "Pluto", "c3e4b0f8-55cb-11ea-2926-15256bba5781", DepTypeDeps, `1"); run(x`)}}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, files, nil, bad); err == nil || !strings.Contains(err.Error(), "invalid package") {
		t.Errorf("unsafe version = %v", err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, map[string][]byte{"Manifest.toml": []byte("[[x")}, nil, locks); err == nil {
		t.Error("unreadable manifest")
	}
	nolock := filepath.Join(root, "nolock")
	if _, err := (Locker{}).ProcessUpdates(t.Context(), nolock, nil, nil, nil, locks); err == nil || !strings.Contains(err.Error(), "need a Manifest.toml") {
		t.Errorf("lockfile-only without manifest = %v", err)
	}
	if out, err := (Locker{}).ProcessUpdates(t.Context(), nolock, nil, nil, nil, nil); out != nil || err != nil {
		t.Errorf("no manifest = %v, %v", out, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, files, nil, locks); err == nil || err.Error() != "julia not found (needed to update Manifest.toml)" {
		t.Errorf("no julia = %v", err)
	}
}

func TestMaintain(t *testing.T) {
	log := stub(t)
	root := repo(t, testdata(t, "packageanalyzer", map[string]string{"nolock/Project.toml": ""}))
	dir := filepath.Join(root, "packageanalyzer")
	manifest := read(t, filepath.Join(dir, "Manifest.toml"))
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{"Manifest.toml": manifest})
	if err != nil || string(out["Manifest.toml"]) != string(manifest)+"# resolved\n" {
		t.Fatalf("Maintain = %v, %v", len(out), err)
	}
	if b := string(read(t, log)); !strings.Contains(b, "Pkg.activate(ARGS[1])\nPkg.update()\n") {
		t.Errorf("log:\n%s", b)
	}
	if out, err := (Locker{}).Maintain(t.Context(), filepath.Join(root, "nolock"), nil); out != nil || err != nil {
		t.Errorf("no manifest = %v, %v", out, err)
	}
}
