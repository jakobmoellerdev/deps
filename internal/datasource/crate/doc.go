package crate

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the crate datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "crate name (case-insensitive); the dep's source is its registry name or index URL (\"\" = crates.io)",
	Endpoints: "`GET <index>/<prefix>/<name>` of the sparse index (crates.io: `DEPS_CRATES_INDEX_URL` or `https://index.crates.io`; prefixes `1/<n>`, `2/<n>`, `3/<first char>/<n>`, `<n[0:2]>/<n[2:4]>/<n>`), or the same path of a git index (shallow blob-less clone, `git cat-file`); newline-delimited JSON, yanked versions dropped; not found → `crate <name> not found` (`… in registry <name>`). " +
		"Partial requirements also get a derived `X.Y` and `X` per stable release line. " +
		"Source repository (release notes): `GET <api>/api/v1/crates/<name>/<version>` `repository` (crates.io: `DEPS_CRATES_API_URL` or `https://crates.io`; other registries: `api` of their `config.json`), only for versions in a pull request",
	Auth:         "alternative registries: `CARGO_REGISTRIES_<NAME>_TOKEN`, `$CARGO_HOME/credentials.toml` or `.cargo/config.toml` `[registries.<name>] token` as `Authorization`, only to the index host; without a token, the platform token (Basic) for its host, and the git credential helper after a 401; git indexes: git credential helpers",
	ReleaseTimes: "index `pubtime` (crates.io writes it for every version); a release line has the time of its first release",
}
