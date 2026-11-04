package terraform

import (
	"bytes"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// DepTypeOpenTofuVersionFile is the DepType of tofuenv's .opentofu-version.
const DepTypeOpenTofuVersionFile = "opentofu-version"

var (
	versionFileRe = regexp.MustCompile(`(^|/)\.(terraform|opentofu)-version$`)
	tflintRe      = regexp.MustCompile(`(^|/)\.tflint\.hcl$`)
)

type versionManager struct{}

// NewVersion returns the terraform-version manager (tfenv's .terraform-version and tofuenv's
// .opentofu-version).
func NewVersion() manager.Manager { return versionManager{} }

func (versionManager) Name() string { return VersionName }

// Match reports whether the slash path is a .terraform-version or .opentofu-version file.
func (versionManager) Match(p string) bool { return versionFileRe.MatchString(p) }

// Extract returns the version on the first non-empty line: a Terraform release, or an OpenTofu
// release for .opentofu-version. tfenv/tofuenv keywords (`latest`, `latest:<regex>`,
// `min-required`, `latest-allowed`) are skipped.
func (versionManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	depType, name, source := DepTypeVersionFile, terraformRepo, terraformSource
	if strings.HasSuffix(file, ".opentofu-version") {
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
				Manager: VersionName, Datasource: "github-releases", Versioning: "hashicorp", DepType: depType,
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

// Match reports whether the slash path is a .tflint.hcl file.
func (tflintManager) Match(p string) bool { return tflintRe.MatchString(p) }

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
	d.Name, d.Source = owner+"/"+repo, "https://"+host+"/"+owner+"/"+repo
	if host != "github.com" {
		d.Name = d.Source // GitHub Enterprise Server
	}
	if version == nil {
		d.Skip = "no version"
		return d
	}
	d.Line = lineAt(x.src, version.off)
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

// Edits replaces the version.
func (tflintManager) Edits(u dep.Update) ([]patch.Edit, error) { return versionEdit(u) }
