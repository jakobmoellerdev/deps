package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

type schemaNode struct {
	Schema               string                 `json:"$schema"`
	ID                   string                 `json:"$id"`
	Ref                  string                 `json:"$ref"`
	Type                 string                 `json:"type"`
	Description          string                 `json:"description"`
	Pattern              string                 `json:"pattern"`
	Enum                 []any                  `json:"enum"`
	Default              any                    `json:"default"`
	Required             []string               `json:"required"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
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
	if s.Schema != "https://json-schema.org/draft/2020-12/schema" || s.Type != "object" || s.ID != "" {
		t.Errorf("$schema %q $id %q type %q", s.Schema, s.ID, s.Type)
	}
	rule, custom, actions := s.Defs["Rule"], s.Defs["Custom"], s.Defs["Actions"]
	if rule == nil || custom == nil || actions == nil {
		t.Fatalf("$defs = %v", s.Defs)
	}
	for name, n := range map[string]*schemaNode{"root": &s, "Rule": rule, "Custom": custom, "Actions": actions} {
		if n.AdditionalProperties == nil || *n.AdditionalProperties {
			t.Errorf("%s: additionalProperties must be false", name)
		}
		for k, p := range n.Properties {
			if p.Description == "" {
				t.Errorf("%s.%s has no description", name, k)
			}
		}
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
		{"rules.manager", rule.Properties["manager"], config.Managers},
		{"rules.depTypes", rule.Properties["depTypes"].Items, config.DepTypes},
		{"rules.datasources", rule.Properties["datasources"].Items, config.DatasourceNames},
		{"rules.updates", rule.Properties["updates"].Items, updates},
		{"rules.updateTypes", rule.Properties["updateTypes"].Items, updates},
		{"rules.commitType", rule.Properties["commitType"], config.CommitTypes},
		{"custom.datasource", custom.Properties["datasource"], config.DatasourceNames},
		{"rebaseWhen", s.Properties["rebaseWhen"], config.RebaseWhens},
	}
	for _, e := range enums {
		if got := strs(e.got.Enum); !slices.Equal(got, e.want) {
			t.Errorf("%s enum = %v, want %v (keep the jsonschema tags in sync)", e.name, got, e.want)
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
				t.Errorf("%s pattern %q rejects %q (keep the jsonschema tag in sync)", name, n.Pattern, v)
			}
		}
		if re.MatchString("calver") || re.MatchString("regex:") {
			t.Errorf("%s pattern %q is too loose", name, n.Pattern)
		}
	}
	if want := []string{"version", "pin", "timezone", "schedule", "security", "branchPrefix", "rebaseWhen", "monorepos", "actions", "minimumReleaseAge", "ignorePaths",
		"separateMajorMinor", "separateMultipleMajor", "separateMinorPatch", "updateNotScheduled", "rules", "custom"}; len(s.Properties) != len(want) {
		t.Errorf("root properties = %d, want %v", len(s.Properties), want)
	} else {
		for _, k := range want {
			if s.Properties[k] == nil {
				t.Errorf("root lacks %q", k)
			}
		}
	}
	if s.Properties["rules"].Items.Ref != "#/$defs/Rule" || s.Properties["custom"].Items.Ref != "#/$defs/Custom" || s.Properties["actions"].Ref != "#/$defs/Actions" {
		t.Error("rules/custom/actions do not reference their $defs")
	}
	if !slices.Equal(custom.Required, []string{"files"}) || rule.Required != nil || s.Required != nil {
		t.Errorf("required: root %v rule %v custom %v", s.Required, rule.Required, custom.Required)
	}
	if s.Properties["pin"].Default != "keep" || s.Properties["security"].Default != true || custom.Properties["versioning"].Default != "semver" ||
		s.Properties["separateMajorMinor"].Default != true || s.Properties["separateMultipleMajor"].Default != false ||
		s.Properties["separateMinorPatch"].Default != false || s.Properties["updateNotScheduled"].Default != true {
		t.Error("defaults missing")
	}
	if _, stderr, code := execute(t, "schema", "x"); code != 2 || !strings.Contains(stderr, "unexpected arguments") {
		t.Errorf("extra args: exit %d\n%s", code, stderr)
	}
}

func TestUnescapeHTML(t *testing.T) {
	for in, want := range map[string]string{
		`"\u003ca\u003e \u0026"`: `"<a> &"`,
		`"\\u003c"`:              `"\\u003c"`,
		`"\u00e9\n\\"`:           `"\u00e9\n\\"`,
		`\`:                      `\`,
		`"\u003`:                 `"\u003`,
	} {
		if got := string(unescapeHTML([]byte(in))); got != want {
			t.Errorf("unescapeHTML(%s) = %s, want %s", in, got, want)
		}
	}
}
