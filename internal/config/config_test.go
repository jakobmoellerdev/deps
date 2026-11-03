package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"github.com/Masterminds/semver/v3"
)

func GoTestVer(s string) *semver.Version { return semver.MustParse(s) }

func load(t *testing.T, name, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Load(dir, "")
}

func TestLoadDefaults(t *testing.T) {
	for _, tc := range []struct{ name, body string }{{"", ""}, {".deps.yaml", ""}, {".deps.yml", "rules: []\n"}} {
		c, err := load(t, tc.name, tc.body)
		if err != nil || c.Version != 1 || c.Pin != "keep" {
			t.Errorf("%q: %+v %v", tc.name, c, err)
		}
	}
	if _, err := Load(t.TempDir(), "missing.yaml"); err == nil {
		t.Error("explicit missing path must error")
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"pinn: digest\n", "line 1"},
		{"version: 2\n", "version: must be 1"},
		{"pin: always\n", "pin: must be keep or digest"},
		{"rules: [{manager: npm}]\n", "rules[0].manager"},
		{"rules: [{names: ['[']}]\n", "rules[0].names[0]"},
		{"rules: [{pin: x}]\n", "rules[0].pin"},
		{"rules: [{allow: '>>1'}]\n", "rules[0].allow"},
		{"rules: [{updates: [huge]}]\n", "rules[0].updates[0]"},
		{"ignorePaths: ['[']\n", "ignorePaths[0]"},
	} {
		_, err := load(t, ".deps.yaml", tc.body)
		var ce *Error
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ".deps.yaml") {
			t.Errorf("%q: err = %v", tc.body, err)
		}
	}
	_, err := load(t, ".deps.yaml", "pinn: digest\n")
	if !strings.Contains(err.Error(), "pinn") {
		t.Errorf("unknown key not named: %v", err)
	}
}

func TestPolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `pin: digest
rules:
  - names: ["actions/*"]
    allow: "< 7"
    updates: [major, minor]
  - manager: github-actions
    names: ["actions/cache"]
    updates: [patch]
    pin: keep
  - manager: gomod
    ignore: true
`)
	if err != nil {
		t.Fatal(err)
	}
	co := c.Policy(dep.Dep{Manager: "github-actions", Name: "actions/checkout"})
	if co.Pin != "digest" || co.Allow == nil || !co.Updates[dep.Minor] || co.Updates[dep.Patch] || co.Ignore {
		t.Errorf("checkout: %+v", co)
	}
	if !co.Accepts(dep.Major, GoTestVer("6.1.0")) || co.Accepts(dep.Major, GoTestVer("7.0.0")) || co.Accepts(dep.Major, nil) || co.Accepts(dep.Patch, GoTestVer("6.0.1")) {
		t.Error("checkout Accepts")
	}
	ca := c.Policy(dep.Dep{Manager: "github-actions", Name: "actions/cache"})
	if ca.Pin != "keep" || ca.Updates[dep.Minor] || !ca.Updates[dep.Patch] || ca.Allow == nil {
		t.Errorf("cache: %+v", ca)
	}
	other := c.Policy(dep.Dep{Manager: "github-actions", Name: "foo/bar"})
	if other.Pin != "digest" || len(other.Updates) != 5 || other.Allow != nil || !other.Accepts(dep.Pin, nil) {
		t.Errorf("other: %+v", other)
	}
	if !c.Policy(dep.Dep{Manager: "gomod", Name: "x"}).Ignore {
		t.Error("gomod ignore")
	}
}
