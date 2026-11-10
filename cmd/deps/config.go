package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonschema"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func newConfig(o *rootOpts) *cli.Command {
	return &cli.Command{
		Name:     "config",
		Usage:    "Check, show and describe the configuration (.deps.yaml)",
		Commands: []*cli.Command{newConfigCheck(o), newConfigShow(o), newConfigDescribe()},
	}
}

func newConfigCheck(o *rootOpts) *cli.Command {
	return &cli.Command{
		Name:  "check",
		Usage: "Validate the configuration offline, e.g. in a pre-commit hook",
		Metadata: map[string]any{docgen.MetaExample: "  deps config check\n" +
			"  deps config check --config .github/deps.yaml"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return configCheck(ctx, cmd, o)
		},
		Flags: append(scanFlags(o, scanOffline), repositoriesFlag(&o.repositories, "check the config of")),
	}
}

// loadOffline loads the config of --dir without a session: no lookups, only the files its
// extends entries name are fetched.
func loadOffline(ctx context.Context, o *rootOpts) (*config.Config, error) {
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return nil, err
	}
	info, _ := platform.FromEnv(os.Getenv)
	fetcher := extendsFetcher(info, envToken(), root)
	defer closeFetcher(fetcher)
	cfg, err := config.Load(ctx, root, o.config, fetcher)
	if err != nil {
		return nil, err
	}
	logExtends(ctx, cfg)
	return cfg, nil
}

// configCheck loads (and so validates) the config, with the files it extends, and prints its
// lint warnings; a config error is returned as is (exit 2).
func configCheck(ctx context.Context, cmd *cli.Command, o *rootOpts) error {
	if entries := repositoryEntries(cmd, o.repositories); len(entries) > 0 {
		return offlineRepositories(ctx, cmd, o, entries, func(ro *rootOpts, name string) error { return configCheckDir(ctx, cmd, ro, name) })
	}
	return configCheckDir(ctx, cmd, o, o.dir)
}

// configCheckDir checks the config of o.dir, named display in the message about a missing
// config file (the repository for a clone of --repositories).
func configCheckDir(ctx context.Context, cmd *cli.Command, o *rootOpts, display string) error {
	cfg, err := loadOffline(ctx, o)
	if err != nil {
		return err
	}
	printLint(cmd.Root().ErrWriter, cfg)
	out := cmd.Root().Writer
	if cfg.Path() == "" {
		fmt.Fprintf(out, "no .deps.yaml or .deps.yml in %s: the built-in defaults apply\n", display)
		return nil
	}
	fmt.Fprintf(out, "%s: ok\n", cfg.Path())
	return nil
}

// offlineRepositories clones every repository of entries in turn and runs fn on a copy of o
// pointing at the clone, for the commands that read only the config (config check, config
// show). A failing repository does not stop the others.
func offlineRepositories(ctx context.Context, cmd *cli.Command, o *rootOpts, entries []string, fn func(*rootOpts, string) error) error {
	stderr := cmd.Root().ErrWriter
	src, err := newRemoteSource(ctx, o, entries, envToken(), newHTTPClient())
	if err != nil {
		return err
	}
	_, failed, err := src.eachRepository(ctx, o, stderr, nil, func(dir, name string, _ platform.Info) error {
		ro := *o
		ro.dir = dir
		return fn(&ro, name)
	})
	if err != nil {
		return err
	}
	if failed > 0 {
		return errReported
	}
	return nil
}

// printLint prints the static warnings of cfg (dead or impossible rules, see Config.Lint) to
// w as "warn    <path>: <field>: <msg>" lines, and annotates them on GitHub Actions.
func printLint(w io.Writer, cfg *config.Config) {
	p := cmp.Or(cfg.Path(), ".deps.yaml")
	for _, lw := range cfg.Lint() {
		fmt.Fprintf(w, "warn    %s: %s: %s\n", p, lw.Field, lw.Msg)
		actionsLog.configWarning(cfg, lw.Field, lw.Msg)
	}
}

// Value sources of `config show --sources`.
const (
	sourceDefault = "default"
	sourceFile    = "file"
	sourceExtends = "extends" // followed by the extended file's name
	sourceEnv     = "env"
	sourceFlag    = "flag"
)

type showOpts struct {
	sources bool
	author  runOpts // authorName and authorEmail, as for run
}

func newConfigShow(o *rootOpts) *cli.Command {
	so := &showOpts{}
	return &cli.Command{
		Name:  "show",
		Usage: "Print the effective configuration with defaults filled in",
		Metadata: map[string]any{docgen.MetaExample: "  deps config show\n" +
			"  DEPS_GIT_AUTHOR='bot <bot@example.com>' deps config show --sources"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return configShow(ctx, cmd, o, so)
		},
		Flags: append(scanFlags(o, scanOffline),
			&cli.BoolFlag{Name: "sources", Destination: &so.sources, Usage: "annotate each value with where it comes from: default, file, env or flag"},
			&cli.StringFlag{Name: "author-name", Destination: &so.author.authorName, Usage: "commit author name, as for run (when not set: $DEPS_GIT_AUTHOR, then git.author of the config, then " + defaultAuthorName + ")"},
			&cli.StringFlag{Name: "author-email", Destination: &so.author.authorEmail, Usage: "commit author email, as for run (when not set: $DEPS_GIT_AUTHOR, then git.author of the config, then " + defaultAuthorEmail + ")"},
			repositoriesFlag(&o.repositories, "show the config of"),
		),
	}
}

func configShow(ctx context.Context, cmd *cli.Command, o *rootOpts, so *showOpts) error {
	if entries := repositoryEntries(cmd, o.repositories); len(entries) > 0 {
		return offlineRepositories(ctx, cmd, o, entries, func(ro *rootOpts, _ string) error { return configShowDir(ctx, cmd, ro, so) })
	}
	return configShowDir(ctx, cmd, o, so)
}

// configShowDir prints the effective config of o.dir.
func configShowDir(ctx context.Context, cmd *cli.Command, o *rootOpts, so *showOpts) error {
	cfg, err := loadOffline(ctx, o)
	if err != nil {
		return err
	}
	s, err := reflectConfig()
	if err != nil {
		return err
	}
	var file *yaml.Node // the top-level mapping as written (extends merged); nil without a file
	if doc := cfg.Document(); doc != nil {
		file = expand(doc)
		stripComments(file)
	}
	var base yaml.Node
	if err := base.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	useFileItems(s, &base, file)
	eff := effective(s, s, &base, true)

	sources := fileSources(s, file)
	for key, src := range sources {
		top, _, _ := strings.Cut(key, ".")
		if from := cfg.KeyFile(top); src == sourceFile && from != cfg.Path() {
			sources[key] = sourceExtends + " " + from
		}
	}
	name, email, err := commitAuthor(cmd, &so.author, os.Getenv("DEPS_GIT_AUTHOR"), cfg.Git.Author)
	if err != nil {
		return err
	}
	git := lookup(eff, "git")
	setString(lookup(git, "author"), name+" <"+email+">")
	// A flag set by applyEnv (DEPS_AUTHOR_NAME, DEPS_AUTHOR_EMAIL) is set too; flagSource
	// tells it from one given on the command line.
	if os.Getenv("DEPS_GIT_AUTHOR") != "" {
		sources["git.author"] = sourceEnv
	}
	for _, fl := range []string{"author-name", "author-email"} {
		if !cmd.IsSet(fl) {
			continue
		}
		if strings.HasPrefix(flagSource(cmd, fl), "--") {
			sources["git.author"] = sourceFlag
			break
		}
		sources["git.author"] = sourceEnv
	}
	info, _ := platform.FromEnv(os.Getenv)
	host, src := actionsHost(cfg, info)
	setString(lookup(lookup(eff, "actions"), "defaultHost"), host)
	if src == "env" {
		sources["actions.defaultHost"] = sourceEnv
	}
	u, src := platform.GitLabURL(cfg.GitLabCI.URL, info, os.Getenv)
	setString(lookup(lookup(eff, "gitlabci"), "url"), u)
	if src == "env" {
		sources["gitlabci.url"] = sourceEnv
	}
	pc, src, err := platformCommitSetting(os.Getenv(platformCommitEnv), cfg.Git.PlatformCommit)
	if err != nil {
		return err
	}
	setString(lookup(git, "platformCommit"), pc)
	if src == platformCommitEnv {
		sources["git.platformCommit"] = sourceEnv
	}
	if so.sources {
		annotate(s, eff, sources)
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{eff}}
	doc.HeadComment = "effective configuration: built-in defaults (no config file)"
	if p := cfg.Path(); p != "" {
		doc.HeadComment = "effective configuration of " + p
		if len(cfg.Extends) > 0 {
			doc.HeadComment += " (extends " + strings.Join(cfg.Extends, ", ") + ")"
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if _, err := cmd.Root().Writer.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// effective returns the mapping of the object def (of the reflected schema root) with its keys
// in schema order: the values of m, defaults for unset scalars with a default. all keeps keys
// without a value (the top level and nested objects such as actions); list items and map
// values (rules[], customDatasources.<name>) keep only set keys and defaults.
func effective(root, def *jsonschema.Schema, m *yaml.Node, all bool) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode}
	for key, p := range def.Properties.All() {
		v := lookup(m, key)
		if v != nil {
			v = effectiveValue(root, p, v)
		} else if p.Default != nil {
			v = encodeNode(p.Default)
		}
		if all && v != nil && p.Type == "string" && v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			v = encodeNode("") // an unset optional string (prTitle) is empty
		}
		if v == nil {
			if !all {
				continue
			}
			v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	}
	return out
}

// effectiveValue is the effective value of property p set to v.
func effectiveValue(root, p *jsonschema.Schema, v *yaml.Node) *yaml.Node {
	c, sep, _ := docgen.Children(root, p) // the schema is ours: its references resolve
	switch {
	case c == nil:
	case sep == ".":
		return effective(root, docgen.Resolve(root, c), v, true)
	case sep == "[]." && v.Kind == yaml.SequenceNode:
		for i, item := range v.Content {
			v.Content[i] = effective(root, docgen.Resolve(root, c), item, false)
		}
	case sep == ".<name>." && v.Kind == yaml.MappingNode:
		for i := 1; i < len(v.Content); i += 2 {
			v.Content[i] = effective(root, docgen.Resolve(root, c), v.Content[i], false)
		}
	}
	if p.Items != nil || p.AdditionalProperties != nil {
		return v
	}
	if p.Default != nil && v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
		return encodeNode(p.Default)
	}
	return v
}

// nested returns the definition of the object property p holds directly (actions), or nil.
func nested(root, p *jsonschema.Schema) *jsonschema.Schema {
	if c, sep, _ := docgen.Children(root, p); c != nil && sep == "." {
		return docgen.Resolve(root, c)
	}
	return nil
}

func encodeNode(v any) *yaml.Node {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		panic(err) // schema defaults are strings, numbers, booleans and string lists
	}
	return &n
}

// fileSources marks the top-level keys of the config file, and the keys of its nested objects
// (actions.defaultHost), as set by the file; every other key is a default.
func fileSources(root *jsonschema.Schema, file *yaml.Node) map[string]string {
	sources := map[string]string{}
	for key, p := range root.Properties.All() {
		v := lookup(file, key)
		def := nested(root, p)
		if def == nil {
			sources[key] = sourceDefault
			if v != nil {
				sources[key] = sourceFile
			}
			continue
		}
		for sub := range def.Properties.All() {
			sources[key+"."+sub] = sourceDefault
			if lookup(v, sub) != nil {
				sources[key+"."+sub] = sourceFile
			}
		}
	}
	return sources
}

// annotate appends the source of each top-level key of m, and of each key of a nested object
// (actions.defaultHost), as a line comment.
func annotate(root *jsonschema.Schema, m *yaml.Node, sources map[string]string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if nested(root, root.Properties.Value(k.Value)) == nil {
			comment(k, v, sources[k.Value])
			continue
		}
		for j := 0; j+1 < len(v.Content); j += 2 {
			comment(v.Content[j], v.Content[j+1], sources[k.Value+"."+v.Content[j].Value])
		}
	}
}

// comment sets the line comment of the pair k: v. The encoder writes a key's comment only
// before a block value, so scalars and flow or empty collections carry it themselves.
func comment(k, v *yaml.Node, c string) {
	if v.Kind == yaml.ScalarNode || v.Style&yaml.FlowStyle != 0 || len(v.Content) == 0 {
		v.LineComment = c
		return
	}
	k.LineComment = c
}

// setString makes n the plain string scalar s.
func setString(n *yaml.Node, s string) {
	*n = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// useFileItems replaces the lists and maps of objects in base (rules, custom,
// customDatasources) by the config file's: decoding loses which of their fields were set.
func useFileItems(root *jsonschema.Schema, base, file *yaml.Node) {
	for key, p := range root.Properties.All() {
		fv := lookup(file, key)
		c, sep, _ := docgen.Children(root, p)
		objects := c != nil && sep != "."
		if fv == nil || !objects || fv.Kind == yaml.ScalarNode {
			continue
		}
		for i := 0; i+1 < len(base.Content); i += 2 {
			if base.Content[i].Value == key {
				base.Content[i+1] = fv
			}
		}
	}
}

// expand returns a deep copy of n with every alias replaced by a copy of its anchor's value
// (without the anchor name) and merge keys (<<: *base, <<: [*a, *b]) resolved: merged keys
// never override the mapping's own, and earlier merged mappings win over later ones.
func expand(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return expand(n.Alias)
	}
	c := *n
	c.Anchor, c.Content = "", nil
	if n.Kind != yaml.MappingNode {
		for _, e := range n.Content {
			c.Content = append(c.Content, expand(e))
		}
		return &c
	}
	var merged []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag != "!!merge" {
			c.Content = append(c.Content, expand(k), expand(v))
			continue
		}
		v = expand(v)
		ms := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			ms = v.Content
		}
		for _, m := range ms {
			if m.Kind == yaml.MappingNode {
				merged = append(merged, m.Content...)
			}
		}
	}
	for i := 0; i+1 < len(merged); i += 2 {
		if lookup(&c, merged[i].Value) == nil {
			c.Content = append(c.Content, merged[i], merged[i+1])
		}
	}
	return &c
}

func stripComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}

// lookup returns the value of key in mapping m, or nil.
func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
