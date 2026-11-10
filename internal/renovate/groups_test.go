package renovate

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
)

// toOmap converts a decoded YAML node into the translator's values: mappings to omap, lists
// of mappings to []item, lists of strings to []string, scalars to their value.
func toOmap(t *testing.T, n *yaml.Node) any {
	t.Helper()
	switch n.Kind {
	case yaml.MappingNode:
		var m omap
		for i := 0; i+1 < len(n.Content); i += 2 {
			m.set(n.Content[i].Value, toOmap(t, n.Content[i+1]))
		}
		return m
	case yaml.SequenceNode:
		var ss []string
		var items []item
		for _, c := range n.Content {
			switch v := toOmap(t, c).(type) {
			case omap:
				items = append(items, item{body: v})
			case string:
				ss = append(ss, v)
			default:
				t.Fatalf("unsupported list element %v", v)
			}
		}
		if items != nil {
			return items
		}
		return ss
	}
	var v any
	if err := n.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// ruleItems parses a YAML rules list into translated rules.
func ruleItems(t *testing.T, src string) []item {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	return toOmap(t, n.Content[0]).([]item)
}

// samePolicies checks that before and after (configs) give every dep the grid built from
// before's rules the same policy for every update type: deps named after each names entry (a
// glob's literal prefix plus a suffix), of each manager, dep type and datasource the rules
// list.
func samePolicies(t *testing.T, before, after []byte) {
	t.Helper()
	b, err := config.Parse(before, "before.yaml")
	if err != nil {
		t.Fatalf("before: %v\n%s", err, before)
	}
	a, err := config.Parse(after, "after.yaml")
	if err != nil {
		t.Fatalf("after: %v\n%s", err, after)
	}
	names, managers, depTypes, datasources := []string{"other/name"}, []string{"gomod"}, []string{""}, []string{""}
	add := func(l *[]string, vs ...string) {
		for _, v := range vs {
			if !slices.Contains(*l, v) {
				*l = append(*l, v)
			}
		}
	}
	for _, r := range b.Rules {
		for _, n := range r.Names {
			n = strings.TrimPrefix(n, "!")
			if i := strings.IndexAny(n, `*?[]{}\`); i >= 0 {
				n = n[:i] + "x/y"
			}
			add(&names, n)
		}
		add(&managers, r.Managers...)
		add(&depTypes, r.DepTypes...)
		add(&datasources, r.Datasources...)
	}
	kinds := []dep.UpdateType{"", dep.Major, dep.Minor, dep.Patch, dep.Digest, dep.Pin}
	n := 0
	for _, name := range names {
		for _, m := range managers {
			for _, dt := range depTypes {
				for _, ds := range datasources {
					d := dep.Dep{Name: name, Manager: m, DepType: dt, Datasource: config.CanonicalDatasource(ds)}
					for _, k := range kinds {
						pb, pa := b.PolicyFor(d, k), a.PolicyFor(d, k)
						if kb, ka := policyKey(t, pb), policyKey(t, pa); kb != ka {
							t.Errorf("%s %s %s %s %s:\nbefore %s\nafter  %s", m, name, dt, ds, k, kb, ka)
						}
						n++
					}
				}
			}
		}
	}
	t.Logf("compared %d policies", n)
}

// policyKey renders the parts of p the settings decide.
func policyKey(t *testing.T, p config.Policy) string {
	t.Helper()
	tmpl := func(x *prtemplate.Template) string {
		if x == nil {
			return "-"
		}
		s, err := x.Execute(prtemplate.Data{Type: "chore", Name: "n", Group: "g", From: "1", To: "2"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	return fmt.Sprintf("ignore=%v group=%q schedule=%q abp=%q minAge=%v tsReq=%v abandon=%v sep=%v/%v/%v automerge=%v/%s/%s draft=%v creation=%s labels=%q reviewers=%q assignees=%q title=%q header=%q footer=%q approval=%v",
		p.Ignore, p.Group, p.Schedule.String(), p.AdditionalBranchPrefix, p.MinAge, p.TimestampRequired, p.AbandonAfter,
		p.SeparateMajorMinor, p.SeparateMultipleMajor, p.SeparateMinorPatch, p.Automerge, p.AutomergeStrategy, p.AutomergeType,
		p.DraftPR, p.PRCreation, p.Labels, p.Reviewers, p.Assignees, tmpl(p.Templates.Title), tmpl(p.Templates.Header), tmpl(p.Templates.Footer), p.DashboardApproval)
}

func TestHoistGroupSettings(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		want        string // groups as YAML, "" for none
		wantRules   string // rules after the move, "" when unchanged
	}{
		{
			name: "keys every rule of the group sets alike move",
			rules: `
- {managers: [gomod], group: go, schedule: ['0 5 * * 1'], automerge: true, labels: [go]}
- {managers: [custom], group: go, schedule: ['0 5 * * 1'], automerge: false, labels: [go]}
- {managers: [npm], automerge: true}`,
			want: `go:
  schedule: [0 5 * * 1]
  labels: [go]
`,
			wantRules: `  - managers: [gomod]
    group: go
    automerge: true
  - managers: [custom]
    group: go
    automerge: false
  - managers: [npm]
    automerge: true
`,
		},
		{
			name: "a group of one rule keeps its settings",
			rules: `
- {managers: [gomod], group: go, schedule: ['0 5 * * 1']}`,
		},
		{
			name: "a later rule matching the group's deps with another value blocks",
			rules: `
- {names: ['a/**'], group: a, schedule: ['0 5 * * 1']}
- {names: ['a/x/**'], group: a, schedule: ['0 5 * * 1']}
- {names: ['a/y'], schedule: ['0 6 * * 1']}`,
		},
		{
			name: "a later rule with the same value or for other deps does not",
			rules: `
- {names: ['a/**'], group: a, schedule: ['0 5 * * 1']}
- {names: ['a/x/**'], group: a, schedule: ['0 5 * * 1']}
- {names: ['a/y'], schedule: ['0 5 * * 1']}
- {names: ['b/**', 'c'], schedule: ['0 6 * * 1']}`,
			want: `a:
  schedule: [0 5 * * 1]
`,
		},
		{
			name: "a later group without the key that can take the group's deps blocks",
			rules: `
- {managers: [docker], updateTypes: [digest, pin], group: d, schedule: ['0 5 * * 1']}
- {managers: [github-actions], updateTypes: [digest, pin], group: d, schedule: ['0 5 * * 1']}
- {names: ['ghcr.io/x/**'], group: x}`,
		},
		{
			name: "a later group setting the key itself or for other deps does not",
			rules: `
- {managers: [docker], updateTypes: [digest, pin], group: d, schedule: ['0 5 * * 1']}
- {managers: [github-actions], updateTypes: [digest, pin], group: d, schedule: ['0 5 * * 1']}
- {names: ['ghcr.io/x/**'], group: x, schedule: ['0 6 * * 1']}
- {managers: [npm], group: js}
- {managers: [docker], updateTypes: [major], group: majors}`,
			want: `d:
  schedule: [0 5 * * 1]
`,
		},
		{
			name: "groups taking each other's deps move together",
			rules: `
- {names: ['a/**'], group: a, automerge: true}
- {names: ['a/b/**'], group: b, automerge: false}
- {names: ['a/c'], group: a, automerge: true}
- {names: ['a/b/c'], group: b, automerge: false}`,
			want: `a:
  automerge: true
b:
  automerge: false
`,
		},
		{
			name: "labels a rule adds to stay",
			rules: `
- {managers: [gomod], group: go, labels: [go]}
- {managers: [custom], group: go, labels: [go], addLabels: [custom]}
- {managers: [npm], group: js, labels: [js]}
- {managers: [npm], group: js, labels: [js]}
- {names: ['react'], addLabels: [react]}`,
		},
		{
			name: "built-in group names stay",
			rules: `
- {names: ['k8s.io/api'], group: kubernetes monorepo, automerge: true}
- {names: ['k8s.io/client-go'], group: kubernetes monorepo, automerge: true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := ruleItems(t, tc.rules)
			before := mustMarshal(omap{{"rules", slices.Clone(rules)}}, "")
			groups := hoistGroupSettings(rules)
			after := omap{{"rules", rules}}
			if len(groups) > 0 {
				after.set("groups", groups)
			}
			out := mustMarshal(after, "")
			var got string
			if len(groups) > 0 {
				got = string(mustMarshal(groups, ""))
			}
			if got != tc.want {
				t.Errorf("groups:\n%s\nwant:\n%s", got, tc.want)
			}
			if gotRules := string(mustMarshal(omap{{"rules", rules}}, "")); tc.wantRules != "" && gotRules != "rules:\n"+tc.wantRules {
				t.Errorf("rules:\n%s\nwant:\nrules:\n%s", gotRules, tc.wantRules)
			} else if tc.want == "" && gotRules != string(before) {
				t.Errorf("rules changed:\n%s\nwas:\n%s", gotRules, before)
			}
			samePolicies(t, before, out)
		})
	}
}

func TestLoadHoistsGroupSettings(t *testing.T) {
	res, err := load(t, `{packageRules: [
		{matchManagers: ['github-actions'], groupName: 'GitHub Actions', schedule: ['before 6am on monday'], labels: ['actions'], automerge: true},
		{matchManagers: ['gomod'], matchUpdateTypes: ['major'], automerge: false}]}`)
	if err != nil {
		t.Fatal(err)
	}
	const want = `  GitHub Actions:
    schedule: [before 6am on monday]
    automerge: true
    labels: [actions]`
	if got := section(t, res, "groups"); got != want {
		t.Errorf("groups:\n%s\nwant:\n%s", got, want)
	}
	if got := section(t, res, "rules"); !strings.HasPrefix(got, `  - # packageRules[0]
    managers: [github-actions]
    group: GitHub Actions
  - # packageRules[0]
    managers: [docker]
    depTypes: [workflow, action]
    group: GitHub Actions
`) {
		t.Errorf("rules:\n%s", got)
	}
	if !slices.Equal(res.Windows, []string{"before 6am on monday"}) {
		t.Errorf("windows = %v, want the group's schedule", res.Windows)
	}
}

// TestExampleOCMGroups checks the groups of docs/examples-ocm.deps.yaml: written back onto
// every rule of their group (the file before groups existed), every dep gets the same
// policies.
func TestExampleOCMGroups(t *testing.T) {
	src, err := os.ReadFile("../../docs/examples-ocm.deps.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(src, &n); err != nil {
		t.Fatal(err)
	}
	doc := toOmap(t, n.Content[0]).(omap)
	gv, ok := doc.get("groups")
	if !ok {
		t.Fatal("no groups in the example")
	}
	groups := gv.(omap)
	var unhoisted omap
	for _, f := range doc {
		switch f.k {
		case "groups":
			continue
		case "rules":
			rules := slices.Clone(f.v.([]item))
			for i, r := range rules {
				if g, ok := r.body.get("group"); ok {
					if settings, ok := groups.get(g.(string)); ok {
						rules[i].body = append(slices.Clone(r.body), settings.(omap)...)
					}
				}
			}
			unhoisted.set("rules", rules)
		default:
			unhoisted.set(f.k, f.v)
		}
	}
	samePolicies(t, mustMarshal(unhoisted, ""), src)
}
