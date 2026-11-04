package tfregistry

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	discovery = "service discovery `GET https://<host>/.well-known/terraform.json` once per registry (relative and absolute service URLs)"
	tokenAuth = "`Authorization: Bearer <token>`, resolved once per host as the Terraform CLI does: `$TF_TOKEN_<host>` (dots as `_`, hyphens as is or `__`, e.g. `TF_TOKEN_app_terraform_io`; punycode hosts verbatim), " +
		"then `credentials \"<host>\" { token = ... }` of the CLI config (`$TF_CLI_CONFIG_FILE`, else `~/.terraformrc` plus the `*.tfrc`/`*.tfrc.json` files of `~/.terraform.d` such as `credentials.tfrc.json`; `%APPDATA%/terraform.rc` and `%APPDATA%/terraform.d` on Windows), " +
		"then the `credentials_helper` (`terraform-credentials-<name> <args...> get <host>` from `~/.terraform.d/plugins[/<os>_<arch>]`, else PATH; missing → `terraform-credentials-<name> not found (needed for <host> credentials)`); else anonymous."
	published = "`published_at` of the version document (`<service><name>/<version>`; registry.terraform.io serves it, other registries are unknown). A partial version (`5.31` of `~> 5.31`) has the time of its line's first release."
)

// ProviderDoc is the reference documentation of the terraform-provider datasource.
var ProviderDoc = refdoc.Datasource{
	Name:         ProviderName,
	NameForm:     "`namespace/type`; the dep's source is the registry (`https://registry.terraform.io` when empty)",
	Endpoints:    discovery + ", then `GET <providers.v1>/<namespace>/<type>/versions`, once per provider; 404 → `provider <name> not found in <registry>`",
	Auth:         tokenAuth,
	ReleaseTimes: published,
}

// ModuleDoc is the reference documentation of the terraform-module datasource.
var ModuleDoc = refdoc.Datasource{
	Name:         ModuleName,
	NameForm:     "`namespace/name/provider`; the dep's source is the registry (`https://registry.terraform.io` when empty)",
	Endpoints:    discovery + ", then `GET <modules.v1>/<namespace>/<name>/<provider>/versions`, once per module; 404 → `module <name> not found in <registry>`",
	Auth:         tokenAuth,
	ReleaseTimes: published,
}
