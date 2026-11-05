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

// listingPost is a post-processor scoping to the manifest's directory that reads extra
// outside it (ScopeInputLister).
type listingPost struct {
	postFunc
	extra []string
}

func (p listingPost) ScopeInputs(_, dir string) ([]string, error) {
	out := make([]string, len(p.extra))
	for i, e := range p.extra {
		out[i] = dir + "/" + e
	}
	return out, nil
}

func TestInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "mod/go.mod", "module example.com/x\n\nrequire github.com/google/go-github/v92 v92.0.0\n")
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
	post := (&recorder{}).post()
	post[gomod.Name] = listingPost{extra: []string{"../shared"}}

	got, err := Inputs(root, managers, ups, post)
	if err != nil {
		t.Fatal(err)
	}
	// The updated files, the .go file the module path update rewrites (not b.go), each scope
	// directory once (none for nolock) and what the gomod post-processor reads beside it.
	want := []string{"mod", "mod/a.go", "mod/go.mod", "nolock/package.json", "shared", "ws", "ws/package.json", "ws/packages/b/package.json"}
	if !slices.Equal(got, want) {
		t.Errorf("Inputs = %q, want %q", got, want)
	}

	// Without post-processors only the edited files count.
	if got, err := Inputs(root, managers, []dep.Update{major}, nil); err != nil || !slices.Equal(got, []string{"mod/a.go", "mod/go.mod"}) {
		t.Errorf("Inputs without post = %q, %v", got, err)
	}

	// A scope input outside root cannot be fingerprinted.
	post[gomod.Name] = listingPost{scope: func(string, string) (string, bool) { return ".", true }, extra: []string{"../outside"}}
	if _, err := Inputs(root, managers, []dep.Update{major}, post); err == nil || !strings.Contains(err.Error(), "outside the root") {
		t.Errorf("Inputs outside root err = %v", err)
	}

	// FileEdits failures are errors.
	writeFile(t, root, "mod/broken.go", "package\nimport \"github.com/google/go-github/v92\"\n")
	if _, err := Inputs(root, managers, []dep.Update{major}, nil); err == nil {
		t.Error("Inputs ignored a FileEdits failure")
	}
}

// failingLister fails ScopeInputs.
type failingLister struct{ postFunc }

func (failingLister) ScopeInputs(string, string) ([]string, error) { return nil, context.Canceled }

func TestInputsScopeInputsError(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "a/package.json")
	_, err := Inputs(root, nil, []dep.Update{up}, map[string]PostProcessor{npm.Name: failingLister{}})
	if err == nil || !strings.HasPrefix(err.Error(), "a/package.json: ") {
		t.Errorf("err = %v", err)
	}
}
