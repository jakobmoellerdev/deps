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
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

// Pin modes.
const (
	PinKeep   = "keep"
	PinDigest = "digest"
)

// Config is the parsed .deps.yaml.
type Config struct {
	Version     int      `yaml:"version"`
	Pin         string   `yaml:"pin"`
	Actions     Actions  `yaml:"actions"`
	IgnorePaths []string `yaml:"ignorePaths"`
	Rules       []Rule   `yaml:"rules"`

	path string // source path for error messages
}

// Actions holds github-actions manager settings.
type Actions struct {
	DefaultHost string `yaml:"defaultHost"`
}

// Rule overrides the policy for matching deps.
type Rule struct {
	Manager string   `yaml:"manager"`
	Names   []string `yaml:"names"`
	Ignore  *bool    `yaml:"ignore"`
	Pin     string   `yaml:"pin"`
	Allow   string   `yaml:"allow"`
	Updates []string `yaml:"updates"`

	allow *semver.Constraints
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
	if err := c.Validate(); err != nil {
		return nil, &Error{err}
	}
	return c, nil
}

// Validate checks every field and compiles rule constraints. Errors are
// joined and formatted as "<path>: <field>: <msg>".
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
	for i, g := range c.IgnorePaths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("ignorePaths[%d]", i), "invalid glob %q: %v", g, err)
		}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		f := fmt.Sprintf("rules[%d]", i)
		switch r.Manager {
		case "", "github-actions", "gomod":
		default:
			add(f+".manager", "must be github-actions or gomod, got %q", r.Manager)
		}
		for j, n := range r.Names {
			if _, err := path.Match(n, ""); err != nil {
				add(fmt.Sprintf("%s.names[%d]", f, j), "invalid glob %q: %v", n, err)
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
	}
	return errors.Join(errs...)
}
