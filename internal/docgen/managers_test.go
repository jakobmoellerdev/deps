package docgen

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

func TestManagers(t *testing.T) {
	in := ManagersInput{
		Intro: "Intro.",
		Skips: []refdoc.Skip{{Reason: "ignored by rule #<i>", When: "a rule"}},
		Managers: []refdoc.Manager{{
			Name:    "fake",
			Summary: "Finds fakes.",
			Files:   []string{"`*.fake`"},
			DepTypes: []refdoc.DepType{
				{Name: "direct", What: "`require`", Datasource: "`go`", Versioning: "`go`"},
				{Name: "indirect", What: "`// indirect`", Versioning: "`go`", Datasources: []string{"go", "git-tags"}},
			},
			Skips:    []refdoc.Skip{{Reason: "a|b", When: "pipes are escaped"}},
			Sections: []refdoc.Section{{Title: "Edits", Body: "Body.", Items: []string{"one", "two\nlines"}}},
		}, {
			Name:     "other",
			Files:    []string{"x"},
			Sections: []refdoc.Section{{Title: "Edits", Items: []string{"same title again"}}},
		}},
		Datasources: []refdoc.Datasource{{
			Name: "ds", NameForm: "name", Endpoints: "`GET /x`", Auth: "none", ReleaseTimes: "none",
			Notes: []refdoc.Section{{Title: "ds note", Body: "Note body."}},
		}},
		Aliases:        map[string]string{"z-alias": "ds", "a-alias": "ds"},
		Shared:         refdoc.Section{Title: "Shared", Items: []string{"one client"}},
		Notes:          []refdoc.Section{{Title: "Checksums", Items: []string{"cap"}}},
		IgnoredDefault: func(t string) bool { return t == "indirect" },
	}
	b, err := Managers(in)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		Header + "\n# Managers and datasources\n\nIntro.\n\n## Contents\n\n- [Skip reasons of every manager](#skip-reasons-of-every-manager)\n" +
			"- [`fake`](#fake)\n- [`other`](#other)\n- [Datasources](#datasources)\n",
		"## Skip reasons of every manager\n\n| Reason | When |\n|---|---|\n| `ignored by rule #<i>` | a rule |\n",
		"## fake\n\nFinds fakes.\n\n**Files:**\n\n- `*.fake`\n",
		"| `direct` | `require` | `go` | `go` | updated |\n| `indirect` | `// indirect` | `go`, `git-tags` | `go` | ignored (opt in with a rule setting `ignore: false`) |\n",
		"**Skips:**\n\n| Reason | When |\n|---|---|\n| `a\\|b` | pipes are escaped |\n",
		"**Edits.**\n\nBody.\n\n- one\n- two\n  lines\n",
		"## Datasources\n\n- one client\n\n| Datasource | Dep name | Endpoints | Auth | Release times |\n|---|---|---|---|---|\n| `ds` | name | `GET /x` | none | none |\n",
		"aliases (`custom[].datasource`, `rules[].datasources`, markers): `a-alias` = `ds`, `z-alias` = `ds`.\n",
		"### ds note\n\nNote body.\n\n### Checksums\n\n- cap\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks\n%s\n--- output:\n%s", want, out)
		}
	}
}
