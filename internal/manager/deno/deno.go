// Package deno extracts the `jsr:`, `npm:` and deno.land URL dependencies of Deno
// configuration files (deno.json, deno.jsonc: import map `imports` and `scopes`, `tasks`,
// `compilerOptions` types and JSX import sources, `lint.plugins`), rewrites their npm-style
// ranges per rangeStrategy, reads the versions deno.lock locks, and updates deno.lock with
// `deno install --lockfile-only` (see Locker).
package deno

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/deno"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/jsr"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "deno"

// Versioning is the versioning of every dep: npm ranges for `jsr:` and `npm:` specifiers (as
// Renovate's deno versioning, which is its npm versioning), semver for deno.land URLs.
const Versioning = "semver"

// DepTypes, as Renovate names them.
const (
	DepTypeImports              = "imports"
	DepTypeScopes               = "scopes"
	DepTypeTasks                = "tasks"
	DepTypeTaskCommand          = "tasks.command"
	DepTypeTypes                = "compilerOptions.types"
	DepTypeJSXImportSource      = "compilerOptions.jsxImportSource"
	DepTypeJSXImportSourceTypes = "compilerOptions.jsxImportSourceTypes"
	DepTypeLintPlugins          = "lint.plugins"
)

// Skip reasons (besides npm's range reasons, e.g. `tag` for `npm:chalk@latest`).
const (
	skipLocal       = "local path"
	skipURL         = "unsupported specifier"
	skipBare        = "bare specifier"
	skipUnversioned = "unversioned"
	skipName        = "invalid name"
	skipVersion     = "unsupported version"
	skipEscaped     = "escaped value"
)

var (
	matchRe = regexp.MustCompile(`(^|/)deno\.jsonc?$`)
	// taskSpecRe finds versioned `jsr:`/`npm:` specifiers and deno.land URLs in a task command;
	// unversioned ones are not pins (`deno task npm:build` names a task).
	taskSpecRe = regexp.MustCompile(`(?:^|[\s"'=(])((?:jsr|npm):/?(?:@[\w.-]+/)?[\w.-]+@[^\s/"'\\]+|https://deno\.land/(?:x/\w+|std)@[^\s/"'\\]+)`)
	// npmRenamer validates npm package names (npm's validate-npm-package-name).
	npmRenamer      = npm.New().(manager.Renamer)
	semverScheme, _ = versioning.Get(Versioning)
)

type denoManager struct{}

// New returns the deno manager.
func New() manager.Manager { return denoManager{} }

func (denoManager) Name() string { return Name }

// Match reports whether the slash path is a deno.json or deno.jsonc. The suffix check skips the
// regexp for the paths that cannot match.
func (denoManager) Match(p string) bool {
	return (strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonc")) && matchRe.MatchString(p)
}

// Extract returns the deps of src in file order. Comments and trailing commas are blanked
// first (jsonc.Strip), so offsets are those of src. A `jsr:`/`npm:` specifier's range is the
// dep's Range (RefSpan its bytes) with its npm anchor as Version; a deno.land URL's version
// is the Version.
func (denoManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	clean := jsonc.Strip(src)
	x := extractor{file: file, src: clean, dec: jsontext.NewDecoder(bytes.NewReader(clean), jsontext.AllowDuplicateNames(true)), line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	return x.deps, nil
}

// Edits rewrites a `jsr:`/`npm:` range for the new version under the update's rangeStrategy
// (newRange) and replaces the version of a deno.land URL.
func (denoManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if u.NewDigest != "" {
		return nil, errors.New("deno dependencies cannot be pinned to a digest")
	}
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	nr, ok := newRange(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("range %q cannot be rewritten for %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: nr}}, nil
}

// NewRange implements manager.RangeRewriter (newRange).
func (denoManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if d.Range == "" {
		return "", false
	}
	return newRange(d.Range, v, strategy)
}

// newRange is npm's rewrite of rng for v (npm.NewRange), with widen as replace: a specifier's
// requirement takes no `||` alternatives or blanks (deno rejects `^1.0.0 || ^2.0.0`), so,
// as Renovate's deno versioning, it cannot be widened.
func newRange(rng, v, strategy string) (string, bool) {
	if strategy == dep.RangeWiden {
		strategy = ""
	}
	return npm.NewRange(rng, v, strategy)
}

// Specifier is a `jsr:` or `npm:` package specifier split up: `jsr:@std/path@^1.0.0/posix`
// is Datasource jsr, Name @std/path, Version ^1.0.0 at VersionAt (-1 without a version).
type Specifier struct {
	Datasource, Name, Version string
	VersionAt                 int
}

// ParseSpecifier splits s, a `jsr:` or `npm:` specifier (`npm:/name@1` too); ok is false for
// any other string or one without a package name.
func ParseSpecifier(s string) (Specifier, bool) {
	ds, rest, ok := strings.Cut(s, ":")
	if !ok || ds != jsr.Name && ds != "npm" {
		return Specifier{}, false
	}
	off := len(ds) + 1
	if strings.HasPrefix(rest, "/") {
		rest, off = rest[1:], off+1
	}
	n := strings.IndexAny(rest, "@/")
	if strings.HasPrefix(rest, "@") {
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return Specifier{}, false
		}
		n = strings.IndexAny(rest[slash+1:], "@/")
		if n >= 0 {
			n += slash + 1
		}
	}
	if n < 0 {
		n = len(rest)
	}
	sp := Specifier{Datasource: ds, Name: rest[:n], VersionAt: -1}
	if sp.Name == "" {
		return Specifier{}, false
	}
	if n < len(rest) && rest[n] == '@' {
		v := rest[n+1:]
		if i := strings.IndexByte(v, '/'); i >= 0 {
			v = v[:i]
		}
		sp.Version, sp.VersionAt = v, off+n+1
	}
	return sp, true
}

type extractor struct {
	file string
	src  []byte // src with comments and trailing commas blanked
	dec  *jsontext.Decoder
	deps []dep.Dep

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	if ok, err := x.open('{'); err != nil || !ok {
		if err == nil {
			err = errors.New("not an object")
		}
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		switch key {
		case DepTypeImports:
			err = x.mapping(DepTypeImports)
		case DepTypeScopes:
			err = x.members(func(string) error { return x.mapping(DepTypeScopes) })
		case DepTypeTasks:
			err = x.members(func(string) error { return x.task() })
		case "compilerOptions":
			err = x.members(x.compilerOption)
		case "lint":
			err = x.members(func(k string) error {
				if k == "plugins" {
					return x.list(DepTypeLintPlugins)
				}
				return x.dec.SkipValue()
			})
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

// open reads the opening token of an object ('{') or array ('[') and reports true; any other
// value is skipped.
func (x *extractor) open(kind jsontext.Kind) (bool, error) {
	if x.dec.PeekKind() != kind {
		return false, x.dec.SkipValue()
	}
	_, err := x.dec.ReadToken()
	return err == nil, err
}

// key reads an object member name.
func (x *extractor) key() (string, error) {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// members calls each with the name of every member of the object value (each consumes the
// member's value); another value is skipped.
func (x *extractor) members(each func(key string) error) error {
	if ok, err := x.open('{'); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		if err := each(key); err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// str reads a string value: its content as written in the file, the offset of the content
// and whether it holds escapes (the content then is not the string's value). ok is false
// (and the value consumed) for any other value.
func (x *extractor) str() (raw string, at int, escaped, ok bool, err error) {
	if x.dec.PeekKind() != '"' {
		return "", 0, false, false, x.dec.SkipValue()
	}
	v, err := x.dec.ReadValue()
	if err != nil {
		return "", 0, false, false, err
	}
	end := int(x.dec.InputOffset())
	at = end - len(v) + 1
	raw = string(x.src[at : end-1])
	return raw, at, strings.IndexByte(raw, '\\') >= 0, true, nil
}

// mapping adds a dep per string value of the object value (an import map's `imports`, one
// scope of `scopes`).
func (x *extractor) mapping(depType string) error {
	return x.members(func(string) error { return x.value(depType) })
}

// list adds a dep per string of the array value.
func (x *extractor) list(depType string) error {
	if ok, err := x.open('['); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != ']' {
		if err := x.value(depType); err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// value adds the dep of the specifier in the next string value.
func (x *extractor) value(depType string) error {
	raw, at, escaped, ok, err := x.str()
	if err != nil || !ok {
		return err
	}
	d := dep.Dep{Manager: Name, Versioning: Versioning, DepType: depType, File: x.file, Line: x.lineAt(at), Name: raw}
	if escaped {
		d.Skip = skipEscaped
	} else {
		specifier(&d, raw, at)
	}
	x.deps = append(x.deps, d)
	return nil
}

// compilerOption adds the deps of the compilerOptions member key.
func (x *extractor) compilerOption(key string) error {
	switch key {
	case "types":
		return x.list(DepTypeTypes)
	case "jsxImportSource":
		return x.value(DepTypeJSXImportSource)
	case "jsxImportSourceTypes":
		return x.value(DepTypeJSXImportSourceTypes)
	}
	return x.dec.SkipValue()
}

// task adds the versioned specifiers of a task: a command string (DepTypeTasks) or the
// `command` of a task object (DepTypeTaskCommand). Commands are scanned as written, so a
// specifier next to an escaped quote is found at its offset.
func (x *extractor) task() error {
	if x.dec.PeekKind() == '{' {
		return x.members(func(k string) error {
			if k == "command" {
				return x.command(DepTypeTaskCommand)
			}
			return x.dec.SkipValue()
		})
	}
	return x.command(DepTypeTasks)
}

func (x *extractor) command(depType string) error {
	raw, at, _, ok, err := x.str()
	if err != nil || !ok {
		return err
	}
	for _, m := range taskSpecRe.FindAllStringSubmatchIndex(raw, -1) {
		s := raw[m[2]:m[3]]
		d := dep.Dep{Manager: Name, Versioning: Versioning, DepType: depType, File: x.file, Line: x.lineAt(at + m[2]), Name: s}
		specifier(&d, s, at+m[2])
		x.deps = append(x.deps, d)
	}
	return nil
}

// specifier fills d from the specifier s written at offset at: a `jsr:` or `npm:` package
// (pkg), a deno.land module URL (denoLand), else a skip reason.
func specifier(d *dep.Dep, s string, at int) {
	switch {
	case strings.HasPrefix(s, "jsr:"), strings.HasPrefix(s, "npm:"):
		pkg(d, s, at)
	case strings.HasPrefix(s, deno.Prefix):
		denoLand(d, s, at)
	case strings.HasPrefix(s, "./"), strings.HasPrefix(s, "../"), strings.HasPrefix(s, "/"), strings.HasPrefix(s, "file:"):
		d.Skip = skipLocal
	case strings.Contains(s, ":"):
		d.Skip = skipURL
	default:
		d.Skip = skipBare
	}
}

// pkg fills d from the `jsr:`/`npm:` specifier s at offset at: Name the package, Range the
// version requirement (npm.SetRange).
func pkg(d *dep.Dep, s string, at int) {
	sp, ok := ParseSpecifier(s)
	if !ok {
		d.Skip = skipName
		return
	}
	d.Datasource, d.Name = sp.Datasource, sp.Name
	valid := jsr.ValidName(sp.Name)
	if sp.Datasource == "npm" {
		_, err := npmRenamer.Rename(*d, sp.Name)
		valid = err == nil
	}
	switch {
	case !valid:
		d.Skip = skipName
	case sp.VersionAt < 0 || sp.Version == "":
		d.Skip = skipUnversioned
	default:
		npm.SetRange(d, sp.Version, dep.Span{Start: at + sp.VersionAt, End: at + sp.VersionAt + len(sp.Version)})
	}
}

// denoLand fills d from the deno.land URL s at offset at: `https://deno.land/x/<module>@<v>/…`
// or `https://deno.land/std@<v>/…`, Name the module URL (Renovate's depName), Version v.
func denoLand(d *dep.Dep, s string, at int) {
	rest := s[len(deno.Prefix):]
	n := strings.IndexAny(rest, "@/")
	if strings.HasPrefix(rest, "x/") {
		n = strings.IndexAny(rest[2:], "@/")
		if n >= 0 {
			n += 2
		}
	}
	if n < 0 {
		n = len(rest)
	}
	name := deno.Prefix + rest[:n]
	if _, ok := deno.Module(name); !ok {
		d.Skip = skipURL
		return
	}
	d.Datasource, d.Name = deno.Name, name
	if n == len(rest) || rest[n] != '@' {
		d.Skip = skipUnversioned
		return
	}
	v := rest[n+1:]
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	if !semverScheme.Valid(v) {
		d.Skip = skipVersion
		return
	}
	start := at + len(deno.Prefix) + n + 1
	d.Version, d.RefSpan = v, dep.Span{Start: start, End: start + len(v)}
}

// lineAt returns the 1-based line of offset off (non-decreasing across calls).
func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}
