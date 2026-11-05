package rusttoolchain

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the rust-toolchain manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Rust toolchain rustup selects for a directory: `channel` of the `[toolchain]` table in `rust-toolchain.toml` (and in a `rust-toolchain` file written as TOML), " +
		"or the toolchain name of a legacy one-line `rust-toolchain` file (a single line without `=` or `[`, as rustup reads it). " +
		"The TOML is read with the span-keeping parser `internal/tomlspan`, so comments, `components`, `targets` and `profile` are kept.",
	Files: []string{"any `rust-toolchain.toml`", "any `rust-toolchain`"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "the pinned toolchain version (name `" + DepName + "`), `X.Y.Z` or `X.Y`, optionally followed by a host triple (`1.82.0-x86_64-unknown-linux-gnu`)", Versioning: "`semver`; `X.Y` `tag`", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipChannel, When: "a release channel that is no version: `stable`, `beta`, `nightly` (with or without a host triple)"},
		{Reason: skipDated, When: "a dated channel such as `nightly-2025-10-10` or `beta-2025-10-01`"},
		{Reason: skipPath, When: "`[toolchain] path` (a custom toolchain directory)"},
		{Reason: skipNoChannel, When: "a `[toolchain]` table without a string `channel`"},
		{Reason: skipLocate, When: "`channel` is written with escapes, so its bytes cannot be replaced in place"},
		{Reason: skipUnsupported, When: "any other toolchain name, such as a beta `1.83.0-beta.5` or a custom toolchain linked with `rustup toolchain link`"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"A full `X.Y.Z` uses `semver`; betas are never proposed.",
			"A release line `X.Y` uses `tag` versioning and keeps its precision (`1.82` → `1.91`) against the `X.Y` lines the datasource derives.",
			"Only the version is replaced; a host triple after it and the rest of the file are kept.",
			"A file without a `[toolchain]` table has no dep.",
			"`rust-version` in `Cargo.toml` (`package.rust-version`, `workspace.package.rust-version`), the minimum supported Rust version, is not extracted: neither Renovate nor Dependabot updates it.",
		}},
	},
}
