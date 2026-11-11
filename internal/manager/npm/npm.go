// Package npm extracts dependencies, the packageManager field and the engines/volta tool
// versions from package.json files, reads the versions package-lock.json locks them to, and
// lists the lock roots lock file maintenance refreshes.
package npm

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "npm"

// DepTypes of the packageManager field, the engines and volta tool versions, and the
// overrides of npm (`overrides`), yarn (`resolutions`) and pnpm (`pnpm.overrides`).
const (
	DepTypePackageManager = "packageManager"
	DepTypeEngines        = "engines"
	DepTypeVolta          = "volta"
	DepTypeOverrides      = "overrides"
	DepTypeResolutions    = "resolutions"
	DepTypePnpmOverrides  = "pnpm.overrides"
)

// skipRange is the skip reason of a value outside npm's range grammar (and of a packageManager
// version that is not a full version).
const skipRange = "unsupported range"

var (
	matchRe = regexp.MustCompile(`(^|/)package\.json$`)
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

// tools maps the engines/volta tool keys to their datasource (volta has no bun).
var tools = map[string]string{"node": "node-version", "npm": "npm", "pnpm": "npm", "yarn": "npm", "bun": "npm"}

// packageManagers are the tools a packageManager field may name.
var packageManagers = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}

type npmManager struct{}

// New returns the npm manager.
func New() manager.Manager { return npmManager{} }

func (npmManager) Name() string { return Name }

// Match reports whether the slash path is a package.json file. The suffix check skips the
// regexp for the paths that cannot match.
func (npmManager) Match(p string) bool {
	return strings.HasSuffix(p, "package.json") && matchRe.MatchString(p)
}

// Extract streams src with encoding/json/jsontext and returns one Dep per entry of the
// dependency sections plus the packageManager field. A dependency-section range is the dep's
// Range and RefSpan covers all of it; Version is its bare version (`^1.2.3` → 1.2.3) or, for
// the rest of npm's range grammar (`||`, x-ranges, partial versions, `<`, hyphen ranges), its
// anchor (npmRange.anchor). For the packageManager field and the engines/volta tools RefSpan
// covers only the version, so an operator (^1.2.3) is preserved; other engines/volta ranges are
// range deps as in the dependency sections (extractor.tools). Values the grammar does not
// cover are skipped with a reason. Duplicate names are accepted, as npm's JSON.parse does.
func (npmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(blankBOM(src)), jsontext.AllowDuplicateNames(true))
	x := extractor{file: file, src: src, dec: dec, line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	return x.deps, nil
}

// bom is the UTF-8 byte order mark, which editors on Windows write before a package.json's
// first byte; npm's JSON.parse skips it.
var bom = []byte("\ufeff")

// blankBOM returns src with a leading byte order mark replaced by blanks, which the decoder
// reads as the whitespace before the document. Blanking rather than trimming keeps every byte
// offset, and so every span, that of src.
func blankBOM(src []byte) []byte {
	if !bytes.HasPrefix(src, bom) {
		return src
	}
	out := slices.Clone(src)
	for i := range bom {
		out[i] = ' '
	}
	return out
}

// Edits rewrites a range (dependency sections, engines/volta ranges) for the new version
// under the update's rangeStrategy (compiledRange.newRange), and replaces the version token of
// the other deps (a git tag), the commit of a git dependency (abbreviated to its length) and the
// Corepack hash of the packageManager field. A replacement (u.NewName) also rewrites the package
// name.
func (npmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	switch {
	case d.Range != "":
		r, ok := compileRange(d.Range)
		if !ok {
			return nil, fmt.Errorf("unsupported range %q", d.Range)
		}
		nv, _ := newVersion(u.NewVersion) // nil when it is not a version: newRange keeps the range
		edits = []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: r.newRange(nv, u.RangeStrategy)}}
	case u.NewVersion != d.Version:
		edits = []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	}
	if u.NewDigest != "" {
		if d.Digest == "" {
			return nil, fmt.Errorf("cannot pin %s: no commit to update", d.Name)
		}
		nd := u.NewDigest
		if len(d.Digest) < len(nd) {
			nd = nd[:len(d.Digest)]
		}
		if !strings.EqualFold(nd, d.Digest) {
			edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: nd})
		}
	}
	edits, err := manager.ChecksumEdits(edits, u)
	if err != nil {
		return nil, err
	}
	if u.NewName != "" {
		ne, err := manager.NameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = append(edits, ne...)
	}
	return edits, nil
}

// NewRange implements manager.RangeRewriter (NewRange).
func (npmManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return NewRange(d.Range, v, strategy)
}

// packageNameRe is npm's package name grammar (validate-npm-package-name for new packages).
var packageNameRe = regexp.MustCompile(`^(?:@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)

// Rename implements manager.Renamer for the entries of the dependency sections.
func (npmManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if !packageNameRe.MatchString(name) || len(name) > 214 {
		return d, fmt.Errorf("invalid package name %q", name)
	}
	d.Name = name
	return d, nil
}

// ReadLocks records the version the lock root's lockfile locks each dependency-section dep to
// (nodetool.ReadLocked), or LockUnread when it cannot be read. npm: aliases, git
// dependencies and overrides are not looked up: the lockfile does not key them by the name.
func (npmManager) ReadLocks(root string, files [][]dep.Dep) []error {
	var wants []nodetool.Want
	var refs []*dep.Dep
	for _, f := range files {
		for j := range f {
			// engines/volta ranges, git dependencies and aliases lock nothing
			if d := &f[j]; d.Range != "" && d.Skip == "" && sections[d.DepType] && d.Datasource == "npm" && !isAlias(*d) {
				wants = append(wants, nodetool.Want{Manifest: d.File, Name: d.Name, Range: d.Range})
				refs = append(refs, d)
			}
		}
	}
	if len(wants) == 0 {
		return nil
	}
	states, errs := nodetool.ReadLocked(root, wants)
	for i, s := range states {
		refs[i].Locked, refs[i].LockUnread = s.Version, s.Unread
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the lockfile the post-processor updates
// (nodetool.Lockfile) in each file's lock root (nodetool.Locker.Scope), with the package.json
// next to it; workspace members share their root's. A root without package.json (only
// pnpm-workspace.yaml) is left out: the package managers need it.
func (npmManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		dir, ok := nodetool.Locker{}.Scope(context.Background(), root, f)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(filepath.Join(abs, "package.json")); err != nil || !st.Mode().IsRegular() {
			continue
		}
		name, err := nodetool.Lockfile(abs)
		if err != nil || name == "" {
			slog.Debug("lock root unreadable", "dir", dir, "err", err)
			continue
		}
		out = append(out, manager.LockRoot{Manifest: path.Join(dir, "package.json"), Lockfile: path.Join(dir, name)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

type extractor struct {
	file string
	src  []byte
	dec  *jsontext.Decoder
	deps []dep.Dep

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return errors.New("top level is not an object")
	}
	for x.dec.PeekKind() != '}' {
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
		case key == DepTypeOverrides:
			err = x.overrides("")
		case key == DepTypeResolutions:
			err = x.flatOverrides(DepTypeResolutions, resolutionName)
		case key == "pnpm":
			err = x.pnpm()
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err = x.dec.ReadToken()
	return err
}

// key reads an object member name.
func (x *extractor) key() (string, error) {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// value reads the next value; ok is false (and the value consumed) when it is not a string.
// span is the content of the quoted string, or the zero span when the raw bytes differ from
// the decoded string (escapes).
func (x *extractor) value() (s string, span dep.Span, ok bool, err error) {
	if x.dec.PeekKind() != '"' {
		return "", dep.Span{}, false, x.dec.SkipValue()
	}
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", dep.Span{}, false, err
	}
	s = tok.String()
	return s, x.span(s), true, nil
}

// span returns the content span of the string token s just read, or the zero span when the
// raw bytes differ from it (escapes).
func (x *extractor) span(s string) dep.Span {
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	if start >= 0 && x.src[start] == '"' && x.src[end-1] == '"' && string(x.src[start+1:end-1]) == s {
		return dep.Span{Start: start + 1, End: end - 1}
	}
	return dep.Span{}
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
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		name, err := x.key()
		if err != nil {
			return err
		}
		nameSpan := x.span(name)
		raw, span, ok, err := x.value()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := x.newDep(depType, name, raw, span)
		if nameSpan != (dep.Span{}) {
			d.NameParts = []dep.NamePart{{Span: nameSpan, Text: name}}
		}
		x.setValue(&d, raw, span)
		x.deps = append(x.deps, d)
	}
	_, err := x.dec.ReadToken()
	return err
}

// tools extracts the node/npm/pnpm/yarn entries of the engines or volta object; other keys
// are ignored. A full version with an optional ^, ~, >= or = keeps its operator and only the
// version is replaced; so is a partial one, under tag versioning. The rest of npm's range
// grammar (`>=18 <27`, `20.x`, `^20 || ^22`, `18 - 22`) is a range dep as in the dependency
// sections (Range, anchored version), rewritten under rangeStrategy.
func (x *extractor) tools(depType string) error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		name, err := x.key()
		if err != nil {
			return err
		}
		ds, known := tools[name]
		if !known {
			if err := x.dec.SkipValue(); err != nil {
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
		isRange := false
		if m := partialRe.FindStringSubmatchIndex(raw); m != nil {
			ver, off, skip = raw[m[4]:m[5]], m[4], ""
			d.Versioning = "tag"
		} else if skip == skipRange {
			if r, ok := parseRange(raw); ok {
				if a := r.anchor(); a != "" {
					ver, off, skip, isRange = a, 0, "", true
				}
			}
		}
		x.finish(&d, span, ver, off, skip)
		if isRange && d.Skip == "" {
			d.Range, d.RefSpan = raw, span
		}
		x.deps = append(x.deps, d)
	}
	_, err := x.dec.ReadToken()
	return err
}

// packageManager extracts `<tool>@<version>[+<algo>.<hex>]`; Name is the tool. A Corepack
// hash is the dep's Checksum, which an update replaces with the hash of the new version's
// artifact (corepackArtifact) in the same algorithm.
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
	ver, hash, hashed := strings.Cut(ver, "+")
	var skip string
	switch {
	case !found || !packageManagers[tool]:
		skip = "unsupported packageManager"
	case !versionRe.MatchString(ver):
		skip = skipRange
	case hashed && !setHash(&d, tool, ver, hash, span, len(tool)+1+len(ver)+1):
		skip = "packageManager with hash"
	}
	x.finish(&d, span, ver, len(tool)+1, skip)
	x.deps = append(x.deps, d)
	return nil
}

// hashLens are the hex lengths of the Corepack hash algorithms deps computes (not sha1).
var hashLens = map[string]int{"sha224": 56, "sha256": 64, "sha512": 128}

// setHash sets the Corepack hash `<algo>.<hex>` at off within the value at span as d's
// Checksum; false when it is not one deps can compute (an unknown algorithm, a bun hash:
// Corepack does not install bun).
func setHash(d *dep.Dep, tool, ver, hash string, span dep.Span, off int) bool {
	algo, hx, _ := strings.Cut(hash, ".")
	if n, ok := hashLens[algo]; !ok || !manager.IsHex(hx, n) || tool == "bun" {
		return false
	}
	off += len(algo) + 1
	d.Checksum, d.ChecksumSpan = hx, dep.Span{Start: span.Start + off, End: span.Start + off + len(hx)}
	d.ChecksumSource = &dep.ChecksumSource{Artifact: corepackArtifact(tool, ver)}
	return true
}

// corepackArtifact is the URL template of the file Corepack hashes for tool: yarn berry's
// yarn.js bundle, else the npm registry tarball.
func corepackArtifact(tool, ver string) string {
	if tool == "yarn" && YarnBerry(ver) {
		return "https://repo.yarnpkg.com/{{version}}/packages/yarnpkg-cli/bin/yarn.js"
	}
	return "https://registry.npmjs.org/" + tool + "/-/" + tool + "-{{version}}.tgz"
}

// YarnBerry reports whether the yarn version (or range) v is yarn 2 or later, which is
// published as @yarnpkg/cli-dist rather than yarn.
func YarnBerry(v string) bool {
	v = strings.TrimLeft(v, "^~>=v ")
	end := 0
	for end < len(v) && '0' <= v[end] && v[end] <= '9' {
		end++
	}
	major, err := strconv.Atoi(v[:end])
	return err == nil && major >= 2
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
	if off, ok := scanRange(v); ok {
		return v[off:], off, ""
	}
	switch {
	case strings.HasPrefix(v, "npm:"):
		return "", 0, "npm alias"
	case hasAnyPrefix(v, localPrefixes...):
		return "", 0, "local dependency"
	case hasAnyPrefix(v, "github:", "gitlab:", "bitbucket:", "gist:", "git+", "git://", "git@"), shorthandRe.MatchString(v):
		return "", 0, "git dependency"
	case hasAnyPrefix(v, "http://", "https://"):
		return "", 0, "url dependency"
	case tagRe.MatchString(v) && !isRangeLike(v):
		return "", 0, "tag"
	}
	return "", 0, skipRange
}

// localPrefixes start the values of local dependencies.
var localPrefixes = []string{"file:", "link:", "workspace:", "./", "../", "/", "~/"}

// scanRange matches the supported range grammar: an optional ^, ~, >= or = operator and a
// full MAJOR.MINOR.PATCH[-prerelease] version running to the end of v. It returns the
// version's offset.
func scanRange(v string) (off int, ok bool) {
	switch {
	case strings.HasPrefix(v, ">="):
		off = 2
	case v != "" && (v[0] == '^' || v[0] == '~' || v[0] == '='):
		off = 1
	}
	i := off
	for part := range 3 {
		if part > 0 {
			if i >= len(v) || v[i] != '.' {
				return 0, false
			}
			i++
		}
		start := i
		for i < len(v) && v[i] >= '0' && v[i] <= '9' {
			i++
		}
		if i == start {
			return 0, false
		}
	}
	if i == len(v) {
		return off, true
	}
	if v[i] != '-' {
		return 0, false
	}
	for i < len(v) { // at '-' or '.', followed by a non-empty identifier
		i++
		start := i
		for i < len(v) && isIdentByte(v[i]) {
			i++
		}
		if i == start || (i < len(v) && v[i] != '.') {
			return 0, false
		}
	}
	return off, true
}

func isIdentByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-'
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

// object reads the '{' of the next value and reports true; any other value is skipped.
func (x *extractor) object() (bool, error) {
	if x.dec.PeekKind() != '{' {
		return false, x.dec.SkipValue()
	}
	_, err := x.dec.ReadToken()
	return err == nil, err
}
