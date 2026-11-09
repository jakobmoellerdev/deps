package vcpkg

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the vcpkg datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "port name (`fmt`); the dep's source is the registry's file tree at its baseline commit (`https://raw.githubusercontent.com/microsoft/vcpkg/<baseline>`, `<repository>/raw/<baseline>` off github.com), or at the new commit when the baseline moves in the same run",
	Endpoints: "`GET <source>/versions/<first letter>-/<port>.json` once per (registry commit, port); each entry's `version`, `version-semver`, `version-date` or `version-string` is a release, only of the newest entry's scheme (relaxed and semver count as one; vcpkg does not order across schemes), " +
		"`#<port-version>` appended when the port version is not 0 (`1.3.1#2`, as `version>=` writes it). A 404 fails the dep: the port is not in the registry at the baseline. " +
		"For release notes, `GET <source>/ports/<port>/vcpkg.json` once per (registry commit, port): its `homepage` is the source repository when it is on github.com, gitlab.com or codeberg.org",
	Auth:         "none",
	ReleaseTimes: "none (the versions database records no dates)",
}
