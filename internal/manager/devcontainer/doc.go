package devcontainer

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the devcontainer manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the image, the Dev Container Features and the node, go, python and ruby feature versions of devcontainer.json files.",
	Files: []string{
		"`.devcontainer.json`, `.devcontainer/devcontainer.json`, `.devcontainer/<folder>/devcontainer.json`, at any depth",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeImage, What: "the top-level `image`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: DepTypeFeature, What: "the keys of `features` that are OCI references (`ghcr.io/devcontainers/features/node:1`)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: DepTypeFeatureVersion, What: "the `version` option (or a string option value) of the features `ghcr.io/devcontainers/features/node` (name `node`), `…/go` (`go`), `…/python` (`python`) and `…/ruby` (`ruby`)", Versioning: "as [asdf](#asdf): `semver`/`tag` (node), `tag` (go), `pep440`/`tag` (python), `ruby`/`tag` (ruby)", Datasources: []string{"node-version", "go-toolchain", "python-version", "ruby-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "local feature", When: "a feature in a folder of the repository (`./my-feature`)"},
		{Reason: "tarball feature", When: "a feature downloaded from an `https://` tarball URL"},
		{Reason: "not an OCI reference", When: "a feature without a registry host: a legacy short id (`docker-in-docker`) or a GitHub release reference (`owner/repo/feature@v1`)"},
		{Reason: "variable reference", When: "`$` in the image or feature (`${localEnv:…}`)"},
		{Reason: "invalid reference", When: "the image or feature reference does not parse"},
		{Reason: "no tag", When: "an image or feature without tag"},
		{Reason: "digest without tag", When: "an image or feature pinned by digest only"},
		{Reason: "no version", When: "a node, go, python or ruby feature without a `version` option (the feature's default, typically `lts` or `latest`)"},
		{Reason: "non-version value", When: "a version option that is a word: `lts`, `latest`, `none`, `os-provided`"},
		{Reason: "partial version", When: "a go version with one part (`1`)"},
		{Reason: "unsupported range", When: "a version option that is no version (`>=18`)"},
		{Reason: "cannot locate value", When: "the string is written with JSON escapes"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"The file is JSON with comments (`//`, `/* */`) and trailing commas; edits replace the version bytes only, so comments and formatting stay.",
			"The image and the features are parsed and edited like the [docker](#docker) manager's references: the tag is updated, an image digest is refreshed or, with `pin: digest`, pinned. Features are never pinned (as Renovate's `pinDigests: false`); a digest already written after a feature tag is refreshed.",
			"Feature tags are usually a major (`:1`): docker versioning keeps the precision, so `:1` moves to `:2`.",
			"Feature version options are read like the [asdf](#asdf) tools of the same name: `20` and `20.11` keep their precision (node, python, ruby), `1.22` and `1.22.3` are go-toolchain versions.",
			"`build.dockerfile` and `dockerComposeFile` are not read: the [docker](#docker) manager finds the Dockerfile (`Dockerfile`, `Dockerfile.<x>`, `<x>.Dockerfile`) and the Compose file on its own.",
		}},
	},
}
