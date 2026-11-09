package terraform

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// DepTypeOpenTofuVersionFile is the DepType of tofuenv's .opentofu-version.
const DepTypeOpenTofuVersionFile = "opentofu-version"

// TerragruntVersionName is the name of the terragrunt-version manager, and
// DepTypeTerragruntVersionFile the DepType of its tgenv .terragrunt-version.
const (
	TerragruntVersionName        = "terragrunt-version"
	DepTypeTerragruntVersionFile = "terragrunt-version"
)

// The Terragrunt CLI releases (.terragrunt-version).
const (
	terragruntRepo   = "gruntwork-io/terragrunt"
	terragruntSource = "https://github.com/gruntwork-io/terragrunt"
)

var (
	versionFileRe           = regexp.MustCompile(`(^|/)\.(terraform|opentofu)-version$`)
	terragruntVersionFileRe = regexp.MustCompile(`(^|/)\.terragrunt-version$`)
	tflintRe                = regexp.MustCompile(`(^|/)\.tflint\.hcl$`)
)

// versionManager reads a single-version file of a tfenv-style version manager: the version on
// its first non-empty line.
type versionManager struct {
	name  string
	match *regexp.Regexp
}

// NewVersion returns the terraform-version manager (tfenv's .terraform-version and tofuenv's
// .opentofu-version).
func NewVersion() manager.Manager { return versionManager{VersionName, versionFileRe} }

// NewTerragruntVersion returns the terragrunt-version manager (tgenv's .terragrunt-version).
func NewTerragruntVersion() manager.Manager {
	return versionManager{TerragruntVersionName, terragruntVersionFileRe}
}

func (m versionManager) Name() string { return m.name }

// Match reports whether the slash path is a version file of the manager. The suffix check
// skips the regexp for the paths that cannot match.
func (m versionManager) Match(p string) bool {
	return strings.HasSuffix(p, "-version") && m.match.MatchString(p)
}

// Extract returns the version on the first non-empty line: a Terraform release, an OpenTofu
// release for .opentofu-version or a Terragrunt release for .terragrunt-version.
// tfenv/tofuenv/tgenv keywords (`latest`, `latest:<regex>`, `min-required`, `latest-allowed`)
// are skipped.
func (m versionManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	depType, name, source := DepTypeVersionFile, terraformRepo, terraformSource
	switch {
	case m.name == TerragruntVersionName:
		depType, name, source = DepTypeTerragruntVersionFile, terragruntRepo, terragruntSource
	case strings.HasSuffix(file, ".opentofu-version"):
		depType, name, source = DepTypeOpenTofuVersionFile, opentofuRepo, opentofuSource
	}
	off := 0
	for off < len(src) {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src) - off
		}
		line := src[off : off+end]
		if t := bytes.TrimSpace(line); len(t) > 0 {
			start := off + bytes.Index(line, t)
			d := dep.Dep{
				Manager: m.name, Datasource: "github-releases", Versioning: "hashicorp", DepType: depType,
				File: file, Line: lineAt(src, start), Name: name, Source: source, Version: string(t),
			}
			if versionRe.Match(t) {
				d.RefSpan = dep.Span{Start: start, End: start + len(t)}
			} else {
				d.Skip = "non-version value"
			}
			return []dep.Dep{d}, nil
		}
		off += end + 1
	}
	return nil, nil
}

// Edits replaces the version.
func (versionManager) Edits(u dep.Update) ([]patch.Edit, error) { return versionEdit(u) }

type tflintManager struct{}

// NewTFLint returns the tflint-plugin manager.
func NewTFLint() manager.Manager { return tflintManager{} }

func (tflintManager) Name() string { return TFLintName }

// Match reports whether the slash path is a .tflint.hcl file. The suffix check skips the regexp
// for the paths that cannot match.
func (tflintManager) Match(p string) bool {
	return strings.HasSuffix(p, ".tflint.hcl") && tflintRe.MatchString(p)
}

// Extract returns the plugin blocks with a `source` (plugins without one are bundled with
// tflint): `github.com/<owner>/<repo>` release versions.
func (tflintManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	b, err := parseHCL(src)
	if err != nil {
		return nil, err
	}
	x := extractor{file: file, src: src}
	for _, blk := range b.blocks {
		if blk.typ != "plugin" || len(blk.labels) != 1 {
			continue
		}
		var source, version *attr
		for i, a := range blk.body.attrs {
			switch a.name {
			case "source":
				source = &blk.body.attrs[i]
			case "version":
				version = &blk.body.attrs[i]
			}
		}
		if source == nil {
			continue
		}
		d := x.newDep(TFLintName, DepTypePlugin, source.off)
		d.Datasource, d.Versioning = "github-releases", "semver"
		x.deps = append(x.deps, x.plugin(d, blk.labels[0], source, version))
	}
	return x.deps, nil
}

// plugin completes the dep of a tflint plugin from its source and version attributes.
func (x *extractor) plugin(d dep.Dep, label string, source, version *attr) dep.Dep {
	d.Name = label
	if source.val.kind != kindString {
		d.Skip = "non-literal source"
		return d
	}
	host, owner, repo, ok := pluginSource(source.val.str)
	if !ok {
		d.Name, d.Skip = source.val.str, "invalid plugin source"
		return d
	}
	d.Name, d.Source = pluginName(host, owner, repo)
	d.NameParts = nameParts(source.val, len(source.val.str))
	if version == nil {
		d.Skip = "no version"
		return d
	}
	d.Line = x.lineAt(version.off)
	switch {
	case version.val.kind != kindString:
		d.Skip = "non-literal value"
	case version.val.span == (dep.Span{}):
		d.Version, d.Skip = version.val.str, "cannot locate value"
	default:
		d.Version, d.RefSpan = version.val.str, version.val.span
	}
	return d
}

// pluginName returns the Name and Source of the plugin released at host/owner/repo.
func pluginName(host, owner, repo string) (name, source string) {
	source = "https://" + host + "/" + owner + "/" + repo
	if host != "github.com" {
		return source, source // GitHub Enterprise Server
	}
	return owner + "/" + repo, source
}

// pluginSource splits `host/owner/repo`.
func pluginSource(s string) (host, owner, repo string, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return "", "", "", false
	}
	for _, p := range parts {
		if !providerPartRe.MatchString(p) {
			return "", "", "", false
		}
	}
	return strings.ToLower(parts[0]), parts[1], parts[2], true
}

// Edits replaces the version and, for a replacement (u.NewName), the source.
func (tflintManager) Edits(u dep.Update) ([]patch.Edit, error) { return edits(u) }

// Rename implements manager.Renamer: name is a plugin source `host/owner/repo` as the source
// attribute writes it (github.com/terraform-linters/tflint-ruleset-aws).
func (tflintManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	host, owner, repo, ok := pluginSource(name)
	if !ok {
		return d, fmt.Errorf("%q is not a plugin source host/owner/repo", name)
	}
	d.Name, d.Source = pluginName(host, owner, repo)
	return d, nil
}
