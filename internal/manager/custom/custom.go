// Package custom implements the user-defined managers of the `custom` config section.
//
// Annotation mode (no `match`): a comment `deps: <datasource> <name> [versioning=<scheme>]`
// annotates the next line, whose first version token is the dependency version.
// Regex mode: an RE2 `match` with a required `version` group and optional `name`,
// `datasource` and `digest` groups is run over the whole file; every match is a dependency.
package custom

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name shared by every custom manager.
const Name = "custom"

// Dependency types of custom deps.
const (
	DepTypeAnnotation = "annotation"
	DepTypeRegex      = "regex"
)

const defaultVersioning = "semver"

var (
	// annotationRe finds `deps: <datasource> <name>` right after a comment marker, with an
	// optional `versioning=<scheme>` field.
	annotationRe = regexp.MustCompile(`(?:#|//|--|;|<!--)[ \t]*deps:[ \t]+(\S+)[ \t]+(\S+)(?:[ \t]+versioning=([A-Za-z0-9_-]+))?`)
	// versionRe is a version token: an optional "v", at least two dot-separated numbers and an
	// optional "-"/"+" suffix of alphanumeric components separated by ".", "-" or "+"
	// (1.2, v1.2.3, 1.22-alpine, 2.0.0-rc.1+build.5). Boundaries are checked by firstVersion.
	versionRe = regexp.MustCompile(`v?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z]+(?:[.+-][0-9A-Za-z]+)*)?`)
)

type customManager struct {
	files      []string
	re         *regexp.Regexp // nil = annotation mode
	datasource string
	name       string
	versioning string
	checksum   *dep.ChecksumSource // nil = no checksum group
	// submatch group numbers in re; -1 = absent
	gVersion, gName, gDatasource, gDigest, gChecksum int
}

// New returns one manager per config entry, in order. Entries are validated again here so the
// managers are usable with configs that did not go through config.Load.
func New(cfg []config.Custom) ([]manager.Manager, error) {
	ms := make([]manager.Manager, 0, len(cfg))
	var errs []error
	for i, c := range cfg {
		m, err := newManager(c)
		if err != nil {
			errs = append(errs, fmt.Errorf("custom[%d]: %w", i, err))
			continue
		}
		ms = append(ms, m)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return ms, nil
}

func newManager(c config.Custom) (*customManager, error) {
	if len(c.Files) == 0 {
		return nil, errors.New("files: must not be empty")
	}
	for _, g := range c.Files {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("files: invalid glob %q", g)
		}
	}
	m := &customManager{files: c.Files, datasource: c.Datasource, name: c.Name, versioning: c.Versioning,
		gVersion: -1, gName: -1, gDatasource: -1, gDigest: -1, gChecksum: -1}
	if m.versioning == "" {
		m.versioning = defaultVersioning
	}
	if !slices.Contains(config.Versionings, m.versioning) {
		return nil, fmt.Errorf("versioning: unknown %q", m.versioning)
	}
	if m.datasource != "" && !slices.Contains(config.CustomDatasources, m.datasource) {
		return nil, fmt.Errorf("datasource: unknown %q", m.datasource)
	}
	if c.Match == "" {
		return m, nil
	}
	re, err := regexp.Compile(c.Match)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	m.re = re
	m.gVersion = re.SubexpIndex("version")
	m.gName = re.SubexpIndex("name")
	m.gDatasource = re.SubexpIndex("datasource")
	m.gDigest = re.SubexpIndex("digest")
	m.gChecksum = re.SubexpIndex("checksum")
	switch {
	case m.gVersion < 0:
		return nil, errors.New("match: must have a (?P<version>...) group")
	case m.datasource == "" && m.gDatasource < 0:
		return nil, errors.New("datasource: required unless match has a (?P<datasource>...) group")
	case m.name == "" && m.gName < 0:
		return nil, errors.New("name: required unless match has a (?P<name>...) group")
	case (c.Checksum != nil) != (m.gChecksum >= 0):
		return nil, errors.New("checksum: requires a (?P<checksum>...) group in match and vice versa")
	}
	if c.Checksum != nil {
		m.checksum = &dep.ChecksumSource{Asset: c.Checksum.Asset, URL: c.Checksum.URL, File: c.Checksum.File}
	}
	return m, nil
}

func (*customManager) Name() string { return Name }

// Match reports whether the slash path matches any of the entry's files globs.
func (m *customManager) Match(path string) bool {
	for _, g := range m.files {
		if doublestar.MatchUnvalidated(g, path) {
			return true
		}
	}
	return false
}

// Extract returns the annotated or regex-matched deps of src.
func (m *customManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if m.re == nil {
		return m.annotations(file, src), nil
	}
	return m.matches(file, src), nil
}

// Edits replaces the version, and the digest and checksum when the dep stores them. The edits
// depend only on the dep's spans, so any custom manager can edit any custom dep (the engine
// picks the first manager named "custom").
func (*customManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	hasDigest := d.DigestSpan.End > d.DigestSpan.Start
	hasChecksum := d.ChecksumSpan.End > d.ChecksumSpan.Start
	switch {
	case u.NewDigest != "" && !hasDigest:
		return nil, errors.New("dependency has no digest to update")
	case u.NewDigest == "" && hasDigest && u.NewVersion != d.Version:
		return nil, errors.New("digest must be updated together with the version, but no digest was resolved")
	case u.NewChecksum != "" && !hasChecksum:
		return nil, errors.New("dependency has no checksum to update")
	case u.NewChecksum == "" && hasChecksum && u.NewVersion != d.Version:
		return nil, errors.New("checksum must be updated together with the version, but no checksum was resolved")
	case u.NewChecksum != "" && len(u.NewChecksum) != len(d.Checksum):
		return nil, errors.New("checksum length mismatch")
	}
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if u.NewDigest != "" && u.NewDigest != d.Digest {
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	}
	if u.NewChecksum != "" && u.NewChecksum != d.Checksum {
		edits = append(edits, patch.Edit{Start: d.ChecksumSpan.Start, End: d.ChecksumSpan.End, Old: d.Checksum, New: u.NewChecksum})
	}
	return edits, nil
}

// annotations scans src line by line for annotation comments.
func (m *customManager) annotations(file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	var pending *dep.Dep // annotation waiting for its next line
	lineNo := 0
	for pos := 0; pos < len(src); {
		lineNo++
		end := bytes.IndexByte(src[pos:], '\n')
		next := len(src)
		if end < 0 {
			end = len(src)
		} else {
			end += pos
			next = end + 1
		}
		line := bytes.TrimSuffix(src[pos:end], []byte{'\r'})
		ann := annotationRe.FindSubmatchIndex(line)

		if pending != nil {
			d := *pending
			pending = nil
			d.Line = lineNo
			switch s, e, ok := firstVersion(line); {
			case d.Skip != "":
			case ann != nil || !ok: // an annotation line is never an annotated line
				d.Skip = "no version on next line"
			default:
				d.Version = string(line[s:e])
				d.RefSpan = dep.Span{Start: pos + s, End: pos + e}
			}
			deps = append(deps, d)
		}
		if ann != nil {
			d := dep.Dep{Manager: Name, DepType: DepTypeAnnotation, File: file, Line: lineNo,
				Datasource: string(line[ann[2]:ann[3]]),
				Name:       strings.TrimSuffix(string(line[ann[4]:ann[5]]), "-->"),
				Versioning: m.versioning}
			if ann[6] >= 0 {
				d.Versioning = string(line[ann[6]:ann[7]])
			}
			classify(&d)
			if d.Skip == "" && !slices.Contains(config.Versionings, d.Versioning) {
				d.Skip = fmt.Sprintf("unknown versioning %q", d.Versioning)
			}
			pending = &d
		}
		pos = next
	}
	if pending != nil {
		if pending.Skip == "" {
			pending.Skip = "no version on next line"
		}
		deps = append(deps, *pending)
	}
	return deps
}

// firstVersion returns the span of the first version token in line that is not glued to
// surrounding word characters.
func firstVersion(line []byte) (int, int, bool) {
	for _, loc := range versionRe.FindAllIndex(line, -1) {
		s, e := loc[0], loc[1]
		if s > 0 && (isWord(line[s-1]) || line[s-1] == '.') {
			continue
		}
		if e < len(line) && (isWord(line[e]) ||
			(line[e] == '.' || line[e] == '-' || line[e] == '+') && e+1 < len(line) && isWord(line[e+1])) {
			continue
		}
		return s, e, true
	}
	return 0, 0, false
}

func isWord(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
}

// matches runs the regex over src; spans come from the submatch indices.
func (m *customManager) matches(file string, src []byte) []dep.Dep {
	locs := m.re.FindAllSubmatchIndex(src, -1)
	deps := make([]dep.Dep, 0, len(locs))
	line, at := 1, 0 // line number of offset at; matches are ordered, so counting is incremental
	group := func(loc []int, g int) (int, int, bool) {
		if g < 0 || loc[2*g] < 0 {
			return 0, 0, false
		}
		return loc[2*g], loc[2*g+1], true
	}
	for _, loc := range locs {
		d := dep.Dep{Manager: Name, DepType: DepTypeRegex, File: file, Datasource: m.datasource, Name: m.name, Versioning: m.versioning}
		vs, ve, ok := group(loc, m.gVersion)
		off := loc[0]
		if ok {
			off = vs
		}
		line += bytes.Count(src[at:off], []byte{'\n'})
		at = off
		d.Line = line
		if s, e, ok := group(loc, m.gName); ok && e > s {
			d.Name = string(src[s:e])
		}
		if s, e, ok := group(loc, m.gDatasource); ok && e > s {
			d.Datasource = string(src[s:e])
		}
		if s, e, ok := group(loc, m.gDigest); ok && e > s {
			d.Digest = string(src[s:e])
			d.DigestSpan = dep.Span{Start: s, End: e}
		}
		if s, e, ok := group(loc, m.gChecksum); ok && e > s {
			d.Checksum = string(src[s:e])
			d.ChecksumSpan = dep.Span{Start: s, End: e}
			d.ChecksumSource = m.checksum
		}
		switch {
		case !ok || ve == vs:
			d.Skip = "empty version"
		case d.Name == "":
			d.Skip = "empty name"
		case d.Datasource == "":
			d.Skip = "empty datasource"
		case m.gChecksum >= 0 && !isChecksum(d.Checksum):
			d.Skip = "checksum is not 64 or 128 hex digits"
		default:
			d.Version = string(src[vs:ve])
			d.RefSpan = dep.Span{Start: vs, End: ve}
			classify(&d)
		}
		deps = append(deps, d)
	}
	return deps
}

// isChecksum reports whether s is a sha256 or sha512 hex digest.
func isChecksum(s string) bool {
	if len(s) != 64 && len(s) != 128 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// classify checks the datasource and derives Source from Name; failures set Skip.
//   - git-tags: Name is `https://host/path` or `host/path`; Source is the https URL.
//   - github-releases: Name is `owner/repo` (github.com) or `https://host/owner/repo` (GHES);
//     Source is the repository URL.
//   - go, docker: Name is the module path or image; Source is empty.
func classify(d *dep.Dep) {
	switch d.Datasource {
	case "git-tags":
		src, _, ok := repoURL(d.Name)
		if !ok {
			d.Skip = fmt.Sprintf("invalid git-tags name %q", d.Name)
			return
		}
		d.Source = src
	case "github-releases":
		name := d.Name
		if !strings.Contains(name, "://") {
			name = "github.com/" + name
		}
		src, segs, ok := repoURL(name)
		if !ok || segs != 2 {
			d.Skip = fmt.Sprintf("invalid github-releases name %q", d.Name)
			return
		}
		d.Source = src
	case "go", "docker":
	default:
		d.Skip = fmt.Sprintf("unknown datasource %q", d.Datasource)
	}
}

// repoURL turns `http(s)://host/path` or `host/path` into a URL without trailing slash and
// returns the number of path segments.
func repoURL(name string) (string, int, bool) {
	raw := name
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", 0, false
	}
	p := strings.Trim(u.Path, "/")
	segs := strings.Split(p, "/")
	if p == "" || slices.Contains(segs, "") {
		return "", 0, false
	}
	return u.Scheme + "://" + u.Host + "/" + p, len(segs), true
}
