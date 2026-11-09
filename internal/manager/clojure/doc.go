package clojure

import (
	clojureds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/clojure"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// leinWhat describes the dependency vectors of the Leiningen dep types.
const leinWhat = "dependency vectors `[group/artifact \"1.2.3\" & options]` (`[artifact \"1.2.3\"]` is `artifact:artifact`) of every "

// versionItems are the version forms both managers read.
var versionItems = []string{
	"A version is a string: a plain version, or a Maven range (`[1.0,2.0)`) updated as the maven manager does (see its Version ranges); an exact `[1.0]` is updated like a plain version.",
	"Libs are looked up with the `clojure` datasource: Clojars then Maven Central, unless the file names repositories (below). Versions are compared with the `maven` versioning.",
}

// LeiningenDoc is the reference documentation of the leiningen manager.
var LeiningenDoc = refdoc.Manager{
	Name: LeiningenName,
	Summary: "Updates the dependencies of Leiningen's `project.clj` as Renovate's leiningen manager: the dependency vectors of `:dependencies`, `:managed-dependencies`, `:plugins` and `:pom-plugins` anywhere in the `defproject` form (also in `:profiles`), and `:parent-project` `:coords`. " +
		"The file is read with a small Clojure reader (comments, `#_` discards, metadata, reader conditionals); only the version bytes are rewritten.",
	Files: []string{"`project.clj` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: leinWhat + "`:dependencies` vector", Versioning: "`maven`", Datasources: []string{clojureds.Name}},
		{Name: DepTypeManaged, What: leinWhat + "`:managed-dependencies` vector; vectors without a version (managed elsewhere) are none", Versioning: "`maven`", Datasources: []string{clojureds.Name}},
		{Name: DepTypePlugins, What: leinWhat + "`:plugins` vector (looked up in `:plugin-repositories` when set)", Versioning: "`maven`", Datasources: []string{clojureds.Name}},
		{Name: DepTypePomPlugins, What: leinWhat + "`:pom-plugins` vector (Maven plugins of the generated pom)", Versioning: "`maven`", Datasources: []string{clojureds.Name}},
		{Name: DepTypeParent, What: "`:parent-project {:coords [group/artifact \"1.2.3\"]}` (lein-parent)", Versioning: "`maven`", Datasources: []string{clojureds.Name}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unresolved version ~<name>", When: "a `~name` version that no top-level `(def name \"…\")` of the file defines as a string"},
		{Reason: skipNonLiteral, When: "a version computed by an unquoted expression (`~(str …)`)"},
		{Reason: skipLocate, When: "a version string with escapes"},
		{Reason: skipRange, When: "a range Maven rejects (`[2.0,1.0)`, `[1.0`)"},
		{Reason: skipDynamic, When: "`\"RELEASE\"` or `\"LATEST\"`"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: append([]string{
			"A `~name` version (Leiningen's unquote) is read from the top-level `(def name \"1.2.3\")` of the file; the update edits the `def`, and deps sharing it are grouped into one PR.",
		}, versionItems...)},
		{Title: "Repositories", Items: []string{
			"The http(s) URLs of the `defproject`'s `:repositories` (`[[\"name\" \"url\"]]`, `[[\"name\" {:url \"url\"}]]`, maps) follow Leiningen's defaults Maven Central and Clojars; `^:replace` drops the defaults. " +
				"`:plugin-repositories`, when set, replace `:repositories` for `:plugins`. Credentials (`:creds`, `:username`) are not used.",
		}},
	},
}

// ednWhat describes the libs of the deps-edn dep types.
const ednWhat = "libs `group/artifact {:mvn/version \"1.2.3\"}` (`artifact` = `artifact:artifact`, a `$classifier` dropped) and git libs (`:git/url`, `:git/tag`, `:git/sha`) of "

// The datasource and versioning columns of the deps-edn dep types.
const (
	ednDatasource = "`clojure` (Maven), `git-tags` (git tag), `git-refs` (git sha alone)"
	ednVersioning = "`maven` (Maven), `tag` (git)"
)

// DepsEdnDoc is the reference documentation of the deps-edn manager.
var DepsEdnDoc = refdoc.Manager{
	Name: DepsEdnName,
	Summary: "Updates the libs of the Clojure CLI's `deps.edn` and Babashka's `bb.edn` as Renovate's deps-edn manager: Maven libs (`:mvn/version`) and git libs (`:git/tag` with `:git/sha`, or `:git/sha` alone) of the top-level `:deps` and of each alias. " +
		"`:local/root` libs are no deps. Only the version and commit bytes are rewritten.",
	Files: []string{"`deps.edn` at any depth", "`bb.edn` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDeps, What: ednWhat + "the top-level `:deps`", Datasource: ednDatasource, Versioning: ednVersioning, Datasources: []string{clojureds.Name, "git-tags", "git-refs"}},
		{Name: DepTypeExtraDeps, What: ednWhat + "an alias's `:extra-deps`", Datasource: ednDatasource, Versioning: ednVersioning, Datasources: []string{clojureds.Name, "git-tags", "git-refs"}},
		{Name: DepTypeOverrideDeps, What: ednWhat + "an alias's `:override-deps`", Datasource: ednDatasource, Versioning: ednVersioning, Datasources: []string{clojureds.Name, "git-tags", "git-refs"}},
		{Name: DepTypeReplaceDeps, What: ednWhat + "an alias's `:replace-deps` or `:deps`", Datasource: ednDatasource, Versioning: ednVersioning, Datasources: []string{clojureds.Name, "git-tags", "git-refs"}},
		{Name: DepTypeDefaultDeps, What: ednWhat + "an alias's `:default-deps`", Datasource: ednDatasource, Versioning: ednVersioning, Datasources: []string{clojureds.Name, "git-tags", "git-refs"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocate, When: "a version or tag string with escapes"},
		{Reason: skipRange, When: "a range Maven rejects (`[2.0,1.0)`, `[1.0`)"},
		{Reason: skipDynamic, When: "`\"RELEASE\"` or `\"LATEST\"`"},
		{Reason: skipGitURL, When: "a git lib whose `:git/url` is no http(s), ssh, git or scp-style repository URL, or without `:git/url` whose name infers none"},
		{Reason: skipGitRef, When: "a git lib with neither `:git/tag` nor `:git/sha`"},
		{Reason: skipGitSHA, When: "a `:git/sha` that is not 7 to 40 hex digits"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: versionItems},
		{Title: "Git libs", Items: []string{
			"The repository is `:git/url`, else inferred from the lib name as the Clojure CLI does: `io.github.<owner>/<repo>` (also `com.github.`) on GitHub, `io.gitlab.`/`com.gitlab.` on GitLab, `io.bitbucket.`/`org.bitbucket.` on Bitbucket, `ht.sr.~<user>/<repo>` on SourceHut. The dep's name is the lib as written.",
			"A lib with `:git/tag` is a `git-tags` dep: an update writes the new tag and, when `:git/sha` is set, the tag's commit at the length the sha is written with (`\"1a2b3c4\"` stays 7 digits). A lib with `:git/sha` alone tracks the default branch head (`git-refs`, version `HEAD`) and gets digest updates. The legacy unqualified `:tag` and `:sha` keys are read too.",
		}},
		{Title: "Repositories", Items: []string{
			"`:mvn/repos {\"name\" {:url \"…\"}}` adds repositories after Maven Central and Clojars (the Clojure CLI's defaults); `\"central\"` or `\"clojars\"` with another `:url` replace a default, with `nil` remove it. Without `:mvn/repos` the `clojure` datasource's defaults apply. Credentials (`~/.m2/settings.xml`) are not used.",
		}},
	},
}
