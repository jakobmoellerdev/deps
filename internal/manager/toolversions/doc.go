package toolversions

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// nodeSkips are the skip reasons of a Node.js version (nvm, nodenv).
var nodeSkips = []refdoc.Skip{
	{Reason: "non-version value", When: "empty or a word or alias such as `lts/*`, `lts/iron`, `node`, `stable`, `system`"},
	{Reason: "unsupported range", When: "anything else that is not `[v]X[.Y[.Z]]`, such as `>=20` or `20.x`"},
}

// toolSkips are the skip reasons of a tool version (asdf, mise).
var toolSkips = []refdoc.Skip{
	{Reason: "unsupported tool", When: "a tool that is not in the tool table (or, for mise, an unsupported backend)"},
	{Reason: "non-version value", When: "empty or a word such as `latest`, `system`, `lts`"},
	{Reason: "git ref", When: "`ref:…`"},
	{Reason: "local path", When: "`path:…`"},
	{Reason: "unsupported range", When: "`prefix:…` or anything else that is not a version"},
	{Reason: "partial version", When: "`X` or `X.Y` for a tool whose datasource lists only full versions (github-releases, npm; `X` for go)"},
}

var nodeVersionItems = []string{
	"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style.",
	"A partial `20` or `v20.11` uses `tag` versioning and keeps its precision (`20` → `22`); a leading `v` stays outside the replaced token.",
	"Only the version token is replaced; comments and the rest of the file are kept.",
}

// DocNvm is the reference documentation of the nvm manager (docs/reference/managers.md).
var DocNvm = refdoc.Manager{
	Name:    NameNvm,
	Summary: "Updates the Node.js version in `.nvmrc`: the first token of the first line that is neither blank nor a `#` comment.",
	Files:   []string{"any `.nvmrc`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNode, What: "the Node.js version (name `node`)", Datasource: "`node-version`", Versioning: "`semver`; partial versions `tag`"},
	},
	Skips:    nodeSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: nodeVersionItems}},
}

// DocNodenv is the reference documentation of the nodenv manager (docs/reference/managers.md).
var DocNodenv = refdoc.Manager{
	Name:    NameNodenv,
	Summary: "Updates the Node.js version in `.node-version`: the first token of the first line that is neither blank nor a `#` comment.",
	Files:   []string{"any `.node-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNode, What: "the Node.js version (name `node`)", Datasource: "`node-version`", Versioning: "`semver`; partial versions `tag`"},
	},
	Skips:    nodeSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: nodeVersionItems}},
}

// DocRubyVersion is the reference documentation of the ruby-version manager
// (docs/reference/managers.md).
var DocRubyVersion = refdoc.Manager{
	Name: NameRubyVersion,
	Summary: "Updates the Ruby version in `.ruby-version` (rbenv, chruby, rvm, asdf, mise, Bundler's `ruby file: \".ruby-version\"`): the first token of the first line that is neither blank nor a `#` comment. " +
		"It shares the built-in `ruby` group with the Gemfile `ruby` directive and asdf/mise `ruby` (see [bundler](#bundler)), so a Ruby bump is one PR.",
	Files: []string{"any `.ruby-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRuby, What: "the Ruby version (name `ruby`), optionally with the `ruby-` prefix (`ruby-3.4.7`)", Datasource: "`ruby-version`", Versioning: "`ruby`; partial versions `tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-version value", When: "empty, another engine (`jruby-9.4.8.0`, `truffleruby-24.1.0`) or a word such as `system`"},
		{Reason: "unsupported range", When: "anything else that is not `[ruby-]X[.Y[.Z[-pre]]]`, such as `3.4.7@gemset`"},
	},
	Sections: []refdoc.Section{{Title: "Versions", Items: rubyVersionItems}},
}

var rubyVersionItems = []string{
	"A full `X.Y.Z[-pre]` uses `ruby` versioning (Gem::Version order); previews such as `3.5.0-preview1` are only proposed from a preview.",
	"A partial `3` or `3.4` uses `tag` versioning and keeps its precision (`3.4` → `4.0`).",
	"The `ruby-` prefix stays outside the replaced token; comments and the rest of the file are kept.",
}

var toolVersionItems = []string{
	"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style; go-toolchain tools use `tag`, `ruby` uses `ruby` (as in `.ruby-version`).",
	"A partial version uses `tag` versioning and keeps its precision for node (`20`, `20.11`), go (`1.27`) and ruby (`3.4`); other tools skip it.",
	"`maven` and `gradle` use `maven` versioning, under which `8.11` and `3.9` are full versions.",
	"`ruby` shares the built-in `ruby` group with the Gemfile `ruby` directive and `.ruby-version` (see [bundler](#bundler)).",
	"Only the version token is replaced; quotes, comments and the rest of the file are kept.",
}

// DocAsdf is the reference documentation of the asdf manager (docs/reference/managers.md).
var DocAsdf = refdoc.Manager{
	Name:    NameAsdf,
	Summary: "Updates `<tool> <version> [fallback versions…]` lines of `.tool-versions`. Only the first version of a line is a dep; `#` starts a comment.",
	Files:   []string{"any `.tool-versions`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTool, What: "a tool line; the dep name comes from the tool table", Datasource: "per tool (see Tools)", Versioning: "`semver`, `tag` or `maven`"},
	},
	Skips:    toolSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: toolVersionItems}, toolsSection()},
}

// DocMise is the reference documentation of the mise manager (docs/reference/managers.md).
var DocMise = refdoc.Manager{
	Name: NameMise,
	Summary: "Updates the `[tools]` table of mise config files. The file is scanned as TOML without decoding it, " +
		"so only the version bytes change.",
	Files: []string{"`mise.toml`, `.mise.toml`", "`mise/config.toml`, `.mise/config.toml`", "`.config/mise.toml`, `.config/mise/config.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTool, What: "a `[tools]` entry; the dep name comes from the tool table or the backend", Datasource: "per tool (see Tools)", Versioning: "`semver`, `tag` or `maven`"},
	},
	Skips: append(slices.Clip(toolSkips), refdoc.Skip{Reason: "cannot locate value", When: "a string with escapes or a multi-line string"}),
	Sections: []refdoc.Section{
		{Title: "Extracted", Items: []string{
			"`tool = \"v\"`, `tool = [\"v\", …]` (the first version only) and `tool = { version = \"v\", … }` in `[tools]`.",
			"Dotted keys (`tools.node = \"22\"`, `node.version = \"22\"` in `[tools]`) and `[tools.<tool>]` tables with a `version` key.",
			"Keys with a backend: `core:<tool>` uses the tool table; `github:<owner>/<repo>` and `ubi:<owner>/<repo>[…]` use `github-releases`; `npm:<package>` uses `npm`. Other backends (`aqua:`, `cargo:`, `go:`, `pipx:`, …) are skipped as `unsupported tool`.",
		}},
		{Title: "Versions", Items: toolVersionItems},
		toolsSection(),
	},
}

// toolsSection documents the tool table, sorted by tool name.
func toolsSection() refdoc.Section {
	var b strings.Builder
	b.WriteString("| Tool | Datasource | Name | Release tags |\n|---|---|---|---|\n")
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		t := tools[name]
		tags := ""
		if t.extract != "" {
			tags = "extractVersion `" + t.extract + "`"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n", name, t.datasource, t.name, tags)
	}
	return refdoc.Section{
		Title: "Tools",
		Body:  strings.TrimSuffix(b.String(), "\n"),
		Items: []string{
			"github-releases tags are the version with or without a leading `v`, unless the table lists an extractVersion; a rule's `extractVersion` overrides it.",
			"Tools that are not listed are skipped as `unsupported tool`.",
		},
	}
}
