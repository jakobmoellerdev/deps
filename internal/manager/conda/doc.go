package conda

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the conda manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the packages of conda environment files: the match specs of `dependencies`, looked up on the file's anaconda.org `channels`, and the requirements of the nested `pip:` list, looked up on PyPI (Dependabot's `conda` ecosystem; Renovate has no conda manager). " +
		"The file is parsed as YAML with byte spans; only the version spec of an entry is rewritten. A `conda-lock.yml` next to the file is read for locked versions and refreshed with `conda-lock` in `apply` and `run` (see Lockfile below).",
	Files: []string{"`environment.yml`, `environment.yaml` (any directory)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`dependencies` match specs: `numpy=1.26` (fuzzy: any 1.26.x), `numpy==1.26.4`, `numpy>=1.26,<2`, `numpy 1.26.*`, `numpy >=1.26`, with a channel prefix `conda-forge::numpy=1.26`", Versioning: "`conda`", Datasources: []string{"conda"}},
		{Name: DepTypePip, What: "the PEP 508 requirements of a `- pip:` list in `dependencies` (`requests>=2.31`, `black==24.4.2`)", Datasource: "`pypi` (the list's `--index-url`, `--extra-index-url`); `git-tags` for `git+https://…@<tag>`", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: pyreq.SkipUnconstrained, When: "a package without version spec (`numpy`, `numpy *`) or with only exclusions (`numpy!=1.26.0`)"},
		{Reason: SkipBuild, When: "a match spec with a build string (`numpy=1.26.4=py312h8753938_0`, `numpy 1.26.4 py312_0`): it pins a build of one version (as Dependabot, deps does not update it)"},
		{Reason: SkipMatchSpec, When: "the bracket form (`numpy[version='>=1.26']`) or `|` alternatives (`numpy=1.25|1.26`)"},
		{Reason: pyreq.SkipRange, When: "a version spec conda's VersionSpec does not read (`numpy>=1.26.*`, `numpy=latest`), or a pip requirement that is no PEP 508 requirement"},
		{Reason: SkipChannel + " <channel>", When: "a `<channel>::` prefix, or a `channels` list without any entry, that the anaconda.org API does not serve: a URL of another host (`https://prefix.dev/conda-forge`), a local or `file://` channel"},
		{Reason: pyreq.SkipLocate, When: "a value whose YAML bytes differ from its text (escapes, block scalars)"},
		{Reason: pyreq.SkipGit, When: "a pip requirement that is a VCS URL (`git+https://…`, also as `name @ git+https://…`) not pinned to a version tag of an http(s) repository"},
		{Reason: pyreq.SkipLocal, When: "a pip requirement that is a path (`./pkg`, `file:…`)"},
		{Reason: pyreq.SkipURL, When: "a pip requirement that is another URL (`https://…/pkg.whl`, `name @ https://…`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Channels", Items: []string{
			"A package is looked up on the `channels` of the file in order; the first channel that has it answers, as with conda's strict channel priority. Without `channels`, on `conda-forge` (Dependabot's and Renovate's default).",
			"A `<channel>::` prefix (`pytorch::pytorch`, `conda-forge/linux-64::numpy`) looks the package up on that channel only.",
			"Channel names keep their name (`/label/<x>` and `/<subdir>` suffixes dropped); `defaults` is the anaconda.org `anaconda` channel (a mirror of Anaconda's main repository), and so is `https://repo.anaconda.com/pkgs/main`; `https://conda.anaconda.org/<channel>` URLs name `<channel>`. A private channel's token URL `https://conda.anaconda.org/t/<token>/<channel>` (the token may be a `${VAR}` reference) is looked up with that token; other channels with `DEPS_CONDA_TOKEN` (see [Datasources](#datasources)). `nodefaults`, URLs of other hosts and local channels are not looked up.",
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
			"Without a `conda-lock.yml` next to the file, under `auto` (`update-lockfile`) and `replace` versions the spec admits are not proposed: `numpy>=1.26` never gets an update, `numpy=1.26` only once 1.27 is out. With one, the locked version is the dep's current version, and under `auto` (`update-lockfile`) versions the spec admits are lockfile-only updates (`, lockfile` in the report; the environment file is unchanged), as for the Python managers: `numpy>=1.26` locked at 1.26.4 moves the lock to the newest numpy.",
			"Rewrites: an exact spec (`==1.26.4`, `1.26.4`) or a fuzzy one (`=1.26`, `1.26.*`) takes the new version, a fuzzy one at its own precision (`=1.26` → `=2.3`); any other spec is rewritten with the pep440 rules of the Python managers (`>=1.26,<2` → `>=2.3,<3`), as Renovate's conda versioning delegates to pep440, when its versions are PEP 440 versions, else the update is not proposed. `pin` writes `==<version>`.",
		}},
		{Title: "pip requirements", Items: []string{
			"The `pip:` list holds requirements as a requirements file does: they are updated like `pip_requirements` deps (PyPI, `pep440`, the specifier rules of the Python managers) and get PyPI security updates from OSV.",
			pyreq.GitURLItem,
			"`--index-url`/`-i` and `--extra-index-url` entries of the list set the index of its requirements; other options (`-r requirements.txt`, `-e .`, `-c`) are no deps.",
			"The `python` spec of `dependencies` (`python=3.11` → `==3.11.*`) is the requirements' Python constraint: releases that do not support it are dropped (see the `pypi` datasource).",
		}},
		{Title: "Lockfile (`apply`, `run`)", Items: []string{
			"The lockfile of an environment file is the `conda-lock.yml` next to it (conda-lock's unified format, `version: 1`). Before resolution its `package` entries are read: a conda package or pip requirement gets the version locked for it (pip names compared PEP 503-normalized); a package locked to different versions on different platforms gets the lowest. An unreadable lockfile is reported once and its deps are bumped.",
			"An update runs `conda-lock lock -f <source>… --lockfile conda-lock.yml -p <platform>… --update <name>…` in a temporary copy of the directory (environment file and lockfile copied, every other entry but `.git` symlinked), with the sources and platforms the lockfile's `metadata` records (the patched file when it records none), and `--update` naming every updated package, manifest and lockfile-only updates alike: those packages move to the newest versions the specs admit on every platform, every other package keeps its locked version, and the `content_hash` is recomputed. conda-lock does not take a version for `--update`, so a version held back by `minimumReleaseAge` can still be locked when it is the newest the spec admits, and a version the solver cannot install with the rest of the environment is replaced by the newest one it can.",
			"`conda-lock` (and the conda, mamba or micromamba it solves with; conda-lock installs micromamba itself when none is found) must be on `PATH`, e.g. `pipx install conda-lock`. Without it, the environment file fails with `conda-lock not found (needed to update conda-lock.yml)`; a failed command reports `conda-lock lock in <dir>: <last 20 output lines>` and the file's changes are dropped. `plan` does not run conda-lock, and prints `note    conda-lock.yml changes are computed by apply/run`. Other lockfile names (`--lockfile`) and the per-platform explicit lockfiles (`--kind explicit`) are not maintained.",
		}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `conda-lock.yml` next to a scanned environment file is re-solved with `conda-lock lock` (same options, without `--update`), which moves every locked package, transitive ones included, to the newest version the specs admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `conda-lock.yml` files; `minimumReleaseAge` does not apply to it."},
	},
}
