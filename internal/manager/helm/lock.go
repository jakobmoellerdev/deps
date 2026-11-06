package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Locker is the helm post-processor (engine.PostProcessor): after deps patched a Chart.yaml
// (or a Helm v2 requirements.yaml) it brings the sibling Chart.lock (requirements.lock) in
// line, in process and without network access. Every exact
// dependency version whose locked version differs is written into the lock (matched by
// position and name, keeping the lock's "v" prefix style), and a rewritten range locks the
// NewVersion of its update (engine.UpdatePinner). A dependency a replacement renamed
// or moved to another repository (its old name still locked at its position) gets its new name
// and repository URL locked too. The lock digest is recomputed as Helm does, so `helm
// dependency build` accepts the lock. The `generated` timestamp is
// kept, so the same update always yields the same bytes. Lock bytes are edited in place.
type Locker struct{}

// lockKind pairs a dependency manifest with its lock file. v2 marks requirements.lock, whose
// digest is Helm 2's (requirements only) or Helm 3's (requirements and lock), see digests.
type lockKind struct {
	manifest, lock string
	v2             bool
}

var lockKinds = [...]lockKind{{chartFile, lockFile, false}, {requirementsFile, requirementsLock, true}}

// Scope returns the chart directory of a Chart.yaml or requirements.yaml; other helm files
// (helmfiles, resources) have no derived files.
func (Locker) Scope(_ context.Context, _, manifest string) (string, bool) {
	if b := path.Base(manifest); b != chartFile && b != requirementsFile {
		return "", false
	}
	return path.Dir(manifest), true
}

// Siblings lists the lock files.
func (Locker) Siblings() []string { return []string{lockFile, requirementsLock} }

// ScopeInputs implements engine.ScopeInputLister: Process reads nothing of the chart
// directory but the manifests it patches (Chart.yaml, requirements.yaml) and the lock siblings.
func (Locker) ScopeInputs(_ context.Context, _, dir string, manifests []string) (paths, digests []string, err error) {
	for _, m := range manifests {
		paths = append(paths, path.Join(dir, m))
	}
	return paths, nil, nil
}

// Process updates Chart.lock and requirements.lock from the patched Chart.yaml and
// requirements.yaml in manifests; without a lock it does nothing. The old digest must be
// reproducible from the unpatched manifest in dir, otherwise the lock is out of sync (or deps'
// digest computation would be wrong) and the update fails.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates implements engine.UpdatePinner: Process, where a dependency whose range an
// update in ups rewrote is locked at the update's NewVersion. Lockfile-only updates (locks)
// do not occur: the helm manager reads no locked versions.
func (Locker) ProcessUpdates(_ context.Context, dir string, manifests, files map[string][]byte, ups, _ map[string][]dep.Update) (map[string][]byte, error) {
	var out map[string][]byte
	for _, k := range lockKinds {
		lockSrc, ok := files[k.lock]
		newChart, patched := manifests[k.manifest]
		if !ok || !patched {
			continue
		}
		b, err := k.process(dir, newChart, lockSrc, ups[k.manifest])
		if err != nil {
			return nil, err
		}
		if b != nil {
			if out == nil {
				out = map[string][]byte{}
			}
			out[k.lock] = b
		}
	}
	return out, nil
}

// digests returns the candidate digests Helm writes for reqs and lock: Helm 3's hashReq, and
// for requirements.lock also Helm 2's hash over the requirements alone, with the repository
// aliases unresolved and resolved.
func (k lockKind) digests(reqs, lock []*chartDependency) ([]string, error) {
	resolved := resolveAliases(reqs, lock)
	sum, err := hashReq(resolved, lock)
	if err != nil || !k.v2 {
		return []string{sum}, err
	}
	raw, err := hashReqV2(reqs)
	if err != nil {
		return nil, err
	}
	res, err := hashReqV2(resolved)
	return []string{sum, raw, res}, err
}

// process returns the updated lock for the patched manifest newChart, nil when unchanged; ups
// are the updates of the manifest.
func (k lockKind) process(dir string, newChart, lockSrc []byte, ups []dep.Update) ([]byte, error) {
	chartFile, lockFile := k.manifest, k.lock
	oldChart, err := os.ReadFile(filepath.Join(dir, chartFile))
	if err != nil {
		return nil, err
	}
	oldReqs, err := chartRequirements(oldChart)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", chartFile, err)
	}
	newReqs, err := chartRequirements(newChart)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", chartFile, err)
	}
	lock, err := parseLock(lockSrc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lockFile, err)
	}
	sums, err := k.digests(oldReqs, lock.deps)
	if err != nil {
		return nil, err
	}
	algo := slices.Index(sums, lock.digest.Value)
	if algo < 0 {
		return nil, fmt.Errorf("%s is out of sync with %s (digest %s, expected %s); run helm dependency update", lockFile, chartFile, lock.digest.Value, sums[0])
	}

	newDeps := make([]*chartDependency, len(lock.deps))
	var edits []patch.Edit
	lines := yamlspan.LineStarts(lockSrc)
	edit := func(node *yaml.Node, old, v, what, chart string) error {
		off := -1
		if node != nil {
			off = yamlspan.Locate(lockSrc, lines, node)
		}
		if off < 0 {
			return fmt.Errorf("%s: cannot locate the %s of %s", lockFile, what, chart)
		}
		edits = append(edits, patch.Edit{Start: off, End: off + len(old), Old: old, New: v})
		return nil
	}
	for i, ld := range lock.deps {
		nd := *ld
		newDeps[i] = &nd
		if i >= len(newReqs) {
			continue
		}
		nr, f := newReqs[i], lock.fields[i]
		want := nr.Version
		if !ExactVersion(want) {
			if want = rangeVersion(oldReqs, i, nr, ups); want == "" {
				continue
			}
		}
		// ld locks the dependency at i, which a replacement may have moved to another chart.
		same := i < len(oldReqs) && oldReqs[i].Name == ld.Name
		if nr.Name != ld.Name {
			if !same {
				continue
			}
			if err := edit(f.name, ld.Name, nr.Name, "name", ld.Name); err != nil {
				return nil, err
			}
			nd.Name = nr.Name
		}
		if same && nr.Repository != oldReqs[i].Repository && !isAlias(nr.Repository) && nr.Repository != ld.Repository {
			if err := edit(f.repository, ld.Repository, nr.Repository, "repository", ld.Name); err != nil {
				return nil, err
			}
			nd.Repository = nr.Repository
		}
		if v := lockStyle(ld.Version, want); v != ld.Version {
			if err := edit(f.version, ld.Version, v, "version", ld.Name); err != nil {
				return nil, err
			}
			nd.Version = v
		}
	}
	if sums, err = k.digests(newReqs, newDeps); err != nil {
		return nil, err
	}
	if sum := sums[algo]; sum != lock.digest.Value {
		off := yamlspan.Locate(lockSrc, lines, lock.digest)
		if off < 0 {
			return nil, fmt.Errorf("%s: cannot locate the digest", lockFile)
		}
		edits = append(edits, patch.Edit{Start: off, End: off + len(lock.digest.Value), Old: lock.digest.Value, New: sum})
	}
	if len(edits) == 0 {
		return nil, nil
	}
	out, err := patch.Apply(lockSrc, edits)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lockFile, err)
	}
	return out, nil
}

// rangeVersion returns the version to lock for the range dependency nr at i of the patched
// manifest: the NewVersion of the update in ups that rewrote the range written at i in oldReqs
// (same chart, the old range as its dep's Range); "" when no update rewrote it.
func rangeVersion(oldReqs []*chartDependency, i int, nr *chartDependency, ups []dep.Update) string {
	if i >= len(oldReqs) || oldReqs[i].Version == nr.Version {
		return ""
	}
	old := oldReqs[i]
	for _, u := range ups {
		if u.Dep.Name == old.Name && u.Dep.Range == old.Version {
			return u.NewVersion
		}
	}
	return ""
}

// chartDependency mirrors Helm's chart.Dependency field for field, with its JSON names and
// order: the lock digest is a hash over its JSON encoding.
type chartDependency struct {
	Name         string   `json:"name" yaml:"name"`
	Version      string   `json:"version,omitempty" yaml:"version,omitempty"`
	Repository   string   `json:"repository" yaml:"repository"`
	Condition    string   `json:"condition,omitempty" yaml:"condition,omitempty"`
	Tags         []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	ImportValues []any    `json:"import-values,omitempty" yaml:"import-values,omitempty"`
	Alias        string   `json:"alias,omitempty" yaml:"alias,omitempty"`
}

// chartRequirements decodes the dependencies of a Chart.yaml. A bare `-` entry decodes as
// nil; it is rejected like in parseLock, as skipping it would misalign the lock indexes.
func chartRequirements(src []byte) ([]*chartDependency, error) {
	var c struct {
		Dependencies []*chartDependency `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(src, &c); err != nil {
		return nil, err
	}
	if hasNil(c.Dependencies) {
		return nil, errors.New("malformed dependencies")
	}
	return c.Dependencies, nil
}

// chartLock is a decoded Chart.lock with the nodes deps edits.
type chartLock struct {
	deps   []*chartDependency
	fields []lockFields // per dep
	digest *yaml.Node
}

// lockFields are the scalars of a locked dependency, nil when absent.
type lockFields struct{ name, repository, version *yaml.Node }

func parseLock(src []byte) (*chartLock, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("empty lock")
	}
	root := doc.Content[0]
	l := &chartLock{digest: yamlspan.Str(yamlspan.Get(root, "digest"))}
	if l.digest == nil {
		return nil, errors.New("no digest")
	}
	if deps := yamlspan.Get(root, "dependencies"); deps != nil {
		if err := deps.Decode(&l.deps); err != nil {
			return nil, err
		}
		for _, n := range deps.Content {
			l.fields = append(l.fields, lockFields{
				name:       yamlspan.Str(yamlspan.Get(n, "name")),
				repository: yamlspan.Str(yamlspan.Get(n, "repository")),
				version:    yamlspan.Str(yamlspan.Get(n, "version")),
			})
		}
	}
	if len(l.fields) != len(l.deps) || hasNil(l.deps) {
		return nil, errors.New("malformed dependencies")
	}
	return l, nil
}

func hasNil(deps []*chartDependency) bool {
	for _, d := range deps {
		if d == nil {
			return true
		}
	}
	return false
}

// resolveAliases returns reqs with `@name` and `alias:name` repositories replaced by the URL
// locked for the same dependency, as `helm dependency build` resolves them through the
// repository config before hashing.
func resolveAliases(reqs, locked []*chartDependency) []*chartDependency {
	out := make([]*chartDependency, len(reqs))
	for i, r := range reqs {
		out[i] = r
		if r == nil || !isAlias(r.Repository) {
			continue
		}
		if i < len(locked) && locked[i].Name == r.Name {
			c := *r
			c.Repository = locked[i].Repository
			out[i] = &c
		}
	}
	return out
}

// isAlias reports whether a Chart.yaml repository names a repository of the user's Helm
// repository config (`@name`, `alias:name`).
func isAlias(repo string) bool {
	return strings.HasPrefix(repo, "@") || strings.HasPrefix(repo, "alias:")
}

// hashReq is Helm's resolver.HashReq: "sha256:" + hex sha256 of the JSON of [req, lock].
// It stays on encoding/json v1, as Helm hashes it: v2 would keep `enabled: false` (omitempty
// only omits empty JSON) and change the digest helm checks.
func hashReq(req, lock []*chartDependency) (string, error) {
	data, err := json.Marshal([2][]*chartDependency{req, lock})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// hashReqV2 is Helm 2's resolver.HashReq: "sha256:" + hex sha256 of the JSON of the
// requirements ({"dependencies": req}) alone.
func hashReqV2(req []*chartDependency) (string, error) {
	data, err := json.Marshal(struct {
		Dependencies []*chartDependency `json:"dependencies"`
	}{req})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// lockStyle writes version v with the "v" prefix convention of the locked version old.
func lockStyle(old, v string) string {
	bare := strings.TrimPrefix(v, "v")
	if strings.HasPrefix(old, "v") {
		return "v" + bare
	}
	return bare
}
