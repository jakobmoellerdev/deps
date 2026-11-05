package swift

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Manifest and lockfile names.
const (
	manifestFile = "Package.swift"
	resolvedFile = "Package.resolved"
)

// pin is a source-control pin of a Package.resolved.
type pin struct {
	key          string // repoKey of its location
	version      string // "" for a branch or revision pin
	versionSpan  dep.Span
	revision     string
	revisionSpan dep.Span
}

// resolved is a parsed Package.resolved (format version 1, 2 or 3).
type resolved struct {
	pins       []pin
	originHash string // v3; "" when absent
	originSpan dep.Span
}

// find returns the index of the pin of the repository with key k, or -1.
func (r *resolved) find(k string) int {
	for i, p := range r.pins {
		if p.key == k {
			return i
		}
	}
	return -1
}

// parseResolved reads the remote source-control pins of a Package.resolved: `pins` (v2, v3)
// or `object.pins` (v1) entries with a `location` (`repositoryURL` in v1) and a `state`
// `revision`; `kind` other than `remoteSourceControl` (local and registry packages) is left
// out. Spans are only recorded for values written without escapes.
func parseResolved(src []byte) (resolved, error) {
	p := resolvedParser{src: src, dec: jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))}
	if err := p.document(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return resolved{}, err
	}
	return p.out, nil
}

type resolvedParser struct {
	src []byte
	dec *jsontext.Decoder
	out resolved
}

func (p *resolvedParser) document() error {
	return p.object(func(key string) error {
		switch key {
		case "pins":
			return p.pins()
		case "object": // v1
			return p.object(func(key string) error {
				if key == "pins" {
					return p.pins()
				}
				return p.dec.SkipValue()
			})
		case "originHash":
			s, sp, ok, err := p.str()
			if ok {
				p.out.originHash, p.out.originSpan = s, sp
			}
			return err
		}
		return p.dec.SkipValue()
	})
}

// object reads an object, calling member for each key with the decoder before its value;
// member must consume the value.
func (p *resolvedParser) object(member func(key string) error) error {
	if p.dec.PeekKind() != '{' {
		return fmt.Errorf("offset %d: expected an object", p.dec.InputOffset())
	}
	if _, err := p.dec.ReadToken(); err != nil {
		return err
	}
	for p.dec.PeekKind() != '}' {
		tok, err := p.dec.ReadToken()
		if err != nil {
			return err
		}
		if err := member(tok.String()); err != nil {
			return err
		}
	}
	_, err := p.dec.ReadToken()
	return err
}

func (p *resolvedParser) pins() error {
	if p.dec.PeekKind() != '[' {
		return fmt.Errorf("offset %d: pins is not an array", p.dec.InputOffset())
	}
	if _, err := p.dec.ReadToken(); err != nil {
		return err
	}
	for p.dec.PeekKind() != ']' {
		var pn pin
		var location, kind string
		err := p.object(func(key string) error {
			switch key {
			case "location", "repositoryURL":
				s, _, _, err := p.str()
				location = s
				return err
			case "kind":
				s, _, _, err := p.str()
				kind = s
				return err
			case "state":
				return p.object(func(key string) error {
					switch key {
					case "version":
						s, sp, ok, err := p.str()
						if ok {
							pn.version, pn.versionSpan = s, sp
						}
						return err
					case "revision":
						s, sp, ok, err := p.str()
						if ok {
							pn.revision, pn.revisionSpan = s, sp
						}
						return err
					}
					return p.dec.SkipValue()
				})
			}
			return p.dec.SkipValue()
		})
		if err != nil {
			return err
		}
		if (kind == "" || kind == "remoteSourceControl") && location != "" && pn.revision != "" {
			pn.key = repoKey(location)
			p.out.pins = append(p.out.pins, pn)
		}
	}
	_, err := p.dec.ReadToken()
	return err
}

// str reads the next value; ok is false (and the value consumed) when it is not a string
// (null) or its raw bytes differ from the decoded string (escapes).
func (p *resolvedParser) str() (s string, span dep.Span, ok bool, err error) {
	if p.dec.PeekKind() != '"' {
		return "", dep.Span{}, false, p.dec.SkipValue()
	}
	tok, err := p.dec.ReadToken()
	if err != nil {
		return "", dep.Span{}, false, err
	}
	s = tok.String()
	end := int(p.dec.InputOffset())
	start := end - len(s) - 2
	if start < 0 || p.src[start] != '"' || p.src[end-1] != '"' || string(p.src[start+1:end-1]) != s {
		return s, dep.Span{}, false, nil
	}
	return s, dep.Span{Start: start + 1, End: end - 1}, true, nil
}

// repoKey normalizes a repository URL for matching manifests to pins: the lowercase host and
// path without scheme, user, `.git` suffix and trailing slash, so `git@github.com:o/r.git`,
// `https://github.com/O/R` and `https://github.com/o/r.git/` are one repository (SwiftPM
// compares package URLs case-insensitively too).
func repoKey(u string) string {
	s := strings.ToLower(strings.TrimSpace(u))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if c := strings.IndexByte(s, ':'); c >= 0 && !strings.Contains(s[:c], "/") { // scp-style
		s = s[:c] + "/" + s[c+1:]
	}
	if at := strings.IndexByte(s, '@'); at >= 0 && at < strings.IndexByte(s+"/", '/') {
		s = s[at+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	return strings.TrimRight(s, "/")
}

// root is a SwiftPM root package: a directory with Package.swift and Package.resolved, and
// the local packages it reaches through `.package(path:)` dependencies, whose remote
// dependencies its Package.resolved pins too.
type root struct {
	dir       string          // slash path relative to the scanned root, "." for the root itself
	members   map[string]bool // package directories of the local graph, dir included
	manifests []string        // the Package.swift files read for the graph, dir's first
}

// resolvedPath is the slash path of the root's Package.resolved.
func (r root) resolvedPath() string { return path.Join(r.dir, resolvedFile) }

// newRoot builds the root of the package in dir (slash, relative to the absolute top): it
// reads Package.swift files from disk, following `path:` dependencies that stay below top.
func newRoot(top, dir string) root {
	r := root{dir: dir, members: map[string]bool{dir: true}}
	queue := []string{dir}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		file := path.Join(d, manifestFile)
		src, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(file)))
		if err != nil {
			continue
		}
		r.manifests = append(r.manifests, file)
		for _, p := range localPaths(src) {
			if path.IsAbs(p) {
				continue
			}
			m := path.Join(d, p)
			if m == ".." || strings.HasPrefix(m, "../") || r.members[m] {
				continue
			}
			r.members[m] = true
			queue = append(queue, m)
		}
	}
	return r
}

// localPaths returns the `path:` values of the `.package(…)` calls in a Package.swift.
func localPaths(src []byte) []string {
	var out []string
	for _, c := range packageCalls(tokenize(src)) {
		for _, a := range c.args {
			if a.label == "path" && len(a.toks) == 1 && a.toks[0].kind == tokString && a.toks[0].plain {
				out = append(out, a.toks[0].text)
			}
		}
	}
	return out
}

// hasResolved reports whether the package directory dir (slash, relative to top) holds a
// Package.resolved.
func hasResolved(top, dir string) bool {
	st, err := os.Stat(filepath.Join(top, filepath.FromSlash(dir), resolvedFile))
	return err == nil && st.Mode().IsRegular()
}
