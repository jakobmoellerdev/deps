// Package vcpkg extracts the deps of vcpkg manifests (`vcpkg.json`) and configurations
// (`vcpkg-configuration.json`, or a manifest's embedded `vcpkg-configuration`): the registry
// baselines, commits of their git repositories, and the port versions of `version>=`
// constraints and `overrides`, looked up in the versions database of the port's registry at
// its baseline.
package vcpkg

import (
	"path"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "vcpkg"

// Dep types.
const (
	DepTypeBuiltinBaseline = "builtin-baseline" // vcpkg.json builtin-baseline
	DepTypeDefaultRegistry = "default-registry" // configuration default-registry baseline
	DepTypeRegistry        = "registry"         // configuration registries[] baseline
	DepTypeDependencies    = "dependencies"     // dependencies[] version>=
	DepTypeOverrides       = "overrides"        // overrides[] version
)

// BuiltinRepo is the repository of the builtin registry.
const BuiltinRepo = "https://github.com/microsoft/vcpkg"

// Versioning is the scheme of port versions: up to four numeric parts separated by `.` or `-`
// (dates: 2024-01-15) and an optional `#<port-version>`, which orders after them.
const Versioning = versioning.RegexPrefix + `^(?<major>\d+)(?:[.-](?<minor>\d+))?(?:[.-](?<patch>\d+))?(?:[.-](?<build>\d+))?(?:#(?<revision>\d+))?$`

const (
	dsRefs      = "git-refs"
	headVersion = "HEAD"
)

// registry is where ports resolve to: the versions database at base, or skip.
type registry struct {
	base     string // vcpkg.RawBase of the repository at the baseline; "" = builtin without baseline
	baseline string // BaselineKey of the baseline dep when it is updatable, else ""
	builtin  bool
	skip     string
	packages []string // registries[] packages patterns
}

// config is a vcpkg configuration.
type config struct {
	def        *registry // default-registry; nil = builtin (unset)
	defNull    bool      // "default-registry": null
	registries []registry
	dir        string   // directory of the file declaring it
	overlays   []string // overlay-ports, relative to dir
}

// manifest is what Link needs of a vcpkg.json.
type manifest struct {
	baseline    string // builtin-baseline
	baselineKey string // BaselineKey of the builtin-baseline dep when it is updatable, else ""
	config      *config
}

type vcpkgManager struct {
	mu        sync.Mutex
	manifests map[string]manifest // by file
	configs   map[string]*config  // vcpkg-configuration.json by directory
	exists    func(path string) bool
}

// New returns the vcpkg manager.
func New() manager.Manager {
	return &vcpkgManager{manifests: map[string]manifest{}, configs: map[string]*config{}}
}

func (*vcpkgManager) Name() string { return Name }

// Match reports whether the slash path is a vcpkg.json or vcpkg-configuration.json.
func (*vcpkgManager) Match(p string) bool {
	b := path.Base(p)
	return b == "vcpkg.json" || b == "vcpkg-configuration.json"
}

// Extract returns the baselines of the file and, for a manifest, its port version deps, whose
// registry Link resolves.
func (m *vcpkgManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	root, err := parseJSON(src)
	if err != nil {
		return nil, err
	}
	if root.kind != '{' {
		return nil, nil
	}
	x := extractor{file: file, src: src}
	m.mu.Lock()
	if env.Exists != nil {
		m.exists = env.Exists
	}
	m.mu.Unlock()
	if path.Base(file) == "vcpkg-configuration.json" {
		cfg := x.config(root)
		m.mu.Lock()
		m.configs[path.Dir(file)] = cfg
		m.mu.Unlock()
		return x.deps, nil
	}
	var mf manifest
	if b := root.get("builtin-baseline"); b != nil && b.kind == '"' {
		mf.baseline = b.str
		mf.baselineKey = x.baseline(DepTypeBuiltinBaseline, BuiltinRepo, "", b)
	}
	if c := root.get("vcpkg-configuration"); c != nil && c.kind == '{' {
		mf.config = x.config(c)
	}
	for _, e := range root.get("dependencies").items() {
		if v := e.get("version>="); v != nil {
			x.port(DepTypeDependencies, e.get("name").text(), v)
		}
	}
	for _, e := range root.get("overrides").items() {
		x.override(e)
	}
	m.mu.Lock()
	m.manifests[file] = mf
	m.mu.Unlock()
	return x.deps, nil
}

type extractor struct {
	file  string
	src   []byte
	lines []int // line starts of src, computed on first use
	deps  []dep.Dep
}

func (x *extractor) line(off int) int {
	if x.lines == nil {
		x.lines = yamlspan.LineStarts(x.src)
	}
	return yamlspan.LineOf(x.lines, off)
}

// baseline adds the dep of a registry baseline commit of the git repository repo, tracking
// branch ref ("" = HEAD). It returns the dep's BaselineKey when it is updatable, else "".
func (x *extractor) baseline(depType, repo, ref string, b *node) string {
	name, source := manager.GitRepo(repo)
	d := dep.Dep{Manager: Name, Datasource: dsRefs, Versioning: "tag", DepType: depType, File: x.file, Line: x.line(b.start),
		Name: name, Source: source, Version: headVersion, Digest: b.text(), DigestSpan: b.span}
	if ref != "" {
		d.Version = ref
	}
	switch {
	case source == "":
		d.Name, d.Skip = repo, "unrecognized repository"
	case b.kind != '"' || !manager.IsHex(b.str, 40):
		d.Skip = "baseline is not a commit"
	case b.span == (dep.Span{}):
		d.Skip = "cannot locate value"
	case validTag(d.Version):
		d.Skip = "reference is a tag"
	}
	x.deps = append(x.deps, d)
	if d.Skip != "" {
		return ""
	}
	return BaselineKey(d)
}

// BaselineKey identifies the baseline dep d (its file and the offset of its commit); the
// ports read at that baseline carry it as dep.Dep.Baseline.
func BaselineKey(d dep.Dep) string {
	return d.File + ":" + strconv.Itoa(d.DigestSpan.Start)
}

// config reads a configuration object, adding the deps of its registry baselines.
func (x *extractor) config(c *node) *config {
	cfg := &config{dir: path.Dir(x.file)}
	for _, o := range c.get("overlay-ports").items() {
		if o.kind == '"' && o.str != "" && !path.IsAbs(o.str) {
			cfg.overlays = append(cfg.overlays, o.str)
		}
	}
	if d := c.get("default-registry"); d != nil {
		if d.kind == 'n' {
			cfg.defNull = true
		} else {
			r := x.registry(DepTypeDefaultRegistry, d)
			cfg.def = &r
		}
	}
	for _, e := range c.get("registries").items() {
		if e.get("kind").text() == "artifact" {
			continue // artifact registries hold no ports
		}
		r := x.registry(DepTypeRegistry, e)
		for _, p := range e.get("packages").items() {
			if p.kind == '"' {
				r.packages = append(r.packages, p.str)
			}
		}
		cfg.registries = append(cfg.registries, r)
	}
	return cfg
}

// registry reads a registry object, adding the dep of its baseline.
func (x *extractor) registry(depType string, r *node) registry {
	b := r.get("baseline")
	switch kind := r.get("kind").text(); kind {
	case "builtin":
		if b == nil {
			return registry{builtin: true}
		}
		key := x.baseline(depType, BuiltinRepo, "", b)
		return registry{builtin: true, base: vcpkg.RawBase(BuiltinRepo, b.text()), baseline: key}
	case "git":
		repo := r.get("repository").text()
		if b == nil {
			return registry{skip: "git registry without baseline"}
		}
		key := x.baseline(depType, repo, r.get("reference").text(), b)
		if _, source := manager.GitRepo(repo); source != "" && !strings.HasPrefix(repo, "git@") {
			return registry{base: vcpkg.RawBase(source, b.text()), baseline: key}
		}
		return registry{skip: "unrecognized registry repository"}
	default:
		return registry{skip: "unsupported registry kind " + kind}
	}
}

// port adds the dep of a port version v; Link sets its registry.
func (x *extractor) port(depType, name string, v *node) {
	d := dep.Dep{Manager: Name, Datasource: vcpkg.Name, Versioning: Versioning, DepType: depType,
		File: x.file, Line: x.line(v.start), Name: name, Version: v.text(), RefSpan: v.span}
	switch {
	case name == "":
		d.Skip = "no name"
	case v.kind != '"':
		d.Skip = "version is not a string"
	case v.span == (dep.Span{}):
		d.Skip = "cannot locate value"
	}
	x.deps = append(x.deps, d)
}

// override adds the dep of an overrides[] entry.
func (x *extractor) override(e *node) {
	name := e.get("name").text()
	for _, k := range [...]string{"version", "version-semver", "version-date"} {
		if v := e.get(k); v != nil {
			x.port(DepTypeOverrides, name, v)
			if e.has("port-version") {
				x.deps[len(x.deps)-1].Skip = "port-version field"
			}
			return
		}
	}
	if v := e.get("version-string"); v != nil {
		x.port(DepTypeOverrides, name, v)
		x.deps[len(x.deps)-1].Skip = "version-string"
	}
}

// Link resolves the registry of every port version dep: the registry of the manifest's
// configuration (embedded, else the vcpkg-configuration.json next to it) whose packages
// match the port, else its default registry, else the builtin registry at builtin-baseline.
func (m *vcpkgManager) Link(files [][]dep.Dep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, deps := range files {
		for i := range deps {
			d := &deps[i]
			if d.Datasource != vcpkg.Name || d.Skip != "" {
				continue
			}
			mf := m.manifests[d.File]
			cfg := mf.config
			if cfg == nil {
				cfg = m.configs[path.Dir(d.File)]
			}
			r := resolve(cfg, d.Name)
			switch {
			case m.overlay(cfg, d.Name):
				d.Skip = "overlay port"
			case r.skip != "":
				d.Skip = r.skip
			case r.base != "":
				d.Source, d.Baseline = r.base, r.baseline
			case mf.baseline != "" && manager.IsHex(mf.baseline, 40):
				d.Source, d.Baseline = vcpkg.RawBase(BuiltinRepo, mf.baseline), mf.baselineKey
			default:
				d.Skip = "no builtin-baseline"
			}
		}
	}
	m.manifests, m.configs = map[string]manifest{}, map[string]*config{}
}

// overlay reports whether port is a port of an overlay-ports directory of cfg (a directory of
// ports, or the port's own directory), which vcpkg prefers to every registry.
func (m *vcpkgManager) overlay(cfg *config, port string) bool {
	if cfg == nil || m.exists == nil {
		return false
	}
	for _, o := range cfg.overlays {
		dir := path.Join(cfg.dir, strings.ReplaceAll(o, `\`, "/"))
		if path.Base(dir) != port {
			dir = path.Join(dir, port)
		}
		if m.exists(path.Join(dir, "portfile.cmake")) {
			return true
		}
	}
	return false
}

// resolve returns the registry of port under cfg (nil = builtin).
func resolve(cfg *config, port string) registry {
	if cfg == nil {
		return registry{builtin: true}
	}
	best, bestLen := -1, -1
	for i, r := range cfg.registries {
		for _, p := range r.packages {
			n := -1
			switch {
			case p == port:
				n = len(p) + 1 // an exact name beats any pattern
			case strings.HasSuffix(p, "*") && strings.HasPrefix(port, p[:len(p)-1]):
				n = len(p) - 1
			}
			if n > bestLen {
				best, bestLen = i, n
			}
		}
	}
	switch {
	case best >= 0:
		return cfg.registries[best]
	case cfg.defNull:
		return registry{skip: "no registry for the port"}
	case cfg.def != nil:
		return *cfg.def
	}
	return registry{builtin: true}
}

// Edits rewrites a baseline commit or a port version.
func (*vcpkgManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == dsRefs {
		if u.NewDigest == "" || u.NewDigest == d.Digest {
			return nil, nil
		}
		return []patch.Edit{{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest}}, nil
	}
	if u.NewVersion == d.Version {
		return nil, nil
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

func validTag(s string) bool {
	_, ok := versioning.ParseTag(s)
	return ok
}
