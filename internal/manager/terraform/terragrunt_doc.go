package terraform

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// TerragruntDoc is the reference documentation of the terragrunt manager.
var TerragruntDoc = refdoc.Manager{
	Name: TerragruntName,
	Summary: "Updates the module `source` of the `terraform` block in Terragrunt configurations (git sources by their `?ref=`, `tfr://` registry modules by their `?version=` or the block's `version` constraint, `oci://` modules by their `?tag=`) and the `engine` plugin version. " +
		"Source expressions built from locals, included locals and path functions are evaluated. `include`, `dependency` and `generate` blocks are not deps. Lock file maintenance refreshes the `.terraform.lock.hcl` next to a `terragrunt.hcl`.",
	Files: []string{"`terragrunt.hcl` (not below a `.terragrunt-cache` directory)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTerragruntGitHub, What: "`terraform { source }` from `github.com` (`github.com/…`, `git::https://github.com/…`, `git::git@github.com:…`, `git::ssh://git@github.com/…`, a tag archive `https://github.com/<owner>/<repo>/archive/[refs/tags/]<tag>.zip|.tar.gz`) with `?ref=`; a 40-hex commit ref follows the default branch head", Datasource: "`git-tags`; commit refs `git-refs`", Versioning: "`semver`, partial refs `tag`", Datasources: []string{"git-tags", "git-refs"}},
		{Name: DepTypeTerragruntGitTags, What: "`terraform { source }` from another git host (`git::https://…`, `git::ssh://…`, `git@host:…`, `bitbucket.org/…`) with `?ref=`; a 40-hex commit ref follows the default branch head", Datasource: "`git-tags`; commit refs `git-refs`", Versioning: "`semver`, partial refs `tag`", Datasources: []string{"git-tags", "git-refs"}},
		{Name: DepTypeTerragruntRegistry, What: "`terraform { source = \"tfr://[host]/<namespace>/<name>/<provider>[//subdir]?version=<version>\" }`, or without `?version=` the block's `version` constraint; the name is `<namespace>/<name>/<provider>`", Versioning: "`hashicorp`", Datasources: []string{"terraform-module"}},
		{Name: DepTypeTerragruntOCI, What: "`terraform { source = \"oci://<registry>/<repository>[//subdir]?tag=<tag>\" }` (Terragrunt's OCI getter); the name is the repository", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: DepTypeTerragruntEngine, What: "`engine { source = \"github.com/<owner>/<repo>\", version = \"…\" }`: the GitHub release Terragrunt downloads; the name is `<owner>/<repo>`", Versioning: "`semver`", Datasources: []string{"github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-literal source", When: "a `source` expression that cannot be evaluated (functions other than the path functions, `dependency.…`, `read_terragrunt_config(…)`, directives, a local or include that does not resolve)"},
		{Reason: "local module", When: "`.`, `./…`, `../…`, `/…`, or a source built from a path function (`${get_repo_root()}/modules/vpc`, `find_in_parent_folders(\"modules\")`)"},
		{Reason: "archive module source", When: "an `http(s)://` URL that is not a GitHub tag archive (an archive or an `X-Terraform-Get` redirect, which name no version)"},
		{Reason: "s3 module source", When: "`s3::…` or an `*.amazonaws.com` S3 URL"},
		{Reason: "gcs module source", When: "`gcs::…` or a `www.googleapis.com/storage/…` URL"},
		{Reason: "hg module source", When: "`hg::…` (Mercurial)"},
		{Reason: "unsupported module source", When: "other go-getter sources and malformed `tfr://` addresses"},
		{Reason: "no ref", When: "a git source without `?ref=`: it follows the default branch, there is no version to update (as with Renovate)"},
		{Reason: "cannot locate value", When: "a source with escapes, a URL-encoded `ref`, `version` or `tag`, or a `ref`/`version` that comes from an included file"},
		{Reason: "no version constraint", When: "a `tfr://` source without `?version=` and without a `version` attribute"},
		{Reason: "unsupported constraint", When: "a `?version=` that is not an exact version (Terragrunt rejects constraints there), or a `version` attribute that is not a constraint"},
		{Reason: "non-literal value", When: "a `version` attribute that is an expression"},
		{Reason: "no tag", When: "an `oci://` source without `?tag=` (OpenTofu and Terragrunt use `latest`)"},
		{Reason: "digest without tag", When: "an `oci://` source pinned by `?digest=` only"},
		{Reason: "invalid reference", When: "an `oci://` source that is not `<registry>/<repository>`"},
		{Reason: "no version", When: "an `engine` without `version` (Terragrunt always downloads the latest release)"},
		{Reason: "local engine", When: "an `engine` `source` that is a local path"},
		{Reason: "unsupported engine source", When: "an `engine` `source` that is a download URL"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"Only the version bytes are replaced: the `?ref=` value of git sources (after a `tags/` or `refs/tags/` prefix, which go-getter accepts), the `?version=` value of `tfr://` sources (an exact version), the version of the `version` attribute, the `?tag=` of `oci://` sources, or the engine `version`.",
			"The `version` attribute follows the terraform manager's constraint rules: `~> 0.0.1` is bumped at its precision, multi-clause ranges (`>= 1.0, < 2.0`) are not rewritten.",
			"A `?ref=` pinned to a 40-hex commit is a `git-refs` dep on the default branch (version `HEAD`): digest updates replace the commit with the branch head's, like Bazel git overrides. Renovate does not update commit refs.",
		}},
		{Title: "Source expressions", Items: []string{
			"A `source` that is not a string literal is evaluated: string templates, `local.<name>` (the file's `locals`), `include.<label>.locals.<name>` (the `locals` of the file a labeled `include` block's `path` names, read from the repository), and the path functions `get_terragrunt_dir()`, `get_repo_root()` (the scanned root), `get_path_to_repo_root()`, `get_parent_terragrunt_dir()`, `find_in_parent_folders([name[, fallback]])` (searched from the parent directory up to the scanned root) and `dirname()`.",
			"`source = \"${include.envcommon.locals.base_source_url}?ref=v0.8.0\"` (the [terragrunt-infrastructure-live-example](https://github.com/gruntwork-io/terragrunt-infrastructure-live-example) layout) is the git source the include names, and its `v0.8.0` is updated in this file. A version written in a local of this file is updated there; one written in the included file is `cannot locate value`, since the included file is no manifest.",
			"Sources built from path functions are local modules. Other functions and references (`get_env()`, `dependency.…`, `read_terragrunt_config(…)`) are not evaluated.",
		}},
		{Title: "Registries", Items: []string{
			"`tfr:///…` (no host) uses `$TG_TF_DEFAULT_REGISTRY_HOST` when set (as Terragrunt does), else [`terraform.defaultRegistry`](configuration.md#terraformdefaultregistry), else `registry.terraform.io`. Terragrunt's own default without either depends on the wrapped CLI (`registry.opentofu.org` for OpenTofu), which deps cannot know.",
			"`tfr://<host>/…` is looked up on that registry through service discovery, like Terraform module addresses.",
			"Git sources are looked up with `git-tags` over HTTPS, as for Terraform modules; the `?ref=` value is the version.",
		}},
		{Title: "Blocks", Items: []string{
			"`include` blocks name local configuration files; they are read only to resolve `include.<label>.locals`. `dependency` blocks name local units (`config_path`). `generate` blocks write code at run time; their `contents` are not parsed. None of them is a dep, as with Renovate.",
			"`engine` blocks are read in `terragrunt.hcl` only; engines configured in an included `root.hcl` are not seen.",
		}},
		{Title: "Lock file", Items: []string{
			"Terragrunt copies the module's `.terraform.lock.hcl` next to the `terragrunt.hcl`. Its providers are the module's, not deps of the `terragrunt.hcl`, so source updates leave it alone (as with Renovate).",
			"[`lockFileMaintenance`](configuration.md#lockfilemaintenance) refreshes it, as Renovate's terragrunt maintenance does: `terraform init -upgrade -backend=false` (`tofu` when the lock names `registry.opentofu.org`) runs in an empty temporary directory holding the lock and a configuration requiring each locked provider under the `constraints` its entry records, so every provider moves to the newest version those admit, with fresh hashes. The module is not downloaded and no credentials are needed; the CLI must be on `PATH`.",
			"Directories that also hold `*.tf`/`*.tofu` files are maintained by the terraform manager.",
		}},
	},
}
