// Package devcontainer extracts the container image (`image`), the Dev Container Features
// (`features` keys, OCI references) and the tool versions of the node, go, python and ruby
// features (`version` options) from devcontainer.json files, which are JSON with comments.
package devcontainer

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "devcontainer"

// Dep types: the image and the features as Renovate names them, and the tool version option
// of a known feature.
const (
	DepTypeImage          = "image"
	DepTypeFeature        = "feature"
	DepTypeFeatureVersion = "feature-version"
)

// fileRe matches the locations the Dev Container spec reads: `.devcontainer.json`,
// `.devcontainer/devcontainer.json` and `.devcontainer/<folder>/devcontainer.json`.
var fileRe = regexp.MustCompile(`(^|/)(\.devcontainer\.json|\.devcontainer/([^/]+/)?devcontainer\.json)$`)

// featureTools maps the features whose `version` option is a tool version to the tool
// (toolversions.ToolDep), as Renovate's devcontainer manager does.
var featureTools = map[string]string{
	"ghcr.io/devcontainers/features/node":   "node",
	"ghcr.io/devcontainers/features/go":     "go",
	"ghcr.io/devcontainers/features/python": "python",
	"ghcr.io/devcontainers/features/ruby":   "ruby",
}

type devcontainerManager struct{}

// New returns the devcontainer manager.
func New() manager.Manager { return devcontainerManager{} }

func (devcontainerManager) Name() string { return Name }

// Match reports whether the slash path is a devcontainer.json at a location the spec reads.
func (devcontainerManager) Match(path string) bool {
	return strings.HasSuffix(path, "devcontainer.json") && fileRe.MatchString(path)
}

// Extract returns the image, feature and feature version deps of src. Comments and trailing
// commas are blanked first (jsonc.Strip), so offsets are those of src.
func (devcontainerManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	clean := jsonc.Strip(src)
	x := extractor{file: file, src: clean, dec: jsontext.NewDecoder(bytes.NewReader(clean), jsontext.AllowDuplicateNames(true)), line: 1, aliases: env.RegistryAliases}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	return x.deps, nil
}

// Edits rewrites the image and feature references like the docker manager, and replaces a
// feature's version option in place.
func (devcontainerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.DepType != DepTypeFeatureVersion {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("feature version options cannot be pinned to a digest")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for the image and the features, like the docker manager.
func (devcontainerManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.DepType == DepTypeFeatureVersion {
		return d, errors.New("feature versions cannot be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	file    string
	src     []byte // src with comments and trailing commas blanked
	dec     *jsontext.Decoder
	aliases map[string]string // config registryAliases
	deps    []dep.Dep

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	if err := x.open(); err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, _, err := x.str()
		if err != nil {
			return err
		}
		switch {
		case key == "image" && x.dec.PeekKind() == '"':
			err = x.image()
		case key == "features" && x.dec.PeekKind() == '{':
			err = x.features()
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	if _, err := x.dec.ReadToken(); err != nil {
		return err
	}
	switch _, err := x.dec.ReadToken(); {
	case err == nil:
		return errors.New("unexpected data after the top-level object")
	case errors.Is(err, io.EOF):
		return nil
	default:
		return err
	}
}

// open reads the `{` of an object.
func (x *extractor) open() error {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return errors.New("not an object")
	}
	return nil
}

// str reads a string token (a member name or a string value) and returns it with the span of
// its content, or the zero span when the raw bytes differ from it (escapes).
func (x *extractor) str() (string, dep.Span, error) {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", dep.Span{}, err
	}
	s := tok.String()
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	if start >= 0 && x.src[start] == '"' && x.src[end-1] == '"' && string(x.src[start+1:end-1]) == s {
		return s, dep.Span{Start: start + 1, End: end - 1}, nil
	}
	return s, dep.Span{}, nil
}

// lineAt returns the 1-based line of offset off (non-decreasing across calls).
func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}

// lineOf returns the line of a string just read at span: its start, or the decoder's offset
// when the string could not be located.
func (x *extractor) lineOf(span dep.Span) int {
	if span == (dep.Span{}) {
		return x.lineAt(int(x.dec.InputOffset()))
	}
	return x.lineAt(span.Start)
}

// ref returns the docker-datasource dep of the image or feature reference ref at span. A
// feature without tag or digest is skipped: features are never pinned.
func (x *extractor) ref(depType, ref string, span dep.Span) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: depType, File: x.file, Line: x.lineOf(span), Name: ref}
	off := span.Start
	if span == (dep.Span{}) {
		off = -1
	}
	docker.ParseImage(&d, off, ref, x.aliases)
	if d.Skip == "" && depType == DepTypeFeature && d.Digest == "" && docker.Untagged(d) {
		d = dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: depType, File: x.file, Line: d.Line, Name: d.Name, Source: d.Source, Skip: "no tag"}
	}
	return d
}

func (x *extractor) image() error {
	ref, span, err := x.str()
	if err != nil {
		return err
	}
	x.deps = append(x.deps, x.ref(DepTypeImage, ref, span))
	return nil
}

// features reads the `features` object: each key is a feature reference, its value the
// feature's options (an object, or a string that is the `version` option).
func (x *extractor) features() error {
	if err := x.open(); err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		id, span, err := x.str()
		if err != nil {
			return err
		}
		var d dep.Dep
		if skip := featureSkip(id); skip != "" {
			d = dep.Dep{Manager: Name, Datasource: "docker", DepType: DepTypeFeature, File: x.file, Line: x.lineOf(span), Name: id, Skip: skip}
		} else {
			d = x.ref(DepTypeFeature, id, span)
		}
		x.deps = append(x.deps, d)
		tool, ok := featureTools[d.Name]
		if !ok {
			if err := x.dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		if err := x.featureVersion(tool, d.Line); err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// featureVersion reads the options of a feature whose `version` option is tool's version and
// records it; a feature without one gets a skipped dep on the feature's line.
func (x *extractor) featureVersion(tool string, line int) error {
	var (
		raw  string
		span dep.Span
		err  error
		set  bool
	)
	switch x.dec.PeekKind() {
	case '"':
		raw, span, err = x.str()
		set = true
	case '{':
		raw, span, set, err = x.versionOption()
	default:
		err = x.dec.SkipValue()
	}
	if err != nil {
		return err
	}
	var d dep.Dep
	switch {
	case !set:
		d = toolversions.ToolDep(Name, DepTypeFeatureVersion, x.file, line, tool, "", 0)
		d.Skip = "no version"
	case span == dep.Span{}:
		d = toolversions.ToolDep(Name, DepTypeFeatureVersion, x.file, x.lineOf(span), tool, raw, 0)
		d.Skip, d.RefSpan = "cannot locate value", dep.Span{}
	default:
		d = toolversions.ToolDep(Name, DepTypeFeatureVersion, x.file, x.lineAt(span.Start), tool, raw, span.Start)
	}
	x.deps = append(x.deps, d)
	return nil
}

// versionOption reads an options object and returns its string `version` member; set is
// false when there is none.
func (x *extractor) versionOption() (raw string, span dep.Span, set bool, err error) {
	if err := x.open(); err != nil {
		return "", dep.Span{}, false, err
	}
	for x.dec.PeekKind() != '}' {
		key, _, err := x.str()
		if err != nil {
			return "", dep.Span{}, false, err
		}
		if key == "version" && x.dec.PeekKind() == '"' {
			if raw, span, err = x.str(); err != nil {
				return "", dep.Span{}, false, err
			}
			set = true
			continue
		}
		if err := x.dec.SkipValue(); err != nil {
			return "", dep.Span{}, false, err
		}
	}
	_, err = x.dec.ReadToken()
	return raw, span, set, err
}

// featureSkip returns why feature reference id is no OCI reference the docker datasource
// looks up: a local folder, a tarball URL, or a legacy reference without a registry host
// (`docker-in-docker`, `owner/repo/feature@v1` GitHub releases); "" = an OCI reference.
func featureSkip(id string) string {
	switch {
	case strings.HasPrefix(id, "./") || strings.HasPrefix(id, "../") || strings.HasPrefix(id, "/"):
		return "local feature"
	case strings.HasPrefix(id, "https://") || strings.HasPrefix(id, "http://"):
		return "tarball feature"
	}
	host, _, ok := strings.Cut(id, "/")
	if !ok || host != "localhost" && !strings.ContainsAny(host, ".:") {
		return "not an OCI reference"
	}
	return ""
}
