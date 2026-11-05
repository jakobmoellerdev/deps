package nodetool

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Want is one dependency whose locked version ReadLocked looks up.
type Want struct {
	Manifest string // slash path of its package.json relative to the scanned root
	Name     string
	Range    string // the range package.json gives it: yarn.lock keys its entries by name@range
}

// LockState is what the lockfile of a want's lock root says about it.
type LockState struct {
	Version string // the version the lockfile resolves the dependency to; "" = not locked
	Unread  bool   // the lock root's lockfile is not one ReadLocked reads, or does not parse
}

// lockWant is a Want within its lock root: member is the manifest's directory relative to it
// ("" for the root).
type lockWant struct {
	member, name, rng string
}

// ReadLocked returns the lock state of every want, in order. The lock root is Locker.Scope's
// and its lockfile the one Locker updates (the packageManager tool's first); each is read once
// per lock root, keeping only what the wants need:
//   - package-lock.json and npm-shrinkwrap.json (lockfileVersion 2 or 3), streamed: the
//     `packages` entries, where Node's resolution applies (a member's own
//     `<member>/node_modules/<name>` wins over the hoisted `node_modules/<name>`); lockfileVersion
//     1: the top-level `dependencies` entries;
//   - pnpm-lock.yaml: the importers section (the member's directory, "." for the root; the
//     top-level sections of a single-project lockfile before version 6), up to `packages:`;
//   - yarn.lock (classic and berry): the entry whose header lists the descriptor
//     `<name>@<range>` (berry: `<name>@npm:<range>`);
//   - bun.lock: the `packages` entry `<member name>/<name>`, else the hoisted `<name>`.
//
// Versions that are not ones (links, `workspace:`, aliases) are not locked. Wants of a lockfile
// that fails to parse are Unread (its error is returned, prefixed with its slash path), as are
// those of a package-lock.json without lockfileVersion.
func ReadLocked(root string, wants []Want) ([]LockState, []error) {
	out := make([]LockState, len(wants))
	scopes := map[string][]int{} // lock root -> want indexes
	var order []string
	scopeOf := map[string]string{} // manifest -> lock root ("" = none)
	cache := newScopeCache()
	for i, w := range wants {
		dir, seen := scopeOf[w.Manifest]
		if !seen {
			if d, ok := lockScope(context.Background(), root, w.Manifest, cache); ok {
				dir = d
			}
			scopeOf[w.Manifest] = dir
		}
		if dir == "" {
			continue
		}
		if _, ok := scopes[dir]; !ok {
			order = append(order, dir)
		}
		scopes[dir] = append(scopes[dir], i)
	}
	var errs []error
	for _, dir := range order {
		idx := scopes[dir]
		ws := make([]lockWant, len(idx))
		for j, i := range idx {
			ws[j] = lockWant{member: memberDir(dir, wants[i].Manifest), name: wants[i].Name, rng: wants[i].Range}
		}
		name, versions, err := readScope(filepath.Join(root, filepath.FromSlash(dir)), ws)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path.Join(dir, name), err))
		}
		for j, i := range idx {
			if versions == nil {
				out[i].Unread = true
				continue
			}
			out[i].Version = versions[j]
		}
	}
	return out, errs
}

// lockKeys are the package-lock.json `packages` keys that may hold w's locked version, in
// resolution order.
func lockKeys(w lockWant) []string {
	hoisted := "node_modules/" + w.name
	if w.member == "" {
		return []string{hoisted}
	}
	return []string{w.member + "/node_modules/" + w.name, hoisted}
}

// memberDir is the directory of manifest relative to the lock root dir ("" for the root).
func memberDir(dir, manifest string) string {
	m := path.Dir(manifest)
	switch {
	case m == dir:
		return ""
	case dir == ".":
		return m
	}
	return strings.TrimPrefix(m, dir+"/")
}

// readScope reads the locked versions of ws (one per want, in order) from the lockfile of the
// lock root at the absolute dir, whose name it returns. versions is nil when the lockfile is
// not read: there is none, it does not parse, or it is a package-lock.json without
// lockfileVersion.
func readScope(dir string, ws []lockWant) (name string, versions []string, err error) {
	if name, err = Lockfile(dir); err != nil || name == "" {
		return name, nil, err
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return name, nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	switch name {
	case PnpmLock:
		versions, err = pnpmVersions(r, ws)
	case YarnLock:
		versions, err = yarnVersions(r, ws)
	case BunLock:
		versions, err = bunVersions(r, ws)
	default:
		versions, err = npmVersions(r, ws)
	}
	if err != nil {
		return name, nil, err
	}
	return name, versions, nil
}

// npmVersions reads the locked versions of ws from a package-lock.json or npm-shrinkwrap.json
// (lockedVersions); nil without lockfileVersion.
func npmVersions(r *bufio.Reader, ws []lockWant) ([]string, error) {
	keys := map[string]bool{}
	for _, w := range ws {
		for _, k := range lockKeys(w) {
			keys[k] = true
		}
	}
	lockVersion, versions, err := lockedVersions(r, keys)
	if err != nil || lockVersion < 1 {
		return nil, err
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		for _, k := range lockKeys(w) {
			if v := versions[k]; v != "" {
				out[i] = v
				break
			}
		}
	}
	return out, nil
}

// Lockfile returns the name of the lockfile Locker updates in the lock root at the absolute
// dir (pickLock with the root package.json's packageManager tool), or "" without one.
func Lockfile(dir string) (string, error) {
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	m, _ := parseRoot(pkg) // a broken package.json is left to the package manager
	tool, _ := m.tool()
	present := map[string][]byte{}
	for _, n := range lockfiles {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
			present[n] = []byte{}
		}
	}
	return pickLock(present, tool), nil
}

// lockedVersions stream-decodes a package-lock.json and returns its lockfileVersion and the
// version of each `packages` entry named in keys (links and entries without a version are left
// out); other entries are skipped without decoding. For lockfileVersion 1 (no `packages`) the
// hoisted keys `node_modules/<name>` come from the top-level `dependencies` entries instead,
// which later versions only keep for old npm releases (skipped once lockfileVersion is 2 or more).
func lockedVersions(r *bufio.Reader, keys map[string]bool) (int, map[string]string, error) {
	dec := jsontext.NewDecoder(r, lenient)
	if err := enterObject(dec); err != nil {
		return 0, nil, err
	}
	lockVersion := 0
	versions := map[string]string{}
	var v1 map[string]string // dependencies entries by name
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, nil, err
		}
		switch k := tok.String(); {
		case k == "lockfileVersion":
			if err := json.UnmarshalDecode(dec, &lockVersion); err != nil {
				return 0, nil, fmt.Errorf("lockfileVersion: %w", err)
			}
		case k == "packages":
			if err := lockEntries(dec, k, keys, versions); err != nil {
				return 0, nil, err
			}
		case k == "dependencies" && lockVersion < 2:
			names := map[string]bool{}
			for key := range keys {
				if n, ok := strings.CutPrefix(key, "node_modules/"); ok && !strings.Contains(n, "/node_modules/") {
					names[n] = true
				}
			}
			v1 = map[string]string{}
			if err := lockEntries(dec, k, names, v1); err != nil {
				return 0, nil, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return 0, nil, err
			}
		}
	}
	if lockVersion < 2 && v1 != nil {
		versions = make(map[string]string, len(v1))
		for n, v := range v1 {
			versions["node_modules/"+n] = v
		}
	}
	return lockVersion, versions, nil
}

// lockEntries reads the object of the lockfile section name and records the version of each
// entry named in keys in versions (links and entries without a version are left out).
func lockEntries(dec *jsontext.Decoder, name string, keys map[string]bool, versions map[string]string) error {
	if err := enterObject(dec); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for dec.PeekKind() != '}' {
		k, ok, err := wantedName(dec, keys)
		if err != nil {
			return err
		}
		if !ok {
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		var e struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		}
		if err := json.UnmarshalDecode(dec, &e, lenient); err != nil {
			return fmt.Errorf("%s[%q]: %w", name, k, err)
		}
		if !e.Link && e.Version != "" {
			versions[k] = e.Version
		}
	}
	_, err := dec.ReadToken()
	return err
}

// wantedName reads the next object name and reports whether keys holds it. Names without
// escapes are looked up without allocating.
func wantedName(dec *jsontext.Decoder, keys map[string]bool) (string, bool, error) {
	if dec.PeekKind() != '"' {
		// Not a name: ReadToken reports the syntax error (ReadValue would word it differently).
		tok, err := dec.ReadToken()
		if err != nil {
			return "", false, err
		}
		k := tok.String()
		return k, keys[k], nil
	}
	n, err := dec.ReadValue()
	if err != nil {
		return "", false, err
	}
	raw := n[1 : len(n)-1]
	if bytes.IndexByte(raw, '\\') < 0 {
		if !keys[string(raw)] {
			return "", false, nil
		}
		return string(raw), true, nil
	}
	b, _ := jsontext.AppendUnquote(nil, n) // the decoder already validated the string
	k := string(b)
	return k, keys[k], nil
}

// enterObject reads the '{' of the next value.
func enterObject(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return fmt.Errorf("want an object, got %v", tok.Kind())
	}
	return nil
}

// lockGroup is the lockfile-only updates of one manifest: dir is its directory relative to the
// lock root ("" for the root) and specs are its `<name>@<version>`, sorted.
type lockGroup struct {
	dir   string
	specs []string
}

// lockGroups groups the lockfile-only updates (manifest path relative to the lock root ->
// updates) by manifest: the root manifest first, then the workspace members in path order.
func lockGroups(locks map[string][]dep.Update) []lockGroup {
	manifests := make([]string, 0, len(locks))
	for m := range locks {
		manifests = append(manifests, m)
	}
	slices.SortFunc(manifests, func(a, b string) int {
		// the root manifest first
		if (a == "package.json") != (b == "package.json") {
			if a == "package.json" {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	groups := make([]lockGroup, 0, len(manifests))
	for _, m := range manifests {
		g := lockGroup{specs: make([]string, 0, len(locks[m]))}
		if m != "package.json" {
			g.dir = path.Dir(m)
		}
		for _, u := range locks[m] {
			g.specs = append(g.specs, u.Dep.Name+"@"+u.NewVersion)
		}
		slices.Sort(g.specs)
		g.specs = slices.Compact(g.specs)
		groups = append(groups, g)
	}
	return groups
}

// lockCommands returns the commands applying the lockfile-only updates after c, the lockfile's
// install command:
//   - npm: c naming `<name>@<version>`, per manifest (lockGroups; a member with
//     `--workspace=<dir>`);
//   - pnpm: `pnpm update --no-save --lockfile-only --ignore-scripts --filter <sel>
//     <name>@<version>…` per manifest, sel being `.` for the root and `./<dir>` for a member;
//     --no-save keeps the specifiers;
//   - yarn berry: `yarn set resolution <name>@npm:<range> npm:<version>` per descriptor, in
//     order.
//
// yarn classic and bun have none: their updates are installed from pinned manifests
// (lockOnly).
func lockCommands(c cmdSpec, lockName string, berry bool, locks map[string][]dep.Update) []cmdSpec {
	var cmds []cmdSpec
	with := func(args ...string) {
		cc := c
		cc.args = args
		cmds = append(cmds, cc)
	}
	switch {
	case lockName == PnpmLock:
		for _, g := range lockGroups(locks) {
			sel := "."
			if g.dir != "" {
				sel = "./" + g.dir
			}
			with(append([]string{"update", "--no-save", "--lockfile-only", "--ignore-scripts", "--filter", sel}, g.specs...)...)
		}
	case lockName == YarnLock && berry:
		var res [][2]string
		for _, ups := range locks {
			for _, u := range ups {
				res = append(res, [2]string{u.Dep.Name + "@npm:" + u.Dep.Range, "npm:" + u.NewVersion})
			}
		}
		slices.SortFunc(res, func(a, b [2]string) int { return cmp.Or(strings.Compare(a[0], b[0]), strings.Compare(a[1], b[1])) })
		for _, r := range slices.Compact(res) {
			with("set", "resolution", r[0], r[1])
		}
	case toolOf[lockName] == "npm":
		for _, g := range lockGroups(locks) {
			args := slices.Clone(c.args)
			if g.dir != "" {
				args = append(args, "--workspace="+g.dir)
			}
			with(append(args, g.specs...)...)
		}
	}
	return cmds
}

// restoreRanges rewrites the ranges npm saved into the lockfile's own package entries
// (`packages[""]` for the root, `packages["<member dir>"]` for a member) when it installed
// `<name>@<version>`, and those bun recorded in `workspaces[""]`/`workspaces["<member dir>"]`
// of bun.lock from the pinned manifests: each becomes the range package.json still holds
// (dep.Dep.Range under its DepType section), so the lockfile stays in sync with the unchanged
// manifest. An entry that is missing is an error: `npm ci` would reject the lockfile.
func restoreRanges(lock []byte, locks map[string][]dep.Update, lockName string) ([]byte, error) {
	top, doc := "packages", lock
	if lockName == BunLock {
		top, doc = "workspaces", jsonc.Strip(lock)
	}
	want := map[string]string{}
	for m, ups := range locks {
		key := ""
		if m != "package.json" {
			key = path.Dir(m)
		}
		for _, u := range ups {
			want[strings.Join([]string{top, key, u.Dep.DepType, u.Dep.Name}, "\x00")] = u.Dep.Range
		}
	}
	edits, found, err := stringEdits(doc, want)
	if err != nil {
		return nil, err
	}
	var missing []string
	for p := range want {
		if !found[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		f := strings.Split(missing[0], "\x00")
		return nil, fmt.Errorf("%s has no %s entry for %s in package %q to restore its range", lockName, f[2], f[3], f[1])
	}
	return patch.Apply(lock, edits)
}

// pinManifests returns the manifests (by path relative to the lock root) of the lockfile-only
// updates with each updated dependency's range replaced by its new version, for the tools that
// cannot install one version within a range (yarn classic, bun).
func pinManifests(manifests map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	out := make(map[string][]byte, len(locks))
	for m, ups := range locks {
		want := map[string]string{}
		for _, u := range ups {
			want[u.Dep.DepType+"\x00"+u.Dep.Name] = u.NewVersion
		}
		edits, found, err := stringEdits(manifests[m], want)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		for _, u := range ups {
			if !found[u.Dep.DepType+"\x00"+u.Dep.Name] {
				return nil, fmt.Errorf("%s has no %s entry for %s", m, u.Dep.DepType, u.Dep.Name)
			}
		}
		if out[m], err = patch.Apply(manifests[m], edits); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// stringEdits returns the edits replacing the string values at the object member paths of want
// (the names from the document's top object down, joined with "\x00") with the JSON-quoted new
// values, and the paths it found. Only the objects on the way to a wanted path are entered.
func stringEdits(doc []byte, want map[string]string) ([]patch.Edit, map[string]bool, error) {
	on := map[string]bool{} // the paths of the objects to enter
	for p := range want {
		for i := range len(p) {
			if p[i] == 0 {
				on[p[:i]] = true
			}
		}
	}
	var edits []patch.Edit
	found := map[string]bool{}
	dec := jsontext.NewDecoder(bytes.NewReader(doc), lenient)
	var walk func(at string, top bool) error
	walk = func(at string, top bool) error {
		if err := enterObject(dec); err != nil {
			return err
		}
		for dec.PeekKind() != '}' {
			k, err := dec.ReadToken()
			if err != nil {
				return err
			}
			p := k.String()
			if !top {
				p = at + "\x00" + p
			}
			if v, ok := want[p]; ok && dec.PeekKind() == '"' {
				old, err := dec.ReadValue()
				if err != nil {
					return err
				}
				end := int(dec.InputOffset())
				q, err := jsontext.AppendQuote(nil, v)
				if err != nil {
					return err
				}
				edits = append(edits, patch.Edit{Start: end - len(old), End: end, Old: string(old), New: string(q)})
				found[p] = true
				continue
			}
			if on[p] && dec.PeekKind() == '{' {
				if err := walk(p, false); err != nil {
					return err
				}
				continue
			}
			if err := dec.SkipValue(); err != nil {
				return err
			}
		}
		_, err := dec.ReadToken()
		return err
	}
	if err := walk("", true); err != nil {
		return nil, nil, err
	}
	return edits, found, nil
}
