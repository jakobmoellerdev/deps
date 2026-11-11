// Package helm updates Helm chart versions: Chart.yaml and Helm v2 requirements.yaml
// dependencies (and the matching Chart.lock / requirements.lock entries and digest, see Locker), helmfile releases,
// the chart references of Kubernetes custom resources (k0s Chart, Flux HelmRelease, Argo CD
// Application, Sveltos, Fleet GitRepo), Rancher Fleet fleet.yaml bundles, Helmsman desired
// state files and vendir.yml configs. Charts are looked up with the helm datasource in classic
// chart repositories (index.yaml) and OCI registries.
package helm

import (
	"path"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "helm"

// Datasource and Versioning of every helm dep.
const (
	Datasource = "helm"
	Versioning = "semver"
)

// Dep types.
const (
	TypeChart        = "chart"
	TypeRequirements = "requirements"
	TypeHelmfile     = "helmfile"
	TypeK0s          = "k0s"
	TypeFlux         = "flux"
	TypeArgoCD       = "argocd"
	TypeSveltos      = "sveltos"
	TypeFleet        = "fleet"
	TypeHelmsman     = "helmsman"
	TypeVendir       = "vendir"
)

var helmfileRe = regexp.MustCompile(`(^|/)(helmfile\.ya?ml|helmfile\.d/[^/]+\.ya?ml)(\.gotmpl)?$`)

type helmManager struct {
	links links
}

// New returns the helm manager.
func New() manager.Manager { return &helmManager{} }

func (*helmManager) Name() string { return Name }

// Match reports whether the slash path is a Chart.yaml, a helmfile or any other YAML file
// (scanned for chart custom resources).
func (*helmManager) Match(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") || isHelmfile(p)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (*helmManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yaml", ".yml", ".gotmpl"}}
}

func isChart(p string) bool { return path.Base(p) == chartFile }

// isRequirements reports whether the slash path is a Helm v2 requirements.y(a)ml.
func isRequirements(p string) bool {
	b := path.Base(p)
	return b == requirementsFile || b == "requirements.yml"
}

// isHelmfile reports whether the slash path is a helmfile. The name check skips the regexp
// for the paths that cannot match.
func isHelmfile(p string) bool { return strings.Contains(p, "helmfile") && helmfileRe.MatchString(p) }

// Extract returns the chart deps of src with RefSpan on the version. A Helm repository config
// (repositories.yaml) is recorded for Link besides.
func (m *helmManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	switch {
	case isChart(file):
		return chartDeps(file, TypeChart, src, env.RegistryAliases)
	case isRequirements(file):
		return chartDeps(file, TypeRequirements, src, env.RegistryAliases)
	case isHelmfile(file):
		return helmfileDeps(file, src)
	case path.Base(file) == repositoriesFile:
		m.repositoryConfig(file, src)
	case isFleet(file):
		return fleetDeps(file, src, env.RegistryAliases)
	case path.Base(file) == vendirFile:
		return vendirDeps(file, src), nil
	}
	if deps, ok := m.helmsman(file, src); ok {
		return deps, nil
	}
	return m.manifests(file, src, env.RegistryAliases), nil
}

// Edits replaces the version (rewrites a range, see VersionEdits) and, for a replacement
// (u.NewName), the chart name and the repository it moves to (see Rename).
func (*helmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	var edits []patch.Edit
	if u.NewName != "" {
		ne, err := NameEdits(u.Dep, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = ne
	}
	return append(edits, VersionEdits(u)...), nil
}

// NewRange implements manager.RangeRewriter (NewRange).
func (*helmManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return NewRange(d, v, strategy)
}
