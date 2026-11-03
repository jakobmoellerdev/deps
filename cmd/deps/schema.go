package main

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

func newSchema() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema (draft 2020-12) of .deps.yaml",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := configSchema()
			if err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(b); err != nil {
				return fmt.Errorf("write schema: %w", err)
			}
			return nil
		},
	}
}

// configSchema reflects config.Config into an indented JSON Schema. Keys come from the yaml
// tags, enums, defaults and descriptions from the jsonschema tags; unknown keys are rejected,
// as config.Load does.
func configSchema() ([]byte, error) {
	r := &jsonschema.Reflector{
		FieldNameTag:               "yaml",
		RequiredFromJSONSchemaTags: true,
		ExpandedStruct:             true,
		Anonymous:                  true, // the package-path $id is not a resolvable URL
	}
	s := r.Reflect(&config.Config{})
	s.Title = "deps configuration (.deps.yaml)"
	allowCustomDatasources(s)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(unescapeHTML(b), '\n'), nil
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

// unescapeHTML undoes encoding/json's HTML escaping of <, > and & so descriptions like
// "(?P<version>)" stay readable. Escaped backslashes are skipped as pairs, so a literal
// `\u003c` text is left alone.
func unescapeHTML(b []byte) []byte {
	out := b[:0]
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			continue
		}
		if i+5 < len(b) && b[i+1] == 'u' && b[i+2] == '0' && b[i+3] == '0' {
			switch string(b[i+4 : i+6]) {
			case "3c":
				out, i = append(out, '<'), i+5
				continue
			case "3e":
				out, i = append(out, '>'), i+5
				continue
			case "26":
				out, i = append(out, '&'), i+5
				continue
			}
		}
		out, i = append(out, b[i], b[i+1]), i+1
	}
	return out
}
