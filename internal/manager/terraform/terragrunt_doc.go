package terraform

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// TerragruntDoc is the reference documentation of the terragrunt manager.
var TerragruntDoc = refdoc.Manager{
	Name: TerragruntName,
	Summary: "Updates the module `source` of the `terraform` block in Terragrunt configurations: git sources by their `?ref=`, `tfr://` registry modules by their `?version=` or the block's `version` constraint. " +
		"Other blocks (`include`, `dependency`, `generate`, `engine`) are not extracted; `.terraform.lock.hcl` is not refreshed.",
	Files: []string{"`terragrunt.hcl` (not below a `.terragrunt-cache` directory)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTerragruntGitHub, What: "`terraform { source }` from `github.com` (`github.com/…`, `git::https://github.com/…`, `git::git@github.com:…`, `git::ssh://git@github.com/…`) with `?ref=`", Versioning: "`semver`, partial refs `tag`", Datasources: []string{"git-tags"}},
		{Name: DepTypeTerragruntGitTags, What: "`terraform { source }` from another git host (`git::https://…`, `git::ssh://…`, `git@host:…`, `bitbucket.org/…`) with `?ref=`", Versioning: "`semver`, partial refs `tag`", Datasources: []string{"git-tags"}},
		{Name: DepTypeTerragruntRegistry, What: "`terraform { source = \"tfr://[host]/<namespace>/<name>/<provider>[//subdir]?version=<version>\" }`, or without `?version=` the block's `version` constraint; the name is `<namespace>/<name>/<provider>`", Versioning: "`hashicorp`", Datasources: []string{"terraform-module"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-literal source", When: "a `source` that is an expression (`\"${include.envcommon.locals.base_source_url}?ref=v0.8.0\"`, `get_repo_root()`)"},
		{Reason: "local module", When: "`.`, `./…`, `../…`, `/…`"},
		{Reason: "unsupported module source", When: "archives (`https://…`), `s3::`, `gcs::`, `hg::`, `oci://` and other go-getter sources, and malformed `tfr://` addresses"},
		{Reason: "no ref", When: "a git source without `?ref=`"},
		{Reason: "commit ref", When: "a git source pinned to a 40-hex commit"},
		{Reason: "cannot locate value", When: "a source with escapes, or a URL-encoded `ref` or `version`"},
		{Reason: "no version constraint", When: "a `tfr://` source without `?version=` and without a `version` attribute"},
		{Reason: "unsupported constraint", When: "a `?version=` that is not an exact version (Terragrunt rejects constraints there), or a `version` attribute that is not a constraint"},
		{Reason: "non-literal value", When: "a `version` attribute that is an expression"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"Only the version bytes are replaced: the `?ref=` value of git sources, the `?version=` value of `tfr://` sources (an exact version), or the version of the `version` attribute.",
			"The `version` attribute follows the terraform manager's constraint rules: `~> 0.0.1` is bumped at its precision, multi-clause ranges (`>= 1.0, < 2.0`) are not rewritten.",
		}},
		{Title: "Registries", Items: []string{
			"`tfr:///…` (no host) uses `registry.terraform.io` (config `terraform.defaultRegistry` overrides it); Terragrunt's own default depends on the wrapped CLI and `TG_TF_DEFAULT_REGISTRY_HOST`, which deps does not read.",
			"`tfr://<host>/…` is looked up on that registry through service discovery, like Terraform module addresses.",
			"Git sources are looked up with `git-tags` over HTTPS, as for Terraform modules; the `?ref=` value is the version.",
		}},
	},
}
