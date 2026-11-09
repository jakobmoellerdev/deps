package config

import (
	"reflect"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestLint(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		want        []Warning
	}{
		{"valid rules", `
  - {managers: [gomod], depTypes: [indirect], ignore: false}
  - {managers: [npm, cargo], depTypes: [devDependencies], group: dev}
  - {datasources: [docker], depTypes: [helm, dockerfile], automerge: true}
  - {managers: [custom], datasources: [custom.envtest, github-tags], schedule: []}
  - {updateTypes: [major], ignore: true}
  - {allowedUpdateTypes: [minor, patch]}`, nil},
		{"impossible manager combination", `
  - {managers: [gomod], depTypes: [devDependencies], datasources: [npm], group: x}
  - {managers: [gomod], depTypes: [devDependencies], datasources: [npm]}`, []Warning{
			{"rules[0].depTypes[0]", `dep type "devDependencies" is not produced by gomod, so it matches nothing`},
			{"rules[0].datasources[0]", `datasource "npm" is not used by gomod, so it matches nothing`},
			{"rules[1]", "sets only match fields and no apply field (ignore, allowedUpdateTypes, group, …), so it changes nothing"},
			{"rules[1].depTypes[0]", `dep type "devDependencies" is not produced by gomod, so it matches nothing`},
			{"rules[1].datasources[0]", `datasource "npm" is not used by gomod, so it matches nothing`},
		}},
		{"several managers", `
  - {managers: [gomod, docker], depTypes: [direct, requirements], group: x}`, []Warning{
			{"rules[0].depTypes[1]", `dep type "requirements" is not produced by gomod or docker, so it matches nothing`},
		}},
		{"custom datasource needs the custom manager", `
  - {managers: [docker], datasources: [custom.envtest], group: x}`, []Warning{
			{"rules[0].datasources[0]", `datasource "custom.envtest" is not used by docker, so it matches nothing`},
		}},
		{"dep types and datasources without overlap", `
  - {depTypes: [direct], datasources: [docker], group: x}
  - {managers: [npm], depTypes: [devDependencies], datasources: [node-version], group: x}`, []Warning{
			{"rules[0]", "matches nothing: no dep of depTypes [direct] uses datasources [docker]"},
			{"rules[1]", "matches nothing: no dep of depTypes [devDependencies] uses datasources [node-version]"},
		}},
		{"empty and match-only rules", `
  - {}
  - {managers: [gomod], names: ['x/**']}`, []Warning{
			{"rules[0]", "is empty: no match field and no apply field"},
			{"rules[1]", "sets only match fields and no apply field (ignore, allowedUpdateTypes, group, …), so it changes nothing"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, ".deps.yaml", "customDatasources: {envtest: {url: 'https://x/y.json'}}\nrules:"+tc.rules+"\n")
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Lint(); !slices.Equal(got, tc.want) {
				t.Errorf("Lint() =\n%v\nwant\n%v", got, tc.want)
			}
		})
	}
}

func TestRuleFieldGroups(t *testing.T) {
	if RuleApplyFields[0] != "ignore" || slices.Contains(RuleApplyFields, "managers") || !slices.Contains(RuleApplyFields, "automergeType") {
		t.Errorf("RuleApplyFields = %v", RuleApplyFields)
	}
	// The match fields come first in Rule, so the reference lists them first.
	var first []string
	for f := range reflect.TypeFor[Rule]().Fields() {
		if k := yamlKey(f); k != "" && len(first) < len(RuleMatchFields) {
			first = append(first, k)
		}
	}
	if !slices.Equal(first, RuleMatchFields) {
		t.Errorf("first Rule keys = %v, want RuleMatchFields %v", first, RuleMatchFields)
	}
}

func TestUnusedRules(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - {managers: [gomod], group: go}
  - {managers: [npm], group: js}
  - {names: ['example.com/**'], updateTypes: [major], ignore: true}
  - {datasources: [golang-version], group: tc}
  - {paths: ['charts/**'], group: charts}
`)
	if err != nil {
		t.Fatal(err)
	}
	deps := []dep.Dep{
		{Manager: "gomod", Name: "example.com/a", DepType: "direct", Datasource: "go", File: "go.mod"},
		{Manager: "gomod", Name: "go", DepType: "go", Datasource: "go-toolchain", File: "go.mod", Skip: "ignored by default"},
	}
	if got := c.UnusedRules(deps); !slices.Equal(got, []int{1, 4}) {
		t.Errorf("UnusedRules = %v, want [1 4]", got)
	}
}

func TestManagerRegistryLists(t *testing.T) {
	byManager := ManagerDepTypes()
	if len(byManager) != len(Managers) || Managers[len(Managers)-1] != "custom" {
		t.Fatalf("Managers = %v, ManagerDepTypes = %v", Managers, byManager)
	}
	if !slices.Equal(byManager["gomod"], []string{"direct", "indirect", "go", "toolchain"}) {
		t.Errorf("gomod dep types = %v", byManager["gomod"])
	}
	if want := append(slices.Clone(Datasources), "azure-tags", "bitbucket-server-tags", "bitbucket-tags", "github-digest", "github-release-attachments", "github-tags", "golang-version", "node"); !slices.Equal(DatasourceNames, want) {
		t.Errorf("DatasourceNames = %v", DatasourceNames)
	}
}
