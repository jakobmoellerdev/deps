// Package terraform extracts the provider, module, Terraform version, helm_release,
// tfe_workspace and container image deps of Terraform and OpenTofu configurations (*.tf,
// *.tofu and their JSON forms), the .terraform-version/.opentofu-version files, the tflint
// plugins of .tflint.hcl and Terragrunt terraform sources, reads and refreshes
// .terraform.lock.hcl (ReadLocks, Locker), and parses Terraform CLI config credentials.
package terraform

import (
	"fmt"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Manager names.
const (
	Name        = "terraform"
	VersionName = "terraform-version"
	TFLintName  = "tflint-plugin"
)

// DepTypes.
const (
	DepTypeProvider    = "required_provider"
	DepTypeVersion     = "required_version"
	DepTypeModule      = "module"
	DepTypeVersionFile = "terraform-version"
	DepTypePlugin      = "plugin"
)

// Datasource names (internal/datasource/tfregistry).
const (
	providerDatasource = "terraform-provider"
	moduleDatasource   = "terraform-module"
)

// Default registry hosts of provider and module addresses without one: Terraform's, and
// OpenTofu's for *.tofu files (config terraform.defaultRegistry overrides both).
const (
	DefaultRegistry  = "registry.terraform.io"
	OpenTofuRegistry = "registry.opentofu.org"
)

// The Terraform and OpenTofu CLI releases (required_version, version files).
const (
	terraformRepo   = "hashicorp/terraform"
	terraformSource = "https://github.com/hashicorp/terraform"
	opentofuRepo    = "opentofu/opentofu"
	opentofuSource  = "https://github.com/opentofu/opentofu"
)

var (
	// versionRe is a version as Terraform constraints write it.
	versionRe = regexp.MustCompile(`^v?\d+(?:\.\d+){0,2}(?:-[0-9A-Za-z.-]+)?$`)
	// moduleRe is a registry module address `[host/]namespace/name/provider[//subdir]`.
	moduleRe = regexp.MustCompile(`^(?:([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)+(?::\d+)?)/)?([0-9A-Za-z][0-9A-Za-z_-]*/[0-9A-Za-z][0-9A-Za-z_-]*/[0-9A-Za-z][0-9A-Za-z_-]*)(?://.*)?$`)
	// providerPartRe is one part of a provider source address.
	providerPartRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_.:-]*$`)
	shaRe          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	fullRe         = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	partialRe      = regexp.MustCompile(`^v?\d+(?:\.\d+)?$`)
)

type tfManager struct{}

// New returns the terraform manager.
func New() manager.Manager { return tfManager{} }

func (tfManager) Name() string { return Name }

// Match reports whether the slash path is a *.tf, *.tofu, *.tf.json or *.tofu.json file
// outside a .terraform data directory (installed modules).
func (tfManager) Match(p string) bool {
	return matchRe.MatchString(p) && !strings.HasPrefix(p, ".terraform/") && !strings.Contains(p, "/.terraform/")
}

// Extract parses src (HCL or JSON) and returns the required_version and required_providers
// entries of the terraform blocks, module sources, legacy provider block versions and the
// deps of helm_release, tfe_workspace, docker and kubernetes resources.
func (tfManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	b, err := parseConfig(file, src)
	if err != nil {
		return nil, err
	}
	x := newExtractor(file, src, env)
	x.walk(b)
	return x.deps, nil
}

type extractor struct {
	file     string
	src      []byte
	registry string // registry host of hostless addresses
	tofu     bool   // an OpenTofu-only file (*.tofu)
	deps     []dep.Dep
}

// walk extracts the deps of the top-level blocks of a configuration.
func (x *extractor) walk(b body) {
	for _, blk := range b.blocks {
		switch {
		case blk.typ == "terraform" && len(blk.labels) == 0:
			x.terraform(blk.body)
		case blk.typ == "module" && len(blk.labels) == 1:
			x.module(blk)
		case blk.typ == "provider" && len(blk.labels) == 1:
			x.providerBlock(blk, b)
		case blk.typ == "resource" && len(blk.labels) == 2:
			x.resource(blk)
		case blk.typ == "data" && len(blk.labels) == 2:
			x.data(blk)
		}
	}
}

// Edits replaces the version token; image references are rewritten like the docker manager
// rewrites them (tag, digest).
func (tfManager) Edits(u dep.Update) ([]patch.Edit, error) {
	if u.Dep.Datasource == "docker" {
		return docker.New().Edits(u)
	}
	return versionEdit(u)
}

// versionEdit replaces the version token with the new version, written at the precision and
// "v" style of the token for hashicorp constraints (the candidate may come ranked against a
// full locked version: `~> 5.0` becomes `~> 6.2`, not `~> 6.2.1`).
func versionEdit(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.RangeFixed || d.RefSpan == (dep.Span{}) {
		return nil, fmt.Errorf("range %q is not rewritten", d.Range)
	}
	v := u.NewVersion
	if d.Versioning == "hashicorp" {
		v = versioning.FitHashicorp(d.Version, v)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: v}}, nil
}

// newExtractor returns the extractor of file: hostless addresses resolve against
// env.TerraformRegistry, else OpenTofu's registry for *.tofu files, else Terraform's.
func newExtractor(file string, src []byte, env manager.Env) *extractor {
	x := &extractor{file: file, src: src, tofu: isTofu(file), registry: DefaultRegistry}
	switch r := strings.TrimSuffix(strings.TrimPrefix(env.TerraformRegistry, "https://"), "/"); {
	case r != "":
		x.registry = strings.ToLower(r)
	case x.tofu:
		x.registry = OpenTofuRegistry
	}
	return x
}

func (x *extractor) newDep(mgr, depType string, off int) dep.Dep {
	return dep.Dep{Manager: mgr, DepType: depType, File: x.file, Line: lineAt(x.src, off)}
}

// cli returns the CLI repository whose releases required_version constrains.
func (x *extractor) cli() (name, source string) {
	if x.tofu {
		return opentofuRepo, opentofuSource
	}
	return terraformRepo, terraformSource
}

func (x *extractor) terraform(b body) {
	for _, a := range b.attrs {
		if a.name != "required_version" {
			continue
		}
		d := x.newDep(Name, DepTypeVersion, a.off)
		d.Datasource = "github-releases"
		d.Name, d.Source = x.cli()
		setConstraint(&d, a.val)
		x.deps = append(x.deps, d)
	}
	for _, blk := range b.blocks {
		if blk.typ != "required_providers" {
			continue
		}
		for _, a := range blk.body.attrs {
			x.provider(a)
		}
	}
}

// provider extracts a required_providers entry: `name = { source = "...", version = "..." }`
// or the legacy `name = "<constraint>"` of the implied hashicorp/<name>.
func (x *extractor) provider(a attr) {
	d := x.newDep(Name, DepTypeProvider, a.off)
	d.Datasource = providerDatasource
	source, version := "hashicorp/"+a.name, a.val
	switch a.val.kind {
	case kindObject:
		version = value{}
		hasVersion := false
		for _, it := range a.val.obj {
			switch it.name {
			case "source":
				if it.val.kind != kindString {
					d.Name, d.Skip = a.name, "non-literal source"
					x.deps = append(x.deps, d)
					return
				}
				source = it.val.str
			case "version":
				version, hasVersion = it.val, true
			}
		}
		if !hasVersion {
			d.Skip = "no version constraint"
		}
	case kindOther:
		d.Name, d.Skip = a.name, "non-literal value"
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
	if host == "terraform.io" && strings.HasPrefix(name, "builtin/") {
		d.Skip = "builtin provider"
	}
	if d.Skip == "" {
		setConstraint(&d, version)
	}
	x.deps = append(x.deps, d)
}

// providerAddress splits a provider source address `[host/]namespace/type` (a bare type means
// hashicorp/<type>) into its lowercase host (registry when absent) and `namespace/type`.
func providerAddress(s, registry string) (host, name string, ok bool) {
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if !providerPartRe.MatchString(p) {
			return "", "", false
		}
	}
	switch len(parts) {
	case 1:
		return registry, "hashicorp/" + parts[0], true
	case 2:
		return registry, parts[0] + "/" + parts[1], true
	case 3:
		return strings.ToLower(parts[0]), parts[1] + "/" + parts[2], true
	}
	return "", "", false
}

// module extracts the source of a module block: a registry address with its version
// constraint, or a git source with its `?ref=`.
func (x *extractor) module(blk block) {
	var src, ver *attr
	for i, a := range blk.body.attrs {
		switch a.name {
		case "source":
			src = &blk.body.attrs[i]
		case "version":
			ver = &blk.body.attrs[i]
		}
	}
	if src == nil {
		return
	}
	d := x.newDep(Name, DepTypeModule, src.off)
	if src.val.kind != kindString {
		d.Name, d.Skip = blk.labels[0], "non-literal source"
		x.deps = append(x.deps, d)
		return
	}
	s := src.val.str
	d.Name = s
	if g, ok := parseGitSource(s); ok {
		x.gitModule(&d, g, src.val.span)
		x.deps = append(x.deps, d)
		return
	}
	switch m := moduleRe.FindStringSubmatch(s); {
	case strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../"):
		d.Skip = "local module"
	case m != nil:
		host := strings.ToLower(m[1])
		if host == "" {
			host = x.registry
		}
		d.Datasource, d.Name, d.Source = moduleDatasource, m[2], "https://"+host
		if ver == nil {
			d.Skip = "no version constraint"
		} else {
			d.Line = lineAt(x.src, ver.off)
			setConstraint(&d, ver.val)
		}
	default:
		d.Skip = "unsupported module source"
	}
	x.deps = append(x.deps, d)
}

// gitModule fills d from the git source g whose literal is at span.
func (x *extractor) gitModule(d *dep.Dep, g gitSource, span dep.Span) {
	d.Datasource, d.Name, d.Source = "git-tags", g.name, g.repo
	switch {
	case !g.hasRef:
		d.Skip = "no ref"
	case strings.Contains(g.ref, "%"):
		d.Version, d.Skip = g.ref, "cannot locate value"
	case shaRe.MatchString(g.ref):
		d.Version, d.Skip = g.ref, "commit ref"
	case span == (dep.Span{}):
		d.Version, d.Skip = g.ref, "cannot locate value"
	default:
		d.Version, d.Versioning = g.ref, "semver"
		if !fullRe.MatchString(g.ref) && partialRe.MatchString(g.ref) {
			d.Versioning = "tag"
		}
		d.RefSpan = dep.Span{Start: span.Start + g.refOff, End: span.Start + g.refOff + len(g.ref)}
	}
}

// setConstraint sets the version of d from a constraint value. A constraint of the hashicorp
// grammar is the dep's Range, and its first version is the dep's Version. Exact (`1.2.3`,
// `= 1.2.3`), pessimistic (`~> 1.2`) and lower-bound (`>= 1.2`, `> 1.2`) constraints get a
// RefSpan on that version, so the range strategy may rewrite it; ranges with an upper bound,
// an exclusion or several clauses (`< 2`, `!= 1.3`, `>= 1.0, < 2.0`) are never rewritten
// (RangeFixed): only versions locked within them move.
func setConstraint(d *dep.Dep, v value) {
	switch {
	case v.kind != kindString:
		d.Skip = "non-literal value"
		return
	case v.span == (dep.Span{}):
		d.Version, d.Skip = v.str, "cannot locate value"
		return
	}
	d.Version, d.Versioning = v.str, "hashicorp"
	clauses, err := versioning.ParseHashicorpConstraint(v.str)
	if err != nil {
		d.Skip = "unsupported constraint"
		return
	}
	c := clauses[0]
	d.Version, d.Range = c.Version, v.str
	switch c.Op {
	case "", "=", "~>", ">=", ">":
		if len(clauses) == 1 {
			d.RefSpan = dep.Span{Start: v.span.Start + c.Off, End: v.span.Start + c.Off + len(c.Version)}
			return
		}
	}
	d.RangeFixed = true
}
