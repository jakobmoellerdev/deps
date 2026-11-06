package terraform

import (
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
)

// DepTypes of provider blocks and of resources that are not named after their resource type.
const (
	DepTypeProviderBlock = "provider"
	DepTypeHelmRelease   = "helm_release"
	DepTypeTFEWorkspace  = "tfe_workspace"
)

// kubernetesWorkloadTypes are the kubernetes provider resources whose container and
// init_container blocks (at any depth) name images, in documentation order.
var kubernetesWorkloadTypes = []string{
	"kubernetes_pod", "kubernetes_pod_v1",
	"kubernetes_deployment", "kubernetes_deployment_v1",
	"kubernetes_daemonset", "kubernetes_daemon_set_v1",
	"kubernetes_stateful_set", "kubernetes_stateful_set_v1",
	"kubernetes_job", "kubernetes_job_v1",
	"kubernetes_cron_job", "kubernetes_cron_job_v1",
	"kubernetes_replication_controller", "kubernetes_replication_controller_v1",
}

// kubernetesWorkloads is kubernetesWorkloadTypes as a set.
var kubernetesWorkloads = func() map[string]bool {
	m := make(map[string]bool, len(kubernetesWorkloadTypes))
	for _, t := range kubernetesWorkloadTypes {
		m[t] = true
	}
	return m
}()

// findAttr returns the attribute name of b, or nil.
func findAttr(b body, name string) *attr {
	for i := range b.attrs {
		if b.attrs[i].name == name {
			return &b.attrs[i]
		}
	}
	return nil
}

// providerBlock extracts the legacy `version` of a provider block (no version: no dep) for the
// source of cfg's required_providers entry of the same name, else hashicorp/<name>.
func (x *extractor) providerBlock(blk block, cfg body) {
	ver := findAttr(blk.body, "version")
	if ver == nil {
		return
	}
	d := x.newDep(Name, DepTypeProviderBlock, ver.off)
	d.Datasource = providerDatasource
	source, ok := requiredSource(cfg, blk.labels[0])
	if !ok {
		d.Name, d.Skip = blk.labels[0], "non-literal source"
		x.deps = append(x.deps, d)
		return
	}
	host, name, ok := providerAddress(source, x.registry)
	if !ok {
		d.Name, d.Skip = source, "invalid provider source"
		x.deps = append(x.deps, d)
		return
	}
	d.Name, d.Source = name, "https://"+host
	setConstraint(&d, ver.val)
	x.deps = append(x.deps, d)
}

// requiredSource returns the source of the required_providers entry name in the terraform
// blocks of cfg (name itself when there is none); ok is false for a non-literal source.
func requiredSource(cfg body, name string) (source string, ok bool) {
	for _, tf := range cfg.blocks {
		if tf.typ != "terraform" || len(tf.labels) != 0 {
			continue
		}
		for _, rp := range tf.body.blocks {
			if rp.typ != "required_providers" {
				continue
			}
			a := findAttr(rp.body, name)
			if a == nil || a.val.kind != kindObject {
				continue
			}
			for _, it := range a.val.obj {
				if it.name == "source" {
					return it.val.str, it.val.kind == kindString
				}
			}
		}
	}
	return name, true
}

// resource extracts the deps of a resource block.
func (x *extractor) resource(blk block) {
	switch typ := blk.labels[0]; {
	case typ == DepTypeHelmRelease:
		x.helmRelease(blk)
	case typ == DepTypeTFEWorkspace:
		x.tfeWorkspace(blk)
	case typ == "docker_image":
		x.image(typ, blk.body, "name")
	case typ == "docker_container":
		x.image(typ, blk.body, "image")
	case typ == "docker_service":
		for _, ts := range blk.body.blocks {
			if ts.typ != "task_spec" {
				continue
			}
			for _, cs := range ts.body.blocks {
				if cs.typ == "container_spec" {
					x.image(typ, cs.body, "image")
				}
			}
		}
	case kubernetesWorkloads[typ]:
		x.containers(typ, blk.body)
	}
}

// data extracts the deps of a data block.
func (x *extractor) data(blk block) {
	if typ := blk.labels[0]; typ == "docker_registry_image" {
		x.image(typ, blk.body, "name")
	}
}

// containers extracts the images of the container and init_container blocks within b.
func (x *extractor) containers(depType string, b body) {
	for _, blk := range b.blocks {
		if blk.typ == "container" || blk.typ == "init_container" {
			x.image(depType, blk.body, "image")
			continue
		}
		x.containers(depType, blk.body)
	}
}

// image extracts the image reference of attribute name of b like the docker manager does; the
// literal names the image, which replacements rewrite. Expressions (mostly references to a
// docker_image or a variable) are not deps.
func (x *extractor) image(depType string, b body, name string) {
	a := findAttr(b, name)
	if a == nil || a.val.kind != kindString || a.val.str == "" {
		return
	}
	d := x.newDep(Name, depType, a.off)
	d.Datasource, d.Versioning, d.Name = "docker", "docker", a.val.str
	off := a.val.span.Start
	if a.val.span == (dep.Span{}) {
		off = -1
	}
	docker.ParseImage(&d, off, a.val.str, x.aliases)
	x.deps = append(x.deps, d)
}

// tfeWorkspace extracts the terraform_version of a Terraform Cloud/Enterprise workspace (none:
// no dep).
func (x *extractor) tfeWorkspace(blk block) {
	a := findAttr(blk.body, "terraform_version")
	if a == nil {
		return
	}
	d := x.newDep(Name, DepTypeTFEWorkspace, a.off)
	d.Datasource, d.Name, d.Source = "github-releases", terraformRepo, terraformSource
	setConstraint(&d, a.val)
	x.deps = append(x.deps, d)
}

// helmRelease extracts the chart of a helm_release (no chart: no dep): `chart` with an http(s)
// or oci:// `repository`, or an `oci://host/path/name` chart.
func (x *extractor) helmRelease(blk block) {
	chart := findAttr(blk.body, "chart")
	if chart == nil {
		return
	}
	d := x.newDep(Name, DepTypeHelmRelease, chart.off)
	d.Datasource, d.Versioning = helm.Datasource, helm.Versioning
	if chart.val.kind != kindString {
		d.Name, d.Skip = blk.labels[1], "non-literal value"
		x.deps = append(x.deps, d)
		return
	}
	d.Source, d.Name, d.Skip = helmChart(chart.val.str, findAttr(blk.body, "repository"))
	if d.Skip == "" {
		x.chartVersion(&d, findAttr(blk.body, "version"))
	}
	x.deps = append(x.deps, d)
}

// helmChart resolves a helm_release chart and its repository attribute (nil when absent) to
// the chart's repository URL and name, or a skip reason.
func helmChart(chart string, repo *attr) (source, name, skip string) {
	if rest, ok := strings.CutPrefix(chart, "oci://"); ok {
		path, name, ok := strings.CutLast(rest, "/")
		if !ok || path == "" || name == "" {
			return "", chart, "invalid chart reference"
		}
		return "oci://" + path, name, ""
	}
	switch {
	case strings.HasPrefix(chart, ".") || strings.HasPrefix(chart, "/") || strings.Contains(chart, "://"):
		return "", chart, "local chart"
	case repo == nil || repo.val.kind == kindString && repo.val.str == "":
		if strings.Contains(chart, "/") {
			return "", chart, "repository alias" // `helm repo add` name
		}
		return "", chart, "local chart"
	case repo.val.kind != kindString:
		return "", chart, "non-literal value"
	}
	switch r := repo.val.str; {
	case strings.HasPrefix(r, "oci://") || strings.HasPrefix(r, "https://") || strings.HasPrefix(r, "http://"):
		return strings.TrimRight(r, "/"), chart, ""
	default:
		return "", chart, "unknown chart repository " + r
	}
}

// chartVersion sets d's version from the version attribute ver (nil when absent): an exact
// version, or a range (dep.Dep.Range) rewritten as the helm manager does (helm.ChartVersion).
func (x *extractor) chartVersion(d *dep.Dep, ver *attr) {
	switch {
	case ver == nil:
		d.Skip = "no version"
		return
	case ver.val.kind != kindString:
		d.Line, d.Skip = x.lineAt(ver.off), "non-literal value"
		return
	}
	d.Line, d.Version = x.lineAt(ver.off), ver.val.str
	v, rng, skip := helm.ChartVersion(ver.val.str)
	switch {
	case skip != "":
		d.Skip = skip
	case ver.val.span == (dep.Span{}):
		d.Skip = "cannot locate value"
	default:
		d.Version, d.Range, d.RefSpan = v, rng, ver.val.span
	}
}
