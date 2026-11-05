package terraform

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the terraform manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates provider, module and Terraform version constraints in Terraform and OpenTofu configurations, then refreshes `.terraform.lock.hcl`. " +
		"HCL files are read with a small scanner that understands the whole syntax (strings, templates, heredocs, comments, nested expressions) but only interprets literal strings and object constructors; " +
		"JSON configuration ([JSON syntax](https://developer.hashicorp.com/terraform/language/syntax/json), e.g. CDK for Terraform's `cdk.tf.json`) is read into the same structure, so it gives the same deps.",
	Files: []string{"`*.tf`, `*.tofu`, `*.tf.json`, `*.tofu.json` (not below a `.terraform` data directory)"},
	DepTypes: append([]refdoc.DepType{
		{Name: DepTypeProvider, What: "`terraform { required_providers { <name> = { source, version } } }`; a bare `<name> = \"<constraint>\"` means `hashicorp/<name>`", Versioning: "`hashicorp`", Datasources: []string{"terraform-provider"}},
		{Name: DepTypeProviderBlock, What: "the legacy `version` of a `provider \"<name>\"` block; the source is the file's `required_providers` entry of that name, else `hashicorp/<name>`. Blocks without `version` are not deps.", Versioning: "`hashicorp`", Datasources: []string{"terraform-provider"}},
		{Name: DepTypeModule, What: "`module` blocks: a registry `source` (`[host/]namespace/name/provider[//subdir]`) with its `version`, or a git `source` with `?ref=`", Datasource: "registry: `terraform-module`; git: `git-tags`", Versioning: "registry: `hashicorp`; git: `semver`, partial refs `tag`", Datasources: []string{"terraform-module", "git-tags"}},
		{Name: DepTypeVersion, What: "`terraform { required_version }`", Datasource: "`github-releases` (`hashicorp/terraform`; `opentofu/opentofu` in `*.tofu` files)", Versioning: "`hashicorp`", Datasources: []string{"github-releases"}},
		{Name: DepTypeHelmRelease, What: "`chart`, `repository` and `version` of a `helm_release` resource (also `chart = \"oci://host/path/name\"`); only exact versions are rewritten, as with the helm manager", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: DepTypeTFEWorkspace, What: "`terraform_version` of a `tfe_workspace` resource", Datasource: "`github-releases` (`hashicorp/terraform`)", Versioning: "`hashicorp`", Datasources: []string{"github-releases"}},
		{Name: "docker_image", What: "`name` of a `docker_image` resource", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "docker_container", What: "`image` of a `docker_container` resource", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "docker_service", What: "`task_spec { container_spec { image } }` of a `docker_service` resource", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "docker_registry_image", What: "`name` of a `docker_registry_image` data source", Versioning: "`docker`", Datasources: []string{"docker"}},
	}, kubernetesDocs()...),
	Skips: []refdoc.Skip{
		{Reason: "unsupported constraint", When: "a value that is not `[op] <version>`, e.g. empty"},
		{Reason: "no version constraint", When: "a provider or registry module without `version`"},
		{Reason: "non-literal value", When: "a version or provider entry that is an expression (`var.v`, `\"${…}\"`)"},
		{Reason: "non-literal source", When: "a `source` that is an expression"},
		{Reason: "cannot locate value", When: "a value with escapes, or a URL-encoded `ref`"},
		{Reason: "invalid provider source", When: "a `source` that is not `[host/]namespace/type`"},
		{Reason: "builtin provider", When: "`terraform.io/builtin/…`"},
		{Reason: "local module", When: "`./…`, `../…`"},
		{Reason: "unsupported module source", When: "archives (`https://…`), `s3::`, `gcs::`, `hg::` and other go-getter sources"},
		{Reason: "no ref", When: "a git module source without `?ref=`"},
		{Reason: "commit ref", When: "a git module pinned to a 40-hex commit"},
		{Reason: "local chart", When: "helm_release: a chart path (`./x`, `/x`), a chart URL (`https://…/x.tgz`), or a chart name without `repository`"},
		{Reason: "repository alias", When: "helm_release: a `repo/chart` chart without `repository` (a `helm repo add` name)"},
		{Reason: "invalid chart reference", When: "helm_release: an `oci://` chart without path and name"},
		{Reason: "unknown chart repository <repository>", When: "helm_release: a `repository` that is not `http(s)://` or `oci://`"},
		{Reason: "version range", When: "helm_release: a version that is not a full `[v]MAJOR.MINOR.PATCH[-pre][+build]`"},
		{Reason: "no version", When: "helm_release without `version`"},
		{Reason: "no tag", When: "an image without tag or digest"},
		{Reason: "digest without tag", When: "an image pinned by digest only"},
		{Reason: "invalid reference", When: "an image literal that is not a valid reference"},
	},
	Sections: []refdoc.Section{
		{Title: "Constraints and rangeStrategy", Items: []string{
			"Every constraint of the Terraform grammar (`=`, `!=`, `>`, `>=`, `<`, `<=`, `~>`, comma-separated) is the dep's range; its first version is the current version. Only the version bytes are replaced, so operator and spacing are kept, and a new version is written at the precision and `v` style of the old one.",
			"[`rangeStrategy`](configuration.md#rangestrategy) decides what happens (top level or per rule). `auto`, the default, is `replace`, Renovate's default for terraform: a version the constraint admits is left alone; otherwise the constraint is replaced (`~> 5.0` → `~> 6.2`, `3.2.1` → `3.3.0`).",
			"`update-lockfile`: an admitted version moves only the provider's `.terraform.lock.hcl` entry (a lockfile-only update, `(<type>, lockfile)` in reports), starting from the locked version; versions outside the constraint replace it as under `replace`. Modules and `required_version` have no lock, so for them it is `replace`.",
			"`bump` always raises the version: `~> 5.0` → `~> 5.46`, `>= 1.5.7` → `>= 1.9.5`, `= 1.2.3` → `= 1.3.0`; `in-range-only` does so only for versions the constraint admits. `widen` and `pin` act as `replace`.",
			"Constraints with an upper bound, an exclusion or several clauses (`< 2.0`, `!= 1.3`, `>= 4.0, < 6.0`) are never rewritten: under `update-lockfile` and `bump` their locked version moves within them, otherwise they are left alone (`outside range <r> (not rewritten)` in `deps explain`).",
			"Under `replace` and `update-lockfile`, candidates are ranked against the version the lock file locks, when there is one.",
		}},
		{Title: "Registries", Items: []string{
			"Provider and module addresses without a host use `registry.terraform.io`, in `*.tofu`/`*.tofu.json` files OpenTofu's `registry.opentofu.org`; [`terraform.defaultRegistry`](configuration.md#terraformdefaultregistry) sets the host for both. An explicit host (`registry.opentofu.org/…`, `app.terraform.io/…`) is looked up on that registry through service discovery.",
			"`required_version` in `*.tofu` files is an OpenTofu version (`github-releases` `opentofu/opentofu`).",
			"Git module sources (`git::https://…`, `git::ssh://…`, `git@host:…`, `github.com/…`, `bitbucket.org/…`) are looked up with `git-tags` over HTTPS; the `?ref=` value is the version.",
		}},
		{Title: "Resources", Items: []string{
			"`helm_release` charts are looked up in their `repository` (`http(s)://` or `oci://`), or in the registry path of an `oci://host/path/name` chart.",
			"Images of the docker provider and of the kubernetes workload resources are parsed and edited like the docker manager's references (tags, `@sha256:` digests, digest refresh and `pin: digest`). Images given as expressions (`docker_image.x.image_id`, `var.image`) are not deps.",
			"`tfe_workspace` `terraform_version` follows Terraform CLI releases like `required_version`.",
		}},
		{Title: "Replacements", Body: "Literal provider `source` addresses, registry module sources (a `//subdir` is kept) and container images in resources can be [replaced](configuration.md#rulesreplacementname): a new name with a host moves to that registry, one without keeps the file's registry (rejected when the file names a host). The lock entry of a renamed provider is dropped and the new one locked by `init`. Git, URL and local module sources, implied `hashicorp/` providers and the tool version are not replaced."},
		{Title: "Lock file", Items: []string{
			"`.terraform.lock.hcl` next to a configuration file gives its provider deps their locked version (lock entries are matched by `namespace/type`, so OpenTofu's `registry.opentofu.org/…` entries and a custom default registry match too). A lock file that does not parse is an error of the run.",
			"Post-processing (`apply`, `run`), scope: the directory of a patched configuration file that has a `.terraform.lock.hcl` next to it. Without a lock file nothing happens.",
			"Providers whose constraint changed, and providers with a lockfile-only update, are refreshed; module and `required_version` updates leave the lock alone.",
			"Their lock entries are removed, and `terraform init -backend=false -input=false -no-color` runs on a temporary copy of the directory (sibling directories reachable through local `../` module sources are symlinked), with `TF_IN_AUTOMATION=1`, `TF_INPUT=0` and `CHECKPOINT_DISABLE=1`. Terraform locks the newest version matching the constraint and records its `zh:` hashes for every platform and `h1:` for the runner's. Other entries keep their version.",
			"A lockfile-only update locks exactly its version: the copy gets an override file (`deps_lock_override.tf`) constraining the provider to `= <version>` under its local name, and the entry's old `constraints` are written back afterwards. If Terraform locks another version, the update fails with `.terraform.lock.hcl locked <name> <v>, want <version>`.",
			"Directories with `*.tofu` files or `registry.opentofu.org` entries are refreshed with `tofu` instead.",
			"Hashes need the CLI: without `terraform` (or `tofu`) on `PATH` the directory's changes fail with `terraform not found (needed to update .terraform.lock.hcl)` and are dropped. Install it on the runner (for example `hashicorp/setup-terraform`). Remote modules are downloaded by `init`.",
			"A failed init reports `terraform init in <dir>: <last 20 output lines>`.",
			"[`lockFileMaintenance`](configuration.md#lockfilemaintenance) refreshes every lock file next to a scanned configuration with `terraform init -upgrade -backend=false` (`tofu` for OpenTofu) on a copy of the directory: each locked provider moves to the newest version its constraints admit, with fresh hashes; the configuration is unchanged.",
			"`plan` does not run init, and prints `note    .terraform.lock.hcl changes are computed by apply/run`.",
		}},
		{Title: "JSON files", Items: []string{
			"Strings with `${`/`%{` templates are non-literal; escaped strings (`\\u003e`) are `cannot locate value`. `\"//\"` comment keys are ignored, and a top-level array of objects is merged.",
		}},
	},
}

// kubernetesDocs documents the dep types of the kubernetes workload resources.
func kubernetesDocs() []refdoc.DepType {
	out := make([]refdoc.DepType, len(kubernetesWorkloadTypes))
	for i, t := range kubernetesWorkloadTypes {
		out[i] = refdoc.DepType{Name: t, What: "`image` of every `container` / `init_container` block of a `" + t + "` resource", Versioning: "`docker`", Datasources: []string{"docker"}}
	}
	return out
}

// VersionDoc is the reference documentation of the terraform-version manager.
var VersionDoc = refdoc.Manager{
	Name:    VersionName,
	Summary: "Updates the Terraform version pinned in tfenv's `.terraform-version` file, or the OpenTofu version in tofuenv's `.opentofu-version` file (the first non-empty line).",
	Files:   []string{"`.terraform-version`", "`.opentofu-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeVersionFile, What: "the version on the first non-empty line of `.terraform-version`", Datasource: "`github-releases` (`hashicorp/terraform`)", Versioning: "`hashicorp`", Datasources: []string{"github-releases"}},
		{Name: DepTypeOpenTofuVersionFile, What: "the version on the first non-empty line of `.opentofu-version`", Datasource: "`github-releases` (`opentofu/opentofu`)", Versioning: "`hashicorp`", Datasources: []string{"github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-version value", When: "tfenv/tofuenv keywords: `latest`, `latest:<regex>`, `min-required`, `latest-allowed`"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Body: "A full version (`1.9.5`) moves to the newest release; a partial one (`1.9`) keeps its precision. The `v` style of the file is kept."},
	},
}

// TFLintDoc is the reference documentation of the tflint-plugin manager.
var TFLintDoc = refdoc.Manager{
	Name:    TFLintName,
	Summary: "Updates the versions of tflint ruleset plugins in `.tflint.hcl`. Plugins without a `source` are bundled with tflint and not extracted.",
	Files:   []string{"`.tflint.hcl`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePlugin, What: "`plugin \"<name>\" { source = \"github.com/<owner>/<repo>\", version = \"…\" }`; the name is `<owner>/<repo>` (`https://<host>/<owner>/<repo>` on other hosts)", Versioning: "`semver`", Datasources: []string{"github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "no version", When: "a plugin with a `source` but without `version`"},
		{Reason: "invalid plugin source", When: "a `source` that is not `<host>/<owner>/<repo>`"},
		{Reason: "non-literal source", When: "a `source` that is an expression"},
		{Reason: "non-literal value", When: "a `version` that is an expression"},
		{Reason: "cannot locate value", When: "a `version` with escapes"},
	},
}
