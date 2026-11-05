package rustversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the rust-version datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "any (`rust`)",
	Endpoints: "`<DEPS_RUST_MANIFESTS_URL or https://static.rust-lang.org/manifests.txt>`, the index of every channel manifest rustup can install, once per run; " +
		"stable `X.Y.Z` and numbered betas `X.Y.Z-beta.N` from the manifest names (channel manifests such as `channel-rust-stable.toml` and dated nightlies are not listed), plus a derived `X.Y` per stable line. " +
		"Source repository (release notes): `rust-lang/rust`",
	Auth:         "none",
	ReleaseTimes: "the date directory of the version's first manifest (`dist/2024-10-17/channel-rust-1.82.0.toml`), day precision",
}
