package bot

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
)

// templated sets the prTitle, prHeader and prFooter templates of a/x's update; the result has
// only that update.
func templated(t *testing.T, res *engine.Result, title, header, footer string) {
	t.Helper()
	parse := func(src string, f func(string) (*prtemplate.Template, error)) *prtemplate.Template {
		if src == "" {
			return nil
		}
		tpl, err := f(src)
		if err != nil {
			t.Fatal(err)
		}
		return tpl
	}
	res.Updates = res.Updates[:1]
	res.Updates[0].Templates = prtemplate.Set{
		Title:  parse(title, prtemplate.ParseTitle),
		Header: parse(header, prtemplate.Parse),
		Footer: parse(footer, prtemplate.Parse),
	}
}

// A created PR has the templated title, which is also the commit subject, and its body the
// header first and the footer above the rebase checkbox.
func TestRunPRTemplates(t *testing.T) {
	root, res := fixture(t)
	templated(t, res, "deps: bump {{.Name}} from {{.From}} to {{.To}}", "Hello from {{.Name}}.", "{{.Count}} dependency, {{.UpdateType}}")
	pc, g := &fakePC{}, &fakeGit{}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g)
	if len(errs) > 0 || statusOf(acts, branchA) != StatusCreated {
		t.Fatalf("%v %+v", errs, acts)
	}
	const title = "deps: bump a/x from v1 to v2"
	if len(pc.titles) != 1 || pc.titles[0] != title || acts[0].Title != title {
		t.Errorf("titles = %q, action %+v", pc.titles, acts[0])
	}
	if len(g.messages) != 1 || !strings.HasPrefix(g.messages[0], title+"\n\n- wf.yml:2 v1 -> v2\n") {
		t.Errorf("commit messages = %q", g.messages)
	}
	body := pc.bodies[0]
	if !strings.HasPrefix(body, "Hello from a/x.\n\n| File |") || !strings.Contains(body, "\n1 dependency, major\n\n---\n\n"+rebaseCheckbox+"\n") {
		t.Errorf("body:\n%s", body)
	}
}

// The fingerprints cover the content only: a changed title template leaves an open PR with
// unchanged content alone, and the versions marker keeps a closed PR closed under a new title.
func TestRunPRTemplateChanges(t *testing.T) {
	root, res := fixture(t)
	templated(t, res, "new title for {{.Name}}", "", "")
	branch := engine.GroupUpdates(res.Updates, "")[0].Branch
	fp := fingerprintOf(t, root, res, "a/x")
	yes := true

	open := platform.PR{Number: 1, Head: branch, Title: titleA, Body: "<!-- deps:fingerprint=" + fp + " -->", Mergeable: &yes}
	pc := &fakePC{open: []platform.PR{open}}
	g := &fakeGit{branches: map[string]string{"refs/heads/" + branch: "x"}, authors: map[string]string{branch: email}}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g)
	if len(errs) > 0 || statusOf(acts, branch) != StatusUpToDate || len(g.pushed) != 0 || len(pc.updated) != 0 {
		t.Fatalf("open PR: %v %+v pushed %v updated %v", errs, acts, g.pushed, pc.updated)
	}

	closed := platform.PR{Number: 4, Head: branch, Title: titleA, Body: "x\n<!-- deps:versions=a%2Fx=v2 -->\n"}
	pc = &fakePC{closed: []platform.PR{closed}}
	acts, errs = Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, &fakeGit{})
	if len(errs) > 0 || statusOf(acts, branch) != StatusSkipClosed || len(pc.created) != 0 {
		t.Fatalf("closed PR: %v %+v created %v", errs, acts, pc.created)
	}
}
