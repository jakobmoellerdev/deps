package nodetool

import (
	"bufio"
	"bytes"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Want is one dependency whose locked version ReadLocked looks up.
type Want struct {
	Manifest string // slash path of its package.json relative to the scanned root
	Name     string
}

// LockState is what the lockfile of a want's lock root says about it.
type LockState struct {
	Version string // the version package-lock.json resolves the dependency to; "" = not locked
	Unread  bool   // the lock root's lockfile is not one ReadLocked reads
}

// ReadLocked returns the lock state of every want, in order. The lock root is Locker.Scope's
// and its lockfile the one Locker updates (the packageManager tool's first). Only
// package-lock.json with lockfileVersion 2 or 3 is read: once per lock root, streamed, keeping
// the `packages` entries of the wanted names only. Node's resolution applies: a member's own
// `<member>/node_modules/<name>` entry wins over the hoisted `node_modules/<name>`. Wants of
// pnpm-lock.yaml, yarn.lock and v1 package-lock.json roots are Unread, as are those of a
// lockfile that fails to parse (its error is returned, prefixed with its slash path).
func ReadLocked(root string, wants []Want) ([]LockState, []error) {
	out := make([]LockState, len(wants))
	type scope struct {
		dir  string
		idx  []int
		keys map[string]bool
	}
	scopes := map[string]*scope{}
	var order []string
	scopeOf := map[string]string{} // manifest -> lock root ("" = none)
	for i, w := range wants {
		dir, seen := scopeOf[w.Manifest]
		if !seen {
			if d, ok := (Locker{}).Scope(root, w.Manifest); ok {
				dir = d
			}
			scopeOf[w.Manifest] = dir
		}
		if dir == "" {
			continue
		}
		s := scopes[dir]
		if s == nil {
			s = &scope{dir: dir, keys: map[string]bool{}}
			scopes[dir] = s
			order = append(order, dir)
		}
		s.idx = append(s.idx, i)
		for _, k := range lockKeys(dir, w) {
			s.keys[k] = true
		}
	}
	var errs []error
	for _, dir := range order {
		s := scopes[dir]
		versions, readable, err := readScope(filepath.Join(root, filepath.FromSlash(dir)), s.keys)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path.Join(dir, PackageLock), err))
		}
		for _, i := range s.idx {
			if !readable {
				out[i].Unread = true
				continue
			}
			for _, k := range lockKeys(dir, wants[i]) {
				if v := versions[k]; v != "" {
					out[i].Version = v
					break
				}
			}
		}
	}
	return out, errs
}

// lockKeys are the package-lock.json `packages` keys that may hold w's locked version, in
// resolution order.
func lockKeys(dir string, w Want) []string {
	hoisted := "node_modules/" + w.Name
	member := memberDir(dir, w.Manifest)
	if member == "" {
		return []string{hoisted}
	}
	return []string{member + "/node_modules/" + w.Name, hoisted}
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

// readScope reads the locked versions of keys in the lock root at the absolute dir. readable
// is false when the root's lockfile is not a package-lock.json of version 2 or later, or does
// not parse.
func readScope(dir string, keys map[string]bool) (versions map[string]string, readable bool, err error) {
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	m, _ := parseRoot(pkg) // a broken package.json is left to the package manager
	tool, _ := m.tool()
	present := map[string][]byte{}
	for _, n := range lockfiles {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
			present[n] = []byte{}
		}
	}
	if pickLock(present, tool) != PackageLock {
		return nil, false, nil
	}
	f, err := os.Open(filepath.Join(dir, PackageLock))
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	version, versions, err := lockedVersions(bufio.NewReader(f), keys)
	if err != nil || version < 2 {
		return nil, false, err
	}
	return versions, true, nil
}

// lockedVersions stream-decodes a package-lock.json and returns its lockfileVersion and the
// version of each `packages` entry named in keys (links and entries without a version are left
// out); other entries are skipped without decoding.
func lockedVersions(r *bufio.Reader, keys map[string]bool) (int, map[string]string, error) {
	dec := jsontext.NewDecoder(r, lenient)
	if err := enterObject(dec); err != nil {
		return 0, nil, err
	}
	lockVersion := 0
	versions := map[string]string{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, nil, err
		}
		switch tok.String() {
		case "lockfileVersion":
			if err := json.UnmarshalDecode(dec, &lockVersion); err != nil {
				return 0, nil, fmt.Errorf("lockfileVersion: %w", err)
			}
		case "packages":
			if err := enterObject(dec); err != nil {
				return 0, nil, fmt.Errorf("packages: %w", err)
			}
			for dec.PeekKind() != '}' {
				tok, err := dec.ReadToken()
				if err != nil {
					return 0, nil, err
				}
				k := tok.String()
				if !keys[k] {
					if err := dec.SkipValue(); err != nil {
						return 0, nil, err
					}
					continue
				}
				var e struct {
					Version string `json:"version"`
					Link    bool   `json:"link"`
				}
				if err := json.UnmarshalDecode(dec, &e, lenient); err != nil {
					return 0, nil, fmt.Errorf("packages[%q]: %w", k, err)
				}
				if !e.Link && e.Version != "" {
					versions[k] = e.Version
				}
			}
			if _, err := dec.ReadToken(); err != nil {
				return 0, nil, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return 0, nil, err
			}
		}
	}
	return lockVersion, versions, nil
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

// lockCommands returns the npm install argument lists applying the lockfile-only updates
// (manifest path relative to the lock root -> updates): one for the root manifest, then one per
// workspace member (`--workspace=<dir>`) in path order, each naming `<name>@<version>` sorted.
func lockCommands(locks map[string][]dep.Update) [][]string {
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
	cmds := make([][]string, 0, len(manifests))
	for _, m := range manifests {
		var args []string
		if m != "package.json" {
			args = append(args, "--workspace="+path.Dir(m))
		}
		specs := make([]string, 0, len(locks[m]))
		for _, u := range locks[m] {
			specs = append(specs, u.Dep.Name+"@"+u.NewVersion)
		}
		slices.Sort(specs)
		cmds = append(cmds, append(args, slices.Compact(specs)...))
	}
	return cmds
}

// restoreRanges rewrites the ranges npm saved into the lockfile's own package entries
// (`packages[""]` for the root, `packages["<member dir>"]` for a member) when it installed
// `<name>@<version>`: each becomes the range package.json still holds (dep.Dep.Range under its
// DepType section), so the lockfile stays in sync with the unchanged manifest. An entry that
// is missing is an error: `npm ci` would reject the lockfile.
func restoreRanges(lock []byte, locks map[string][]dep.Update) ([]byte, error) {
	type target struct{ pkg, section, name string }
	want := map[target]string{}
	pkgs := map[string]bool{}
	for m, ups := range locks {
		key := ""
		if m != "package.json" {
			key = path.Dir(m)
		}
		pkgs[key] = true
		for _, u := range ups {
			want[target{key, u.Dep.DepType, u.Dep.Name}] = u.Dep.Range
		}
	}
	var edits []patch.Edit
	found := map[target]bool{}
	dec := jsontext.NewDecoder(bytes.NewReader(lock), lenient)
	// walk descends into the objects on the path to a wanted range and records one edit per
	// range; depth 0 is the document, 1 a packages entry, 2 a section.
	var walk func(depth int, t target) error
	walk = func(depth int, t target) error {
		if err := enterObject(dec); err != nil {
			return err
		}
		for dec.PeekKind() != '}' {
			k, err := dec.ReadToken()
			if err != nil {
				return err
			}
			next, descend := t, false
			switch depth {
			case 0:
				descend = k.String() == "packages"
			case 1:
				next.pkg, descend = k.String(), pkgs[k.String()]
			case 2:
				next.section, descend = k.String(), true
			case 3:
				next.name = k.String()
				rng, ok := want[next]
				if !ok || dec.PeekKind() != '"' {
					break
				}
				v, err := dec.ReadValue()
				if err != nil {
					return err
				}
				end := int(dec.InputOffset())
				q, err := jsontext.AppendQuote(nil, rng)
				if err != nil {
					return err
				}
				edits = append(edits, patch.Edit{Start: end - len(v), End: end, Old: string(v), New: string(q)})
				found[next] = true
				continue
			}
			if descend && dec.PeekKind() == '{' {
				if err := walk(depth+1, next); err != nil {
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
	if err := walk(0, target{}); err != nil {
		return nil, err
	}
	for t := range want {
		if !found[t] {
			return nil, fmt.Errorf("%s has no %s entry for %s in package %q to restore its range", PackageLock, t.section, t.name, t.pkg)
		}
	}
	return patch.Apply(lock, edits)
}
