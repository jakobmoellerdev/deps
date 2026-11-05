package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// listingPost is a post-processor scoping to the manifest's directory that names its inputs
// (ScopeInputLister): extra relative to the scope directory and one digest. calls records the
// manifests of each ScopeInputs call.
type listingPost struct {
	postFunc
	extra []string
	calls *[][]string
}

func (p listingPost) ScopeInputs(_, dir string, manifests []string) ([]string, []string, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, manifests)
	}
	out := make([]string, len(p.extra))
	for i, e := range p.extra {
		out[i] = dir + "/" + e
	}
	return out, []string{"digest " + dir}, nil
}

func TestInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "mod/go.mod", "module example.com/x\n\nrequire github.com/google/go-github/v92 v92.0.0\n")
	writeFile(t, root, "mod/go.sum", "")
	writeFile(t, root, "mod/a.go", "package a\n\nimport \"github.com/google/go-github/v92/github\"\n\nvar _ github.Client\n")
	writeFile(t, root, "mod/b.go", "package a\n")
	major := dep.Update{
		Dep:  dep.Dep{Manager: gomod.Name, File: "mod/go.mod", Name: "github.com/google/go-github/v92", Version: "v92.0.0"},
		Type: dep.Major, NewVersion: "v93.0.0", NewName: "github.com/google/go-github/v93",
	}
	ups := []dep.Update{
		pkgUpdate(t, root, "ws/packages/b/package.json"),
		pkgUpdate(t, root, "ws/package.json"),
		pkgUpdate(t, root, "nolock/package.json"),
		major,
	}
	managers := []manager.Manager{npm.New(), gomod.New()}
	var calls [][]string
	post := (&recorder{}).post()
	post[gomod.Name] = listingPost{siblings: []string{"go.sum", "go.work"}, extra: []string{"../shared"}, calls: &calls}

	got, errs := Inputs(root, managers, post, [][]dep.Update{ups, nil, {major}})
	if errs[0] != nil || errs[1] != nil || errs[2] != nil {
		t.Fatal(errs)
	}
	// The updated files, the .go file the module path update rewrites (not b.go), the npm
	// scope directory once (none for nolock: no lockfile), and for the listing gomod
	// post-processor its present sibling and what it names instead of its directory.
	want := BuildInputs{
		Paths:   []string{"mod/a.go", "mod/go.mod", "mod/go.sum", "nolock/package.json", "shared", "ws", "ws/package.json", "ws/packages/b/package.json"},
		Digests: []string{"digest mod"},
	}
	if !slices.Equal(got[0].Paths, want.Paths) || !slices.Equal(got[0].Digests, want.Digests) {
		t.Errorf("Inputs = %q, want %q", got[0], want)
	}
	if got[1].Paths != nil || got[1].Digests != nil {
		t.Errorf("skipped group got %q", got[1])
	}
	if !slices.Equal(got[2].Paths, []string{"mod/a.go", "mod/go.mod", "mod/go.sum", "shared"}) {
		t.Errorf("second group = %q", got[2])
	}
	// The scope shared by both groups is listed once, with the manifests relative to it.
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"a.go", "go.mod"}) {
		t.Errorf("ScopeInputs calls = %q", calls)
	}

	// Without post-processors only the edited files count.
	if got, errs := Inputs(root, managers, nil, [][]dep.Update{{major}}); errs[0] != nil || !slices.Equal(got[0].Paths, []string{"mod/a.go", "mod/go.mod"}) || got[0].Digests != nil {
		t.Errorf("Inputs without post = %q, %v", got[0], errs[0])
	}

	// A scope input outside root cannot be fingerprinted.
	post[gomod.Name] = listingPost{scope: func(string, string) (string, bool) { return ".", true }, extra: []string{"../outside"}}
	if _, errs := Inputs(root, managers, post, [][]dep.Update{{major}}); errs[0] == nil || !strings.Contains(errs[0].Error(), "outside the root") {
		t.Errorf("Inputs outside root err = %v", errs[0])
	}

	// FileEdits failures are errors of their group only.
	writeFile(t, root, "mod/broken.go", "package\nimport \"github.com/google/go-github/v92\"\n")
	got, errs = Inputs(root, managers, nil, [][]dep.Update{{major}, {ups[1]}})
	if errs[0] == nil || errs[1] != nil || !slices.Equal(got[1].Paths, []string{"ws/package.json"}) {
		t.Errorf("Inputs with a FileEdits failure = %q, %v", got, errs)
	}
}

// failingLister fails ScopeInputs.
type failingLister struct{ postFunc }

func (failingLister) ScopeInputs(string, string, []string) ([]string, []string, error) {
	return nil, nil, context.Canceled
}

func TestInputsScopeInputsError(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "a/package.json")
	_, errs := Inputs(root, nil, map[string]PostProcessor{npm.Name: failingLister{}}, [][]dep.Update{{up}})
	if errs[0] == nil || !strings.HasPrefix(errs[0].Error(), "a/package.json: ") {
		t.Errorf("err = %v", errs[0])
	}
}
