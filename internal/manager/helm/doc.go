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
		"other `*.y(a)ml` containing the API group of a k0s, Flux, Argo CD or Sveltos resource below, parsed document by document (DepTypes `k0s`, `flux`, `argocd`, `sveltos`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "chart", What: "`dependencies[].version` with `name` and `repository`", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "helmfile", What: "`releases[].version` with `chart: <repo>/<name>` (resolved through `repositories[]`, `oci: true` → `oci://`) or `chart: oci://host/path/name`", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "k0s", What: "k0s `Chart` (`helm.k0sproject.io`) `spec.chartName` + `spec.version`, and ClusterConfig (`k0s.k0sproject.io`, also embedded in a k0sctl `Cluster` as `spec.k0s.config`) `spec.extensions.helm.charts[].chartname` + `version`; the repository is `spec.repository.url`, the `<alias>` of `<alias>/<chart>` from a ClusterConfig's `spec.extensions.helm.repositories[]` in any file, or `oci://host/path/<chart>`", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "flux", What: "`HelmRelease` (`helm.toolkit.fluxcd.io/v2*`) `spec.chart.spec.chart` + `version` from its `sourceRef` `HelmRepository` (`source.toolkit.fluxcd.io`, any file; `spec.type: oci` → `oci://`), and the `OCIRepository` chart (`spec.url` + `spec.ref.tag`) a `spec.chartRef` names or whose `spec.layerSelector.mediaType` is the Helm chart type", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "argocd", What: "`Application` `spec.source` / `spec.sources[]` and `ApplicationSet` `spec.template.spec.source(s)` with a `chart`: `repoURL` + `chart` + `targetRevision`; a `repoURL` without scheme is an OCI registry", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "sveltos", What: "`ClusterProfile` and `Profile` (`config.projectsveltos.io`) `spec.helmCharts[]`, `ClusterPromotion` `spec.profileSpec.helmCharts[]` and `EventTrigger` (`lib.projectsveltos.io`) `spec.helmCharts[]`: `chartVersion` of `chartName` (without its `<repositoryName>/` prefix) in `repositoryURL`; an `oci://` `repositoryURL` is the chart reference itself", Versioning: "`semver`", Datasources: []string{"helm"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "version range", When: "anything but a full `MAJOR.MINOR.PATCH[-pre][+build]` (optionally `v`-prefixed): `~1.2.0`, `^1`, `>=1.0.0`, `1.2.x`, `1.2`"},
		{Reason: "no version", When: "the version is missing or empty"},
		{Reason: "template value", When: "the version (or a resource's chart name, `repoURL` or `repositoryURL`) contains `{{`"},
		{Reason: "local chart", When: "Chart.yaml: empty or `file://` repository; helmfile: a chart path (`./x`, `../x`, `/x`, no `/`); k0s: a chart path (`/x`, `./x`, `*.tgz`)"},
		{Reason: "repository alias", When: "Chart.yaml: `@name` or `alias:name` repositories (resolved by the user's Helm repository config)"},
		{Reason: "unknown chart repository <repo>", When: "Chart.yaml: a repository that is no URL; helmfile: a `<repo>/` prefix not in `repositories[]`; k0s: an alias no scanned ClusterConfig declares"},
		{Reason: "ambiguous chart repository <alias>", When: "k0s: ClusterConfigs in other files declare the alias with different URLs (the chart's own file declares none)"},
		{Reason: "invalid chart reference", When: "helmfile and k0s: `oci://` without a path, or `<repo>/a/b`; k0s: a chart name without `/`"},
		{Reason: "unknown HelmRepository <namespace>/<name>", When: "flux: no scanned HelmRepository matches the `sourceRef`"},
		{Reason: "ambiguous HelmRepository <name>", When: "flux: the `sourceRef` matches HelmRepositories with different URLs"},
		{Reason: "chart from <kind> source", When: "flux: the `sourceRef` is a `GitRepository` or `Bucket`"},
		{Reason: "no sourceRef", When: "flux: the HelmRelease chart has no `sourceRef` kind"},
		{Reason: "pinned by digest", When: "flux: the OCIRepository selects `spec.ref.digest`"},
		{Reason: "no repoURL", When: "argocd: a Helm source without `repoURL`"},
		{Reason: "no repositoryURL", When: "sveltos: a `helmCharts[]` entry without `repositoryURL`"},
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
		{Title: "Kubernetes resources", Items: []string{
			"Repository references are resolved across files once every file was extracted: a k0s alias from any ClusterConfig (its own file first), a HelmRelease `sourceRef` by namespace and name (the namespace defaults to the HelmRelease's). When one side has no namespace (a Kustomization sets it), the name alone must match a single URL.",
			"A Flux `OCIRepository` (`spec.ref.semver` skips as `version range`) that neither a HelmRelease `chartRef` names nor declares the Helm chart media type is a Kustomization artifact, not a dep.",
			"HelmReleases with a `chartRef` carry no version; the OCIRepository does. Container images in the same files (values) are found by the docker manager.",
		}},
		{Title: "Replacements", Body: "A chart is [replaced](configuration.md#rulesreplacementname) by a chart name (same repository), or `<http(s):// or oci:// URL>/<chart>` (or `host/path/<chart>` for OCI) to move it, where the repository is written next to the chart (Chart.yaml `repository`, Argo CD `repoURL`, Sveltos `repositoryURL`, Flux OCIRepository `url`, k0s `url`, `oci://` references); alias and `sourceRef` repositories only allow a new chart name, and Sveltos `oci://` charts, named in both `repositoryURL` and `chartName`, none. `Chart.lock` entries follow the renamed or moved chart."},
		{Title: "Edits", Body: "The version is replaced in place."},
	},
}
