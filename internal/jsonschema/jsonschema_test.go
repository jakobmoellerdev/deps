package jsonschema

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

type node struct {
	Name     string  `yaml:"name" jsonschema:"required,pattern=^[a-z]+$"`
	Weight   float64 `yaml:"weight" jsonschema:"minimum=0.5,default=1.5"`
	Tags     []int   `yaml:"tags" jsonschema:"enum=1,enum=2,minItems=1"`
	Children []*node `yaml:"children"`
	Skipped  string  `yaml:"-"`
	NoTag    bool
}

func TestReflect(t *testing.T) {
	s, err := Reflect(&node{})
	if err != nil {
		t.Fatal(err)
	}
	s.Properties.Value("name").Extras = map[string]any{"z": 1, "a": "x"}
	b, err := json.Marshal(s, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"$defs":{"node":{"properties":{"name":{"type":"string","pattern":"^[a-z]+$"},"weight":{"type":"number","minimum":0.5,"default":1.5},` +
		`"tags":{"items":{"type":"integer","enum":[1,2]},"type":"array","minItems":1},"children":{"items":{"$ref":"#/$defs/node"},"type":"array"},` +
		`"NoTag":{"type":"boolean"}},"additionalProperties":false,"type":"object","required":["name"]}},` +
		`"properties":{"name":{"type":"string","pattern":"^[a-z]+$","a":"x","z":1},"weight":{"type":"number","minimum":0.5,"default":1.5},` +
		`"tags":{"items":{"type":"integer","enum":[1,2]},"type":"array","minItems":1},"children":{"items":{"$ref":"#/$defs/node"},"type":"array"},` +
		`"NoTag":{"type":"boolean"}},"additionalProperties":false,"type":"object","required":["name"]}`
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

type shared struct {
	Mode string `yaml:"mode" jsonschema:"default=a" jsonschema_description:"Top mode." rule_description:"Rule mode."`
}

type withShared struct {
	Before string `yaml:"before"`
	shared `yaml:",inline"`
	Rules  []ruleShared `yaml:"rules"`
}

type ruleShared struct {
	shared `yaml:",inline" jsonschema_variant:"rule"`
	After  int `yaml:"after"`
}

// The fields of an inline embedded struct are properties of the embedding object in its
// place; a variant takes their description and keywords from its own tags.
func TestReflectInline(t *testing.T) {
	s, err := Reflect(&withShared{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"$defs":{"ruleShared":{"properties":{"mode":{"type":"string","description":"Rule mode."},"after":{"type":"integer"}},"additionalProperties":false,"type":"object"}},` +
		`"properties":{"before":{"type":"string"},"mode":{"type":"string","description":"Top mode.","default":"a"},` +
		`"rules":{"items":{"$ref":"#/$defs/ruleShared"},"type":"array"}},"additionalProperties":false,"type":"object"}`
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

func TestReflectErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		v    any
		want string
	}{
		"not a struct": {new(int), "not a struct"},
		"unknown keyword": {&struct {
			A string `jsonschema:"maxLength=3"`
		}{}, `unknown jsonschema tag keyword "maxLength"`},
		"bad default": {&struct {
			A bool `jsonschema:"default=yes"`
		}{}, `default "yes"`},
		"bad enum": {&struct {
			A []int `jsonschema:"enum=x"`
		}{}, `enum "x"`},
		"bad minimum": {&struct {
			A int `jsonschema:"minimum=x"`
		}{}, `minimum "x"`},
		"bad minItems": {&struct {
			A []int `jsonschema:"minItems=-1"`
		}{}, `minItems "-1"`},
		"unsupported": {&struct{ A chan int }{}, "unsupported type chan int"},
		"map key":     {&struct{ A map[int]string }{}, "map key int is not a string"},
	} {
		if _, err := Reflect(tc.v); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
