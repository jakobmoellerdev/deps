package bitrise

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the bitrise datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "step id (`script`, `git-clone`); the step library is the dep's Source, `https://github.com/bitrise-io/bitrise-steplib.git` by default",
	Endpoints: "the GitHub contents API of the step library (github.com or GitHub Enterprise Server): `GET /repos/<owner>/<repo>/contents/steps/<id>` once per step lists the version directories (Renovate's endpoint); " +
		"`steps/<id>/<version>/step.yml` only for the versions whose release time or source repository is asked for. " +
		"After the listed versions a derived `X.Y` and `X` per release line, so partial versions (`script@1`) are updated with their precision",
	Auth:         "the GitHub token of the step library's host (as for `github-releases`)",
	ReleaseTimes: "`published_at` of the version's `step.yml`; a derived `X.Y`/`X` has the time of the line's first release",
}
