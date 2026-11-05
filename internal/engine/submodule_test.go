package engine

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gitsubmodules"
)

// fakeBranches serves branch commits by repository URL and branch.
type fakeBranches map[string]string

func (fakeBranches) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	return nil, fmt.Errorf("releases listed for %s", d.Name)
}

func (f fakeBranches) Digest(_ context.Context, d dep.Dep, ref string) (string, error) {
	if c, ok := f[d.Source+"#"+ref]; ok {
		return c, nil
	}
	return "", fmt.Errorf("branch %s not found upstream", ref)
}

// Submodules tracking a branch get digest updates of their gitlink, those on a version tag
// tag updates that rewrite `branch`; both move the gitlink in Build.
func TestSubmodules(t *testing.T) {
	const (
		a, b, c = "https://github.com/acme/a", "https://github.com/acme/b", "https://github.com/acme/c"
		old     = "0000000000000000000000000000000000000000"
		head    = "1111111111111111111111111111111111111111"
	)
	gitmodules := "[submodule \"a\"]\n\tpath = lib/a\n\turl = " + a + ".git\n" +
		"[submodule \"b\"]\n\tpath = lib/b\n\turl = " + b + "\n\tbranch = v1.0.0\n" +
		"[submodule \"c\"]\n\tpath = lib/c\n\turl = " + c + "\n\tbranch = main\n"
	e := env
	e.Gitlinks = func([]string) (map[string]string, error) {
		return map[string]string{"lib/a": old, "lib/b": sha(b + "@v1.0.0"), "lib/c": head}, nil
	}
	ms := []manager.Manager{gitsubmodules.New()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"git-refs": fakeBranches{a + "#HEAD": head, c + "#main": head},
		"git-tags": fakeTags{b: {"v1.0.0", "v1.1.0", "v2.0.0-rc.1"}},
	}}

	// Disabled by default, as in Renovate.
	root, cfg := setup(t, "", map[string]string{".gitmodules": gitmodules})
	res, err := Resolve(t.Context(), root, cfg, e, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Updates) > 0 || len(res.Skipped) != 3 ||
		res.Skipped[0].Skip != "ignored by default (submodule; enable with a rule setting ignore: false)" {
		t.Fatalf("default: err = %v, errors = %v, updates = %v, skipped = %+v", err, res.Errors, res.Updates, res.Skipped)
	}

	root, cfg = setup(t, "rules: [{manager: git-submodules, ignore: false}]\n", map[string]string{".gitmodules": gitmodules})
	res, err = Resolve(t.Context(), root, cfg, e, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{{"acme/a", "HEAD", head, dep.Digest}, {"acme/b", "v1.1.0", sha(b + "@v1.1.0"), dep.Minor}}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}

	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	wantChanges := []FileChange{
		{Path: ".gitmodules", Mode: 0o644, Old: []byte(gitmodules), New: []byte(
			"[submodule \"a\"]\n\tpath = lib/a\n\turl = " + a + ".git\n" +
				"[submodule \"b\"]\n\tpath = lib/b\n\turl = " + b + "\n\tbranch = v1.1.0\n" +
				"[submodule \"c\"]\n\tpath = lib/c\n\turl = " + c + "\n\tbranch = main\n")},
		{Path: "lib/a", Gitlink: true, Old: []byte(old), New: []byte(head)},
		{Path: "lib/b", Gitlink: true, Old: []byte(sha(b + "@v1.0.0")), New: []byte(sha(b + "@v1.1.0"))},
	}
	if len(changes) != len(wantChanges) {
		t.Fatalf("changes = %+v", changes)
	}
	for i, w := range wantChanges {
		g := changes[i]
		if g.Path != w.Path || g.Gitlink != w.Gitlink || !bytes.Equal(g.Old, w.Old) || !bytes.Equal(g.New, w.New) || !w.Gitlink && g.Mode != w.Mode {
			t.Errorf("change %d = {%s %v %q -> %q}, want {%s %v %q -> %q}", i, g.Path, g.Gitlink, g.Old, g.New, w.Path, w.Gitlink, w.Old, w.New)
		}
	}
}
