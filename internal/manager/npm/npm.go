// Package npm extracts dependencies, the packageManager field and the engines/volta tool
// versions from package.json files.
package npm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "npm"

// DepTypes of the packageManager field and of the engines and volta tool versions.
const (
	DepTypePackageManager = "packageManager"
	DepTypeEngines        = "engines"
	DepTypeVolta          = "volta"
)

var (
	matchRe = regexp.MustCompile(`(^|/)package\.json$`)
	// rangeRe is the supported range grammar: an optional ^, ~, >= or = operator and a full
	// MAJOR.MINOR.PATCH[-prerelease] version (group 2).
	rangeRe = regexp.MustCompile(`^(\^|~|>=|=)?(\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)$`)
	// partialRe is an optional operator and a MAJOR[.MINOR] version (group 2), matched with
	// the precision-keeping tag versioning (^26 -> ^27).
	partialRe = regexp.MustCompile(`^(\^|~|>=|=)?(\d+(?:\.\d+)?)$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	tagRe     = regexp.MustCompile(`^[A-Za-z][0-9A-Za-z._-]*$`)
	// shorthandRe is a GitHub `owner/repo[#ref]` shorthand.
	shorthandRe = regexp.MustCompile(`^[^@\s/:][^\s/:]*/[^\s/:]+$`)
)

// sections maps the package.json dependency objects to their DepType.
var sections = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"optionalDependencies": true,
	"peerDependencies":     true,
}

// tools maps the engines/volta tool keys to their datasource.
var tools = map[string]string{"node": "node-version", "npm": "npm", "pnpm": "npm", "yarn": "npm"}

// packageManagers are the tools a packageManager field may name.
var packageManagers = map[string]bool{"npm": true, "pnpm": true, "yarn": true}

type npmManager struct{}

// New returns the npm manager.
func New() manager.Manager { return npmManager{} }

func (npmManager) Name() string { return Name }

// Match reports whether the slash path is a package.json file.
func (npmManager) Match(p string) bool { return matchRe.MatchString(p) }

// Extract streams src with encoding/json and returns one Dep per entry of the dependency
// sections plus the packageManager field. RefSpan covers only the version, so an operator
// (^1.2.3) is preserved. Values the range grammar does not cover are skipped with a reason.
func (npmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	x := extractor{file: file, src: src, dec: json.NewDecoder(bytes.NewReader(src)), line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	return x.deps, nil
}

// Edits replaces the version token.
func (npmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

type extractor struct {
	file string
	src  []byte
	dec  *json.Decoder
	deps []dep.Dep

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	tok, err := x.dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("top level is not an object")
	}
	for x.dec.More() {
		key, err := x.key()
		if err != nil {
			return err
		}
		switch {
		case sections[key]:
			err = x.section(key)
		case key == "packageManager":
			err = x.packageManager()
		case key == DepTypeEngines || key == DepTypeVolta:
			err = x.tools(key)
		default:
			err = skipValue(x.dec)
		}
		if err != nil {
			return err
		}
	}
	_, err = x.dec.Token()
	return err
}

func (x *extractor) key() (string, error) {
	tok, err := x.dec.Token()
	if err != nil {
		return "", err
	}
	k, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("unexpected token %v", tok)
	}
	return k, nil
}

// value reads the next value; ok is false (and the value consumed) when it is not a string.
// span is the content of the quoted string, or the zero span when the raw bytes differ from
// the decoded string (escapes).
func (x *extractor) value() (s string, span dep.Span, ok bool, err error) {
	tok, err := x.dec.Token()
	if err != nil {
		return "", dep.Span{}, false, err
	}
	s, ok = tok.(string)
	if !ok {
		return "", dep.Span{}, false, skipRest(x.dec, tok)
	}
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	if start >= 0 && x.src[start] == '"' && x.src[end-1] == '"' && string(x.src[start+1:end-1]) == s {
		span = dep.Span{Start: start + 1, End: end - 1}
	}
	return s, span, true, nil
}

// lineAt returns the 1-based line of offset off (non-decreasing across calls).
func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}

func (x *extractor) newDep(depType, name, raw string, span dep.Span) dep.Dep {
	off := span.Start
	if span == (dep.Span{}) {
		off = int(x.dec.InputOffset())
	}
	return dep.Dep{
		Manager: Name, Datasource: "npm", Versioning: "semver", DepType: depType,
		File: x.file, Line: x.lineAt(off), Name: name, Version: raw,
	}
}

func (x *extractor) section(depType string) error {
	tok, err := x.dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return skipRest(x.dec, tok)
	}
	for x.dec.More() {
		name, err := x.key()
		if err != nil {
			return err
		}
		raw, span, ok, err := x.value()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := x.newDep(depType, name, raw, span)
		ver, off, skip := classify(raw)
		x.finish(&d, span, ver, off, skip)
		x.deps = append(x.deps, d)
	}
	_, err = x.dec.Token()
	return err
}

// tools extracts the node/npm/pnpm/yarn entries of the engines or volta object; other keys
// are ignored.
func (x *extractor) tools(depType string) error {
	tok, err := x.dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return skipRest(x.dec, tok)
	}
	for x.dec.More() {
		name, err := x.key()
		if err != nil {
			return err
		}
		ds, known := tools[name]
		if !known {
			if err := skipValue(x.dec); err != nil {
				return err
			}
			continue
		}
		raw, span, ok, err := x.value()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := x.newDep(depType, name, raw, span)
		d.Datasource = ds
		ver, off, skip := classify(raw)
		if m := partialRe.FindStringSubmatchIndex(raw); m != nil {
			ver, off, skip = raw[m[4]:m[5]], m[4], ""
			d.Versioning = "tag"
		}
		x.finish(&d, span, ver, off, skip)
		x.deps = append(x.deps, d)
	}
	_, err = x.dec.Token()
	return err
}

// packageManager extracts `<tool>@<version>`; Name is the tool.
func (x *extractor) packageManager() error {
	raw, span, ok, err := x.value()
	if err != nil || !ok {
		return err
	}
	tool, ver, found := strings.Cut(raw, "@")
	name := tool
	if !found || !packageManagers[tool] {
		name = raw
	}
	d := x.newDep(DepTypePackageManager, name, raw, span)
	var skip string
	switch {
	case !found || !packageManagers[tool]:
		skip = "unsupported packageManager"
	case strings.Contains(ver, "+"):
		skip = "packageManager with hash"
	case !versionRe.MatchString(ver):
		skip = "unsupported range"
	}
	x.finish(&d, span, ver, len(tool)+1, skip)
	x.deps = append(x.deps, d)
	return nil
}

// finish sets the version and its span (at off within the value) or the skip reason.
func (*extractor) finish(d *dep.Dep, span dep.Span, ver string, off int, skip string) {
	switch {
	case skip != "":
		d.Skip = skip
	case span == (dep.Span{}):
		d.Skip = "cannot locate value"
	default:
		d.Version = ver
		d.RefSpan = dep.Span{Start: span.Start + off, End: span.Start + off + len(ver)}
	}
}

// classify returns the bare version of a supported range and its offset in v, or the skip
// reason.
func classify(v string) (ver string, off int, skip string) {
	if m := rangeRe.FindStringSubmatchIndex(v); m != nil {
		return v[m[4]:m[5]], m[4], ""
	}
	switch {
	case strings.HasPrefix(v, "npm:"):
		return "", 0, "npm alias"
	case hasAnyPrefix(v, "file:", "link:", "workspace:", "./", "../", "/", "~/"):
		return "", 0, "local dependency"
	case hasAnyPrefix(v, "github:", "gitlab:", "bitbucket:", "gist:", "git+", "git://", "git@"), shorthandRe.MatchString(v):
		return "", 0, "git dependency"
	case hasAnyPrefix(v, "http://", "https://"):
		return "", 0, "url dependency"
	case tagRe.MatchString(v) && !isRangeLike(v):
		return "", 0, "tag"
	}
	return "", 0, "unsupported range"
}

// isRangeLike reports whether a letter-led value is a range rather than a dist-tag: x/X
// wildcards (x, x.1) and v-prefixed versions (v1.2.3).
func isRangeLike(v string) bool {
	switch {
	case v == "x" || v == "X" || strings.HasPrefix(v, "x.") || strings.HasPrefix(v, "X."):
		return true
	case len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9':
		return true
	}
	return false
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// skipValue consumes the next value.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	return skipRest(dec, tok)
}

// skipRest consumes the rest of the value that started with tok.
func skipRest(dec *json.Decoder, tok json.Token) error {
	if tok != json.Delim('{') && tok != json.Delim('[') {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}
