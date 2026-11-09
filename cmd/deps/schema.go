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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonschema"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

func newSchema() *cli.Command {
	return &cli.Command{
		Name:  "schema",
		Usage: "Print the JSON Schema (draft 2020-12) of .deps.yaml",
		Description: "Prints the JSON Schema of `.deps.yaml`, reflected from the config types, with every key's " +
			"description (`markdownDescription` holds the long form editors show on hover). Unknown keys are " +
			"rejected (`additionalProperties: false`), as in the loader. The repository's `deps.schema.json` is " +
			"this output. `$id` is the schema's URL for this version: the `deps.schema.json` asset of the " +
			"release (`dev` builds: the file on main).",
		Metadata: map[string]any{docgen.MetaExample: "  deps schema > deps.schema.json\n" +
			"  # then start .deps.yaml with the schema of your deps version (`deps version`):\n" +
			"  # yaml-language-server: $schema=" + releaseURL + "/<version>/deps.schema.json"},
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

// reflectConfig reflects config.Config into a JSON Schema. Keys come from the yaml tags,
// defaults and descriptions from the jsonschema tags, the allowed values from config's lists
// (addEnums), and the long descriptions from config.KeyDocs; unknown keys are rejected, as
// config.Load does.
func reflectConfig() (*jsonschema.Schema, error) {
	s, err := jsonschema.Reflect(&config.Config{})
	if err != nil {
		return nil, err
	}
	s.Title = "deps configuration (.deps.yaml)"
	s.ID = schemaURL(version)
	addEnums(s)
	allowCustomDatasources(s)
	// postUpgradeTasks also takes a plain list of commands (config.PostUpgradeTasks.UnmarshalYAML).
	tasks := s.Definitions["Rule"].Properties.Value("postUpgradeTasks")
	tasks.AnyOf = []*jsonschema.Schema{{Items: &jsonschema.Schema{Type: "string"}, Type: "array"}, {Ref: tasks.Ref}}
	tasks.Ref = ""
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

// releaseURL is the download URL prefix of the Gitea release assets (append /<tag>/<asset>).
const releaseURL = "https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download"

// releaseTagRe matches the versions of release builds: their tags, v<major>.<minor>.<patch>.
var releaseTagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// schemaURL is the URL of the schema of the given deps version: the release asset for a
// release tag (v<major>.<minor>.<patch>), otherwise (dev and git-describe builds) the schema on main.
func schemaURL(version string) string {
	if releaseTagRe.MatchString(version) {
		return releaseURL + "/" + version + "/deps.schema.json"
	}
	return "https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main"
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

// enumOf converts a list of allowed values to an enum.
func enumOf[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// addEnums sets the allowed values of the config keys from the lists config validates against,
// so the schema never repeats them: scalar keys get an enum, list keys an enum of their items,
// versioning keys a pattern of the scheme names or regex:<RE2>. rules[].depTypes is narrowed
// to the dep types of the manager when rules[].managers lists exactly one (addRuleDepTypes).
func addEnums(s *jsonschema.Schema) {
	rule, custom := s.Definitions["Rule"], s.Definitions["Custom"]
	scalar := func(n *jsonschema.Schema, key string, vs []any) { n.Properties.Value(key).Enum = vs }
	items := func(n *jsonschema.Schema, key string, vs []any) { n.Properties.Value(key).Items.Enum = vs }
	scalar(s, "version", enumOf([]int{config.FormatVersion}))
	for _, n := range []*jsonschema.Schema{s, rule} {
		scalar(n, "pin", enumOf(config.Pins))
		scalar(n, "rangeStrategy", enumOf(config.RangeStrategies))
		scalar(n, "automergeStrategy", enumOf(config.AutomergeStrategies))
		scalar(n, "automergeType", enumOf(config.AutomergeTypes))
		scalar(n, "minimumReleaseAgeBehaviour", enumOf(config.MinimumReleaseAgeBehaviours))
		scalar(n, "prCreation", enumOf(config.PRCreations))
	}
	scalar(s, "rebaseWhen", enumOf(config.RebaseWhens))
	scalar(s, "platformCommit", enumOf(config.PlatformCommits))
	scalar(s, "useBaseBranchConfig", enumOf(config.BaseConfigs))
	items(rule, "managers", enumOf(config.Managers))
	items(rule, "depTypes", enumOf(config.DepTypes))
	items(rule, "datasources", enumOf(config.DatasourceNames))
	items(rule, "updateTypes", enumOf(config.UpdateTypeSelectors))
	items(rule, "allowedUpdateTypes", enumOf(config.UpdateTypes))
	scalar(rule, "commitType", enumOf(config.CommitTypes))
	scalar(custom, "datasource", enumOf(config.DatasourceNames))
	scalar(custom, "fileFormat", enumOf(config.CustomFileFormats))
	scalar(s.Definitions["CustomDatasource"], "format", enumOf(customds.Formats))
	scalar(s.Definitions["PostUpgradeTasks"], "executionMode", enumOf(config.ExecutionModes))
	alts := make([]string, len(config.Versionings))
	for i, v := range config.Versionings {
		alts[i] = regexp.QuoteMeta(v)
	}
	versioningPattern := "^(" + strings.Join(alts, "|") + "|" + regexp.QuoteMeta(versioning.RegexPrefix) + ".+)$"
	rule.Properties.Value("versioning").Pattern = versioningPattern
	custom.Properties.Value("versioning").Pattern = versioningPattern
	addRuleDepTypes(rule)
}

// addRuleDepTypes adds one if/then per manager to the Rule definition: when managers lists
// only that manager, depTypes takes only its dep types (config.ManagerDepTypes), so editors
// offer and check the dep types that manager produces.
func addRuleDepTypes(rule *jsonschema.Schema) {
	byManager := config.ManagerDepTypes()
	minItems := uint64(1)
	for _, m := range config.Managers {
		ifProps := jsonschema.NewProperties()
		ifProps.Set("managers", &jsonschema.Schema{Items: &jsonschema.Schema{Enum: []any{m}}, MinItems: &minItems})
		thenProps := jsonschema.NewProperties()
		thenProps.Set("depTypes", &jsonschema.Schema{Items: &jsonschema.Schema{Enum: enumOf(byManager[m])}})
		rule.AllOf = append(rule.AllOf, &jsonschema.Schema{
			If:   &jsonschema.Schema{Properties: ifProps, Required: []string{"managers"}},
			Then: &jsonschema.Schema{Properties: thenProps},
		})
	}
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
