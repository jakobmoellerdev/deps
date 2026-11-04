package docgen

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonschema"
)

type fakeConfig struct {
	Mode    string           `yaml:"mode" jsonschema:"enum=fast,enum=slow,default=fast" jsonschema_description:"How to run: 'fast' or 'slow'."`
	Limit   int              `yaml:"limit" jsonschema:"minimum=0,default=0" jsonschema_description:"Maximum count; 0 is unlimited."`
	Paths   []string         `yaml:"paths" jsonschema_description:"Globs such as <dir>/**."`
	Inner   fakeInner        `yaml:"inner" jsonschema_description:"Inner settings."`
	Items   []fakeItem       `yaml:"items" jsonschema_description:"A list of items."`
	Sources map[string]fakeS `yaml:"sources" jsonschema_description:"Sources by name."`
}

type fakeInner struct {
	Host string `yaml:"host" jsonschema_description:"Host name."`
}

type fakeItem struct {
	Files []string `yaml:"files" jsonschema:"required,minItems=1" jsonschema_description:"Files to scan."`
	Kind  string   `yaml:"kind" jsonschema:"pattern=^(a|b:.+)$" jsonschema_description:"The kind."`
	Check *fakeS   `yaml:"check" jsonschema_description:"A nested check."`
}

type fakeS struct {
	URL string `yaml:"url" jsonschema:"required" jsonschema_description:"Where to fetch."`
}

func reflectFake(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.Reflect(&fakeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s.Title = "Fake config"
	s.Extras = map[string]any{MarkdownDescription: "Intro with **markdown**."}
	limit := s.Properties.Value("limit")
	limit.Extras = map[string]any{MarkdownDescription: "Maximum count; 0 is unlimited.\n\nDetails about `limit`.\n\n```yaml\nlimit: 3\n```"}
	return s
}

func TestConfig(t *testing.T) {
	b, err := Config(reflectFake(t))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		Header + "\n# Fake config\n\nIntro with **markdown**.\n",
		// table of contents with nested paths and anchors
		"- [`mode`](#mode)\n- [`limit`](#limit)\n- [`paths`](#paths)\n- [`inner`](#inner)\n  - [`inner.host`](#innerhost)\n" +
			"- [`items`](#items)\n  - [`items[].files`](#itemsfiles)\n  - [`items[].kind`](#itemskind)\n  - [`items[].check`](#itemscheck)\n" +
			"    - [`items[].check.url`](#itemscheckurl)\n- [`sources`](#sources)\n  - [`sources.<name>.url`](#sourcesnameurl)\n",
		// enum and default; plain description converted to markdown
		"## `mode`\n\nHow to run: `fast` or `slow`.\n\n- **Type:** string\n- **Default:** `fast`\n- **Values:** `fast`, `slow`\n",
		// markdownDescription: summary, facts, then details
		"## `limit`\n\nMaximum count; 0 is unlimited.\n\n- **Type:** integer\n- **Default:** `0`\n- **Minimum:** `0`\n\nDetails about `limit`.\n\n```yaml\nlimit: 3\n```\n",
		"## `paths`\n\nGlobs such as \\<dir>/\\*\\*.\n\n- **Type:** list of strings\n",
		"## `inner`\n\nInner settings.\n\n- **Type:** object\n",
		"### `inner.host`\n",
		"## `items`\n\nA list of items.\n\n- **Type:** list of objects\n- **Required keys:** `files`\n",
		"### `items[].files`\n\nFiles to scan.\n\n- **Type:** list of strings\n- **Minimum entries:** 1\n",
		"- **Pattern:** `^(a|b:.+)$`",
		"### `items[].check`\n\nA nested check.\n\n- **Type:** object\n- **Required keys:** `url`\n",
		"#### `items[].check.url`\n",
		"## `sources`\n\nSources by name.\n\n- **Type:** map of name → object\n",
		"### `sources.<name>.url`\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks\n%s\n--- output:\n%s", want, out)
		}
	}
	again, err := Config(reflectFake(t))
	if err != nil || string(again) != out {
		t.Error("output is not deterministic")
	}
}

func TestConfigAnyOfAndDefaults(t *testing.T) {
	s := &jsonschema.Schema{Title: "T", Properties: jsonschema.NewProperties()}
	s.Properties.Set("ds", &jsonschema.Schema{Description: "Datasource.", AnyOf: []*jsonschema.Schema{
		{Type: "string", Enum: []any{"a", "b"}},
		{Type: "string", Pattern: "^custom\\..+$"},
	}})
	s.Properties.Set("list", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}, Default: []string{"x/**", "y"}})
	s.Properties.Set("empty", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}, Default: []any{}})
	s.Properties.Set("str", &jsonschema.Schema{Type: "string", Default: ""})
	s.Properties.Set("flag", &jsonschema.Schema{Type: "boolean", Default: true})
	s.Properties.Set("names", &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Type: "string"},
		PropertyNames: &jsonschema.Schema{Pattern: "^[a-z]+$"}})
	b, err := Config(s)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"- **Type:** string\n- **Values:** `a`, `b`, values matching `^custom\\..+$`\n",
		"- **Default:** `x/**`, `y`\n",
		"- **Default:** `[]`\n",
		"- **Default:** `\"\"`\n",
		"- **Default:** `true`\n",
		"- **Type:** map of name → string\n- **Names:** must match `^[a-z]+$`\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n--- output:\n%s", want, out)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	ref := &jsonschema.Schema{Ref: "#/$defs/Missing"}
	s := &jsonschema.Schema{Title: "T", Properties: jsonschema.NewProperties()}
	s.Properties.Set("x", ref)
	if _, err := Config(s); err == nil || !strings.Contains(err.Error(), `x: unresolvable $ref "#/$defs/Missing"`) {
		t.Errorf("unresolvable ref: %v", err)
	}

	loop := &jsonschema.Schema{Type: "object", Properties: jsonschema.NewProperties()}
	loop.Properties.Set("self", &jsonschema.Schema{Ref: "#/$defs/Loop"})
	s = &jsonschema.Schema{Title: "T", Properties: jsonschema.NewProperties(), Definitions: jsonschema.Definitions{"Loop": loop}}
	s.Properties.Set("a", &jsonschema.Schema{Ref: "#/$defs/Loop"})
	if _, err := Config(s); err == nil || !strings.Contains(err.Error(), "recursive definition") {
		t.Errorf("recursion: %v", err)
	}

	// x.y and xy have the same anchor.
	inner := &jsonschema.Schema{Type: "object", Properties: jsonschema.NewProperties()}
	inner.Properties.Set("y", &jsonschema.Schema{Type: "string"})
	s = &jsonschema.Schema{Title: "T", Properties: jsonschema.NewProperties(), Definitions: jsonschema.Definitions{"X": inner}}
	s.Properties.Set("x", &jsonschema.Schema{Ref: "#/$defs/X"})
	s.Properties.Set("xy", &jsonschema.Schema{Type: "string"})
	if _, err := Config(s); err == nil || !strings.Contains(err.Error(), "duplicate heading anchor #xy") {
		t.Errorf("duplicate anchor: %v", err)
	}

	// no properties: only the title and intro
	b, err := Config(&jsonschema.Schema{Title: "Empty", Description: "Nothing."})
	if err != nil || string(b) != Header+"\n# Empty\n\nNothing.\n" {
		t.Errorf("empty schema: %q, %v", b, err)
	}
}
