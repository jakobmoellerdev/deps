package gotool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/mod/modfile"
)

// ImportCache memoizes the scans of module directories (scanModule) by absolute path, so that
// a module scanned for its own scope is not scanned again as the local replace target of
// another (grafana's root go.mod replaces 36 modules of the repository). The files must not
// change while it is in use; the zero value is not usable, use NewImportCache.
type ImportCache struct {
	mu sync.Mutex
	m  map[string]*importEntry
}

type importEntry struct {
	once  sync.Once
	lines []string
	err   error
}

// NewImportCache returns an empty ImportCache.
func NewImportCache() *ImportCache { return &ImportCache{m: map[string]*importEntry{}} }

// scan is scanModule of dir, memoized when c is not nil.
func (c *ImportCache) scan(dir string) ([]string, error) {
	if c == nil {
		return scanModule(dir)
	}
	c.mu.Lock()
	e := c.m[dir]
	if e == nil {
		e = &importEntry{}
		c.m[dir] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.lines, e.err = scanModule(dir) })
	return e.lines, e.err
}

// importDigest returns the hex sha256 over the scan lines of a module (scanModule) without
// the imports of standard library packages: an import path whose first element has no dot
// is one, as for the go command, unless that element starts a module path of the go.mod
// whose tidy reads the module (local: its module, requirements and replacements), which can
// then provide it. Standard packages come with the go command, never from go.mod, so adding
// `import "fmt"` leaves tidy's result and the digest unchanged.
func importDigest(lines []string, local map[string]bool) string {
	h := sha256.New()
	for _, l := range lines {
		if kind, q, ok := strings.Cut(l, " "); ok && (kind == "import" || kind == "test") {
			elem, _, _ := strings.Cut(q, "/")
			if !strings.Contains(elem, ".") && !local[elem] {
				continue
			}
		}
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// scanModule returns what `go mod tidy` reads of the .go files of the module in dir (an
// absolute directory), as sorted unique lines: "import <path>" for the imports of its
// non-test files, "test <path>" for those of its test files, "package <dir>" and "tests
// <dir>" for the directories holding non-test and test files (its packages), "module <dir>"
// for the nested module directories it stops at and "invalid <file>" for a file whose
// imports do not parse. An import of one of the module's own packages with non-test files is
// left out: the package line already says the module provides it. Files and directories are
// chosen as the go command does for the "all" pattern with every build tag: directories
// named testdata or starting with . or _ are left out, vendor directories contribute only
// their own files, and a .go file counts unless its name starts with . or _ or its build
// constraint needs the ignore tag (or does not parse). Only the file header up to the
// imports is parsed: anything else in a .go file, and every other file, leaves the result
// unchanged, as it does tidy's.
func scanModule(dir string) ([]string, error) {
	var lines []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
				return filepath.SkipDir
			}
			if parent := filepath.Dir(p); parent != dir && filepath.Base(parent) == "vendor" {
				return filepath.SkipDir // the go command scans vendor itself but not below it
			}
			if st, err := os.Stat(filepath.Join(p, "go.mod")); err == nil && !st.IsDir() {
				lines = append(lines, "module "+rel)
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !strings.HasSuffix(name, ".go") {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 && !regularFile(p) || d.Type()&fs.ModeSymlink == 0 && !d.Type().IsRegular() {
			return nil // a broken symlink, one to a directory or another non-regular file
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !shouldBuild(src) {
			return nil
		}
		kind, pkg := "import ", "package "
		if strings.HasSuffix(name, "_test.go") {
			kind, pkg = "test ", "tests "
		}
		f, perr := parser.ParseFile(fset, "", src, parser.ImportsOnly|parser.SkipObjectResolution)
		if perr != nil {
			lines = append(lines, "invalid "+rel) // the go command reports it
		}
		if f != nil {
			for _, s := range f.Imports {
				if q, err := strconv.Unquote(s.Path.Value); err == nil {
					lines = append(lines, kind+q)
				}
			}
		}
		lines = append(lines, pkg+path.Dir(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	b, _ := os.ReadFile(filepath.Join(dir, "go.mod")) // a missing go.mod is listed as an input
	modPath := modfile.ModulePath(b)
	if modPath == "" {
		return lines, nil
	}
	pkgs := map[string]bool{}
	for _, l := range lines {
		if d, ok := strings.CutPrefix(l, "package "); ok {
			pkgs[d] = true
		}
	}
	return slices.DeleteFunc(lines, func(l string) bool {
		kind, q, _ := strings.Cut(l, " ")
		if kind != "import" && kind != "test" {
			return false
		}
		rel, ok := strings.CutPrefix(q, modPath)
		switch {
		case !ok:
			return false
		case rel == "":
			rel = "."
		case rel[0] == '/':
			rel = rel[1:]
		default:
			return false // another module path with the same prefix
		}
		return pkgs[rel]
	}), nil
}

// shouldBuild reports whether the go command loads the file src for every build tag
// (cmd/go/internal/imports.ShouldBuild with AnyTags): its //go:build line, else its
// // +build lines, must hold with every tag but ignore. A header with two //go:build lines
// or an unparsable one excludes the file, as it does for the go command.
func shouldBuild(src []byte) bool {
	header, goBuild, ok := fileHeader(src)
	if !ok {
		return false
	}
	if goBuild != nil {
		x, err := constraint.Parse(string(goBuild))
		return err == nil && evalAny(x, true)
	}
	for line := range bytes.Lines(header) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("//")) || !bytes.Contains(line, []byte("+build")) {
			continue
		}
		text := string(line)
		if !constraint.IsPlusBuild(text) {
			continue
		}
		if x, err := constraint.Parse(text); err == nil && !evalAny(x, true) {
			return false
		}
	}
	return true
}

// fileHeader returns the leading blank and // comment lines of src up to the last blank line
// before other content (where // +build lines count) and the //go:build line, as
// cmd/go/internal/imports.parseFileHeader does; ok is false for a second //go:build line.
func fileHeader(src []byte) (header, goBuild []byte, ok bool) {
	end := 0
	p := src
	ended, inSlashStar := false, false
Lines:
	for len(p) > 0 {
		line := p
		if i := bytes.IndexByte(line, '\n'); i >= 0 {
			line, p = line[:i], p[i+1:]
		} else {
			p = p[len(p):]
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 && !ended {
			end = len(src) - len(p)
			continue
		}
		if !bytes.HasPrefix(line, []byte("//")) {
			ended = true
		}
		if !inSlashStar && isGoBuild(line) {
			if goBuild != nil {
				return nil, nil, false
			}
			goBuild = line
		}
		for len(line) > 0 {
			if inSlashStar {
				i := bytes.Index(line, []byte("*/"))
				if i < 0 {
					break
				}
				inSlashStar = false
				line = bytes.TrimSpace(line[i+2:])
				continue
			}
			if bytes.HasPrefix(line, []byte("//")) {
				break
			}
			if bytes.HasPrefix(line, []byte("/*")) {
				inSlashStar = true
				line = bytes.TrimSpace(line[2:])
				continue
			}
			break Lines
		}
	}
	return src[:end], goBuild, true
}

// regularFile reports whether p is, or links to, a regular file.
func regularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// isGoBuild reports whether line is a //go:build comment.
func isGoBuild(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte("//go:build"))
	return ok && (len(rest) == 0 || len(bytes.TrimSpace(rest)) < len(rest))
}

// evalAny evaluates x with every tag but ignore both set and unset (the go command's
// AnyTags): a tag is prefer, ignore and malformed tags are false.
func evalAny(x constraint.Expr, prefer bool) bool {
	switch x := x.(type) {
	case *constraint.TagExpr:
		for _, c := range x.Tag {
			if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '.' {
				return false
			}
		}
		return x.Tag != "" && x.Tag != "ignore" && prefer
	case *constraint.NotExpr:
		return !evalAny(x.X, !prefer)
	case *constraint.AndExpr:
		return evalAny(x.X, prefer) && evalAny(x.Y, prefer)
	case *constraint.OrExpr:
		return evalAny(x.X, prefer) || evalAny(x.Y, prefer)
	}
	return false
}
