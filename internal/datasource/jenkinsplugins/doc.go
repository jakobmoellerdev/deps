package jenkinsplugins

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the jenkins-plugins datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "the plugin's artifact ID (`git`, `workflow-aggregator`)",
	Endpoints: "`GET <DEPS_JENKINS_PLUGINS_URL or https://updates.jenkins.io>/current/plugin-versions.json` once per run (about 25 MB for the Jenkins update center), read as a stream and keeping only the plugins the run looks up; every member of a plugin's object is a version, in the file's order. " +
		"A plugin the file lacks (its distribution stopped) skips the dep (`plugin not found on <site>`). " +
		"Source repository (release notes): the plugin's `scm` in `GET <site>/current/update-center.actual.json`, once per run and only when a PR is opened",
	Auth:         "none",
	ReleaseTimes: "each version's `releaseTimestamp`, else its `buildDate`",
}
