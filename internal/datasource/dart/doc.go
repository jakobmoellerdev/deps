package dart

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the dart datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "the package name as in pubspec.yaml (`http`); the pub manager sets the repository of a `hosted` dependency",
	Endpoints: "`GET <repository>/api/packages/<name>` once per repository and package: the `hosted` URL of the dependency, else `PUB_HOSTED_URL` (as for the pub client), else `https://pub.dev`; retracted versions are dropped. " +
		"Source repository (release notes): the `repository` of the newest version's pubspec, else its `homepage`",
	Auth:         "none",
	ReleaseTimes: "the `published` time of each version",
}
