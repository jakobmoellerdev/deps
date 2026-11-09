package bundler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// NameCocoaPods is the name of the CocoaPods manager (Renovate's): Podfiles are Ruby, and their
// pod requirements are Gem::Requirements, so it shares the Gemfile lexer and requirement
// rewriting.
const NameCocoaPods = "cocoapods"

// Datasources of CocoaPods deps: pods of spec repositories, and git pods pinned to a tag.
const (
	PodDatasource = "pod"
	GitDatasource = "git-tags"
)

// DepTypePods is the dep type of every `pod` statement.
const DepTypePods = "pods"

// Podfile names: the manifest and its lockfile.
const (
	podfileName = "Podfile"
	podfileLock = "Podfile.lock"
)

// Skip reasons of the cocoapods manager.
const (
	skipPodspec     = "podspec dependency"
	skipExternal    = "external source"
	skipUnspecified = "unspecified version"
	skipGitURL      = "unrecognized git URL"
	skipPodNoLock   = "no Podfile.lock version for a requirement without a floor"
)

type cocoapodsManager struct{}

// NewCocoaPods returns the cocoapods manager.
func NewCocoaPods() manager.Manager { return cocoapodsManager{} }

func (cocoapodsManager) Name() string { return NameCocoaPods }

// Match reports whether the slash path is a Podfile.
func (cocoapodsManager) Match(p string) bool {
	return p == podfileName || strings.HasSuffix(p, "/"+podfileName)
}

// Extract returns one Dep per `pod` statement, in file order. A pod with requirements is a dep
// of the pod datasource, its requirements read and rewritten as a Gemfile gem's (Range,
// Constraint, Version, RefSpan: setRequirements); Source is its `:source`, else the Podfile's
// `source` statements in order. A git pod with a literal `:tag` is a git-tags dep of the
// repository with the tag as its Version.
func (cocoapodsManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	f := parsePodfile(src)
	global := make([]string, len(f.sources))
	for i, s := range f.sources {
		global[i] = strings.TrimRight(s, "/")
	}
	deps := make([]dep.Dep, 0, len(f.pods))
	for _, p := range f.pods {
		d := dep.Dep{Manager: NameCocoaPods, Datasource: PodDatasource, Versioning: Versioning, DepType: DepTypePods, File: file, Line: p.line, Name: p.name, Source: strings.Join(global, " ")}
		if p.ownSource {
			d.Source = strings.TrimRight(p.source, "/")
		}
		switch {
		case p.skip != "":
			d.Skip = p.skip
		case p.git != nil || p.gitRef:
			gitDep(&d, p)
		case p.nonLiteral:
			d.Skip = skipNonLiteral
		case len(p.reqs) == 0:
			d.Skip = skipUnspecified
		default:
			setRequirements(&d, src, p.reqs)
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// gitDep makes d the git-tags dep of the git pod p: its repository is Source, its literal tag
// Version (RefSpan the tag's content). A branch, commit or a pod without a tag is skipped.
func gitDep(d *dep.Dep, p podDecl) {
	d.Datasource, d.Versioning = GitDatasource, "tag"
	if p.gitRef || p.git == nil || p.tag == nil {
		d.Skip = skipGit
		return
	}
	_, d.Source = manager.GitRepo(p.git.text)
	if d.Source == "" {
		d.Skip = skipGitURL
		return
	}
	d.Version = p.tag.text
	d.RefSpan = dep.Span{Start: p.tag.end - 1 - len(p.tag.text), End: p.tag.end - 1}
	d.Line = p.tag.line
}

// Edits rewrites a pod's requirement strings for the new version under u.RangeStrategy, as
// for a gem (bundler's Edits), and replaces the tag of a git pod.
func (cocoapodsManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == GitDatasource {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	return bundlerManager{}.Edits(u)
}

// NewRange implements manager.RangeRewriter (reqList.rewrite).
func (cocoapodsManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return bundlerManager{}.NewRange(d, v, strategy)
}

// podLockPath returns the slash path of the Podfile.lock of the Podfile manifest.
func podLockPath(manifest string) string { return path.Join(path.Dir(manifest), podfileLock) }

// lockedPods returns the version the PODS section of a Podfile.lock locks each pod to (a
// subspec's entry `Firebase/Analytics (10.18.0)` counts for its pod). A file without a PODS
// section is not a Podfile.lock.
func lockedPods(b []byte) (map[string]string, error) {
	out := map[string]string{}
	pods := false
	for line := range strings.Lines(string(b)) {
		l := strings.TrimRight(line, "\r\n")
		if l == "" {
			continue
		}
		if l[0] != ' ' {
			if pods && l != "PODS:" {
				return out, nil
			}
			pods = l == "PODS:"
			continue
		}
		entry, ok := strings.CutPrefix(l, "  - ")
		if !pods || !ok {
			continue
		}
		entry = strings.Trim(strings.TrimSuffix(entry, ":"), `"'`)
		name, v, ok := strings.Cut(entry, " (")
		if !ok || !strings.HasSuffix(v, ")") {
			continue
		}
		out[podName(name)] = strings.TrimSuffix(v, ")")
	}
	if !pods {
		return nil, errors.New("not a Podfile.lock (no PODS section)")
	}
	return out, nil
}

// podName returns the pod of a dependency name: the part before the first "/" of a subspec.
func podName(name string) string {
	pod, _, _ := strings.Cut(name, "/")
	return pod
}

// ReadLocks records the version the Podfile.lock next to each Podfile locks its pods to
// (dep.Dep.Locked; a subspec has its pod's). A requirement without a floor (`< 2`) takes the
// locked version as its Version, or is skipped without one. An unreadable lockfile is
// reported once and its pods are bumped (LockUnread).
func (cocoapodsManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pods map[string]string
		err  error
	}
	locks := map[string]*lockState{}
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Skip != "" || d.Datasource != PodDatasource {
				continue
			}
			file := podLockPath(d.File)
			st := locks[file]
			if st == nil {
				st = &lockState{}
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err == nil {
					st.pods, err = lockedPods(b)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					st.err = fmt.Errorf("%s: %w", file, err)
					errs = append(errs, st.err)
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
			} else {
				d.Locked = st.pods[podName(d.Name)]
			}
			if d.Version == "" {
				if d.Locked == "" {
					d.Skip = skipPodNoLock
					continue
				}
				d.Version = d.Locked
			}
		}
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the Podfile.lock of each Podfile with one.
func (cocoapodsManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (PodLocker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: podLockPath(f)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return cmp.Compare(a.Lockfile, b.Lockfile) })
	return out
}
