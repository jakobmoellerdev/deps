package hexpmbob

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the hexpm-bob datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "`elixir` for Elixir, `otp/<os>-<version>` (`otp/ubuntu-22.04`) for the Erlang/OTP builds of that system. The [asdf](#asdf) and [mise](#mise) `elixir` tool uses `elixir`; " +
		"use it from a [custom manager](#custom) (Renovate's name) for Dockerfile `ARG`s, CI files or the OTP builds. " +
		"Elixir versions are listed without their `v` (`1.18.4`) and per OTP release (`1.18.4-otp-27`), OTP versions without `OTP-` (`27.3.4.1`, `27.0-rc1`); " +
		"branch builds (`main`, `v1.18`, `maint-27`) are not versions. For a pinned `1.18.4-otp-27` that should stay on its OTP release, use " +
		"`versioning: 'regex:^(?<major>\\d+)\\.(?<minor>\\d+)\\.(?<patch>\\d+)(?:-(?<prerelease>[a-z]+\\.\\d+))?(?:-otp-(?<compatibility>\\d+))?$'` (what the asdf/mise `elixir` tool uses); OTP's up to four parts need `loose`",
	Endpoints:    "`GET <DEPS_HEXPM_BOB_URL or https://builds.hex.pm>/builds/<name>/builds.txt`, once per name. Source repository (release notes): `https://github.com/elixir-lang/elixir` or `https://github.com/erlang/otp`",
	Auth:         "none",
	ReleaseTimes: "the build time of each build in the list",
}
