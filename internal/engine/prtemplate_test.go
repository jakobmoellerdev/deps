package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
)

func TestGroupUpdatesTemplates(t *testing.T) {
	title := func(src string) *prtemplate.Template {
		t.Helper()
		tpl, err := prtemplate.ParseTitle(src)
		if err != nil {
			t.Fatal(err)
		}
		return tpl
	}
	text := func(src string) *prtemplate.Template {
		t.Helper()
		tpl, err := prtemplate.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		return tpl
	}
	// data renders every field of prtemplate.Data.
	data := title("{{.Type}}|{{.Scope}}|{{.Name}}|{{.Group}}|{{.From}}|{{.To}}|{{.UpdateType}}|{{.Count}}|{{.IsSecurity}}")
	custom := prtemplate.Set{
		Title:  title("{{.Type}}{{with .Scope}}({{.}}){{end}}: bump {{.Name}}{{with .To}} to {{.}}{{end}}"),
		Header: text("Updates {{.Count}} {{if eq .Count 1}}dependency{{else}}dependencies{{end}}."),
		Footer: text("Group: {{or .Group \"none\"}}"),
	}
	gomod := func(name, from, to string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: "gomod", Name: name, Version: from}, Type: dep.Minor, NewVersion: to, CommitType: "fix", CommitScope: "deps"}
	}
	with := func(u dep.Update, f func(*dep.Update)) dep.Update { f(&u); return u }
	adv := []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name                  string
		updates               []dep.Update
		title, header, footer string
	}{
		{
			name:    "single dependency",
			updates: []dep.Update{with(gomod("a", "v1.0.0", "v1.1.0"), func(u *dep.Update) { u.Templates = custom })},
			title:   "fix(deps): bump a to v1.1.0", header: "Updates 1 dependency.", footer: "Group: none",
		},
		{
			name:    "no templates: built-in title, no header or footer",
			updates: []dep.Update{gomod("a", "v1.0.0", "v1.1.0")},
			title:   "fix(deps): update a to v1.1.0",
		},
		{
			name: "group: the first member's templates and type rules",
			updates: []dep.Update{
				with(gomod("a", "v1.0.0", "v1.1.0"), func(u *dep.Update) {
					u.Group, u.Templates, u.CommitType, u.CommitScope = "G", custom, "chore", ""
				}),
				with(gomod("b", "v2.0.0", "v2.1.0"), func(u *dep.Update) {
					u.Group, u.Templates = "G", prtemplate.Set{Title: title("other")}
				}),
			},
			title: "fix: bump G", header: "Updates 2 dependencies.", footer: "Group: G",
		},
		{
			name: "data of a group with shared versions",
			updates: []dep.Update{
				with(gomod("a", "v1.0.0", "v1.1.0"), func(u *dep.Update) { u.Group, u.Templates.Title = "G", data }),
				with(gomod("b", "v1.0.0", "v1.1.0"), func(u *dep.Update) { u.Group, u.Type = "G", dep.Patch }),
			},
			title: "fix|deps|G|G|v1.0.0|v1.1.0||2|false",
		},
		{
			name: "data of a single-dependency group with a lockfile-only update",
			updates: []dep.Update{with(gomod("a", "v1.0.0", "v1.3.0"), func(u *dep.Update) {
				u.Group, u.Templates.Title, u.Lockfile, u.Dep.Locked = "G", data, true, "v1.2.0"
			})},
			title: "fix|deps|a|G|v1.2.0|v1.3.0|minor|1|false",
		},
		{
			name: "security title keeps the suffix and fix",
			updates: []dep.Update{with(gomod("a", "v1.0.0", "v1.0.1"), func(u *dep.Update) {
				u.Advisories, u.Templates, u.CommitType = adv, custom, "chore"
			})},
			title: "fix(deps): bump a to v1.0.1 [security]", header: "Updates 1 dependency.", footer: "Group: none",
		},
		{
			name: "security title with its own suffix",
			updates: []dep.Update{with(gomod("a", "v1.0.0", "v1.0.1"), func(u *dep.Update) {
				u.Advisories, u.Templates.Title = adv, title("bump {{.Name}}{{if .IsSecurity}} [security]{{end}}")
			})},
			title: "bump a [security]",
		},
		{
			name: "security group",
			updates: []dep.Update{
				with(gomod("a", "v1.0.0", "v1.0.1"), func(u *dep.Update) { u.Advisories, u.SecurityGroup, u.Templates.Title = adv, "S", data }),
				with(gomod("b", "v2.0.0", "v2.0.1"), func(u *dep.Update) { u.Advisories, u.SecurityGroup = adv, "S" }),
			},
			title: "fix|deps|S|S|||minor|2|true [security]",
		},
		{
			name: "lock file maintenance keeps its title",
			updates: []dep.Update{{
				Dep: dep.Dep{Manager: "cargo", File: "Cargo.toml", Name: "Cargo.lock"}, Type: dep.LockFileMaintenance,
				Templates: prtemplate.Set{Title: custom.Title, Header: data},
			}},
			title: "chore(deps): lock file maintenance", header: "chore|deps|lock file maintenance||||lockFileMaintenance|1|false",
		},
		{
			name: "a template failing on real data renders the default",
			updates: []dep.Update{with(gomod("ab", "v1.0.0", "v1.1.0"), func(u *dep.Update) {
				u.Templates = prtemplate.Set{Title: title("{{slice .Name 3}}"), Footer: text("{{slice .Name 3}}")}
			})},
			title: "fix(deps): update ab to v1.1.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupUpdates(tt.updates, "")
			if len(got) != 1 {
				t.Fatalf("groups = %+v", got)
			}
			if g := got[0]; g.Title != tt.title || g.Header != tt.header || g.Footer != tt.footer {
				t.Errorf("title, header, footer = %q, %q, %q, want %q, %q, %q", g.Title, g.Header, g.Footer, tt.title, tt.header, tt.footer)
			}
		})
	}
}

// Resolve gives every update the templates of its policy, so GroupUpdates renders them.
func TestResolveTemplatesFromPolicy(t *testing.T) {
	root, cfg := setup(t, "prTitle: 'top {{.Name}}'\nrules: [{updateTypes: [major, minor, patch], prTitle: 'rule {{.Name}} {{.To}}', prFooter: 'by rule'}]\n", map[string]string{"go.mod": modA})
	res, err := Resolve(t.Context(), root, cfg, env, sources(nil, goVersions))
	if err != nil || len(res.Updates) != 1 {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	g := GroupUpdates(res.Updates, "")[0]
	if want := "rule " + res.Updates[0].Dep.Name + " " + res.Updates[0].NewVersion; g.Title != want || g.Footer != "by rule" || g.Header != "" {
		t.Errorf("title %q footer %q header %q, want %q", g.Title, g.Footer, g.Header, want)
	}
}
