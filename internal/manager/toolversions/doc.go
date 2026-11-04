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
	{Reason: "unsupported tag prefix", When: "a known tool whose release tags carry a prefix other than `v` (see the tool table)"},
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

var toolVersionItems = []string{
	"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style; go-toolchain tools use `tag`.",
	"A partial version uses `tag` versioning and keeps its precision for node (`20`, `20.11`) and go (`1.27`); other tools skip it.",
	"Only the version token is replaced; quotes, comments and the rest of the file are kept.",
}

// DocAsdf is the reference documentation of the asdf manager (docs/reference/managers.md).
var DocAsdf = refdoc.Manager{
	Name:    NameAsdf,
	Summary: "Updates `<tool> <version> [fallback versions…]` lines of `.tool-versions`. Only the first version of a line is a dep; `#` starts a comment.",
	Files:   []string{"any `.tool-versions`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTool, What: "a tool line; the dep name comes from the tool table", Datasource: "per tool (see Tools)", Versioning: "`semver` or `tag`"},
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
		{Name: DepTypeTool, What: "a `[tools]` entry; the dep name comes from the tool table or the backend", Datasource: "per tool (see Tools)", Versioning: "`semver` or `tag`"},
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
	b.WriteString("| Tool | Datasource | Name | Skip |\n|---|---|---|---|\n")
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		t := tools[name]
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n", name, t.datasource, t.name, t.skip)
	}
	return refdoc.Section{
		Title: "Tools",
		Body:  strings.TrimSuffix(b.String(), "\n"),
		Items: []string{
			"github-releases repositories are listed when their release tags are the version with or without a leading `v`.",
			"Tools that are not listed are skipped as `unsupported tool`.",
		},
	}
}
