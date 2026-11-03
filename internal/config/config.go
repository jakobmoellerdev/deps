// Package config loads and validates .deps.yaml.
package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Pin modes.
const (
	PinKeep   = "keep"
	PinDigest = "digest"
)

// Known values for validation.
var (
	Managers = []string{"github-actions", "gomod", "docker", "npm", "custom"}
	DepTypes = []string{"direct", "indirect", "go", "toolchain", "action", "dockerfile", "compose", "workflow", "kubernetes", "helm", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "packageManager", "annotation", "regex"}
	// Datasources are the deps datasource names (dep.Dep.Datasource).
	Datasources = []string{"git-tags", "github-releases", "go", "go-toolchain", "docker", "npm"}
	// DatasourceAliases maps Renovate datasource names to deps datasource names.
	DatasourceAliases = map[string]string{
		"golang-version":             "go-toolchain",
		"github-tags":                "git-tags",
		"github-release-attachments": "github-releases",
	}
	// DatasourceNames are the accepted datasource names of custom[].datasource and
	// rules[].datasources: Datasources followed by the DatasourceAliases keys.
	DatasourceNames = []string{"git-tags", "github-releases", "go", "go-toolchain", "docker", "npm", "golang-version", "github-tags", "github-release-attachments"}
	Versionings     = []string{"tag", "go", "docker", "semver"}
	// CommitTypes are the accepted conventional-commit types of rules[].commitType.
	CommitTypes = []string{"fix", "chore", "build", "ci", "docs", "feat", "perf", "refactor", "revert", "style", "test"}
)

// CanonicalDatasource maps a Renovate alias to its deps datasource name; other names are
// returned unchanged.
func CanonicalDatasource(name string) string {
	if a, ok := DatasourceAliases[name]; ok {
		return a
	}
	return name
}

// DefaultBranchPrefix is the branch prefix used when branchPrefix is not set.
const DefaultBranchPrefix = "deps/"

// DefaultIgnorePaths are used when the config file has no ignorePaths key: dependency
// trees, examples and tests are not scanned.
var DefaultIgnorePaths = []string{
	"**/node_modules/**", "**/bower_components/**", "**/vendor/**", "**/examples/**",
	"**/__tests__/**", "**/test/**", "**/tests/**", "**/__fixtures__/**",
}

// Config is the parsed .deps.yaml.
type Config struct {
	Version      int      `yaml:"version" jsonschema:"enum=1,default=1" jsonschema_description:"Config format version; must be 1 if set."`
	Pin          string   `yaml:"pin" jsonschema:"enum=keep,enum=digest,default=keep" jsonschema_description:"Default pin mode for actions and docker: keep the current style, or pin to the digest with the version in a comment."`
	Timezone     string   `yaml:"timezone" jsonschema_description:"IANA time zone for schedule windows; empty means UTC."`
	Schedule     []string `yaml:"schedule" jsonschema_description:"Cron windows (5 fields, minute must be *) in which update PRs may be created or updated; empty means always."`
	Security     *bool    `yaml:"security" jsonschema:"default=true" jsonschema_description:"Look up OSV advisories and propose security updates that bypass allow, updates and schedule."`
	BranchPrefix string   `yaml:"branchPrefix" jsonschema:"default=deps/" jsonschema_description:"Prefix of update branches (<prefix><manager>/<name>, <prefix>group/<group>); must end with '/'. Only branches and PRs under it are managed."`
	Monorepos    *bool    `yaml:"monorepos" jsonschema:"default=true" jsonschema_description:"Group deps released from one source repository (built-in list, e.g. kubernetes, opentelemetry-go, babel) into one PR; rules can override the group."`
	Actions      Actions  `yaml:"actions" jsonschema_description:"github-actions manager settings."`
	IgnorePaths  []string `yaml:"ignorePaths" jsonschema_description:"Doublestar globs on slash paths relative to --dir; matching files and directories are not scanned. When absent: node_modules, bower_components, vendor, examples, __tests__, test, tests and __fixtures__ directories; any list (also []) replaces that default."`
	Rules        []Rule   `yaml:"rules" jsonschema_description:"Policy rules, evaluated top to bottom; every set field of a matching rule overrides earlier ones."`
	Custom       []Custom `yaml:"custom" jsonschema_description:"Custom managers: '# deps: <datasource> <name>' annotations, or RE2 regex managers when match is set."`

	path     string        // source path for error messages
	schedule *schedule.Set // parsed Schedule
}

// SecurityEnabled reports whether OSV lookups are on (default true).
func (c *Config) SecurityEnabled() bool { return c.Security == nil || *c.Security }

// MonoreposEnabled reports whether the built-in monorepo groups apply (default true).
func (c *Config) MonoreposEnabled() bool { return c.Monorepos == nil || *c.Monorepos }

// Prefix returns BranchPrefix, or DefaultBranchPrefix when unset.
func (c *Config) Prefix() string { return cmp.Or(c.BranchPrefix, DefaultBranchPrefix) }

// Actions holds github-actions manager settings.
type Actions struct {
	DefaultHost string `yaml:"defaultHost" jsonschema_description:"Host for owner/repo@ref actions; empty means auto-detected from the platform."`
}

// Rule overrides the policy for matching deps.
type Rule struct {
	Manager     string   `yaml:"manager" jsonschema:"enum=github-actions,enum=gomod,enum=docker,enum=npm,enum=custom" jsonschema_description:"Only match deps of this manager."`
	Names       []string `yaml:"names" jsonschema_description:"Only match deps whose name matches any of these doublestar globs ('*' stays within a path segment, '**' crosses segments, '{a,b}' alternates)."`
	DepTypes    []string `yaml:"depTypes" jsonschema:"enum=direct,enum=indirect,enum=go,enum=toolchain,enum=action,enum=dockerfile,enum=compose,enum=workflow,enum=kubernetes,enum=helm,enum=dependencies,enum=devDependencies,enum=optionalDependencies,enum=peerDependencies,enum=packageManager,enum=annotation,enum=regex" jsonschema_description:"Only match deps of these types."`
	Paths       []string `yaml:"paths" jsonschema_description:"Only match deps whose file (slash path relative to --dir) matches any of these doublestar globs, e.g. 'charts/**'."`
	Datasources []string `yaml:"datasources" jsonschema:"enum=git-tags,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=golang-version,enum=github-tags,enum=github-release-attachments" jsonschema_description:"Only match deps looked up with these datasources; Renovate names (golang-version, github-tags, github-release-attachments) match their deps datasource."`
	UpdateTypes []string `yaml:"updateTypes" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Only match candidate updates of these types: the rule applies once the type of an update is known (e.g. group minor and patch updates, ignore majors). Cannot set pin."`
	Ignore      *bool    `yaml:"ignore" jsonschema_description:"Do not update matching deps; false opts indirect Go requirements, go directives and npm peerDependencies in. With updateTypes: reject updates of these types."`
	Pin         string   `yaml:"pin" jsonschema:"enum=keep,enum=digest" jsonschema_description:"Pin mode for matching deps."`
	Allow       string   `yaml:"allow" jsonschema_description:"Semver constraint new versions must satisfy, e.g. '< 7'."`
	Updates     []string `yaml:"updates" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Allowed update types; replaces the earlier list."`
	Group       string   `yaml:"group" jsonschema_description:"Group name: all deps with the same group share one PR."`
	CommitType  string   `yaml:"commitType" jsonschema:"enum=fix,enum=chore,enum=build,enum=ci,enum=docs,enum=feat,enum=perf,enum=refactor,enum=revert,enum=style,enum=test" jsonschema_description:"Conventional commit type of commit and PR titles; default fix for production deps (gomod direct, npm dependencies), chore otherwise. Security updates always use fix."`
	CommitScope *string  `yaml:"commitScope" jsonschema_description:"Conventional commit scope; default deps, empty string means no scope ('chore: ...')."`
	Schedule    []string `yaml:"schedule" jsonschema_description:"Cron windows overriding the top-level schedule; [] means always."` // non-nil overrides the top-level schedule; [] = always

	allow    *semver.Constraints
	schedule *schedule.Set
}

// Custom is a custom manager: annotation mode when Match is empty, regex mode otherwise.
type Custom struct {
	Files      []string  `yaml:"files" jsonschema:"required,minItems=1" jsonschema_description:"Doublestar globs of the files this manager scans."`
	Match      string    `yaml:"match" jsonschema_description:"RE2 regexp with a (?P<version>) or (?P<currentValue>) group and optional name (or depName/packageName), datasource, digest and checksum groups; empty means annotation mode."`
	Datasource string    `yaml:"datasource" jsonschema:"enum=git-tags,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=golang-version,enum=github-tags,enum=github-release-attachments" jsonschema_description:"Datasource of the matched deps; required in regex mode unless match has a (?P<datasource>) group. Renovate names (golang-version, github-tags, github-release-attachments) are aliases."`
	Name       string    `yaml:"name" jsonschema_description:"Dependency name; required in regex mode unless match has a (?P<name>), (?P<depName>) or (?P<packageName>) group."`
	Versioning string    `yaml:"versioning" jsonschema:"enum=tag,enum=go,enum=docker,enum=semver,default=semver" jsonschema_description:"Versioning scheme of the matched versions."` // default semver
	Checksum   *Checksum `yaml:"checksum" jsonschema_description:"Where the checksum of a new version comes from; regex mode only, requires a (?P<checksum>) group in match."`
}

// Checksum locates the sha256/sha512 checksum written next to a version. Its fields are
// templates; {{version}} is the new version exactly as written into the file.
type Checksum struct {
	Asset string `yaml:"asset" jsonschema_description:"github-releases only: release asset whose API digest is the checksum, e.g. 'gh_{{version}}_linux_amd64.tar.gz'. Exclusive with url."`
	URL   string `yaml:"url" jsonschema_description:"URL of a checksum document (sha256sum style or a bare hex digest). Exclusive with asset."`
	File  string `yaml:"file" jsonschema_description:"Entry name inside the url document ('<hex>  <name>' or '<hex> *<name>'); empty means the first hex token of the body."`
}

// templateVarRe finds {{...}} template references.
var templateVarRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// validateTemplate reports references other than {{version}} and unbalanced braces.
func validateTemplate(field, t string, add addFunc) {
	for _, v := range templateVarRe.FindAllString(t, -1) {
		if v != "{{version}}" {
			add(field, "unknown template variable %s (only {{version}})", v)
		}
	}
	if rest := templateVarRe.ReplaceAllString(t, ""); strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		add(field, "unbalanced template braces in %q", t)
	}
}

func (ch *Checksum) validate(f, datasource string, add addFunc) {
	switch {
	case ch.Asset == "" && ch.URL == "":
		add(f, "one of asset and url is required")
	case ch.Asset != "" && ch.URL != "":
		add(f, "asset and url are mutually exclusive")
	}
	if ch.Asset != "" && datasource != "github-releases" {
		add(f+".asset", "requires datasource github-releases")
	}
	if ch.File != "" && ch.URL == "" {
		add(f+".file", "only allowed with url")
	}
	validateTemplate(f+".asset", ch.Asset, add)
	validateTemplate(f+".url", ch.URL, add)
	validateTemplate(f+".file", ch.File, add)
}

// Error marks an invalid configuration (parse, unknown key or validation
// failure, or an unreadable config file). Callers use errors.As to map it to
// a usage exit code.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Default returns the configuration used when no file exists.
func Default() *Config {
	return &Config{Version: 1, Pin: PinKeep, IgnorePaths: slices.Clone(DefaultIgnorePaths)}
}

// Load reads explicitPath (which must exist) or, if empty, .deps.yaml then
// .deps.yml in dir, falling back to Default. All failures are *Error.
func Load(dir, explicitPath string) (*Config, error) {
	var f *os.File
	var p string
	if explicitPath != "" {
		p = explicitPath
		var err error
		if f, err = os.Open(p); err != nil {
			return nil, &Error{err}
		}
	} else {
		for _, n := range []string{".deps.yaml", ".deps.yml"} {
			cand := filepath.Join(dir, n)
			h, err := os.Open(cand)
			if err == nil {
				f, p = h, cand
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, &Error{err}
			}
		}
		if f == nil {
			return Default(), nil
		}
	}
	defer f.Close()

	src, err := io.ReadAll(f)
	if err != nil {
		return nil, &Error{fmt.Errorf("%s: %w", p, err)}
	}
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, &Error{fmt.Errorf("%s: %w", p, err)}
	}
	if c.IgnorePaths == nil && !hasTopLevelKey(src, "ignorePaths") {
		c.IgnorePaths = slices.Clone(DefaultIgnorePaths)
	}
	c.path = p
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Pin == "" {
		c.Pin = PinKeep
	}
	for i := range c.Custom {
		if c.Custom[i].Versioning == "" {
			c.Custom[i].Versioning = "semver"
		}
	}
	if err := c.Validate(); err != nil {
		return nil, &Error{err}
	}
	return c, nil
}

// hasTopLevelKey reports whether the yaml document src has key at the top level, also
// when its value is null (`ignorePaths:` or `ignorePaths: ~`).
func hasTopLevelKey(src []byte, key string) bool {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if m[i].Value == key {
			return true
		}
	}
	return false
}

// Validate checks every field and compiles rule constraints and schedules. Errors
// are joined and formatted as "<path>: <field>: <msg>".
func (c *Config) Validate() error {
	p := c.path
	if p == "" {
		p = ".deps.yaml"
	}
	var errs []error
	add := func(field, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s: %s", p, field, fmt.Sprintf(format, a...)))
	}
	if c.Version != 0 && c.Version != 1 {
		add("version", "must be 1, got %d", c.Version)
	}
	if c.Pin != "" && c.Pin != PinKeep && c.Pin != PinDigest {
		add("pin", "must be keep or digest, got %q", c.Pin)
	}
	if c.BranchPrefix != "" {
		if msg := checkBranchPrefix(c.BranchPrefix); msg != "" {
			add("branchPrefix", "%s, got %q", msg, c.BranchPrefix)
		}
	}
	tz := c.Timezone
	if _, err := schedule.Parse(nil, tz); err != nil {
		add("timezone", "%v", err)
		tz = ""
	}
	c.schedule = parseSchedule("schedule", c.Schedule, tz, add)
	for i, g := range c.IgnorePaths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("ignorePaths[%d]", i), "invalid glob %q: %v", g, err)
		}
	}
	for i := range c.Rules {
		c.validateRule(i, tz, add)
	}
	for i := range c.Custom {
		c.Custom[i].validate(fmt.Sprintf("custom[%d]", i), add)
	}
	return errors.Join(errs...)
}

type addFunc func(field, format string, a ...any)

// parseSchedule validates each expression separately for precise errors and returns the
// parsed set, or nil when exprs is empty or invalid.
func parseSchedule(field string, exprs []string, tz string, add addFunc) *schedule.Set {
	ok := true
	for i, e := range exprs {
		if _, err := schedule.Parse([]string{e}, ""); err != nil {
			add(fmt.Sprintf("%s[%d]", field, i), "%v", err)
			ok = false
		}
	}
	if !ok {
		return nil
	}
	s, _ := schedule.Parse(exprs, tz) // expressions and tz are valid here
	return s
}

func (c *Config) validateRule(i int, tz string, add addFunc) {
	r := &c.Rules[i]
	f := fmt.Sprintf("rules[%d]", i)
	if r.Manager != "" && !slices.Contains(Managers, r.Manager) {
		add(f+".manager", "must be one of %s, got %q", list(Managers), r.Manager)
	}
	for j, n := range r.Names {
		if _, err := doublestar.Match(n, ""); err != nil {
			add(fmt.Sprintf("%s.names[%d]", f, j), "invalid glob %q: %v", n, err)
		}
	}
	for j, t := range r.DepTypes {
		if !slices.Contains(DepTypes, t) {
			add(fmt.Sprintf("%s.depTypes[%d]", f, j), "must be one of %s, got %q", list(DepTypes), t)
		}
	}
	for j, g := range r.Paths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("%s.paths[%d]", f, j), "invalid glob %q: %v", g, err)
		}
	}
	for j, ds := range r.Datasources {
		if !slices.Contains(DatasourceNames, ds) {
			add(fmt.Sprintf("%s.datasources[%d]", f, j), "must be one of %s, got %q", list(DatasourceNames), ds)
		}
	}
	if r.UpdateTypes != nil && len(r.UpdateTypes) == 0 {
		add(f+".updateTypes", "must not be empty (omit it to match every update type)")
	}
	for j, u := range r.UpdateTypes {
		if !slices.Contains(dep.UpdateTypes, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updateTypes[%d]", f, j), "must be one of major, minor, patch, digest, pin, got %q", u)
		}
	}
	if len(r.UpdateTypes) > 0 && r.Pin != "" {
		add(f+".pin", "cannot be combined with updateTypes: the pin mode is decided before the update type is known")
	}
	if r.Pin != "" && r.Pin != PinKeep && r.Pin != PinDigest {
		add(f+".pin", "must be keep or digest, got %q", r.Pin)
	}
	r.allow = nil
	if r.Allow != "" {
		cs, err := semver.NewConstraint(r.Allow)
		if err != nil {
			add(f+".allow", "invalid constraint %q: %v", r.Allow, err)
		} else {
			r.allow = cs
		}
	}
	for j, u := range r.Updates {
		if !slices.Contains(dep.UpdateTypes, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updates[%d]", f, j), "must be one of major, minor, patch, digest, pin, got %q", u)
		}
	}
	if r.CommitType != "" && !slices.Contains(CommitTypes, r.CommitType) {
		add(f+".commitType", "must be one of %s, got %q", list(CommitTypes), r.CommitType)
	}
	if r.CommitScope != nil && *r.CommitScope != "" && !commitScopeRe.MatchString(*r.CommitScope) {
		add(f+".commitScope", "must match %s, got %q", commitScopeRe, *r.CommitScope)
	}
	r.schedule = parseSchedule(f+".schedule", r.Schedule, tz, add)
}

func (cu *Custom) validate(f string, add addFunc) {
	if len(cu.Files) == 0 {
		add(f+".files", "must not be empty")
	}
	for j, g := range cu.Files {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("%s.files[%d]", f, j), "invalid glob %q: %v", g, err)
		}
	}
	if cu.Datasource != "" && !slices.Contains(DatasourceNames, cu.Datasource) {
		add(f+".datasource", "must be one of %s, got %q", list(DatasourceNames), cu.Datasource)
	}
	if cu.Versioning != "" && !slices.Contains(Versionings, cu.Versioning) {
		add(f+".versioning", "must be one of %s, got %q", list(Versionings), cu.Versioning)
	}
	if cu.Match == "" {
		if cu.Checksum != nil {
			add(f+".checksum", "requires match (regex mode)")
		}
		return
	}
	re, err := regexp.Compile(cu.Match)
	if err != nil {
		add(f+".match", "invalid RE2 regexp: %v", err)
		return
	}
	groups := re.SubexpNames()
	if !slices.Contains(groups, "version") && !slices.Contains(groups, "currentValue") {
		add(f+".match", "must have a (?P<version>...) or (?P<currentValue>...) group")
	}
	if cu.Datasource == "" && !slices.Contains(groups, "datasource") {
		add(f+".datasource", "required unless match has a (?P<datasource>...) group")
	}
	if cu.Name == "" && !slices.ContainsFunc(groups, func(g string) bool { return g == "name" || g == "depName" || g == "packageName" }) {
		add(f+".name", "required unless match has a (?P<name>...), (?P<depName>...) or (?P<packageName>...) group")
	}
	hasGroup := slices.Contains(groups, "checksum")
	switch {
	case cu.Checksum != nil && !hasGroup:
		add(f+".checksum", "requires a (?P<checksum>...) group in match")
	case cu.Checksum == nil && hasGroup:
		add(f+".match", "(?P<checksum>...) group requires checksum")
	}
	if cu.Checksum != nil {
		cu.Checksum.validate(f+".checksum", cu.Datasource, add)
	}
}

func list(vs []string) string { return strings.Join(vs, ", ") }

// commitScopeRe is the accepted conventional-commit scope syntax.
var commitScopeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// checkBranchPrefix returns why prefix cannot start a git branch name (git check-ref-format
// rules for the prefix's components), or "" when it is valid.
func checkBranchPrefix(prefix string) string {
	if !strings.HasSuffix(prefix, "/") {
		return "must end with /"
	}
	if strings.ContainsFunc(prefix, func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) }) ||
		strings.Contains(prefix, "..") || strings.Contains(prefix, "@{") {
		return "must be a valid git ref prefix (no spaces, control characters, ~^:?*[\\, '..' or '@{')"
	}
	for comp := range strings.SplitSeq(strings.TrimSuffix(prefix, "/"), "/") {
		if comp == "" || strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return "must be a valid git ref prefix (empty components, components starting with '.' or ending with '.lock')"
		}
	}
	return ""
}
