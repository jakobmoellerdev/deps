package terraform

import (
	"fmt"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// TerragruntName is the name of the terragrunt manager.
const TerragruntName = "terragrunt"

// DepTypes of the terragrunt manager: the `terraform { source }` of a terragrunt.hcl from
// GitHub, from another git host, or from a module registry (`tfr://`), as Renovate names them,
// from an OCI registry, and the `engine` plugin.
const (
	DepTypeTerragruntGitHub   = "github"
	DepTypeTerragruntGitTags  = "gitTags"
	DepTypeTerragruntRegistry = "terragrunt"
	DepTypeTerragruntOCI      = "oci"
	DepTypeTerragruntEngine   = "engine"
)

var terragruntRe = regexp.MustCompile(`(^|/)terragrunt\.hcl$`)

type terragruntManager struct{}

// NewTerragrunt returns the terragrunt manager.
func NewTerragrunt() manager.Manager { return terragruntManager{} }

func (terragruntManager) Name() string { return TerragruntName }

// Match reports whether the slash path is a terragrunt.hcl outside a .terragrunt-cache
// directory (downloaded sources). The suffix check skips the regexp for the paths that cannot
// match.
func (terragruntManager) Match(p string) bool {
	return strings.HasSuffix(p, "terragrunt.hcl") && terragruntRe.MatchString(p) &&
		!strings.HasPrefix(p, ".terragrunt-cache/") && !strings.Contains(p, "/.terragrunt-cache/")
}

// Extract parses src and returns the source of each top-level terraform block and the engine
// block's plugin. include, dependency and generate blocks are not deps: they name local
// configurations or generate code at run time; includes only resolve `include.<label>.locals`.
func (terragruntManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	b, err := parseHCL(src)
	if err != nil {
		return nil, err
	}
	x := newExtractor(file, src, env)
	if r := strings.TrimSuffix(strings.TrimPrefix(env.TerragruntRegistry, "https://"), "/"); r != "" {
		x.registry = strings.ToLower(r)
	}
	ev := &tgEval{exists: env.Exists, readFile: env.ReadFile}
	var sc *tgScope // the file's locals and includes, collected for the first terraform block
	for _, blk := range b.blocks {
		switch {
		case blk.typ == "terraform" && len(blk.labels) == 0:
			if sc == nil {
				sc = newScope(file, src, b, false)
			}
			x.terragrunt(blk.body, ev, sc)
		case blk.typ == "engine" && len(blk.labels) == 0:
			x.engine(blk.body)
		}
	}
	return x.deps, nil
}

// engine extracts the plugin of an engine block: `source = "github.com/<owner>/<repo>"` with
// the GitHub release `version` Terragrunt downloads (without one it always takes the latest).
func (x *extractor) engine(b body) {
	var source, version *attr
	for i, a := range b.attrs {
		switch a.name {
		case "source":
			source = &b.attrs[i]
		case "version":
			version = &b.attrs[i]
		}
	}
	if source == nil {
		return
	}
	d := x.newDep(TerragruntName, DepTypeTerragruntEngine, source.off)
	d.Datasource, d.Versioning = "github-releases", "semver"
	if s := source.val.str; source.val.kind == kindString && !strings.HasPrefix(s, "github.com/") {
		d.Name, d.Skip = s, "unsupported engine source" // a download URL or local path
		if strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") {
			d.Skip = "local engine"
		}
		x.deps = append(x.deps, d)
		return
	}
	x.deps = append(x.deps, x.plugin(d, "engine", source, version))
}

// Edits replaces the version token and, for a replacement (u.NewName), the registry address.
func (terragruntManager) Edits(u dep.Update) ([]patch.Edit, error) { return edits(u) }

// Rename implements manager.Renamer for `tfr://` sources: name is a registry module address
// `[host/]namespace/name/provider`, with a host when the source names one (`tfr://host/…`) and
// without when it names the default registry (`tfr:///…`).
func (terragruntManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.Datasource != moduleDatasource {
		return d, fmt.Errorf("%s deps cannot be renamed", d.DepType)
	}
	host, path, ok := moduleAddress(name)
	hosted := strings.Count(written(d), "/") == 3 // host/namespace/name/provider
	switch {
	case !ok:
		return d, fmt.Errorf("%q is not a %s address", name, d.Datasource)
	case host != "" && !hosted:
		return d, fmt.Errorf("%q names a registry host, which the written tfr:/// source does not", name)
	}
	return registryRename(d, name, host, path, hosted)
}

// terragrunt extracts `source` of a terraform block: a git source with its `?ref=`, a
// `tfr://` registry module with its `?version=` or the block's `version` constraint, or an
// `oci://` repository with its `?tag=`. A source expression is evaluated as far as tgEval
// can (locals, included locals, path functions); a version written verbatim in this file
// stays editable.
func (x *extractor) terragrunt(b body, ev *tgEval, sc *tgScope) {
	var src, ver *attr
	for i, a := range b.attrs {
		switch a.name {
		case "source":
			src = &b.attrs[i]
		case "version":
			ver = &b.attrs[i]
		}
	}
	if src == nil {
		return
	}
	d := x.newDep(TerragruntName, DepTypeTerragruntRegistry, src.off)
	t, ok := ev.eval(sc, src.val)
	switch {
	case t.local:
		d.Name, d.Skip = "terraform.source", "local module"
		if ok {
			d.Name = t.s
		}
		x.deps = append(x.deps, d)
		return
	case !ok:
		d.Name, d.Skip = "terraform.source", "non-literal source"
		x.deps = append(x.deps, d)
		return
	}
	s := t.s
	d.Name = s
	g, ok := parseGitSource(s)
	if !ok {
		g, ok = parseArchiveSource(s)
	}
	if ok {
		d.DepType = DepTypeTerragruntGitTags
		if strings.HasPrefix(g.name, "github.com/") {
			d.DepType = DepTypeTerragruntGitHub
		}
		x.gitModule(&d, g, t)
		x.deps = append(x.deps, d)
		return
	}
	if strings.HasPrefix(s, "oci://") {
		d.DepType = DepTypeTerragruntOCI
		ociModule(&d, t)
		x.deps = append(x.deps, d)
		return
	}
	tfr, ok := parseTFRSource(s)
	switch {
	case ok:
		x.tfrModule(&d, tfr, t, ver)
	case s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/"):
		d.Skip = "local module"
	default:
		d.Skip = unsupportedSource(s)
	}
	x.deps = append(x.deps, d)
}

// tfrModule fills d from the registry source t whose text is src; without `?version=` the
// version is the constraint of the terraform block's version attribute.
func (x *extractor) tfrModule(d *dep.Dep, t tfrSource, src text, ver *attr) {
	host := t.host
	if host == "" {
		host = x.registry
	}
	d.Datasource, d.Name, d.Source = moduleDatasource, t.name, "https://"+host
	d.NameParts = nameParts(src.lit(t.addrOff, len(t.addr)), len(t.addr))
	var v value
	if t.hasVersion {
		v = src.lit(t.versionOff, len(t.version))
	}
	switch {
	case t.hasVersion && (strings.Contains(t.version, "%") || v.span == (dep.Span{})):
		d.Version, d.Skip = t.version, "cannot locate value"
	case t.hasVersion && !versionRe.MatchString(t.version):
		d.Version, d.Versioning, d.Skip = t.version, "hashicorp", "unsupported constraint"
	case t.hasVersion:
		setConstraint(d, v)
	case ver != nil:
		d.Line = x.lineAt(ver.off)
		setConstraint(d, ver.val)
	default:
		d.Skip = "no version constraint"
	}
}

// tfrSource is a `tfr://` registry module source.
type tfrSource struct {
	host       string // lowercase registry host; empty for `tfr:///` (the default registry)
	name       string // namespace/name/provider
	addr       string // the address as written: host/name, or name for the default registry
	addrOff    int    // offset of addr in the source string
	version    string // the ?version= value
	versionOff int    // offset of version in the source string
	hasVersion bool
}

// parseTFRSource parses `tfr://[host]/<namespace>/<name>/<provider>[//subdir][?version=<v>]`.
func parseTFRSource(s string) (tfrSource, bool) {
	rest, ok := strings.CutPrefix(s, "tfr://")
	if !ok {
		return tfrSource{}, false
	}
	var t tfrSource
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		base := len("tfr://") + q + 1
		for query := rest[q+1:]; query != ""; {
			param, next, _ := strings.Cut(query, "&")
			if v, found := strings.CutPrefix(param, "version="); found {
				t.version, t.versionOff, t.hasVersion = v, base+len("version="), true
				break
			}
			base += len(param) + 1
			query = next
		}
		rest = rest[:q]
	}
	host, path, _ := strings.Cut(rest, "/")
	m := moduleRe.FindStringSubmatch(path)
	if len(m) == 0 || m[1] != "" {
		return tfrSource{}, false
	}
	t.host, t.name = strings.ToLower(host), m[2]
	t.addr, t.addrOff = m[2], len("tfr:///")
	if host != "" {
		t.addr, t.addrOff = host+"/"+m[2], len("tfr://")
	}
	return t, true
}
