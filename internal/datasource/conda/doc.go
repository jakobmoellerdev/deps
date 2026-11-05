package conda

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the conda datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "package name as on the channel (`numpy`, `r-base`); the dep's source is its whitespace-separated anaconda.org channels in lookup order (\"\" = `conda-forge`). " +
		"A custom dep may name its channel as Renovate's packageName does: `conda-forge/numpy`",
	Endpoints: "`GET <DEPS_CONDA_URL or https://api.anaconda.org>/package/<channel>/<name>`, once per (channel, package); " +
		"a version is a release when one of its files is under the `main` label and not marked `broken` (files only under `rc`, `dev` or migration labels are not installed by default), the builds of every platform merged; " +
		"the first channel that has the package answers (conda's strict channel priority), none → `conda package <name> not found in <channels>`. " +
		"Source repository (release notes): the package's `dev_url`, an http(s) URL, from the same response",
	Auth:         "none: public anaconda.org channels only",
	ReleaseTimes: "earliest `upload_time` of the version's main files; a version cut to a lower precision (`2.3` for 2.3.0) gets the time of the release it equals",
}
