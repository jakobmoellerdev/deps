// Package setupcfg extracts the requirements of setuptools' declarative configuration
// (setup.cfg): `[options]` install_requires, setup_requires and tests_require and every key of
// `[options.extras_require]`.
package setupcfg

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// Name is the manager name (Renovate's).
const Name = "setup-cfg"

// DepTypes (Renovate's).
const (
	DepTypeInstall = "install"
	DepTypeSetup   = "setup"
	DepTypeTest    = "test"
	DepTypeExtra   = "extra"
)

var matchRe = regexp.MustCompile(`(^|/)setup\.cfg$`)

// optionKeys maps the `[options]` keys holding requirements to their dep types.
var optionKeys = map[string]string{
	"install_requires": DepTypeInstall,
	"setup_requires":   DepTypeSetup,
	"tests_require":    DepTypeTest,
}

// markerNames are the PEP 508 marker variables: a `;`-separated part of a one-line value that
// starts with one is the previous requirement's marker, not a requirement.
var markerNames = map[string]bool{
	"python_version": true, "python_full_version": true, "os_name": true, "sys_platform": true,
	"platform_release": true, "platform_system": true, "platform_version": true, "platform_machine": true,
	"platform_python_implementation": true, "implementation_name": true, "implementation_version": true, "extra": true,
}

type setupCfgManager struct{}

// New returns the setup-cfg manager.
func New() manager.Manager { return setupCfgManager{} }

func (setupCfgManager) Name() string { return Name }

// Match reports whether the slash path is a setup.cfg file.
func (setupCfgManager) Match(p string) bool {
	return strings.HasSuffix(p, "setup.cfg") && matchRe.MatchString(p)
}

// Edits rewrites the range (pyreq.Edits).
func (setupCfgManager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (setupCfgManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (setupCfgManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return pyreq.NewRange(d, v, strategy)
}

// Extract returns one dep per requirement of the requirement keys, as setuptools reads them:
// a value is the rest of the key line plus its indented continuation lines, one requirement
// per line (`;`-separated on the key line). `#`/`;` comment lines, inline `#` comments and
// `file:`/`attr:` directives are ignored. `[options] python_requires` is every dep's Python
// constraint.
func (setupCfgManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var (
		deps     []dep.Dep
		section  string
		depType  string // of the current key; "" when it holds no requirements
		python   string
		lineNo   int
		lineFrom int
	)
	text := string(src)
	for lineFrom <= len(text) {
		lineNo++
		end := strings.IndexByte(text[lineFrom:], '\n')
		if end < 0 {
			end = len(text) - lineFrom
		}
		at, line := lineFrom, strings.TrimRight(text[lineFrom:lineFrom+end], "\r")
		lineFrom += end + 1
		if c := strings.IndexByte(line, '#'); c >= 0 {
			line = line[:c]
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || trimmed[0] == ';':
			continue
		case line[0] == '[':
			section = strings.TrimSpace(strings.Trim(trimmed, "[]"))
			depType = ""
			continue
		case line[0] == ' ' || line[0] == '\t':
			if depType != "" {
				deps = appendReq(deps, file, depType, line, at, lineNo)
			}
			continue
		}
		key, value, ok := cutKey(line)
		depType = ""
		if !ok {
			continue
		}
		switch section {
		case "options":
			if key == "python_requires" {
				python = strings.TrimSpace(value)
			}
			depType = optionKeys[key]
		case "options.extras_require":
			depType = DepTypeExtra
		}
		if depType == "" || strings.HasPrefix(strings.TrimSpace(value), "file:") || strings.HasPrefix(strings.TrimSpace(value), "attr:") {
			depType = ""
			continue
		}
		valueAt := at + len(line) - len(value)
		for off := 0; off <= len(value); {
			part := value[off:]
			n := strings.IndexByte(part, ';')
			if n >= 0 {
				part = part[:n]
			}
			if r, ok := pyreq.Parse(part); !ok || !markerNames[r.Name] {
				deps = appendReq(deps, file, depType, part, valueAt+off, lineNo)
			}
			if n < 0 {
				break
			}
			off += n + 1
		}
	}
	if python != "" {
		for i := range deps {
			deps[i].PythonConstraint = python
		}
	}
	return deps, nil
}

// cutKey splits an ini `key = value` (or `key: value`) line.
func cutKey(line string) (key, value string, ok bool) {
	i := strings.IndexAny(line, "=:")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), line[i+1:], true
}

// appendReq appends the dep of the requirement s found at file offset at; blank parts and
// strings that are no requirement are ignored.
func appendReq(deps []dep.Dep, file, depType, s string, at, line int) []dep.Dep {
	if strings.TrimSpace(s) == "" {
		return deps
	}
	r, ok := pyreq.Parse(s)
	if !ok {
		return deps
	}
	d := pyreq.NewDep(Name, depType, file, r.Name, line)
	if r.URL != "" {
		d.Skip = pyreq.URLSkip(r.URL)
	} else {
		pyreq.SetVersion(&d, r.Spec, at+r.SpecStart, true, pyreq.PEP440)
	}
	return append(deps, d)
}
