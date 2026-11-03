// Package config loads and validates .deps.yaml.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
	Managers          = []string{"github-actions", "gomod", "docker", "npm", "custom"}
	DepTypes          = []string{"direct", "indirect", "go", "toolchain", "action", "dockerfile", "compose", "workflow", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "packageManager", "annotation", "regex"}
	CustomDatasources = []string{"git-tags", "github-releases", "go", "docker"}
	Versionings       = []string{"tag", "go", "docker", "semver"}
)

// Config is the parsed .deps.yaml.
type Config struct {
	Version     int      `yaml:"version" jsonschema:"enum=1,default=1" jsonschema_description:"Config format version; must be 1 if set."`
	Pin         string   `yaml:"pin" jsonschema:"enum=keep,enum=digest,default=keep" jsonschema_description:"Default pin mode for actions and docker: keep the current style, or pin to the digest with the version in a comment."`
	Timezone    string   `yaml:"timezone" jsonschema_description:"IANA time zone for schedule windows; empty means UTC."`
	Schedule    []string `yaml:"schedule" jsonschema_description:"Cron windows (5 fields, minute must be *) in which update PRs may be created or updated; empty means always."`
	Security    *bool    `yaml:"security" jsonschema:"default=true" jsonschema_description:"Look up OSV advisories and propose security updates that bypass allow, updates and schedule."`
	Actions     Actions  `yaml:"actions" jsonschema_description:"github-actions manager settings."`
	IgnorePaths []string `yaml:"ignorePaths" jsonschema_description:"Doublestar globs on slash paths relative to --dir; matching files and directories are not scanned."`
	Rules       []Rule   `yaml:"rules" jsonschema_description:"Policy rules, evaluated top to bottom; every set field of a matching rule overrides earlier ones."`
	Custom      []Custom `yaml:"custom" jsonschema_description:"Custom managers: '# deps: <datasource> <name>' annotations, or RE2 regex managers when match is set."`

	path     string        // source path for error messages
	schedule *schedule.Set // parsed Schedule
}

// SecurityEnabled reports whether OSV lookups are on (default true).
func (c *Config) SecurityEnabled() bool { return c.Security == nil || *c.Security }

// Actions holds github-actions manager settings.
type Actions struct {
	DefaultHost string `yaml:"defaultHost" jsonschema_description:"Host for owner/repo@ref actions; empty means auto-detected from the platform."`
}

// Rule overrides the policy for matching deps.
type Rule struct {
	Manager  string   `yaml:"manager" jsonschema:"enum=github-actions,enum=gomod,enum=docker,enum=npm,enum=custom" jsonschema_description:"Only match deps of this manager."`
	Names    []string `yaml:"names" jsonschema_description:"Only match deps whose name matches any of these path.Match globs."`
	DepTypes []string `yaml:"depTypes" jsonschema:"enum=direct,enum=indirect,enum=go,enum=toolchain,enum=action,enum=dockerfile,enum=compose,enum=workflow,enum=dependencies,enum=devDependencies,enum=optionalDependencies,enum=peerDependencies,enum=packageManager,enum=annotation,enum=regex" jsonschema_description:"Only match deps of these types."`
	Ignore   *bool    `yaml:"ignore" jsonschema_description:"Do not update matching deps; false opts indirect Go requirements, go directives and npm peerDependencies in."`
	Pin      string   `yaml:"pin" jsonschema:"enum=keep,enum=digest" jsonschema_description:"Pin mode for matching deps."`
	Allow    string   `yaml:"allow" jsonschema_description:"Semver constraint new versions must satisfy, e.g. '< 7'."`
	Updates  []string `yaml:"updates" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Allowed update types; replaces the earlier list."`
	Group    string   `yaml:"group" jsonschema_description:"Group name: all deps with the same group share one PR."`
	Schedule []string `yaml:"schedule" jsonschema_description:"Cron windows overriding the top-level schedule; [] means always."` // non-nil overrides the top-level schedule; [] = always

	allow    *semver.Constraints
	schedule *schedule.Set
}

// Custom is a custom manager: annotation mode when Match is empty, regex mode otherwise.
type Custom struct {
	Files      []string `yaml:"files" jsonschema:"required,minItems=1" jsonschema_description:"Doublestar globs of the files this manager scans."`
	Match      string   `yaml:"match" jsonschema_description:"RE2 regexp with a (?P<version>) group and optional name, datasource and digest groups; empty means annotation mode."`
	Datasource string   `yaml:"datasource" jsonschema:"enum=git-tags,enum=github-releases,enum=go,enum=docker" jsonschema_description:"Datasource of the matched deps; required in regex mode unless match has a (?P<datasource>) group."`
	Name       string   `yaml:"name" jsonschema_description:"Dependency name; required in regex mode unless match has a (?P<name>) group."`
	Versioning string   `yaml:"versioning" jsonschema:"enum=tag,enum=go,enum=docker,enum=semver,default=semver" jsonschema_description:"Versioning scheme of the matched versions."` // default semver
}

// Error marks an invalid configuration (parse, unknown key or validation
// failure, or an unreadable config file). Callers use errors.As to map it to
// a usage exit code.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Default returns the configuration used when no file exists.
func Default() *Config { return &Config{Version: 1, Pin: PinKeep} }

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

	c := &Config{}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, &Error{fmt.Errorf("%s: %w", p, err)}
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
		if _, err := path.Match(n, ""); err != nil {
			add(fmt.Sprintf("%s.names[%d]", f, j), "invalid glob %q: %v", n, err)
		}
	}
	for j, t := range r.DepTypes {
		if !slices.Contains(DepTypes, t) {
			add(fmt.Sprintf("%s.depTypes[%d]", f, j), "must be one of %s, got %q", list(DepTypes), t)
		}
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
	if cu.Datasource != "" && !slices.Contains(CustomDatasources, cu.Datasource) {
		add(f+".datasource", "must be one of %s, got %q", list(CustomDatasources), cu.Datasource)
	}
	if cu.Versioning != "" && !slices.Contains(Versionings, cu.Versioning) {
		add(f+".versioning", "must be one of %s, got %q", list(Versionings), cu.Versioning)
	}
	if cu.Match == "" {
		return
	}
	re, err := regexp.Compile(cu.Match)
	if err != nil {
		add(f+".match", "invalid RE2 regexp: %v", err)
		return
	}
	groups := re.SubexpNames()
	if !slices.Contains(groups, "version") {
		add(f+".match", "must have a (?P<version>...) group")
	}
	if cu.Datasource == "" && !slices.Contains(groups, "datasource") {
		add(f+".datasource", "required unless match has a (?P<datasource>...) group")
	}
	if cu.Name == "" && !slices.Contains(groups, "name") {
		add(f+".name", "required unless match has a (?P<name>...) group")
	}
}

func list(vs []string) string { return strings.Join(vs, ", ") }
