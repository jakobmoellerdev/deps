package helm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the helm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates Helm chart versions in Chart.yaml dependencies (with Chart.lock), helmfiles and chart custom resources, from classic chart repositories and OCI registries.",
	Files: []string{
		"`Chart.yaml` (DepType `chart`)",
		"`helmfile.y(a)ml`, `helmfile.d/*.y(a)ml`, each optionally `.gotmpl` (DepType `helmfile`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "chart", What: "`dependencies[].version` with `name` and `repository`", Datasource: "`helm`", Versioning: "`semver`"},
		{Name: "helmfile", What: "`releases[].version` with `chart: <repo>/<name>` (resolved through `repositories[]`, `oci: true` → `oci://`) or `chart: oci://host/path/name`", Datasource: "`helm`", Versioning: "`semver`"},
		{Name: "k0s", What: "k0s `Chart` resources", Datasource: "`helm`", Versioning: "`semver`"},
		{Name: "flux", What: "Flux `HelmRelease` resources", Datasource: "`helm`", Versioning: "`semver`"},
		{Name: "argocd", What: "Argo CD `Application` Helm sources", Datasource: "`helm`", Versioning: "`semver`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "version range", When: "anything but a full `MAJOR.MINOR.PATCH[-pre][+build]` (optionally `v`-prefixed): `~1.2.0`, `^1`, `>=1.0.0`, `1.2.x`, `1.2`"},
		{Reason: "no version", When: "the version is missing or empty"},
		{Reason: "template value", When: "the version contains `{{`"},
		{Reason: "local chart", When: "Chart.yaml: empty or `file://` repository; helmfile: a chart path (`./x`, `../x`, `/x`, no `/`)"},
		{Reason: "repository alias", When: "Chart.yaml: `@name` or `alias:name` repositories (resolved by the user's Helm repository config)"},
		{Reason: "unknown chart repository <repo>", When: "Chart.yaml: a repository that is no URL; helmfile: a `<repo>/` prefix not in `repositories[]`"},
		{Reason: "invalid chart reference", When: "helmfile: `oci://` without a path, or `<repo>/a/b`"},
		{Reason: "cannot locate value", When: "the version's bytes could not be found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Versioning", Body: "`semver`: the `v` prefix style of the current value is kept, prereleases are only proposed from a prerelease. Only exact versions are updated; Helm treats everything else as a constraint."},
		{Title: "Chart.lock", Items: []string{
			"After Chart.yaml is patched (`plan`, `apply`, `run`), the sibling `Chart.lock` gets the new version of every exact dependency whose locked version differs, matched by position and name.",
			"`digest` is recomputed as Helm does (`sha256` over the JSON of the Chart.yaml and Chart.lock dependencies), so `helm dependency build` accepts the lock. Repository aliases take the URL locked for them.",
			"The old digest is verified first; when deps cannot reproduce it (a lock out of sync), the chart's update fails with an error instead of writing a lock Helm would reject.",
			"`generated` is kept, so unchanged updates produce identical commits.",
		}},
		{Title: "Edits", Body: "The version is replaced in place."},
	},
}
