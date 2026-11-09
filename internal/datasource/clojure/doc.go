package clojure

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the clojure datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "`groupId:artifactId`; the repositories are the dep's `<url>/` list, " +
		"Clojars (`" + Clojars + "`, `DEPS_CLOJARS_URL`) then Maven Central when empty",
	Endpoints: "as the `maven` datasource: `GET <repository>/<group/path>/<artifactId>/maven-metadata.xml` per repository, " +
		"versions merged in repository order; a 404 contributes nothing; `DEPS_MAVEN_CENTRAL_URL` replaces Maven Central",
	Auth:         "none",
	ReleaseTimes: "as the `maven` datasource (POM `Last-Modified`)",
}
