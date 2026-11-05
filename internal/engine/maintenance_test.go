package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// maintainPost is a cargo post-processor that maintains lockfiles.
type maintainPost struct {
	postFunc
	maintain func(dir string, files map[string][]byte) (map[string][]byte, error)
}

func (p maintainPost) Maintain(_ context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	return p.maintain(dir, files)
}

var maintenanceTree = map[string]string{
	"Cargo.toml":     "[workspace]\nmembers = [\"a\"]\n",
	"Cargo.lock":     "version = 4\n",
	"a/Cargo.toml":   "[dependencies]\nx = \"1.0.0\"\n",
	"lib/Cargo.toml": "[dependencies]\nx = \"1.0.0\"\n", // no lockfile: nothing to maintain
}

func TestResolveLockFileMaintenance(t *testing.T) {
	o := Options{Managers: []manager.Manager{cargo.New()}, Datasources: map[string]Datasource{cargo.Datasource: fakeGo{"x": {"1.0.0"}}}}
	for _, tt := range []struct {
		name, cfg string
		filter    bool
		want      string
	}{
		{"enabled", "lockFileMaintenance: {enabled: true, schedule: ['0 3 * * 1']}\nschedule: ['0 0 * * *']\n", false, "[cargo Cargo.toml Cargo.lock lockFileMaintenance 0 3 * * 1]"},
		{"top-level schedule", "lockFileMaintenance: {enabled: true}\nschedule: ['0 0 * * *']\nautomerge: true\n", false, "[cargo Cargo.toml Cargo.lock lockFileMaintenance 0 0 * * * automerge]"},
		{"every run", "lockFileMaintenance: {enabled: true, schedule: []}\nschedule: ['0 0 * * *']\n", false, "[cargo Cargo.toml Cargo.lock lockFileMaintenance -]"},
		{"disabled", "", false, "[]"},
		{"filtered (explain)", "lockFileMaintenance: {enabled: true}\n", true, "[]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, maps.Clone(maintenanceTree))
			o := o
			if tt.filter {
				o.Filter = func(dep.Dep) bool { return true }
			}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			var got []string
			for _, u := range res.Updates {
				if !u.Maintenance() {
					continue
				}
				s := fmt.Sprintf("%s %s %s %s", u.Dep.Manager, u.Dep.File, u.Dep.Name, u.Type)
				if u.Schedule != nil {
					s += " " + u.Schedule.String()
				} else {
					s += " -"
				}
				if u.Automerge {
					s += " automerge"
				}
				got = append(got, s)
			}
			if fmt.Sprint(got) != tt.want {
				t.Errorf("maintenance = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestGroupAndLocalMaintenance(t *testing.T) {
	m := dep.Update{Dep: dep.Dep{Manager: "cargo", File: "Cargo.toml", Name: "Cargo.lock"}, Type: dep.LockFileMaintenance}
	m2 := dep.Update{Dep: dep.Dep{Manager: "cargo", File: "b/Cargo.toml", Name: "b/Cargo.lock"}, Type: dep.LockFileMaintenance}
	u := dep.Update{Dep: dep.Dep{Manager: "cargo", File: "Cargo.toml", Name: "x", Version: "1.0.0", RefSpan: dep.Span{Start: 1, End: 6}}, Type: dep.Patch, NewVersion: "1.0.1"}
	groups := GroupUpdates([]dep.Update{m, u, m2}, "bot/")
	if len(groups) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if g := groups[1]; g.Branch != "bot/lock-file-maintenance" || g.Title != "chore(deps): lock file maintenance" || len(g.Updates) != 2 {
		t.Errorf("maintenance group = %+v", g)
	}
	local, separate := Local([]dep.Update{m, u})
	if len(local) != 1 || local[0].Dep.Name != "x" || fmt.Sprint(separate) != "[true false]" {
		t.Errorf("local = %+v, separate = %v", local, separate)
	}
}

func TestBuildLockFileMaintenance(t *testing.T) {
	root, _ := setup(t, "", maps.Clone(maintenanceTree))
	managers := []manager.Manager{cargo.New()}
	m := dep.Update{Dep: dep.Dep{Manager: cargo.Name, File: "Cargo.toml", Name: "Cargo.lock"}, Type: dep.LockFileMaintenance}
	var gotFiles map[string][]byte
	post := map[string]PostProcessor{cargo.Name: maintainPost{
		postFunc: postFunc{siblings: []string{"Cargo.lock"}, scope: func(string, string) (string, bool) { return ".", true }, f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
			return nil, errors.New("Process called")
		}},
		maintain: func(_ string, files map[string][]byte) (map[string][]byte, error) {
			gotFiles = files
			return map[string][]byte{"Cargo.lock": []byte("refreshed\n")}, nil
		},
	}}
	changes, errs := Build(t.Context(), root, managers, []dep.Update{m}, post)
	if len(errs) > 0 || len(changes) != 1 || changes[0].Path != "Cargo.lock" || string(changes[0].New) != "refreshed\n" || string(gotFiles["Cargo.lock"]) != "version = 4\n" {
		t.Fatalf("changes = %+v, %v, files %q", changes, errs, gotFiles)
	}

	// Other updates of the same lockfile, a post-processor that cannot maintain, no lockfile.
	bump := dep.Update{Dep: dep.Dep{Manager: cargo.Name, File: "a/Cargo.toml", Name: "x", Version: "1.0.0", RefSpan: dep.Span{Start: 20, End: 25}}, Type: dep.Major, NewVersion: "2.0.0"}
	plain := map[string]PostProcessor{cargo.Name: post[cargo.Name].(maintainPost).postFunc}
	noScope := map[string]PostProcessor{cargo.Name: maintainPost{postFunc: postFunc{scope: func(string, string) (string, bool) { return "", false }}}}
	for _, tt := range []struct {
		name string
		ups  []dep.Update
		post map[string]PostProcessor
		want string
	}{
		{"combined", []dep.Update{m, bump}, post, "Cargo.toml, a/Cargo.toml: lock file maintenance cannot be combined with other updates of its lockfile"},
		{"plain", []dep.Update{m}, plain, "Cargo.toml: the cargo post-processor cannot maintain lockfiles"},
		{"no scope", []dep.Update{m}, noScope, "Cargo.toml: lock file maintenance without a lockfile"},
	} {
		if _, errs := Build(t.Context(), root, managers, tt.ups, tt.post); len(errs) != 1 || errs[0].Error() != tt.want {
			t.Errorf("%s: errs = %v", tt.name, errs)
		}
	}
	// plan (no post-processors): nothing to show.
	if changes, errs := Build(t.Context(), root, managers, []dep.Update{m}, nil); len(changes) != 0 || len(errs) != 0 {
		t.Errorf("plan: changes = %+v, %v", changes, errs)
	}
}

// TestBuildLockFileMaintenanceManifest: a Maintain result under the manifest's path (go.mod
// with moved `// indirect` requirements) replaces the manifest next to the lockfile.
func TestBuildLockFileMaintenanceManifest(t *testing.T) {
	oldMod := "module example.com/x\n\ngo 1.21\n\nrequire example.com/i v1.0.0 // indirect\n"
	newMod := "module example.com/x\n\ngo 1.21\n\nrequire example.com/i v1.1.0 // indirect\n"
	root, _ := setup(t, "", map[string]string{"m/go.mod": oldMod, "m/go.sum": "old sum\n"})
	m := dep.Update{Dep: dep.Dep{Manager: gomod.Name, File: "m/go.mod", Name: "m/go.sum"}, Type: dep.LockFileMaintenance}
	var gotDir string
	post := map[string]PostProcessor{gomod.Name: maintainPost{
		postFunc: postFunc{siblings: []string{"go.sum"}},
		maintain: func(dir string, files map[string][]byte) (map[string][]byte, error) {
			gotDir = dir
			if string(files["go.sum"]) != "old sum\n" {
				return nil, fmt.Errorf("files = %q", files)
			}
			return map[string][]byte{"go.mod": []byte(newMod), "go.sum": []byte("new sum\n")}, nil
		},
	}}
	changes, errs := Build(t.Context(), root, []manager.Manager{gomod.New()}, []dep.Update{m}, post)
	if len(errs) > 0 || len(changes) != 2 {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
	if c := changes[0]; c.Path != "m/go.mod" || string(c.Old) != oldMod || string(c.New) != newMod {
		t.Errorf("go.mod change = %+v", c)
	}
	if c := changes[1]; c.Path != "m/go.sum" || string(c.Old) != "old sum\n" || string(c.New) != "new sum\n" {
		t.Errorf("go.sum change = %+v", c)
	}
	if gotDir != filepath.Join(root, "m") {
		t.Errorf("dir = %s", gotDir)
	}
}
