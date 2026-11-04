package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// linkManager extracts one skipped dep per line of *.link files; Link records the files it
// gets, drops the deps named "drop" and sets the others' skip reason to "linked".
type linkManager struct{ files [][]string }

func (*linkManager) Name() string                           { return "link" }
func (*linkManager) Match(p string) bool                    { return strings.HasSuffix(p, ".link") }
func (*linkManager) Edits(dep.Update) ([]patch.Edit, error) { return nil, nil }

func (*linkManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if string(src) == "bad" {
		return nil, errors.New("bad file")
	}
	var deps []dep.Dep
	for i, l := range strings.Split(strings.TrimSpace(string(src)), "\n") {
		deps = append(deps, dep.Dep{Manager: "link", File: file, Line: i + 1, Name: l, Skip: "unlinked"})
	}
	return deps, nil
}

func (m *linkManager) Link(files [][]dep.Dep) {
	for f, deps := range files {
		var names []string
		for _, d := range deps {
			names = append(names, d.Name)
		}
		m.files = append(m.files, names)
		deps = slices.DeleteFunc(deps, func(d dep.Dep) bool { return d.Name == "drop" })
		for i := range deps {
			deps[i].Skip = "linked"
		}
		files[f] = deps
	}
}

func TestResolveLinksAfterExtraction(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{
		".github/workflows/ci.yml": workflow,
		"a.link":                   "x\ndrop\ny\n",
		"b.link":                   "bad",
		"c.link":                   "z\n",
	})
	lm := &linkManager{}
	o := sources(tags, nil)
	o.Managers = []manager.Manager{actions.New(), lm}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	// The file that failed to extract is not linked; the files come in discovery order.
	if want := [][]string{{"x", "drop", "y"}, {"z"}}; !slices.EqualFunc(lm.files, want, slices.Equal) {
		t.Errorf("linked files = %q, want %q", lm.files, want)
	}
	var got []string
	for _, d := range res.Skipped {
		if d.Manager == "link" {
			got = append(got, d.File+":"+d.Name+":"+d.Skip)
		}
	}
	if want := []string{"a.link:x:linked", "a.link:y:linked", "c.link:z:linked"}; !slices.Equal(got, want) {
		t.Errorf("link deps = %q, want %q", got, want)
	}
	if len(res.Errors) != 2 || !strings.HasPrefix(res.Errors[0].Error(), "b.link: bad file") {
		t.Errorf("errors = %v, want b.link and owner/broken", res.Errors)
	}
}
