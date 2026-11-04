package piprequirements

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pip_requirements manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the PEP 508 requirements of pip requirements files. Lines ending in `\\` are joined and comments are dropped. " +
		"Option lines (`-r`, `-c`, `-e`, `--extra-index-url`, `--find-links`, …) yield no deps; included files are scanned when their own name matches. " +
		"An `--index-url`/`-i` line anywhere in the file becomes the index of every requirement in it, and every `--extra-index-url` line an extra index whose releases are merged in (see the `pypi` datasource). There is no lockfile: under `auto` and `update-lockfile` only versions outside a requirement's range are proposed (Renovate's `replace`). " +
		"Requirements with `--hash` options are updated too: `apply`/`run` regenerate a pip-compile or `uv pip compile` output file " +
		"(recognized by its header) by re-running the command of its header in a temporary copy with `--upgrade-package <name>==<version>` " +
		"per updated requirement, when the header's input files exist (pip-compile runs from PATH, else through `uvx --from pip-tools`); " +
		"in any other file the `--hash` options of each updated requirement are replaced by the sha256 digests of the new version's files on its index, " +
		"one option per digest in pip-compile's order and line style. `plan` shows the version change only.",
	Files: []string{"`requirements.txt`, `requirements-dev.txt`, `requirements.dev.txt`, `dev-requirements.txt`, `requirements.pip` (Renovate's `(^|/)[\\w-]*requirements([-.]\\w+)?\\.(txt|pip)$`)"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "every requirement line (`name[extras] specifier ; marker`)", Datasource: "`pypi`", Versioning: "`pep440`"},
	},
	Skips: slices.Concat(pyreq.SpecifierSkips, []refdoc.Skip{
		{Reason: pyreq.SkipGit, When: "`git+…`, `hg+…`, `svn+…`, `bzr+…` lines or `name @ git+…`"},
		{Reason: pyreq.SkipURL, When: "a URL line or `name @ https://…`"},
		{Reason: pyreq.SkipLocal, When: "a local path, archive or wheel (`./pkg`, `/abs`, `x.whl`, `x.tar.gz`, `file:`)"},
	}),
	Sections: []refdoc.Section{pyreq.SpecifierSection, pyreq.RangeSection},
}
