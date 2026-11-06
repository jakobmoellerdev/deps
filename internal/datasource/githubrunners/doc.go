package githubrunners

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the github-runners datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "the runner name of a GitHub-hosted runner label: `ubuntu`, `macos`, `windows`, `xcode`",
	Endpoints:    "none: a table of the [runner images](" + SourceURL + ") in deps, like Renovate's; versions with a suffix (`24.04-arm`, `15-large`) only update within that suffix",
	Auth:         "none",
	ReleaseTimes: "the day GitHub announced the image generally available",
	Notes: []refdoc.Section{{Title: "Deprecated runner images", Items: []string{
		"Images GitHub deprecated or removed (`ubuntu-20.04`, `macos-13`, `windows-2019`, …) are never update targets.",
		"A dep on one gets a `deprecated` warning on the dependency dashboard and in its update PR.",
		"New images and deprecations arrive with deps releases.",
	}}},
}
