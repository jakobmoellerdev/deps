package conda

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the conda manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the packages of conda environment files: the match specs of `dependencies`, looked up on the file's anaconda.org `channels`, and the requirements of the nested `pip:` list, looked up on PyPI (Dependabot's `conda` ecosystem; Renovate has no conda manager). " +
		"The file is parsed as YAML with byte spans; only the version spec of an entry is rewritten. There is no lockfile (`conda-lock.yml` is not maintained).",
	Files: []string{"`environment.yml`, `environment.yaml` (any directory)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`dependencies` match specs: `numpy=1.26` (fuzzy: any 1.26.x), `numpy==1.26.4`, `numpy>=1.26,<2`, `numpy 1.26.*`, `numpy >=1.26`, with a channel prefix `conda-forge::numpy=1.26`", Versioning: "`conda`", Datasources: []string{"conda"}},
		{Name: DepTypePip, What: "the PEP 508 requirements of a `- pip:` list in `dependencies` (`requests>=2.31`, `black==24.4.2`)", Datasource: "`pypi` (the list's `--index-url`, `--extra-index-url`)", Versioning: "`pep440`", Datasources: []string{"pypi"}},
	},
	Skips: []refdoc.Skip{
		{Reason: pyreq.SkipUnconstrained, When: "a package without version spec (`numpy`, `numpy *`) or with only exclusions (`numpy!=1.26.0`)"},
		{Reason: SkipBuild, When: "a match spec with a build string (`numpy=1.26.4=py312h8753938_0`, `numpy 1.26.4 py312_0`): it pins a build of one version (as Dependabot, deps does not update it)"},
		{Reason: SkipMatchSpec, When: "the bracket form (`numpy[version='>=1.26']`) or `|` alternatives (`numpy=1.25|1.26`)"},
		{Reason: pyreq.SkipRange, When: "a version spec conda's VersionSpec does not read (`numpy>=1.26.*`, `numpy=latest`), or a pip requirement that is no PEP 508 requirement"},
		{Reason: SkipChannel + " <channel>", When: "a `<channel>::` prefix, or a `channels` list without any entry, that the anaconda.org API does not serve: a URL of another host (`https://prefix.dev/conda-forge`), a local or `file://` channel"},
		{Reason: pyreq.SkipLocate, When: "a value whose YAML bytes differ from its text (escapes, block scalars)"},
		{Reason: pyreq.SkipGit, When: "a pip requirement that is a VCS URL (`git+https://…`), also as `name @ git+https://…`"},
		{Reason: pyreq.SkipLocal, When: "a pip requirement that is a path (`./pkg`, `file:…`)"},
		{Reason: pyreq.SkipURL, When: "a pip requirement that is another URL (`https://…/pkg.whl`, `name @ https://…`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Channels", Items: []string{
			"A package is looked up on the `channels` of the file in order; the first channel that has it answers, as with conda's strict channel priority. Without `channels`, on `conda-forge` (Dependabot's and Renovate's default).",
			"A `<channel>::` prefix (`pytorch::pytorch`, `conda-forge/linux-64::numpy`) looks the package up on that channel only.",
			"Channel names keep their name (`/label/<x>` and `/<subdir>` suffixes dropped); `defaults` is the anaconda.org `anaconda` channel (a mirror of Anaconda's main repository), and so is `https://repo.anaconda.com/pkgs/main`; `https://conda.anaconda.org/<channel>` URLs name `<channel>`. `nodefaults`, URLs of other hosts and local channels are not looked up.",
		}},
		{Title: "Versions (`conda` versioning)", Items: []string{
			"Versions are compared with conda's VersionOrder: `[<epoch>!]<version>[+<local>]`, segments separated by `.` or `_`, each a run of numbers and words; a word sorts below a number (`1.1.1w` < `1.1.1`, `1.0rc1` < `1.0`), `dev` below every other word, `post` above every number. Dependabot and Renovate both use conda's ordering for conda packages (Renovate's `conda` versioning, Dependabot's Conda::Version), not PEP 440, which rejects versions such as `1.1.1w`, `2024a` or `9e`.",
			"A version with a `dev`, `rc`, `alpha`, `beta`, `pre` or `preview` word, or an `a`, `b` or `c` followed by a number (`2.0b1`), is a prerelease, proposed only from a prerelease. Other words are part of the release (`1.1.1w`, `2024a`, `9e`).",
			"The first segment decides major updates, the second minor ones, anything after patch ones.",
			"As for pep440, a candidate with non-zero segments beyond the spec's precision is skipped (`precision`) and others are cut to it: `=1.26` sees 2.3.0 as `2.3`, not 2.3.4.",
		}},
		{Title: "Version specs (`rangeStrategy`)", Items: []string{
			"The dep's version is the spec's anchor: the first clause that is not `<`, `<=` or `!=`, else the first upper bound.",
			"Matching follows conda's VersionSpec: clauses joined by `,` must all hold; `=1.26`, `1.26.*` and `1.26*` match the versions whose segments start with 1.26 (1.26, 1.26.0, 1.26.4, not 1.260); a bare version or `==` matches exactly (`1.26` = `1.26.0`); `~=1.4.5` is `>=1.4.5,1.4.*`.",
			"There is no lockfile, so under `auto` (`update-lockfile`) and `replace` versions the spec admits are not proposed: `numpy>=1.26` never gets an update, `numpy=1.26` only once 1.27 is out.",
			"Rewrites: an exact spec (`==1.26.4`, `1.26.4`) or a fuzzy one (`=1.26`, `1.26.*`) takes the new version, a fuzzy one at its own precision (`=1.26` → `=2.3`); any other spec is rewritten with the pep440 rules of the Python managers (`>=1.26,<2` → `>=2.3,<3`), as Renovate's conda versioning delegates to pep440, when its versions are PEP 440 versions, else the update is not proposed. `pin` writes `==<version>`.",
		}},
		{Title: "pip requirements", Items: []string{
			"The `pip:` list holds requirements as a requirements file does: they are updated like `pip_requirements` deps (PyPI, `pep440`, the specifier rules of the Python managers) and get PyPI security updates from OSV.",
			"`--index-url`/`-i` and `--extra-index-url` entries of the list set the index of its requirements; other options (`-r requirements.txt`, `-e .`, `-c`) are no deps.",
			"The `python` spec of `dependencies` (`python=3.11` → `==3.11.*`) is the requirements' Python constraint: releases that do not support it are dropped (see the `pypi` datasource).",
		}},
	},
}
