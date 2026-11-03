package main

import (
	"encoding/json"
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
		{"rules.updates", rule.Properties["updates"].Items, updates},
		{"rules.commitType", rule.Properties["commitType"], config.CommitTypes},
		{"custom.datasource", custom.Properties["datasource"], config.CustomDatasources},
		{"custom.versioning", custom.Properties["versioning"], config.Versionings},
	}
	for _, e := range enums {
		if got := strs(e.got.Enum); !slices.Equal(got, e.want) {
			t.Errorf("%s enum = %v, want %v (keep the jsonschema tags in sync)", e.name, got, e.want)
		}
	}
	if want := []string{"version", "pin", "timezone", "schedule", "security", "branchPrefix", "monorepos", "actions", "ignorePaths", "rules", "custom"}; len(s.Properties) != len(want) {
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
	if s.Properties["pin"].Default != "keep" || s.Properties["security"].Default != true || custom.Properties["versioning"].Default != "semver" {
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
