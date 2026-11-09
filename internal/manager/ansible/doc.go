package ansible

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the ansible manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images in Ansible task files.",
	Files:   []string{"`tasks/*.yml` / `tasks/*.yaml` at any depth (a role's `tasks/main.yml`)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeImage, What: "every scalar `image` value of the task file, at any depth (`community.docker.docker_container`, `containers.podman.podman_container`, …)", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipTemplate, When: "a Jinja2 expression (`{{`) in the image"},
		{Reason: "variable reference", When: "`$` in the image that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"Files without `image` are not parsed.",
		}},
	},
}

// GalaxyDoc is the reference documentation of the ansible-galaxy manager.
var GalaxyDoc = refdoc.Manager{
	Name:    GalaxyName,
	Summary: "Updates the roles and collections of Ansible Galaxy requirements files and the collection dependencies of `galaxy.yml`.",
	Files: []string{
		"`requirements.yml`, `requirements.yaml`, `requirements.ansible.yml` (`.yaml`) at any depth: `roles` and `collections` lists, or a top-level list of roles (the old format)",
		"`galaxy.yml`, `galaxy.yaml`, `galaxy.ansible.yml` (`.yaml`) at any depth: the collection's `dependencies` mapping",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRole, What: "`roles[]` entry: `src` (else `name`) and `version`, or a `src,version` string. A Galaxy role `namespace.name`, or an SCM URL (`git+https://…`, `https://…`, `git@host:owner/repo`; `scm: git`)",
			Datasource: "`galaxy` (`namespace.name`), `git-tags` (SCM URLs: the name is the URL path `owner/repo`, the source its `https://` URL)", Versioning: "`tag`", Datasources: []string{RoleDatasource, GitDatasource}},
		{Name: DepTypeCollection, What: "`collections[]` entry: `name` and `version` (a bare version, `==`, or a range such as `>=1.0.0,<2.0.0`), the Galaxy server of `source`, or `type: git` with the repository URL as name (`git+https://…/repo.git,v1.0.0`); galaxy.yml `dependencies` (`community.general: \">=8.0.0\"`, or an SCM URL key with a git version: `'git+https://github.com/o/r': v1.0.0`)",
			Datasource: "`galaxy-collection` (`namespace.name`, the source the entry's `source`), `git-tags` (`type: git` and SCM URL names)", Versioning: "`semver` (Galaxy), `tag` (git)", Datasources: []string{CollectionDatasource, GitDatasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoVersion, When: "an entry without `version` (or `,version`), an empty or non-string specifier"},
		{Reason: pyreq.SkipUnconstrained, When: "a collection specifier `*` or only `!=` exclusions"},
		{Reason: pyreq.SkipRange, When: "a collection specifier that is not a PEP 440 specifier set"},
		{Reason: skipNonVersion, When: "a role or git version that is not a version tag (a branch, a commit)"},
		{Reason: skipNoName, When: "a collection entry without `name`, a name or galaxy.yml key that is neither `namespace.name` nor an SCM URL"},
		{Reason: skipLocal, When: "a local path, `type: file`, `dir` or `subdirs`"},
		{Reason: skipUnsupported, When: "a tarball URL (`type: url`), `scm: hg`, a role name that is not `namespace.name`, or an SCM URL without an `owner/repo` path"},
		{Reason: skipLocate, When: "the version bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"The role and git versions are replaced in place, keeping quotes and comments; versions cannot be pinned to digests.",
			"Collection versions are semver (prereleases such as `0.3.0-experimental.1` or `1.0.0-dev` are ordered by semver rules). Collection specifiers are PEP 440 specifier sets in which a bare version means `==` (ansible-galaxy's syntax): their version is the range's anchor and they are rewritten per `rangeStrategy` like Python specifiers (`auto` = `update-lockfile`: a bare or `==` version is replaced by the new version, an open range such as `>=8.0.0` only moves for a version outside it).",
			"`include:` entries of requirements files are not followed (the included file is extracted on its own when its name matches). List entries with keys other than `src`, `name`, `role`, `version` and `scm` are not roles (a task file named `requirements.yml`).",
		}},
	},
}
