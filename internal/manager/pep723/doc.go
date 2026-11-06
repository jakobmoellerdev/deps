package pep723

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pep723 manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the PEP 508 requirement strings of PEP 723 inline script metadata: the TOML in the `# /// script` … `# ///` comment block of a Python script (as `uv run` and `pipx run` read it). " +
		"The block's lines are parsed without their `# ` prefix at their own byte offsets, so only the specifier's versions inside the TOML string are rewritten. " +
		"`requires-python` is no dep but drops the releases that do not support all of it (see the `pypi` datasource). A script has no lockfile deps reads.",
	Files: []string{"any `*.py` file with a `# /// script` block (Renovate's pep723 matches no file unless `managerFilePatterns` are set; deps reads every `.py` file and extracts nothing from files without block)"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "every string of the block's `dependencies` array (updates use commit type `fix`)", Versioning: "`pep440`", Datasources: []string{"pypi"}},
	},
	Skips: append(append([]refdoc.Skip{}, pyreq.SpecifierSkips...),
		refdoc.Skip{Reason: pyreq.SkipURL, When: "`name @ https://…`"},
		refdoc.Skip{Reason: pyreq.SkipGit, When: "`name @ git+…`"},
		refdoc.Skip{Reason: pyreq.SkipLocal, When: "`name @ file://…`"},
	),
	Sections: []refdoc.Section{
		{Title: "Block", Items: []string{
			"The block starts at a line that is exactly `# /// script` and runs over the following comment lines (`#` alone or `# ` followed by text) up to the last line among them that is exactly `# ///` (PEP 723's reference regular expression); a block without that closing line is ignored. Only the first `script` block of a file is read.",
			"A block whose TOML does not parse is an extraction error of the file.",
		}},
		pyreq.SpecifierSection, pyreq.RangeSection,
	},
}
