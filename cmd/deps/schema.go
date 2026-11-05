package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonschema"
)

func newSchema() *cli.Command {
	return &cli.Command{
		Name:  "schema",
		Usage: "Print the JSON Schema (draft 2020-12) of .deps.yaml",
		Description: "Prints the JSON Schema of `.deps.yaml`, reflected from the config types, with every key's " +
			"description (`markdownDescription` holds the long form editors show on hover). Unknown keys are " +
			"rejected (`additionalProperties: false`), as in the loader. The repository's `deps.schema.json` is " +
			"this output.",
		Metadata: map[string]any{docgen.MetaExample: "  deps schema > deps.schema.json\n" +
			"  # then start .deps.yaml with:\n" +
			"  # yaml-language-server: $schema=./deps.schema.json"},
		ArgValidator: noArgs,
		Action: func(_ context.Context, cmd *cli.Command) error {
			b, err := configSchema()
			if err != nil {
				return err
			}
			if _, err := cmd.Root().Writer.Write(b); err != nil {
				return fmt.Errorf("write schema: %w", err)
			}
			return nil
		},
	}
}

// configSchema reflects config.Config into an indented JSON Schema (see reflectConfig).
func configSchema() ([]byte, error) {
	s, err := reflectConfig()
	if err != nil {
		return nil, err
	}
	// v2 escapes no HTML characters, so descriptions like "(?P<version>)" stay readable.
	b, err := json.Marshal(s, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(b, '\n'), nil
}

// reflectConfig reflects config.Config into a JSON Schema. Keys come from the yaml tags, enums,
// defaults and descriptions from the jsonschema tags, and the long descriptions from
// config.KeyDocs; unknown keys are rejected, as config.Load does.
func reflectConfig() (*jsonschema.Schema, error) {
	s, err := jsonschema.Reflect(&config.Config{})
	if err != nil {
		return nil, err
	}
	s.Title = "deps configuration (.deps.yaml)"
	allowCustomDatasources(s)
	s.Properties.Value("ignorePaths").Default = config.DefaultIgnorePaths
	docs, err := config.KeyDocs()
	if err != nil {
		return nil, err
	}
	if err := addKeyDocs(s, docs); err != nil {
		return nil, err
	}
	return s, nil
}

// addKeyDocs sets markdownDescription (the keyword editors such as VS Code and
// yaml-language-server prefer over description) on the top level and every key: the
// description as markdown, followed by the key's long description from docs (config.KeyDocs).
// The top level is the definition "Config"; a long description of an unknown key is an error.
func addKeyDocs(s *jsonschema.Schema, docs map[string]map[string]string) error {
	defs := map[string]*jsonschema.Schema{"Config": s}
	maps.Copy(defs, s.Definitions)
	for _, def := range slices.Sorted(maps.Keys(docs)) {
		d := defs[def]
		if d == nil {
			return fmt.Errorf("schema docs: unknown definition %q", def)
		}
		for _, key := range slices.Sorted(maps.Keys(docs[def])) {
			if key != "" && (d.Properties == nil || d.Properties.Value(key) == nil) {
				return fmt.Errorf("schema docs: %s has no key %q", def, key)
			}
		}
		if text := docs[def][""]; text != "" {
			setMarkdown(d, text)
		}
	}
	for def, d := range defs {
		if d.Properties == nil {
			continue
		}
		for key, p := range d.Properties.All() {
			setMarkdown(p, strings.TrimSpace(docgen.PlainToMarkdown(p.Description)+"\n\n"+docs[def][key]))
		}
	}
	return nil
}

func setMarkdown(n *jsonschema.Schema, md string) {
	if n.Extras == nil {
		n.Extras = map[string]any{}
	}
	n.Extras[docgen.MarkdownDescription] = md
}

// allowCustomDatasources lets the datasource fields take custom.<name> besides the enum of
// built-in names (an enum alone would reject them), and constrains customDatasources keys.
func allowCustomDatasources(s *jsonschema.Schema) {
	custom := &jsonschema.Schema{Type: "string", Pattern: `^` + regexp.QuoteMeta(config.CustomDatasourcePrefix) + config.CustomDatasourceNamePattern[1:]}
	anyOf := func(p *jsonschema.Schema) {
		p.AnyOf = []*jsonschema.Schema{{Type: "string", Enum: p.Enum}, custom}
		p.Type, p.Enum = "", nil
	}
	anyOf(s.Definitions["Custom"].Properties.Value("datasource"))
	anyOf(s.Definitions["Rule"].Properties.Value("datasources").Items)
	s.Properties.Value("customDatasources").PropertyNames = &jsonschema.Schema{Pattern: config.CustomDatasourceNamePattern}
}
