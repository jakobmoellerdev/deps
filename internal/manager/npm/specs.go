package npm

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Skip reasons of the dependency values setValue reads.
const (
	skipGit          = "git dependency"
	skipGitNoRef     = "git dependency without ref"
	skipGitUnversion = "unversioned git ref"
	skipAlias        = "invalid npm alias"
	skipReference    = "override reference"
)

// roleAlias is the role of the NamePart of an `npm:<name>@<range>` alias: the aliased package
// inside the value, which a replacement rewrites (the key is the alias the code imports).
const roleAlias = "npm-alias"

// isAlias reports whether d is an `npm:` alias of another package.
func isAlias(d dep.Dep) bool {
	return len(d.NameParts) == 1 && d.NameParts[0].Role == roleAlias
}

// setValue sets d from the dependency value raw at span: an `npm:` alias (alias), a git
// dependency (gitDep), else a range (setRange) or the skip reason.
func (x *extractor) setValue(d *dep.Dep, raw string, span dep.Span) {
	switch {
	case strings.HasPrefix(raw, "npm:"):
		x.alias(d, raw, span)
	case x.gitDep(d, raw, span):
	default:
		x.setRange(d, raw, span, 0)
	}
}

// setRange sets d's version from the range raw[off:], the value raw being at span: Range is
// the range and RefSpan covers it; Version is its bare version or, for the rest of npm's range
// grammar, its anchor (npmRange.anchor). Other values set the skip reason.
func (x *extractor) setRange(d *dep.Dep, raw string, span dep.Span, off int) {
	v := raw[off:]
	ver, voff, skip := classify(v)
	if r, ok := parseRange(v); ok && skip == skipRange {
		if a := r.anchor(); a != "" {
			ver, voff, skip = a, 0, ""
		}
	}
	x.finish(d, span, ver, off+voff, skip)
	if d.Skip == "" {
		d.Version, d.Range, d.RefSpan = ver, v, dep.Span{Start: span.Start + off, End: span.End}
	}
}

// alias reads `npm:<name>@<range>`: the dep is the aliased package name with the range
// (Renovate's npmPackageAlias); `npm:<range>` without a name aliases the key's own package
// (yarn berry resolutions).
func (x *extractor) alias(d *dep.Dep, raw string, span dep.Span) {
	spec := raw[len("npm:"):]
	name, off := "", len("npm:")
	switch at := strings.LastIndexByte(spec, '@'); {
	case at > 0:
		name, off = spec[:at], off+at+1
	case strings.HasPrefix(spec, "@"): // a scoped name without a range
		name, off = spec, len(raw)
	}
	if name != "" && !packageNameRe.MatchString(strings.ToLower(name)) {
		d.Skip = skipAlias
		return
	}
	x.setRange(d, raw, span, off)
	if name == "" || d.Skip != "" {
		return
	}
	d.Name = name
	at := span.Start + len("npm:")
	d.NameParts = []dep.NamePart{{Span: dep.Span{Start: at, End: at + len(name)}, Text: name, Role: roleAlias}}
}

// gitDep reads a git dependency value (gitRepo) into d and reports true, or reports false for
// any other value. A version tag committish (`#v1.2.3`) is a git-tags dep on the repository
// (Name stays the key), `#semver:<range>` a git-tags range, and a commit (7 to 40 hex digits)
// a git-refs digest of the default branch head (`HEAD`), abbreviated as written.
func (x *extractor) gitDep(d *dep.Dep, raw string, span dep.Span) bool {
	if hasAnyPrefix(raw, localPrefixes...) {
		return false
	}
	source, ref, off, isGit := gitRepo(raw)
	if !isGit {
		return false
	}
	if source == "" {
		d.Skip = skipGit
		return true
	}
	d.Datasource, d.Versioning, d.Source = "git-tags", "tag", source
	switch {
	case off < 0 || ref == "":
		d.Skip = skipGitNoRef
	case span == (dep.Span{}):
		d.Skip = "cannot locate value"
	case isCommit(ref):
		d.Datasource, d.Version = "git-refs", "HEAD"
		d.Digest, d.DigestSpan = ref, dep.Span{Start: span.Start + off, End: span.Start + off + len(ref)}
	case strings.HasPrefix(ref, "semver:"):
		d.Versioning = "semver"
		x.setRange(d, raw, span, off+len("semver:"))
		if d.Skip != "" {
			d.Skip = skipGitUnversion
		}
	case semverTagRe.MatchString(ref):
		d.Versioning = "semver"
		d.Version, d.RefSpan = ref, dep.Span{Start: span.Start + off, End: span.Start + off + len(ref)}
	case validTag(ref):
		d.Version, d.RefSpan = ref, dep.Span{Start: span.Start + off, End: span.Start + off + len(ref)}
	default:
		d.Skip = skipGitUnversion
	}
	return true
}

// semverTagRe is a full semver version tag with an optional "v".
var semverTagRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// validTag reports whether ref is a partial version tag of the tag versioning (v1, 1.2).
func validTag(ref string) bool {
	_, ok := versioning.ParseTag(ref)
	return ok
}

// isCommit reports whether ref is a (possibly abbreviated) commit: 7 to 40 hex digits, not
// all decimal unless 40 long (`1234567` is a version tag).
func isCommit(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 || !manager.IsHex(ref, len(ref)) {
		return false
	}
	return len(ref) == 40 || strings.ContainsAny(strings.ToLower(ref), "abcdef")
}

// hostedGit maps npm's hosted git shorthands to their host.
var hostedGit = map[string]string{"github:": "github.com", "gitlab:": "gitlab.com", "bitbucket:": "bitbucket.org"}

// gitHosts are the hosts whose `https://<host>/<owner>/<repo>` URLs npm installs as git
// repositories rather than tarballs.
var gitHosts = map[string]bool{"github.com": true, "gitlab.com": true, "bitbucket.org": true}

// gitRepo splits a git dependency value into the https URL of its repository and the
// committish after `#` at offset off in v (-1 without `#`). isGit is false when v is no git
// dependency; source is "" for one deps cannot look up (`gist:`, `git+file://`). Accepted:
// the `github:`, `gitlab:` and `bitbucket:` shorthands, GitHub's `owner/repo`, `git+<scheme>://`
// (`git+ssh://git@host:owner/repo` too), `git://`, scp-style `git@host:owner/repo`, and
// `http(s)://` URLs ending in `.git` or naming a GitHub, GitLab or Bitbucket repository.
func gitRepo(v string) (source, ref string, off int, isGit bool) {
	base, ref, hasRef := strings.Cut(v, "#")
	off = len(base) + 1
	if !hasRef {
		off = -1
	}
	for p, host := range hostedGit {
		if rest, ok := strings.CutPrefix(base, p); ok {
			if path := strings.TrimSuffix(strings.Trim(rest, "/"), ".git"); strings.Contains(path, "/") {
				source = "https://" + host + "/" + path
			}
			return source, ref, off, true
		}
	}
	switch {
	case strings.HasPrefix(base, "gist:"):
		return "", ref, off, true
	case strings.HasPrefix(base, "git+"), strings.HasPrefix(base, "git://"), strings.HasPrefix(base, "git@"):
		u := strings.TrimPrefix(base, "git+")
		if rest, ok := strings.CutPrefix(u, "ssh://"); ok && scpLike(rest) {
			u = rest
		}
		_, source = manager.GitRepo(u)
		return source, ref, off, true
	case strings.HasPrefix(base, "https://"), strings.HasPrefix(base, "http://"):
		name, src := manager.GitRepo(base)
		host, _, _ := strings.Cut(strings.TrimPrefix(src, "https://"), "/")
		if src == "" || !strings.HasSuffix(strings.TrimSuffix(base, "/"), ".git") && (!gitHosts[host] || strings.Count(name, "/") != 1) {
			return "", "", 0, false
		}
		return src, ref, off, true
	case shorthandRe.MatchString(v):
		return "https://github.com/" + strings.TrimSuffix(base, ".git"), ref, off, true
	}
	return "", "", 0, false
}

// scpLike reports whether the ssh URL rest (after `ssh://`) is scp-style `git@host:owner/repo`,
// which npm accepts: a ':' before the first '/' that is not followed by a port number.
func scpLike(rest string) bool {
	c := strings.IndexByte(rest, ':')
	if c < 0 || strings.Contains(rest[:c], "/") {
		return false
	}
	port, _, _ := strings.Cut(rest[c+1:], "/")
	return strings.Trim(port, "0123456789") != ""
}

// overrides extracts the npm `overrides` object: a string value overrides the package of its
// key (`name` or `name@range`; `.` the package of the enclosing key), an object scopes the
// overrides in it to the dependency tree below the package of its key.
func (x *extractor) overrides(parent string) error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		nameSpan := x.span(key)
		name := specName(key)
		if key == "." {
			name = parent
		}
		if x.dec.PeekKind() == '{' {
			if err := x.overrides(name); err != nil {
				return err
			}
			continue
		}
		if err := x.override(DepTypeOverrides, key, name, nameSpan); err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// flatOverrides extracts an object of override selectors (yarn `resolutions`, pnpm
// `pnpm.overrides`) to values; nameOf returns the package a selector overrides.
func (x *extractor) flatOverrides(depType string, nameOf func(string) string) error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		if err := x.override(depType, key, nameOf(key), x.span(key)); err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// pnpm extracts the `overrides` of the package.json `pnpm` object.
func (x *extractor) pnpm() error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		if key == "overrides" {
			err = x.flatOverrides(DepTypePnpmOverrides, pnpmName)
		} else {
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// override reads the value of the override selector key (at nameSpan) of package name. Only a
// selector that is the bare name is renamable. A `$name` value refers to the version of a
// direct dependency and is skipped; values are read as dependency values (setValue).
func (x *extractor) override(depType, key, name string, nameSpan dep.Span) error {
	raw, span, ok, err := x.value()
	if err != nil || !ok || name == "" {
		return err
	}
	d := x.newDep(depType, name, raw, span)
	if key == name && nameSpan != (dep.Span{}) {
		d.NameParts = []dep.NamePart{{Span: nameSpan, Text: name}}
	}
	if strings.HasPrefix(raw, "$") {
		d.Skip = skipReference
	} else {
		x.setValue(&d, raw, span)
	}
	x.deps = append(x.deps, d)
	return nil
}

// specName returns the package name of `name[@range]` (`@scope/name@range` too).
func specName(s string) string {
	if i := strings.IndexByte(s[min(1, len(s)):], '@'); i >= 0 {
		return s[:i+1]
	}
	return s
}

// resolutionName returns the package a yarn resolution selector overrides: the last segment
// of `[**/][parent/]name[@range]` (scoped segments count as one), without its range.
func resolutionName(key string) string {
	parts := strings.Split(key, "/")
	last := parts[len(parts)-1]
	if n := len(parts); n >= 2 && strings.HasPrefix(parts[n-2], "@") {
		last = parts[n-2] + "/" + last
	}
	return specName(last)
}

// pnpmName returns the package a pnpm override selector (`parent@range>name@range`)
// overrides. As in pnpm's parse-overrides, a '>' after ' ', '|' or '@' is a comparator
// (`name@>=1`), not the parent delimiter.
func pnpmName(key string) string {
	for i := len(key) - 1; i > 0; i-- {
		if key[i] == '>' && !strings.ContainsRune(" |@", rune(key[i-1])) {
			return specName(key[i+1:])
		}
	}
	return specName(key)
}
