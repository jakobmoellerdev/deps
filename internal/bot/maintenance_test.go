package bot

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// maintainer is a cargo post-processor whose Maintain returns lock (nil = unchanged).
type maintainer struct{ lock []byte }

func (maintainer) Scope(_ context.Context, _, manifest string) (string, bool) {
	return filepath.Dir(manifest), true
}
func (maintainer) Siblings() []string { return []string{"Cargo.lock"} }
func (maintainer) Process(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
	return nil, nil
}

func (m maintainer) Maintain(context.Context, string, map[string][]byte) (map[string][]byte, error) {
	if m.lock == nil {
		return nil, nil
	}
	return map[string][]byte{"Cargo.lock": m.lock}, nil
}

const maintBranch = "deps/lock-file-maintenance"

func TestRunLockFileMaintenance(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"Cargo.toml": "[dependencies]\nx = \"1\"\n", "Cargo.lock": "old\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := &engine.Result{Updates: []dep.Update{{Dep: dep.Dep{Manager: cargo.Name, File: "Cargo.toml", Name: "Cargo.lock"}, Type: dep.LockFileMaintenance}}}
	o := Options{AuthorEmail: email, Managers: []manager.Manager{cargo.New()}, MaxNewPRs: 10}

	// A changed lock is pushed and gets the PR.
	g, pc := &fakeGit{}, &fakePC{}
	acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, map[string]engine.PostProcessor{cargo.Name: maintainer{lock: []byte("new\n")}}, pc)
	if len(errs) > 0 || len(acts) != 1 || acts[0].Branch != maintBranch || acts[0].Status != StatusCreated || acts[0].Title != "chore(deps): lock file maintenance" {
		t.Fatalf("actions = %+v, errs = %v", acts, errs)
	}
	if !slices.Equal(g.pushed, []string{maintBranch}) {
		t.Errorf("pushed = %v", g.pushed)
	}

	// An unchanged lock needs no PR: nothing is pushed and the open one is closed.
	g = &fakeGit{branches: map[string]string{"refs/heads/" + maintBranch: "x"}, authors: map[string]string{maintBranch: email}}
	pc = &fakePC{open: []platform.PR{{Number: 7, Head: maintBranch, Title: "chore(deps): lock file maintenance"}}}
	acts, errs = Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, map[string]engine.PostProcessor{cargo.Name: maintainer{}}, pc)
	if len(errs) > 0 || len(g.pushed) != 0 || !slices.Equal(pc.closedNow, []int{7}) || !slices.Equal(g.deleted, []string{maintBranch}) {
		t.Errorf("unchanged: actions = %+v, errs = %v, pushed = %v, closed = %v, deleted = %v", acts, errs, g.pushed, pc.closedNow, g.deleted)
	}
}

func TestPRBodyLockFileMaintenance(t *testing.T) {
	m := dep.Update{Dep: dep.Dep{Manager: cargo.Name, File: "Cargo.toml", Name: "Cargo.lock"}, Type: dep.LockFileMaintenance}
	body := prBody(t.Context(), Options{}, engine.Group{Updates: []dep.Update{m}}, "abc", "")
	for _, want := range []string{"| `Cargo.lock` | | | lock file maintenance |\n", "\nLock file maintenance moves every locked dependency"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	grp := engine.GroupUpdates([]dep.Update{m}, "")[0]
	if msg := commitMessage(grp); msg != "chore(deps): lock file maintenance\n\n- Cargo.lock: lock file maintenance\n" {
		t.Errorf("commit message:\n%s", msg)
	}

	// go.mod maintenance moves its indirect requirements; the body says so.
	g := dep.Update{Dep: dep.Dep{Manager: "gomod", File: "m/go.mod", Name: "m/go.sum"}, Type: dep.LockFileMaintenance}
	if body := prBody(t.Context(), Options{}, engine.Group{Updates: []dep.Update{g}}, "abc", ""); !strings.Contains(body, "the manifests are unchanged (in `go.mod`, only the `// indirect` requirements move).\n") {
		t.Errorf("go.mod body:\n%s", body)
	}
	if body := prBody(t.Context(), Options{}, engine.Group{Updates: []dep.Update{m}}, "abc", ""); !strings.Contains(body, "the manifests are unchanged.\n") {
		t.Errorf("cargo body:\n%s", body)
	}
}
