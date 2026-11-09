package helm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the helm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates Helm chart versions in Chart.yaml dependencies (with Chart.lock), Helm v2 requirements.yaml (with requirements.lock), helmfiles, chart custom resources, Rancher Fleet bundles, Helmsman desired state files and vendir configs, from classic chart repositories and OCI registries.",
	Files: []string{
		"`Chart.yaml` (DepType `chart`)",
		"`requirements.y(a)ml` of Helm v2 (`apiVersion: v1`) charts (DepType `requirements`); files without `dependencies` (Ansible Galaxy) yield nothing",
		"`helmfile.y(a)ml`, `helmfile.d/*.y(a)ml`, each optionally `.gotmpl` (DepType `helmfile`)",
		"other `*.y(a)ml` containing the API group of a k0s, Flux, Argo CD, Sveltos or Fleet resource below, parsed document by document (DepTypes `k0s`, `flux`, `argocd`, `sveltos`, `fleet`)",
		"`repositories.yaml` in Helm's repository config format (`repositories[]` with `name` and `url`): no deps, it resolves Chart.yaml repository aliases (below)",
		"`fleet.y(a)ml` Rancher Fleet bundle configs (DepType `fleet`)",
		"Helmsman desired state files: any other `*.y(a)ml` whose top level has a `helmRepos` mapping, or an `apps` mapping and `namespaces` (DepType `helmsman`; Renovate's helmsman matches no file unless `managerFilePatterns` are set; TOML desired state files are not read, as in Renovate)",
		"`vendir.yml` with `apiVersion: vendir.k14s.io/*` and `kind: Config` (DepType `vendir`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "chart", What: "`dependencies[].version` with `name` and `repository` (a URL, or an alias `@name` / `alias:name`, see Repository aliases)", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "requirements", What: "`dependencies[].version` with `name` and `repository`, as in Chart.yaml", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "helmfile", What: "`releases[].version` with `chart: <repo>/<name>` (resolved through `repositories[]`, `oci: true` → `oci://`) or `chart: oci://host/path/name`", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "k0s", What: "k0s `Chart` (`helm.k0sproject.io`) `spec.chartName` + `spec.version`, and ClusterConfig (`k0s.k0sproject.io`, also embedded in a k0sctl `Cluster` as `spec.k0s.config`) `spec.extensions.helm.charts[].chartname` + `version`; the repository is `spec.repository.url`, the `<alias>` of `<alias>/<chart>` from a ClusterConfig's `spec.extensions.helm.repositories[]` in any file, or `oci://host/path/<chart>`", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "flux", What: "`HelmRelease` (`helm.toolkit.fluxcd.io/v2*`) `spec.chart.spec.chart` + `version` from its `sourceRef` `HelmRepository` (`source.toolkit.fluxcd.io`, any file; `spec.type: oci` → `oci://`; none found: the [`registryAliases`](configuration.md#registryaliases) URL of the `sourceRef` name), the `OCIRepository` chart (`spec.url` + `spec.ref.semver`, else `spec.ref.tag`) a `spec.chartRef` names or whose `spec.layerSelector.mediaType` is the Helm chart type, and the `spec.ref.tag` of a `GitRepository` (`spec.url`, named after its path, `git-tags` with `tag` versioning)", Versioning: "`semver`; GitRepository: `tag`", Datasources: []string{"helm", "git-tags"}},
		{Name: "argocd", What: "`Application` `spec.source` / `spec.sources[]` and `ApplicationSet` `spec.template.spec.source(s)` with a `chart`: `repoURL` + `chart` + `targetRevision`; a `repoURL` without scheme is an OCI registry", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "sveltos", What: "`ClusterProfile` and `Profile` (`config.projectsveltos.io`) `spec.helmCharts[]`, `ClusterPromotion` `spec.profileSpec.helmCharts[]` and `EventTrigger` (`lib.projectsveltos.io`) `spec.helmCharts[]`: `chartVersion` of `chartName` (without its `<repositoryName>/` prefix) in `repositoryURL`; an `oci://` `repositoryURL` is the chart reference itself", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "fleet", What: "fleet.yaml `helm.chart` in `helm.repo` (a [`registryAliases`](configuration.md#registryaliases) key maps it, as in Renovate) or an `oci://` chart, at `helm.version`; each `targetCustomizations[]` entry whose `helm` sets `chart`, `repo` or `version` is a dep of its own, `chart` and `repo` defaulting to the bundle's (its version is never inherited); and the `spec.revision` of a `GitRepo` (`fleet.cattle.io`) of `spec.repo`", Versioning: "`semver`; GitRepo: `tag`", Datasources: []string{"helm", "git-tags"}},
		{Name: "helmsman", What: "`apps.<name>.chart` (`<repo>/<chart>` with `<repo>` a `helmRepos` name, or `oci://host/path/<chart>`) at `apps.<name>.version`, also through merge keys (`<<: *template`); a repo the file does not declare resolves to the `helmRepos` URL of the other scanned desired state files (Helmsman composes several), if they agree", Versioning: "`semver`", Datasources: []string{"helm"}},
		{Name: "vendir", What: "`directories[].contents[]`: `helmChart` `name` in `repository.url` (http(s) or `oci://`) at `version`; `git` `url` at `ref` (a version tag, also as `origin/<tag>`); `githubRelease` `slug` at `tag`", Versioning: "`semver`; git, githubRelease: `tag`", Datasources: []string{"helm", "git-tags", "github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unsupported range", When: "a version range deps cannot rewrite: `*`, `!=1.2.3` (see Version ranges); flux: a GitRepository `spec.ref.semver`; vendir: a `refSelection` or `tagSelection`"},
		{Reason: "no version", When: "the version is missing or empty"},
		{Reason: "template value", When: "the version (or a resource's chart name, `repoURL` or `repositoryURL`) contains `{{`"},
		{Reason: "local chart", When: "Chart.yaml, requirements.yaml: empty or `file://` repository; helmfile: a chart path (`./x`, `../x`, `/x`, no `/`); k0s: a chart path (`/x`, `./x`, `*.tgz`); fleet: a chart without `repo` (a path in the bundle); helmsman: a chart path (`./x`, `/x`)"},
		{Reason: "repository alias", When: "Chart.yaml, requirements.yaml: an `@name` or `alias:name` repository nothing resolves (see Repository aliases); the dep's source is the alias as written"},
		{Reason: "unknown chart repository <repo>", When: "Chart.yaml, fleet: a repository that is no http(s) or `oci://` URL; helmfile: a `<repo>/` prefix not in `repositories[]`; k0s: an alias no scanned ClusterConfig declares; helmsman: a repo no scanned desired state file declares, or whose URL is no http(s) or `oci://` URL (`s3://`, `gs://`)"},
		{Reason: "ambiguous chart repository <alias>", When: "k0s: ClusterConfigs in other files declare the alias with different URLs (the chart's own file declares none); helmsman: other desired state files declare the repo with different URLs"},
		{Reason: "invalid chart reference", When: "helmfile, k0s, fleet, helmsman: `oci://` without a path; helmfile, helmsman: `<repo>/a/b`; k0s, helmsman: a chart name without `/`; fleet, vendir: a chart name with `/`"},
		{Reason: "chart archive URL", When: "fleet: a chart given as the URL of a chart archive (`https://…/chart-1.0.0.tgz`)"},
		{Reason: "variable reference", When: "helmsman: `$` in the chart, the version or the repo URL (Helmsman substitutes environment variables)"},
		{Reason: "no repository", When: "vendir: a `helmChart` without `repository.url` (vendir uses the machine's Helm repositories)"},
		{Reason: "unknown HelmRepository <namespace>/<name>", When: "flux: no scanned HelmRepository matches the `sourceRef`"},
		{Reason: "ambiguous HelmRepository <name>", When: "flux: the `sourceRef` matches HelmRepositories with different URLs"},
		{Reason: "chart from <kind> source", When: "flux: the `sourceRef` is a `GitRepository` or `Bucket`: Flux ignores `version` for them and builds the chart at the source's revision, so the GitRepository's `spec.ref.tag` is the dep"},
		{Reason: "no sourceRef", When: "flux: the HelmRelease chart has no `sourceRef` kind"},
		{Reason: "pinned by digest", When: "flux: the OCIRepository selects `spec.ref.digest`"},
		{Reason: "commit ref", When: "flux: a GitRepository with `spec.ref.commit` (Flux checks out the commit, whatever the tag); fleet, vendir: a 40-hex `revision` / `ref`"},
		{Reason: "non-version ref", When: "flux: a GitRepository following `spec.ref.name`, `spec.ref.branch` or no ref (the default branch); fleet: a GitRepo without `spec.revision` (it follows `spec.branch`) or one that is no `v?N[.N[.N]]` tag; vendir: a `ref` or `tag` that is no such tag (`origin/main`, `latest`), or a git source without `ref`"},
		{Reason: "unrecognized git URL", When: "flux, fleet, vendir: a git URL that is no https/ssh/git URL with a host and path"},
		{Reason: "no repoURL", When: "argocd: a Helm source without `repoURL`"},
		{Reason: "no repositoryURL", When: "sveltos: a `helmCharts[]` entry without `repositoryURL`"},
		{Reason: "cannot locate value", When: "the version's bytes could not be found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Versioning", Body: "`semver`: the `v` prefix style of the current value is kept, prereleases are only proposed from a prerelease."},
		{Title: "Version ranges", Items: []string{
			"Helm reads a chart version as a Masterminds constraint: anything but a full `[v]MAJOR.MINOR.PATCH[-pre][+build]` is a range, `~1.2.0`, `^1`, `1.2.x`, `1.2` (= `1.2.x`), `>=1.0.0 <2`, `>= 1.2, < 2` (`,` is AND), `^1 || ^2`, `1.2 - 1.4`, in every file type above and in kustomize `helmCharts` and terraform `helm_release`. The dep's version is the range's lowest version (`^1.2` → 1.2.0).",
			"Ranges are rewritten as Renovate's helm versioning (its npm versioning) does, keeping operators, precision, wildcards and commas: under [`rangeStrategy`](configuration.md#rulesrangestrategy) `auto` (= `replace`, Renovate's default for Helm) a version the range admits is left alone and a newer one replaces it (`~1.2.0` → `~1.3.0`, `1.2.x` → `1.3.x`, `>= 1.2, < 2` → `>= 2.1, < 3`); `bump` raises the floor, `widen` extends the upper bound (`^1.2.0 || ^2.0.0`), `pin` writes the exact version. `update-lockfile` and `in-range-only` act like `replace` and `bump`: deps reads no locked versions.",
			"A Chart.lock entry of a rewritten range takes the update's version.",
		}},
		{Title: "Repository aliases", Items: []string{
			"A Chart.yaml (requirements.yaml) `repository: \"@name\"` or `alias:name` resolves, first found: the [`registryAliases`](configuration.md#registryaliases) entry `name`; the `name` of a scanned `repositories.yaml` (Helm's repository config, as `helm repo add` writes it, committed anywhere in the repository; one URL per name, else it is ambiguous); the URL the sibling Chart.lock (requirements.lock) locks for the chart, which `helm dependency update` resolved; Renovate's default `stable` → `https://charts.helm.sh/stable`. The user's own Helm config (`$HELM_CONFIG_HOME`) is not read, so runs do not depend on the machine.",
			"An alias dep keeps the alias in the file: replacements can rename its chart, not move its repository.",
		}},
		{Title: "Chart.lock and requirements.lock", Items: []string{
			"After Chart.yaml is patched (`plan`, `apply`, `run`), the sibling `Chart.lock` gets the new version of every exact dependency whose locked version differs, matched by position and name.",
			"`digest` is recomputed as Helm does (`sha256` over the JSON of the Chart.yaml and Chart.lock dependencies), so `helm dependency build` accepts the lock. Repository aliases take the URL locked for them.",
			"The old digest is verified first; when deps cannot reproduce it (a lock out of sync), the chart's update fails with an error instead of writing a lock Helm would reject.",
			"A Helm v2 `requirements.yaml` is handled the same way with its sibling `requirements.lock`; its digest is either Helm 2's (`sha256` over the JSON of the requirements alone, aliases as written or resolved) or Helm 3's (as for Chart.lock), whichever reproduces the old digest, and the new digest uses the same.",
			"`generated` is kept, so unchanged updates produce identical commits.",
		}},
		{Title: "Kubernetes resources", Items: []string{
			"Repository references are resolved across files once every file was extracted: a k0s alias from any ClusterConfig (its own file first), a HelmRelease `sourceRef` by namespace and name (the namespace defaults to the HelmRelease's). When one side has no namespace (a Kustomization sets it), the name alone must match a single URL.",
			"A Flux `OCIRepository` that neither a HelmRelease `chartRef` names nor declares the Helm chart media type is a Kustomization artifact, not a dep.",
			"HelmReleases with a `chartRef` carry no version; the OCIRepository does. Container images in the same files (values) are found by the docker manager.",
		}},
		{Title: "Fleet, Helmsman and vendir", Items: []string{
			"Fleet: a bundle without `helm.chart` deploys the bundle directory (no dep); a targetCustomization that only sets values is no dep. A customization's own `chart`, `repo` and version are edited where it writes them; Renovate names a customization's dep after the customization, deps after the chart.",
			"Helmsman: an app template shared by several apps (`<<: *tomcat`) is one dep at the template's `version`. A repo name an app's own file does not declare is resolved once every file was extracted, from the `helmRepos` of the other desired state files of the repository (one URL per name, trailing slashes ignored).",
			"vendir: `vendir.lock.yml` and the synced directories are not updated: run `vendir sync` after the update (Renovate runs it in its artifact step). `http`, `image`, `imgpkgBundle`, `directory` and `manual` contents are no deps.",
		}},
		{Title: "Replacements", Body: "A chart is [replaced](configuration.md#rulesreplacementname) by a chart name (same repository), or `<http(s):// or oci:// URL>/<chart>` (or `host/path/<chart>` for OCI) to move it, where the repository is written next to the chart (Chart.yaml `repository`, Argo CD `repoURL`, Sveltos `repositoryURL`, Flux OCIRepository `url`, k0s `url`, Fleet `repo`, vendir `repository.url`, `oci://` references); alias, Helmsman `helmRepos` and `sourceRef` repositories only allow a new chart name, and Sveltos `oci://` charts, named in both `repositoryURL` and `chartName`, none. `Chart.lock` entries follow the renamed or moved chart."},
		{Title: "Edits", Body: "The version is replaced in place, a range rewritten in place (Version ranges)."},
	},
}
