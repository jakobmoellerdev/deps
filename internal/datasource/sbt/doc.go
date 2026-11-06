package sbtds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// PackageDoc is the reference documentation of the sbt-package datasource.
var PackageDoc = refdoc.Datasource{
	Name: PackageName,
	NameForm: "`groupId:artifactId` with the artifact's Scala suffix (`org.typelevel:cats-core_2.13`); " +
		"the repositories are the dep's `[ivy=]<url>/` list (Maven Central when empty), `ivy=` marking an Ivy-layout repository",
	Endpoints: "Maven repositories: as the `maven` datasource (`GET <repository>/<group/path>/<artifactId>/maven-metadata.xml`, `DEPS_MAVEN_CENTRAL_URL` applies); " +
		"Ivy repositories: the directory listing `GET <repository>/<groupId>/<artifactId>/`; versions merged, a 404 contributes nothing. " +
		"A Scala artifact no repository has (a `%%%` library published only for Scala.js or Scala Native) is looked up as `<artifact>_sjs1_<scala>`, then `_native0.5_`, `_native0.4_`",
	Auth:         "none",
	ReleaseTimes: "as the `maven` datasource (POM `Last-Modified`) for versions a Maven repository lists; none for Ivy-only versions",
}

// PluginDoc is the reference documentation of the sbt-plugin datasource.
var PluginDoc = refdoc.Datasource{
	Name: PluginName,
	NameForm: "`groupId:artifactId` of an sbt plugin, looked up as the artifact published for the build's sbt version " +
		"(`project/build.properties`): `<artifactId>_2.12_1.0` for sbt 1.x, `<artifactId>_sbt2_3` for sbt 2.x, `<artifactId>_2.10_0.13` for sbt 0.13; repositories as for `sbt-package`",
	Endpoints: "Maven repositories: as the `maven` datasource with the cross-versioned artifact; " +
		"Ivy repositories (by default `" + PluginReleases + "`, which redirects to scala.jfrog.io): the directory listing `GET <repository>/<groupId>/<artifactId>/scala_2.12/sbt_1.0/` (sbt 2.x plugins are only published to Maven repositories); versions merged",
	Auth:         "none",
	ReleaseTimes: "as the `maven` datasource for versions a Maven repository lists; none for Ivy-only versions",
}
