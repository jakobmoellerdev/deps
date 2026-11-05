package juliaregistry

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the julia-registry datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "Julia package name (`JSON`, `CUDA_jll`); the dep's source is the package UUID of the project's `[deps]`, `[weakdeps]` or `[extras]`, which the registry entry must have",
	Endpoints: "the General registry's files on its default branch: `GET <DEPS_JULIA_REGISTRY_URL or https://raw.githubusercontent.com/JuliaRegistries/General/HEAD>/<L>/<Name>/Package.toml` and `…/Versions.toml` " +
		"(`<L>` the name's first letter upper-cased; `jll/<L>/<Name>` for `_jll` packages, as the registry files them), concurrently, once per package; no 1.4 MB Registry.toml download. " +
		"Yanked versions (`yanked = true`) are not listed. A package the registry does not have (a 404: a package of a private registry) or has under another UUID skips the dep with `package <name> is not in the registry <url>` or `… has UUID <x>, not <y>`. " +
		"Source repository (release notes): Package.toml's `repo`",
	Auth:         "userinfo of `DEPS_JULIA_REGISTRY_URL` (`https://user:token@host/…`) as basic auth, for a private mirror; redacted in errors and logs",
	ReleaseTimes: "none: registry entries carry no publication time, so `minimumReleaseAge` treats versions as of unknown age (see `minimumReleaseAgeBehaviour`)",
}
