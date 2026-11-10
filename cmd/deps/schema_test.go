package main

import (
	"encoding/json/v2"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

type schemaNode struct {
	Schema               string                 `json:"$schema"`
	ID                   string                 `json:"$id"`
	Ref                  string                 `json:"$ref"`
	Type                 string                 `json:"type"`
	Description          string                 `json:"description"`
	MarkdownDescription  string                 `json:"markdownDescription"`
	Pattern              string                 `json:"pattern"`
	Enum                 []any                  `json:"enum"`
	Default              any                    `json:"default"`
	Required             []string               `json:"required"`
	AdditionalProperties any                    `json:"additionalProperties"` // false or a schema
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	AnyOf                []*schemaNode          `json:"anyOf"`
	AllOf                []*schemaNode          `json:"allOf"`
	If                   *schemaNode            `json:"if"`
	Then                 *schemaNode            `json:"then"`
	MinItems             int                    `json:"minItems"`
	PropertyNames        *schemaNode            `json:"propertyNames"`
	Defs                 map[string]*schemaNode `json:"$defs"`
}

func strs(vs []any) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i], _ = v.(string)
	}
	return out
}

func TestSchema(t *testing.T) {
	stdout, stderr, code := execute(t, "schema")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if strings.Contains(stdout, `\u003c`) || !strings.Contains(stdout, "(?P<version>)") {
		t.Error("schema output is HTML-escaped")
	}
	var s schemaNode
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if s.Schema != "https://json-schema.org/draft/2020-12/schema" || s.Type != "object" || s.ID != schemaURL("dev") {
		t.Errorf("$schema %q $id %q type %q", s.Schema, s.ID, s.Type)
	}
	rule, custom, actions, customDS, lfm := s.Defs["Rule"], s.Defs["Custom"], s.Defs["Actions"], s.Defs["CustomDatasource"], s.Defs["LockFileMaintenance"]
	git, dashboard, gomod, composer, group := s.Defs["Git"], s.Defs["Dashboard"], s.Defs["Gomod"], s.Defs["Composer"], s.Defs["GroupSettings"]
	if rule == nil || custom == nil || actions == nil || customDS == nil || lfm == nil || git == nil || dashboard == nil || gomod == nil || composer == nil || group == nil {
		t.Fatalf("$defs = %v", s.Defs)
	}
	for name, n := range map[string]*schemaNode{"root": &s, "Rule": rule, "Custom": custom, "Actions": actions, "CustomDatasource": customDS, "LockFileMaintenance": lfm, "Git": git, "Dashboard": dashboard, "Gomod": gomod, "Composer": composer, "GroupSettings": group} {
		if n.AdditionalProperties != false {
			t.Errorf("%s: additionalProperties must be false", name)
		}
		for k, p := range n.Properties {
			if p.Description == "" {
				t.Errorf("%s.%s has no description", name, k)
			}
			if p.MarkdownDescription == "" || strings.Contains(p.MarkdownDescription, "\n## ") {
				t.Errorf("%s.%s: markdownDescription %q", name, k, p.MarkdownDescription)
			}
		}
	}
	if !strings.HasPrefix(s.MarkdownDescription, "`deps` reads `.deps.yaml`") {
		t.Errorf("root markdownDescription = %q", s.MarkdownDescription)
	}
	if md := rule.Properties["allow"].MarkdownDescription; !strings.HasPrefix(md, "New versions must satisfy this Masterminds semver constraint (e.g. `< 7`)") ||
		!strings.Contains(md, "\n\n- A **constraint**") {
		t.Errorf("rules.allow markdownDescription = %q", md)
	}
	if got, ok := s.Properties["ignorePaths"].Default.([]any); !ok || !slices.Equal(strs(got), config.DefaultIgnorePaths) {
		t.Errorf("ignorePaths default = %v", s.Properties["ignorePaths"].Default)
	}
	if got := git.Properties["author"].Default; got != defaultAuthorName+" <"+defaultAuthorEmail+">" {
		t.Errorf("git.author default = %v", got)
	}
	var updates []string
	for _, u := range dep.UpdateTypes {
		updates = append(updates, string(u))
	}
	pins := []string{config.PinKeep, config.PinDigest}
	enums := []struct {
		name string
		got  *schemaNode
		want []string
	}{
		{"pin", s.Properties["pin"], pins},
		{"rules.pin", rule.Properties["pin"], pins},
		{"rules.managers", rule.Properties["managers"].Items, config.Managers},
		{"rules.depTypes", rule.Properties["depTypes"].Items, config.DepTypes},
		{"rules.datasources", datasourceEnum(t, "rules.datasources", rule.Properties["datasources"].Items), config.DatasourceNames},
		{"rules.allowedUpdateTypes", rule.Properties["allowedUpdateTypes"].Items, updates},
		{"rules.updateTypes", rule.Properties["updateTypes"].Items, append(slices.Clone(updates), string(dep.Replacement))},
		{"rules.commitType", rule.Properties["commitType"], config.CommitTypes},
		{"custom.datasource", datasourceEnum(t, "custom.datasource", custom.Properties["datasource"]), config.DatasourceNames},
		{"customDatasources.format", customDS.Properties["format"], customds.Formats},
		{"rebaseWhen", s.Properties["rebaseWhen"], config.RebaseWhens},
		{"git.platformCommit", git.Properties["platformCommit"], config.PlatformCommits},
		{"rangeStrategy", s.Properties["rangeStrategy"], config.RangeStrategies},
		{"rules.rangeStrategy", rule.Properties["rangeStrategy"], config.RangeStrategies},
		{"automergeStrategy", s.Properties["automergeStrategy"], config.AutomergeStrategies},
		{"rules.automergeStrategy", rule.Properties["automergeStrategy"], config.AutomergeStrategies},
		{"automergeType", s.Properties["automergeType"], config.AutomergeTypes},
		{"rules.automergeType", rule.Properties["automergeType"], config.AutomergeTypes},
		{"separate", s.Properties["separate"], config.Separations},
		{"rules.separate", rule.Properties["separate"], config.Separations},
		{"groups.automergeType", group.Properties["automergeType"], config.AutomergeTypes},
		{"groups.prCreation", group.Properties["prCreation"], config.PRCreations},
	}
	for _, e := range enums {
		if got := strs(e.got.Enum); !slices.Equal(got, e.want) {
			t.Errorf("%s enum = %v, want %v", e.name, got, e.want)
		}
	}
	if v := s.Properties["version"].Enum; len(v) != 1 || v[0] != float64(config.FormatVersion) {
		t.Errorf("version enum = %v", v)
	}
	// rules[].depTypes narrows to the dep types of the only listed manager.
	if len(rule.AllOf) != len(config.Managers) {
		t.Fatalf("Rule allOf = %d entries, want one per manager", len(rule.AllOf))
	}
	byManager := config.ManagerDepTypes()
	for i, m := range config.Managers {
		c := rule.AllOf[i]
		mgrs := c.If.Properties["managers"]
		if !slices.Equal(c.If.Required, []string{"managers"}) || mgrs.MinItems != 1 || !slices.Equal(strs(mgrs.Items.Enum), []string{m}) {
			t.Errorf("allOf[%d].if = %+v", i, c.If)
		}
		if got := strs(c.Then.Properties["depTypes"].Items.Enum); !slices.Equal(got, byManager[m]) {
			t.Errorf("%s: then depTypes = %v, want %v", m, got, byManager[m])
		}
	}
	// versioning takes the scheme names or regex:<RE2>, so it is a pattern instead of an enum.
	for name, n := range map[string]*schemaNode{"rules.versioning": rule.Properties["versioning"], "custom.versioning": custom.Properties["versioning"]} {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			t.Fatalf("%s pattern %q: %v", name, n.Pattern, err)
		}
		for _, v := range append(slices.Clone(config.Versionings), `regex:^v(?<major>\d+)$`) {
			if !re.MatchString(v) {
				t.Errorf("%s pattern %q rejects %q", name, n.Pattern, v)
			}
		}
		if re.MatchString("calver") || re.MatchString("regex:") {
			t.Errorf("%s pattern %q is too loose", name, n.Pattern)
		}
	}
	if want := []string{"version", "pin", "rangeStrategy", "schedule", "timezone", "branchPrefix", "baseBranches", "additionalBranchPrefix", "useBaseBranchConfig", "rebaseWhen", "builtins", "git", "prConcurrentLimit", "prHourlyLimit", "actions", "terraform", "gomod", "gitlabci", "composer", "minimumReleaseAge", "minimumReleaseAgeBehaviour", "abandonmentThreshold", "ignorePaths",
		"separate", "updateNotScheduled", "automerge", "automergeStrategy", "automergeType", "draftPR", "prCreation", "prNotPendingHours", "labels", "reviewers", "assignees", "prTitle", "prHeader", "prFooter", "rebaseLabel", "stopUpdatingLabel",
		"dashboard", "dashboardApproval", "extends", "rules", "custom", "customDatasources", "groups", "registryAliases", "lockFileMaintenance"}; len(s.Properties) != len(want) {
		t.Errorf("root properties = %d, want %v", len(s.Properties), want)
	} else {
		for _, k := range want {
			if s.Properties[k] == nil {
				t.Errorf("root lacks %q", k)
			}
		}
	}
	cds := s.Properties["customDatasources"]
	if ap, _ := cds.AdditionalProperties.(map[string]any); cds.Type != "object" || ap["$ref"] != "#/$defs/CustomDatasource" || cds.PropertyNames == nil || cds.PropertyNames.Pattern != config.CustomDatasourceNamePattern {
		t.Errorf("customDatasources = %+v", cds)
	}
	if !slices.Equal(customDS.Required, []string{"url"}) || customDS.Properties["format"].Default != "json" || customDS.Properties["versions"].Default != customds.DefaultVersions {
		t.Errorf("CustomDatasource required %v, defaults %v %v", customDS.Required, customDS.Properties["format"].Default, customDS.Properties["versions"].Default)
	}
	// groups.<name> takes the settings but the pre-policy keys, with no defaults of its own.
	if gs := s.Properties["groups"]; gs == nil {
		t.Error("root lacks groups")
	} else if ap, _ := gs.AdditionalProperties.(map[string]any); gs.Type != "object" || ap["$ref"] != "#/$defs/GroupSettings" {
		t.Errorf("groups = %+v", gs)
	}
	if len(group.Properties) != len(config.GroupKeys) || group.Properties["pin"] != nil || group.Properties["rangeStrategy"] != nil || group.Properties["schedule"] == nil {
		t.Errorf("GroupSettings properties = %v, want %v", slices.Sorted(maps.Keys(group.Properties)), config.GroupKeys)
	}
	for k, p := range group.Properties {
		if p.Default != nil {
			t.Errorf("groups.<name>.%s has default %v", k, p.Default)
		}
	}
	if s.Properties["rules"].Items.Ref != "#/$defs/Rule" || s.Properties["custom"].Items.Ref != "#/$defs/Custom" || s.Properties["actions"].Ref != "#/$defs/Actions" ||
		s.Properties["lockFileMaintenance"].Ref != "#/$defs/LockFileMaintenance" || s.Properties["git"].Ref != "#/$defs/Git" || s.Properties["dashboard"].Ref != "#/$defs/Dashboard" ||
		s.Properties["gomod"].Ref != "#/$defs/Gomod" || s.Properties["composer"].Ref != "#/$defs/Composer" {
		t.Error("rules/custom/actions/lockFileMaintenance/git/dashboard/gomod/composer do not reference their $defs")
	}
	if !slices.Equal(custom.Required, []string{"files"}) || rule.Required != nil || s.Required != nil {
		t.Errorf("required: root %v rule %v custom %v", s.Required, rule.Required, custom.Required)
	}
	if s.Properties["pin"].Default != "keep" || s.Properties["rangeStrategy"].Default != config.RangeAuto || custom.Properties["versioning"].Default != "semver" ||
		s.Properties["separate"].Default != config.SeparateMajor || s.Properties["updateNotScheduled"].Default != true ||
		s.Properties["automerge"].Default != false || s.Properties["automergeStrategy"].Default != "auto" ||
		s.Properties["draftPR"].Default != false || s.Properties["prCreation"].Default != config.PRCreationImmediate ||
		dashboard.Properties["enabled"].Default != false || dashboard.Properties["title"].Default != config.DefaultDashboardTitle || s.Properties["dashboardApproval"].Default != false ||
		gomod.Properties["updateImportPaths"].Default != true || lfm.Properties["enabled"].Default != false ||
		s.Properties["minimumReleaseAgeBehaviour"].Default != config.TimestampOptional {
		t.Error("defaults missing")
	}
	for _, n := range []*schemaNode{s.Properties["minimumReleaseAgeBehaviour"], rule.Properties["minimumReleaseAgeBehaviour"]} {
		if n == nil || !slices.Equal(n.Enum, []any{config.TimestampOptional, config.TimestampRequired}) {
			t.Errorf("minimumReleaseAgeBehaviour enum = %+v", n)
		}
	}
	if _, stderr, code := execute(t, "schema", "x"); code != 2 || !strings.Contains(stderr, "unexpected arguments") {
		t.Errorf("extra args: exit %d\n%s", code, stderr)
	}
}

func TestSchemaURL(t *testing.T) {
	for version, want := range map[string]string{
		"v0.4.0":                  "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.4.0/deps.schema.json",
		"dev":                     "https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main",
		"v0.4.0-3-g1234567-dirty": "https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main",
	} {
		if got := schemaURL(version); got != want {
			t.Errorf("schemaURL(%q) = %q, want %q", version, got, want)
		}
	}
}

// datasourceEnum returns the enum of a datasource field, which is anyOf the built-in names and
// a custom.<name> pattern.
func datasourceEnum(t *testing.T, name string, n *schemaNode) *schemaNode {
	t.Helper()
	if len(n.AnyOf) != 2 || n.Enum != nil {
		t.Fatalf("%s: want anyOf [enum, custom pattern], got %+v", name, n)
	}
	re, err := regexp.Compile(n.AnyOf[1].Pattern)
	if err != nil {
		t.Fatalf("%s: custom pattern: %v", name, err)
	}
	for v, want := range map[string]bool{"custom.envtest": true, "custom.a.b-c_1": true, "custom.": false, "envtest": false, "custom.-x": false} {
		if re.MatchString(v) != want {
			t.Errorf("%s: pattern %q matches %q = %v", name, re, v, !want)
		}
	}
	return n.AnyOf[0]
}

// TestConfigTagsHaveNoValueLists checks that no jsonschema tag of the config types repeats a
// list of allowed values: addEnums takes them from config's lists.
func TestConfigTagsHaveNoValueLists(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[config.Config](), reflect.TypeFor[config.Settings](), reflect.TypeFor[config.Rule](), reflect.TypeFor[config.Custom](),
		reflect.TypeFor[config.CustomDatasource](), reflect.TypeFor[config.Checksum](), reflect.TypeFor[config.LockFileMaintenance](),
		reflect.TypeFor[config.Actions](), reflect.TypeFor[config.Terraform]()} {
		for f := range typ.Fields() {
			if tag := f.Tag.Get("jsonschema"); strings.Contains(tag, "enum=") || strings.Contains(tag, "pattern=") {
				t.Errorf("%s.%s: jsonschema tag %q lists values", typ.Name(), f.Name, tag)
			}
		}
	}
}

func TestAddKeyDocsRejectsUnknownKeys(t *testing.T) {
	for _, c := range []struct {
		docs map[string]map[string]string
		want string
	}{
		{map[string]map[string]string{"Nope": {"x": "y"}}, `unknown definition "Nope"`},
		{map[string]map[string]string{"Rule": {"nope": "y"}}, `Rule has no key "nope"`},
		{map[string]map[string]string{"Config": {"pinn": "y"}}, `Config has no key "pinn"`},
	} {
		s, err := reflectConfig()
		if err != nil {
			t.Fatal(err)
		}
		if err := addKeyDocs(s, c.docs); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}
