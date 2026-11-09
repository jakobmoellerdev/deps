// Package paket extracts the NuGet packages of Paket's `paket.dependencies`, reads the versions
// `paket.lock` resolves them to, and updates the lock with the paket tool.
package paket

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "paket"

// DepType is the dep type of every package (Renovate's).
const DepType = "dependencies"

// Datasource and versioning names.
const (
	Datasource = "nuget"
	Versioning = "nuget"
)

const (
	manifestFile = "paket.dependencies"
	lockFile     = "paket.lock"
	mainGroup    = "Main"
	// unconstrained is the Range of a package without version constraint: Paket reads it as
	// `>= 0`, so only paket.lock moves.
	unconstrained = ">= 0"
	// nugetOrg is nuget.org's v3 service index, the nuget datasource's default feed.
	nugetOrg = "https://api.nuget.org/v3/index.json"
)

// Skip reasons.
const (
	skipConstraint = "unsupported version constraint"
	skipNoLock     = "no version constraint and no paket.lock version"
	skipNoSources  = "no http(s) source in the group"
)

type paketManager struct{}

// New returns the paket manager.
func New() manager.Manager { return &paketManager{} }

func (*paketManager) Name() string { return Name }

// Match reports whether the slash path is a paket.dependencies.
func (*paketManager) Match(p string) bool { return path.Base(p) == manifestFile }

// entry is a `nuget <id> [<constraint>] [<options>]` line of paket.dependencies.
type entry struct {
	group, name string
	line        int
	constraint  string // as written, "" when none
	start       int    // constraint offset in the file
}

// dependencies is a parsed paket.dependencies: its nuget entries and the `source` lines of each
// group (by lower-cased group name), in order.
type dependencies struct {
	entries []entry
	sources map[string][]string
}

// parseDependencies reads the `group`, `source` and `nuget` lines of a paket.dependencies;
// `#` and `//` comments are ignored, and so are every other line (options, `github`, `gist`,
// `http`, `git`, `clitool`).
func parseDependencies(src []byte) dependencies {
	deps := dependencies{sources: map[string][]string{}}
	group := mainGroup
	off := 0
	for n := 1; off < len(src); n++ {
		end := len(src)
		if i := bytes.IndexByte(src[off:], '\n'); i >= 0 {
			end = off + i
		}
		line := strings.TrimRight(string(src[off:end]), "\r")
		if i := strings.Index(line, "//"); i >= 0 && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			line = line[:i]
		}
		toks := fields(line)
		if len(toks) >= 2 && !strings.HasPrefix(line[toks[0].start:], "#") {
			tok := func(i int) string { return line[toks[i].start:toks[i].end] }
			switch tok(0) {
			case "group":
				group = tok(1)
			case "source":
				g := strings.ToLower(group)
				deps.sources[g] = append(deps.sources[g], tok(1))
			case "nuget":
				e := entry{group: group, name: tok(1), line: n}
				if last := constraintEnd(line, toks[2:]); last >= 0 {
					e.constraint = line[toks[2].start:toks[2+last].end]
					e.start = off + toks[2].start
				}
				deps.entries = append(deps.entries, e)
			}
		}
		off = end + 1
	}
	return deps
}

// constraintEnd returns the index in toks of the last token of the version constraint they
// start with (comparisons, then channel names before the first `option:`), or -1 when there
// is none.
func constraintEnd(line string, toks []span) int {
	last := -1
	comparisons := true
	for i, t := range toks {
		text := line[t.start:t.end]
		switch {
		case comparisons && isConstraintToken(text):
			last = i
		case last >= 0 && !strings.ContainsAny(text, ":=<>~"):
			comparisons = false
			last = i // a prerelease channel
		default:
			return last
		}
	}
	return last
}

// Extract returns one dep per `nuget` line: a pinned version (`1.2.3`, `= 1.2.3`, `== 1.2.3`)
// as Version with RefSpan on it, any other constraint as Range (RefSpan all of it, Version its
// floor), and a package without constraint as the fixed Range `>= 0`, which only paket.lock
// moves (ReadLocks). Source lists the group's http(s) `source` URLs.
func (*paketManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	parsed := parseDependencies(src)
	out := make([]dep.Dep, 0, len(parsed.entries))
	for _, e := range parsed.entries {
		d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepType, File: file, Line: e.line, Name: e.name}
		d.Source, d.Skip = groupSource(parsed.sources[strings.ToLower(e.group)])
		switch c, err := parseConstraint(e.constraint); {
		case e.constraint == "":
			d.Range, d.RangeFixed = unconstrained, true
		case err != nil:
			d.Version, d.Skip = e.constraint, skipConstraint
		case c.pinned():
			t := c.terms[0]
			d.Version = t.version
			d.RefSpan = dep.Span{Start: e.start + t.start, End: e.start + t.end}
		default:
			d.Range, d.Version = e.constraint, c.floor()
			d.RefSpan = dep.Span{Start: e.start, End: e.start + len(e.constraint)}
		}
		out = append(out, d)
	}
	return out, nil
}

// groupSource returns the nuget datasource Source of a group's `source` lines: their http(s)
// URLs, nuget.org's v2 feed read as its v3 service index; "" for nuget.org alone or no
// sources. Local folders and `%VAR%` sources are left out; a group with none left is skipped.
func groupSource(sources []string) (string, string) {
	var feeds []string
	for _, s := range sources {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Contains(s, "%") {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		if host == "nuget.org" && strings.TrimSuffix(strings.ToLower(u.Path), "/") == "/api/v2" {
			s = nugetOrg
		}
		feeds = append(feeds, s)
	}
	switch {
	case len(sources) > 0 && len(feeds) == 0:
		return "", skipNoSources
	case len(feeds) == 1 && feeds[0] == nugetOrg:
		return "", ""
	}
	return strings.Join(feeds, " "), ""
}

// lockPath returns the slash path of the paket.lock next to a paket.dependencies.
func lockPath(manifest string) string { return path.Join(path.Dir(manifest), lockFile) }

// parseLock returns the versions a paket.lock resolves its NuGet packages to, by lower-cased
// group name and lower-cased package id: the `    <id> (<version>)` lines of the NUGET
// sections (`GROUP <name>` starts a group; the first one is Main).
func parseLock(src []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	group, nuget := strings.ToLower(mainGroup), false
	for line := range strings.SplitSeq(string(src), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if line[0] != ' ' {
			if g, ok := strings.CutPrefix(line, "GROUP "); ok {
				group = strings.ToLower(strings.TrimSpace(g))
			}
			nuget = line == "NUGET"
			continue
		}
		if !nuget || !strings.HasPrefix(line, "    ") || line[4] == ' ' {
			continue
		}
		id, rest, ok := strings.Cut(line[4:], " (")
		v, _, ok2 := strings.Cut(rest, ")")
		if !ok || !ok2 {
			continue
		}
		if out[group] == nil {
			out[group] = map[string]string{}
		}
		out[group][strings.ToLower(id)] = v
	}
	return out
}

// ReadLocks implements manager.LockReader: each dep gets the version the paket.lock next to its
// paket.dependencies resolves it to in its group (dep.Dep.Locked; ids compared
// case-insensitively), and a range without floor that version as Version. Without a lock, a
// package without constraint is skipped.
func (*paketManager) ReadLocks(root string, files [][]dep.Dep) []error {
	var errs []error
	for _, deps := range files {
		if len(deps) == 0 {
			continue
		}
		file := deps[0].File
		var locked map[string]map[string]string
		var groups map[int]string
		lock, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(lockPath(file))))
		switch {
		case err == nil:
			locked = parseLock(lock)
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
			if err != nil {
				errs = append(errs, err)
				break
			}
			groups = map[int]string{}
			for _, e := range parseDependencies(src).entries {
				groups[e.line] = strings.ToLower(e.group)
			}
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, fmt.Errorf("%s: %w", lockPath(file), err))
		}
		for i := range deps {
			d := &deps[i]
			if d.Skip != "" {
				continue
			}
			if v := locked[groups[d.Line]][strings.ToLower(d.Name)]; nugetScheme.Valid(v) {
				d.Locked = v
			}
			if d.Version == "" {
				if d.Locked == "" {
					d.Skip = skipNoLock
					continue
				}
				d.Version = d.Locked
			}
		}
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the paket.lock next to each paket.dependencies.
func (*paketManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(f)))); err == nil && st.Mode().IsRegular() {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: lockPath(f)})
		}
	}
	return out
}

// Edits replaces a pinned version (RefSpan) with the new version, or rewrites a constraint for
// it (NewRange) under the update's rangeStrategy.
func (m *paketManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	out, ok := m.NewRange(d, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("constraint %s cannot be rewritten for %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}
