// Package custom implements the user-defined managers of the `custom` config section.
//
// Annotation mode (no `match`): a comment `deps: <datasource> <name> [versioning=<scheme>]`
// or a Renovate marker `renovate: datasource=<ds> depName=<name> [packageName=<p>]
// [versioning=<scheme>] [registryUrl=<url>] [extractVersion=<RE2>]` annotates the next line, whose first version
// token is the dependency version. For the docker datasource the next line's first image
// reference with a tag wins instead: its tag is the version and a following `@sha256:` digest
// is captured, so tag and digest update together; a non-version tag (`latest`) with a digest
// keeps its tag and only gets its digest refreshed. Helm charts are named
// `<repository>/<chart>`, `deps: helm <repository> <chart>` or (markers) by `registryUrl`.
// Regex mode: an RE2 `match` with a required `version` (alias `currentValue`) group and
// optional `name` (aliases `depName`, `packageName`), `datasource`, `versioning` and `digest`
// (alias `currentDigest`) groups is run over the whole file; every match is a dependency.
// JSONata mode: a `jsonata` query runs over the file parsed as `fileFormat` and returns dep
// objects; see jsonata.go for how their versions are found in the file.
//
// The entry's `versioning` accepts every scheme including `regex:<RE2>`; the versioning
// fields of markers and the `versioning` group accept the scheme names (config.Versionings),
// since a regex does not fit a marker field. Renovate datasource names are mapped to deps
// names (see config.DatasourceAliases); `custom.<name>` is accepted when customDatasources
// defines name.
package custom

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/recolabs/gnata"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	debds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/deb"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/galaxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubrunners"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	sbtds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/sbt"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom/customdoc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pattern"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name shared by every custom manager. It and the dependency types are
// declared with the manager's reference docs in customdoc, which the manager registry (and
// through it config) imports without this package's dependency on config.
const Name = customdoc.Name

// Dependency types of custom deps.
const (
	DepTypeAnnotation = customdoc.DepTypeAnnotation
	DepTypeRegex      = customdoc.DepTypeRegex
	DepTypeJSONata    = customdoc.DepTypeJSONata
)

var (
	// annotationRe finds `deps: <datasource> <name>` right after a comment marker, with an
	// optional `versioning=<scheme>` field.
	annotationRe = regexp.MustCompile(`(?:#|//|--|;|<!--)[ \t]*deps:[ \t]+(\S+)[ \t]+(\S+)(?:[ \t]+versioning=([A-Za-z0-9_-]+))?`)
	// renovateRe finds a Renovate marker `renovate: <key=value>...` after a comment marker.
	renovateRe = regexp.MustCompile(`(?:#|//|--|;|<!--)[ \t]*renovate:[ \t]+(\S.*)`)
	// dockerRefRe is an image reference with a tag and an optional sha256 digest; the tag is
	// the part after the last ':' (group 1), the digest group 2.
	dockerRefRe = regexp.MustCompile(`[a-z0-9./:_-]+:([A-Za-z0-9._-]+)(?:@(sha256:[0-9a-f]{64}))?`)
	// versionRe is a version token: an optional "v", at least two dot-separated numbers and an
	// optional "-"/"+" suffix of alphanumeric components separated by ".", "-" or "+"
	// (1.2, v1.2.3, 1.22-alpine, 2.0.0-rc.1+build.5). Boundaries are checked by firstVersion.
	versionRe = regexp.MustCompile(`v?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z]+(?:[.+-][0-9A-Za-z]+)*)?`)
	// debVersionRe is a run of Debian version characters from a digit; firstDebVersion
	// checks it.
	debVersionRe = regexp.MustCompile(`\d[0-9A-Za-z.+~:-]*`)
)

type customManager struct {
	known      func(datasource string) bool // config.Config.KnownDatasource
	files      *pattern.List
	re         *regexp.Regexp    // nil = annotation or JSONata mode
	query      *gnata.Expression // nil = annotation or regex mode
	format     string            // fileFormat of query
	datasource string
	name       string
	versioning string              // the entry's; "" = per dep, config.DefaultCustomVersioning
	registry   string              // registryUrl of the entry; "" = none
	checksum   *dep.ChecksumSource // nil = no checksum group
	// submatch group numbers in re; -1 = absent
	gVersion, gName, gDepName, gPackageName, gDatasource, gVersioning, gDigest, gChecksum, gRegistryURL int
}

// registryURLDatasources are the datasources whose deps take a registryUrl (entry field,
// regex group or Renovate marker) as Source: whitespace-separated registry URLs. Other
// datasources keep an empty Source.
var registryURLDatasources = []string{"helm", "gitlab-packages", "rpm", "apk", debds.Name, "artifactory", forge.GitLabReleases, forge.GitLabTags, forge.GiteaReleases, forge.GiteaTags, forge.ForgejoReleases, forge.ForgejoTags, "endoflife-date",
	galaxy.CollectionName, mavends.Name, sbtds.PackageName, sbtds.PluginName}

// setRegistry sets d.Source to registry when d's datasource takes a registry URL.
func setRegistry(d *dep.Dep, registry string) {
	if registry != "" && slices.Contains(registryURLDatasources, d.Datasource) {
		d.Source = registry
	}
}

// New returns one manager per cfg.Custom entry, in order. Entries are validated again here so
// the managers are usable with configs that did not go through config.Load.
func New(cfg *config.Config) ([]manager.Manager, error) {
	ms := make([]manager.Manager, 0, len(cfg.Custom))
	var errs []error
	for i, c := range cfg.Custom {
		m, err := newManager(c, cfg.KnownDatasource)
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

func newManager(c config.Custom, known func(string) bool) (*customManager, error) {
	if len(c.Files) == 0 {
		return nil, errors.New("files: must not be empty")
	}
	files, err := pattern.Compile(c.Files)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	m := &customManager{known: known, files: files, datasource: c.Datasource, name: c.Name, versioning: c.Versioning,
		registry: c.RegistryURL,
		gVersion: -1, gName: -1, gDepName: -1, gPackageName: -1, gDatasource: -1, gVersioning: -1, gDigest: -1, gChecksum: -1, gRegistryURL: -1}
	if m.versioning != "" {
		if _, err := versioning.Parse(m.versioning); err != nil {
			return nil, fmt.Errorf("versioning: %w", err)
		}
	}
	if m.datasource != "" && !known(m.datasource) {
		return nil, fmt.Errorf("datasource: unknown %q", m.datasource)
	}
	if c.JSONata != "" {
		return m.withJSONata(c)
	}
	if c.FileFormat != "" {
		return nil, errors.New("fileFormat: requires jsonata")
	}
	if c.Match == "" {
		return m, nil
	}
	re, err := regexp.Compile(c.Match)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	m.re = re
	m.gVersion = subexp(re, "version", "currentValue")
	m.gName = subexp(re, "name")
	m.gDepName = subexp(re, "depName")
	m.gPackageName = subexp(re, "packageName")
	m.gDatasource = subexp(re, "datasource")
	m.gVersioning = subexp(re, "versioning")
	m.gDigest = subexp(re, "digest", "currentDigest")
	m.gChecksum = subexp(re, "checksum")
	m.gRegistryURL = subexp(re, "registryUrl")
	switch {
	case m.gVersion < 0:
		return nil, errors.New("match: must have a (?P<version>...) group or a (?P<currentValue>...) group")
	case m.datasource == "" && m.gDatasource < 0:
		return nil, errors.New("datasource: required unless match has a (?P<datasource>...) group")
	case m.name == "" && m.gName < 0 && m.gDepName < 0 && m.gPackageName < 0:
		return nil, errors.New("name: required unless match has a (?P<name>...) group")
	case (c.Checksum != nil) != (m.gChecksum >= 0):
		return nil, errors.New("checksum: requires a (?P<checksum>...) group in match and vice versa")
	}
	if c.Checksum != nil {
		m.checksum = &dep.ChecksumSource{Asset: c.Checksum.Asset, URL: c.Checksum.URL, File: c.Checksum.File}
	}
	return m, nil
}

// subexp returns the index of the first present named group, or -1.
func subexp(re *regexp.Regexp, names ...string) int {
	for _, n := range names {
		if i := re.SubexpIndex(n); i >= 0 {
			return i
		}
	}
	return -1
}

func (*customManager) Name() string { return Name }

// Match reports whether the slash path matches the entry's files patterns.
func (m *customManager) Match(path string) bool { return m.files.Match(path) }

// Extract returns the annotated, regex-matched or JSONata-selected deps of src.
func (m *customManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	switch {
	case m.query != nil:
		return m.jsonata(file, src)
	case m.re == nil:
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
		// Both regexps need a case-sensitive literal marker; skip them on lines without it.
		var ann, ren []int
		if bytes.Contains(line, []byte("deps:")) {
			ann = annotationRe.FindSubmatchIndex(line)
		}
		if ann == nil && bytes.Contains(line, []byte("renovate:")) {
			ren = renovateRe.FindSubmatchIndex(line)
		}

		if pending != nil {
			d := *pending
			pending = nil
			d.Line = lineNo
			switch {
			case d.Skip != "":
			case ann != nil || ren != nil: // an annotation line is never an annotated line
				d.Skip = "no version on next line"
			default:
				if !nextLineVersion(&d, line, pos) {
					d.Skip = "no version on next line"
				}
			}
			deps = append(deps, d)
		}
		switch {
		case ann != nil:
			ds := string(line[ann[2]:ann[3]])
			d := dep.Dep{Manager: Name, DepType: DepTypeAnnotation, File: file, Line: lineNo,
				Datasource: ds,
				Name:       strings.TrimSuffix(string(line[ann[4]:ann[5]]), "-->"),
				Versioning: m.depVersioning(ds)}
			if ann[6] >= 0 {
				d.Versioning = string(line[ann[6]:ann[7]])
			}
			setRegistry(&d, m.registry)
			if d.Datasource == "helm" || d.Datasource == debds.Name {
				helmChartToken(&d, line[ann[5]:])
			}
			m.classify(&d)
			if d.Skip == "" && !m.knownVersioning(d.Versioning) {
				d.Skip = fmt.Sprintf("unknown versioning %q", d.Versioning)
			}
			pending = &d
		case ren != nil:
			d := m.renovateMarker(file, lineNo, string(line[ren[2]:ren[3]]))
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

// renovateMarker builds the pending dep of a Renovate marker's key=value fields. Name is
// packageName, else depName; registryUrl is the Source of registryURLDatasources deps and
// ignored otherwise, as are unknown keys. Without a versioning field docker deps use docker
// versioning, others the entry's versioning, else their datasource's (depVersioning).
func (m *customManager) renovateMarker(file string, lineNo int, fields string) dep.Dep {
	d := dep.Dep{Manager: Name, DepType: DepTypeAnnotation, File: file, Line: lineNo}
	var depName, pkgName, scheme, registry string
	for f := range strings.FieldsSeq(strings.TrimSuffix(strings.TrimSpace(fields), "-->")) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "datasource":
			d.Datasource = v
		case "depName":
			depName = v
		case "packageName":
			pkgName = v
		case "versioning":
			scheme = v
		case "registryUrl":
			registry = v
		case "extractVersion":
			d.ExtractVersion = v
		}
	}
	d.Name = cmp.Or(pkgName, depName)
	setRegistry(&d, cmp.Or(registry, m.registry))
	switch {
	case d.Datasource == "":
		d.Skip = "renovate marker without datasource"
		return d
	case d.Name == "":
		d.Skip = "renovate marker without depName"
		return d
	}
	m.classify(&d)
	switch {
	case scheme != "":
		d.Versioning = scheme
	case d.Datasource == "docker":
		d.Versioning = "docker"
	default:
		d.Versioning = m.depVersioning(d.Datasource)
	}
	if d.Skip == "" && !m.knownVersioning(d.Versioning) {
		d.Skip = fmt.Sprintf("unknown versioning %q", d.Versioning)
	}
	if _, err := versioning.CompileExtract(d.ExtractVersion); d.Skip == "" && d.ExtractVersion != "" && err != nil {
		d.Skip = "invalid extractVersion: " + err.Error()
	}
	return d
}

// depVersioning is the versioning of a dep of datasource ds that sets none: the entry's, else
// the datasource's default (config.DefaultCustomVersioning: rpm for rpm, apk for apk, else semver).
func (m *customManager) depVersioning(ds string) string {
	if m.versioning != "" {
		return m.versioning
	}
	return config.DefaultCustomVersioning(config.CanonicalDatasource(ds))
}

// knownVersioning reports whether a dep may use versioning v: the entry's own (validated by
// newManager, also a regex scheme) or a scheme name.
func (m *customManager) knownVersioning(v string) bool {
	return v == m.versioning || slices.Contains(config.Versionings, v)
}

// nextLineVersion sets the version (and for docker refs the digest) of d from the annotated
// line starting at offset pos. Docker deps prefer the first tagged image reference, deb deps
// take the first Debian version (firstDebVersion).
func nextLineVersion(d *dep.Dep, line []byte, pos int) bool {
	if d.Datasource == debds.Name {
		s, e, ok := firstDebVersion(line)
		if ok {
			d.Version = string(line[s:e])
			d.RefSpan = dep.Span{Start: pos + s, End: pos + e}
		}
		return ok
	}
	if d.Datasource == "docker" {
		if loc := firstDockerRef(line); loc != nil {
			d.Version = string(line[loc[2]:loc[3]])
			d.RefSpan = dep.Span{Start: pos + loc[2], End: pos + loc[3]}
			if loc[4] >= 0 {
				d.Digest = string(line[loc[4]:loc[5]])
				d.DigestSpan = dep.Span{Start: pos + loc[4], End: pos + loc[5]}
			}
			return true
		}
	}
	s, e, ok := firstVersion(line)
	if ok {
		d.Version = string(line[s:e])
		d.RefSpan = dep.Span{Start: pos + s, End: pos + e}
	}
	return ok
}

// firstDockerRef returns the submatch indices of the first image reference in line that is
// not glued to surrounding characters, or nil.
func firstDockerRef(line []byte) []int {
	for _, loc := range dockerRefRe.FindAllSubmatchIndex(line, -1) {
		if loc[0] > 0 && (isWord(line[loc[0]-1]) || strings.IndexByte("./:@", line[loc[0]-1]) >= 0) {
			continue
		}
		if e := loc[1]; e < len(line) && (isWord(line[e]) || line[e] == '@' || line[e] == '/') {
			continue
		}
		return loc
	}
	return nil
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

// firstDebVersion returns the span of the first Debian version in line (`8.14.1-2`,
// `1:2.39.5-0+deb12u2`, `3.0.15-1~deb12u1`, `20250419`, with epoch and `~`/`+` suffixes the
// generic version token would cut): a run of version characters starting with a digit that is
// not glued to word characters (nor a package-name suffix like `postgresql-15`), without a
// trailing ".", "-" or ":", that is a valid deb version.
func firstDebVersion(line []byte) (int, int, bool) {
	scheme, _ := versioning.Get(debds.Versioning)
	for _, loc := range debVersionRe.FindAllIndex(line, -1) {
		s, e := loc[0], loc[1]
		if s > 0 && (isWord(line[s-1]) || strings.IndexByte(".+~", line[s-1]) >= 0) || e < len(line) && isWord(line[e]) ||
			s > 1 && line[s-1] == '-' && isWord(line[s-2]) {
			continue
		}
		for e > s && strings.IndexByte(".-:", line[e-1]) >= 0 {
			e--
		}
		if scheme.Valid(string(line[s:e])) {
			return s, e, true
		}
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
		d := dep.Dep{Manager: Name, DepType: DepTypeRegex, File: file, Datasource: m.datasource, Name: m.name}
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
		if s, e, ok := group(loc, m.gDepName); ok && e > s {
			d.Name = string(src[s:e])
		}
		if s, e, ok := group(loc, m.gPackageName); ok && e > s {
			d.Name = string(src[s:e])
		}
		if s, e, ok := group(loc, m.gDatasource); ok && e > s {
			d.Datasource = string(src[s:e])
		}
		d.Versioning = m.depVersioning(d.Datasource)
		if s, e, ok := group(loc, m.gVersioning); ok && e > s {
			d.Versioning = string(src[s:e])
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
		registry := m.registry
		if s, e, ok := group(loc, m.gRegistryURL); ok && e > s {
			registry = string(src[s:e])
		}
		setRegistry(&d, registry)
		switch {
		case !ok || ve == vs:
			d.Skip = "empty version"
		case d.Name == "":
			d.Skip = "empty name"
		case d.Datasource == "":
			d.Skip = "empty datasource"
		case m.gChecksum >= 0 && !manager.IsHex(d.Checksum, 64, 128):
			d.Skip = "checksum is not 64 or 128 hex digits"
		default:
			d.Version = string(src[vs:ve])
			d.RefSpan = dep.Span{Start: vs, End: ve}
			m.classify(&d)
			if d.Skip == "" && !m.knownVersioning(d.Versioning) {
				d.Skip = fmt.Sprintf("unknown versioning %q", d.Versioning)
			}
		}
		deps = append(deps, d)
	}
	return deps
}

// classify maps Renovate datasource aliases, checks the datasource and derives Source from
// Name; failures set Skip.
//   - git-tags, git-refs: Name is `https://host/path` or `host/path`; Source is the https
//     URL. The Renovate aliases github-tags and github-digest take `owner/repo` on github.com;
//     azure-tags and bitbucket-server-tags take the repository URL. A git-refs version that
//     is no version is a branch or tag whose commit (the digest) is kept current.
//   - github-releases: Name is `owner/repo` (github.com) or `https://host/owner/repo` (GHES);
//     Source is the repository URL.
//   - go, go-toolchain, docker, npm, node-version, pypi, python-version, dart, dart-version,
//     flutter-version, hex, hexpm-bob: Name is the module path, image or package (hex: `name`
//     or `name:organization`; hexpm-bob: `elixir` or `otp/<os>-<version>`); Source is empty.
//   - helm: Name is `<repository>/<chart>`, or the chart when Source already holds the
//     repository (annotation `deps: helm <repository> <chart>`, Renovate `registryUrl=`); the
//     repository is an `https://`, `http://` or `oci://` URL and becomes Source, Name the chart.
//   - gitlab-packages: Name is `<project>:<package>`; Source is the registryUrl, if any.
//   - rpm: Name is the package; Source is the registryUrl (empty: the datasource reports the
//     missing repository).
//   - apk: Name is the package; Source is the registryUrl (empty: the datasource reports the
//     missing repository).
//   - artifactory: Name is the folder path below Source (Renovate `registryUrl=`), or the full
//     `https://` or `http://` folder URL when Source is empty; slashes around both are trimmed.
//   - gitlab-releases, gitlab-tags, gitea-releases, gitea-tags, forgejo-releases,
//     forgejo-tags: Name is the project path (`owner/repo`, GitLab `group/subgroup/project`)
//     or a repository URL `https://host/path`; Source is the registryUrl, if any
//     (forge.ParseRepo).
//   - endoflife-date: Name is the endoflife.date product; Source is the registryUrl (the API
//     base URL), if any; only its first URL is used.
//   - galaxy: Name is the role `namespace.name`; Source is empty. galaxy-collection: Name is
//     the collection `namespace.name`; Source is the first registryUrl (the Galaxy server), if
//     any.
//   - maven, sbt-package, sbt-plugin: Name is `groupId:artifactId` (mavenSource).
//   - github-runners: Name is the runner name (`ubuntu`); Source is the runner-images
//     repository, as for runs-on labels.
//   - custom.<name> of a defined custom datasource: Name is free-form; Source is empty.
func (m *customManager) classify(d *dep.Dep) {
	ds := d.Datasource
	d.Datasource = config.CanonicalDatasource(ds)
	switch d.Datasource {
	case "git-tags", "git-refs":
		name := d.Name
		if (ds == "github-tags" || ds == "github-digest") && !strings.Contains(name, "://") {
			name = "github.com/" + name
		}
		src, _, ok := repoURL(name)
		if !ok {
			d.Skip = fmt.Sprintf("invalid git-tags name %q", d.Name)
			if d.Datasource == "git-refs" {
				d.Skip = fmt.Sprintf("invalid git-refs name %q", d.Name)
			}
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
		if ds == "github-release-attachments" && d.ChecksumSource == nil && manager.IsHex(d.Digest, 64, 128) {
			// Renovate's currentDigest of github-release-attachments is the sha256 of one of the
			// release's assets, not a digest of the version: keep it current as a checksum.
			d.Checksum, d.ChecksumSpan = d.Digest, d.DigestSpan
			d.ChecksumSource = &dep.ChecksumSource{Attachment: true}
			d.Digest, d.DigestSpan = "", dep.Span{}
		}
	case "helm":
		repo, chart := d.Source, d.Name
		if repo == "" {
			repo, chart, _ = strings.CutLast(d.Name, "/")
		}
		repo = strings.TrimRight(repo, "/")
		if !helmRepository(repo) || chart == "" || strings.Contains(chart, "/") {
			d.Skip = fmt.Sprintf("invalid helm name %q", strings.TrimPrefix(d.Source+" "+d.Name, " "))
			return
		}
		d.Source, d.Name = repo, chart
	case forge.GitLabReleases, forge.GitLabTags, forge.GiteaReleases, forge.GiteaTags, forge.ForgejoReleases, forge.ForgejoTags:
		if _, err := forge.ParseRepo(d.Datasource, d.Name, d.Source); err != nil {
			d.Skip = err.Error()
		}
	case "go", "go-toolchain", "docker", "npm", "node-version", "pypi", "python-version", "gitlab-packages", "rpm", "apk", "dart", "dart-version", "flutter-version", "hex", "hexpm-bob", galaxy.Name:
	case galaxy.CollectionName:
		if f := strings.Fields(d.Source); len(f) > 0 {
			if !httpURL(f[0]) {
				d.Skip = fmt.Sprintf("invalid galaxy-collection registry URL %q", f[0])
				return
			}
			d.Source = f[0]
		}
	case mavends.Name, sbtds.PackageName, sbtds.PluginName:
		mavenSource(d)
	case githubrunners.Name:
		d.Source = githubrunners.SourceURL
	case "endoflife-date":
		if f := strings.Fields(d.Source); len(f) > 0 && !httpURL(f[0]) {
			d.Skip = fmt.Sprintf("invalid endoflife-date registry URL %q", f[0])
		}
	case debds.Name:
		if d.Name == "" || strings.ContainsAny(d.Name, " \t/") {
			d.Skip = fmt.Sprintf("invalid deb name %q", d.Name)
			return
		}
		for r := range strings.FieldsSeq(d.Source) {
			if _, err := debds.ParseRegistry(r); err != nil {
				d.Skip = err.Error()
				return
			}
		}
	case "artifactory":
		urls := strings.Fields(d.Source)
		for i, u := range urls {
			urls[i] = strings.TrimRight(u, "/")
		}
		if len(urls) == 0 {
			d.Name = strings.TrimRight(d.Name, "/")
			urls = []string{""}
		} else {
			d.Name = strings.Trim(d.Name, "/")
		}
		for _, u := range urls {
			if !httpURL(strings.TrimPrefix(u+"/"+d.Name, "/")) || (u != "" && d.Name == "") {
				d.Skip = fmt.Sprintf("invalid artifactory name %q", strings.TrimPrefix(d.Source+" "+d.Name, " "))
				return
			}
		}
		d.Source = strings.Join(urls, " ")
	default:
		if !strings.HasPrefix(d.Datasource, config.CustomDatasourcePrefix) || !m.known(d.Datasource) {
			d.Skip = fmt.Sprintf("unknown datasource %q", ds)
		}
	}
}

// mavenSource turns the registryUrls of a maven, sbt-package or sbt-plugin dep into its
// Source: the `http(s)://` repository URLs with a trailing slash, Maven Central when there
// are none (sbt-plugin: the sbt plugin releases Ivy repository, then Maven Central, as sbt
// resolves plugins). sbt URLs are Maven-layout repositories unless written `ivy=<url>` or
// the sbt plugin releases repository, which are Ivy-layout. Other URLs skip the dep.
func mavenSource(d *dep.Dep) {
	urls := strings.Fields(d.Source)
	sbt := d.Datasource != mavends.Name
	if len(urls) == 0 && d.Datasource == sbtds.PluginName {
		urls = []string{sbtds.IvyID + "=" + sbtds.PluginReleases, mavends.Central}
	}
	for i, u := range urls {
		ivy := false
		if sbt {
			u, ivy = strings.CutPrefix(u, sbtds.IvyID+"=")
		}
		if !httpURL(u) {
			d.Skip = fmt.Sprintf("invalid %s registry URL %q", d.Datasource, urls[i])
			return
		}
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		if sbt && (ivy || u == sbtds.PluginReleases) {
			u = sbtds.IvyID + "=" + u
		}
		urls[i] = u
	}
	d.Source = strings.Join(urls, " ")
}

// helmRepository reports whether repo is a chart repository URL with a host.
func helmRepository(repo string) bool {
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		if rest, ok := strings.CutPrefix(repo, scheme); ok {
			return rest != "" && !strings.HasPrefix(rest, "/")
		}
	}
	return false
}

// httpURL reports whether u is an `https://` or `http://` URL with a host.
func httpURL(u string) bool {
	for _, scheme := range []string{"https://", "http://"} {
		if rest, ok := strings.CutPrefix(u, scheme); ok {
			return rest != "" && !strings.HasPrefix(rest, "/")
		}
	}
	return false
}

// helmChartToken reads the optional name token of `deps: helm <repository> <chart>` or
// `deps: deb <registry> <package>` (with `[versioning=<scheme>]`) from rest, the text after
// the first token: with a name, the first token is the repository.
func helmChartToken(d *dep.Dep, rest []byte) {
	for f := range strings.FieldsSeq(string(rest)) {
		switch {
		case strings.HasPrefix(f, "-->"):
			return
		case strings.HasPrefix(f, "versioning="):
			d.Versioning = strings.TrimPrefix(f, "versioning=")
		case !strings.Contains(f, "=") && d.Source == "":
			d.Source, d.Name = d.Name, strings.TrimSuffix(f, "-->")
		}
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
