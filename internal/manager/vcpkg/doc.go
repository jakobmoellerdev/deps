package vcpkg

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the vcpkg manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Moves vcpkg registry baselines to the newest commit of the registry repository (as Dependabot's vcpkg ecosystem does) " +
		"and port version constraints (`version>=`, `overrides`) to the newest version in the registry's versions database at the baseline.",
	Files: []string{"`vcpkg.json` at any depth", "`vcpkg-configuration.json` at any depth (it configures the `vcpkg.json` in the same directory)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeBuiltinBaseline, What: "`vcpkg.json` `builtin-baseline`: a commit of `microsoft/vcpkg` (name `microsoft/vcpkg`, digest the commit)", Datasource: "`git-refs` (`HEAD`)", Versioning: "`tag`"},
		{Name: DepTypeDefaultRegistry, What: "`default-registry.baseline` of `vcpkg-configuration.json` or the manifest's embedded `vcpkg-configuration`, of a `builtin` (`microsoft/vcpkg`) or `git` registry (`repository`, tracking `reference` or `HEAD`)", Datasource: "`git-refs`", Versioning: "`tag`"},
		{Name: DepTypeRegistry, What: "`registries[].baseline` of a `builtin` or `git` registry, as `default-registry`; `artifact` registries are ignored", Datasource: "`git-refs`", Versioning: "`tag`"},
		{Name: DepTypeDependencies, What: "`dependencies[]` objects with `version>=` (name the port; `1.3.1`, `1.3.1#2` with a port version); ports without a constraint are not deps", Datasource: "`vcpkg`", Versioning: "`regex` of up to four numeric parts separated by `.` or `-` and an optional `#<port-version>`"},
		{Name: DepTypeOverrides, What: "`overrides[]` `version`, `version-semver` or `version-date` (`21.0.0#2` with a port version)", Datasource: "`vcpkg`", Versioning: "as `dependencies`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "unrecognized repository", When: "a `git` registry `repository` is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: "baseline is not a commit", When: "a baseline is not a 40-hex commit"},
		{Reason: "cannot locate value", When: "the value is written with escapes"},
		{Reason: "reference is a tag", When: "a `git` registry's `reference` is a version tag: its baseline does not move with a branch"},
		{Reason: "no name", When: "a `dependencies` or `overrides` entry has no `name`"},
		{Reason: "version is not a string", When: "`version>=` or the override version is not a string"},
		{Reason: "port-version field", When: "an override sets a separate `port-version`, which an update would have to rewrite too"},
		{Reason: "version-string", When: "an override with a `version-string` (no ordering)"},
		{Reason: "overlay port", When: "an `overlay-ports` directory of the configuration has the port (`<overlay>/<port>/portfile.cmake`, or the overlay is the port's directory): vcpkg takes it from there, not from a registry"},
		{Reason: "no builtin-baseline", When: "a port resolves to the builtin registry but the manifest has no `builtin-baseline` (or the default registry is `builtin` without `baseline`)"},
		{Reason: "no registry for the port", When: "`default-registry` is `null` and no `registries[].packages` pattern matches the port"},
		{Reason: "git registry without baseline", When: "the port's `git` registry has no `baseline`"},
		{Reason: "unrecognized registry repository", When: "the port's `git` registry repository is not an https URL (its versions database cannot be read)"},
		{Reason: "unsupported registry kind <kind>", When: "the port's registry is a `filesystem` (or other non-`builtin`, non-`git`) registry"},
	},
	Sections: []refdoc.Section{
		{Title: "Registries", Items: []string{
			"A port resolves to the registry whose `packages` match it (an exact name beats a `prefix*` pattern, the longest pattern wins), else to `default-registry`, else to the builtin registry at `builtin-baseline`, as vcpkg resolves it. " +
				"A manifest's embedded `vcpkg-configuration` takes precedence over the `vcpkg-configuration.json` next to it.",
			"The versions of a port are read from the registry's versions database at the current baseline (`versions/<first letter>-/<port>.json`), so a proposed `version>=` is always installable with that baseline; a baseline update and the port updates it unlocks come in successive runs.",
			"Baseline updates are `digest` updates of the registry repository's default branch (or `reference`); like git-submodules, they have no release notes.",
		}},
	},
}
