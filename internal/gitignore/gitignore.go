// Package gitignore applies git's ignore rules to the paths of a directory walk, without
// running git. A Matcher holds the patterns of the .gitignore files that apply to the walked
// tree: those of the directories between the repository top and the walked root, the root's
// own, the ones Enter loads while the walk descends, and the repository's .git/info/exclude.
// Patterns in a deeper file override those of a shallower one, and within a file the last
// matching pattern wins, as in gitignore(5). The index is not read, so a tracked file an
// ignore rule matches (git add -f) counts as ignored, and the user-global core.excludesFile
// is not loaded: only files in the repository decide.
package gitignore

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Matcher matches slash paths relative to the walked root against the ignore files in scope.
// The zero value and a nil *Matcher ignore nothing. It is not safe for concurrent use: a walk
// drives it with Enter, in the order filepath.WalkDir visits directories.
type Matcher struct {
	root   string  // filesystem path of the walked root
	levels []level // outermost first; the last matching pattern of the innermost matching level decides
	fixed  int     // levels loaded by New: never popped
}

// level is one ignore file: its patterns and where they are anchored.
type level struct {
	dir  string // directory holding the file, as a slash path relative to the walked root; "" is the root
	up   string // for files above the root: the root's path relative to that directory, e.g. "mod"
	pats []pattern
}

// pattern is one line of an ignore file. Most lines are a plain name ("dist"), a suffix
// ("*.log") or a literal path ("build/out"), which are matched without the glob engine: on a
// large repository doublestar dominated matching (105ms of 107ms for backstage's 14,868
// paths), and these three kinds cover nearly every line.
type pattern struct {
	kind    patternKind
	text    string // kindName: the name; kindSuffix: the part after "*"; kindPath: the path; kindGlob: unused
	glob    string // doublestar glob matched against the path relative to the file's directory
	negate  bool   // the line started with "!"
	dirOnly bool   // the line ended with "/": matches directories only
}

type patternKind uint8

const (
	kindGlob   patternKind = iota // any pattern: doublestar
	kindName                      // a literal name at any depth: the path's last element
	kindSuffix                    // "*<text>" at any depth, text without metacharacters
	kindPath                      // a literal path below the ignore file's directory
)

// match reports whether rel (relative to the ignore file's directory) matches p.
func (p *pattern) match(rel string) bool {
	switch p.kind {
	case kindName:
		return base(rel) == p.text
	case kindSuffix: // "*<text>": git's * matches an empty name part too
		return strings.HasSuffix(base(rel), p.text)
	case kindPath:
		return rel == p.text
	default:
		return doublestar.MatchUnvalidated(p.glob, rel)
	}
}

// base is the last element of a slash path.
func base(rel string) string {
	if _, after, ok := strings.CutLast(rel, "/"); ok {
		return after
	}
	return rel
}

// meta reports whether s holds a glob metacharacter, so it cannot be compared literally.
// Braces are not among them: gitignore(5) has no brace expansion, so "*.{js,ts}" is a name
// ending in the literal text "{js,ts}".
func meta(s string) bool { return strings.ContainsAny(s, `*?[]\`) }

// escapeBraces makes the braces of a doublestar glob literal, leaving the ones already
// escaped alone. Without this doublestar would read them as the alternation git has not.
func escapeBraces(glob string) string {
	if !strings.ContainsAny(glob, "{}") {
		return glob
	}
	var b strings.Builder
	b.Grow(len(glob) + 4)
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(glob) {
				i++
				b.WriteByte(glob[i])
			}
		case '{', '}':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// New loads the ignore files that apply to the tree below root: the repository's
// .git/info/exclude, the .gitignore files of the directories between the repository top (the
// nearest directory at or above root holding .git) and root, and root's own .gitignore. Files
// deeper in the tree are loaded by Enter. Outside a repository only root's own .gitignore
// applies. Unreadable files are skipped.
func New(root string) *Matcher {
	m := &Matcher{root: root}
	if abs, err := filepath.Abs(root); err == nil {
		if top, ok := repoTop(abs); ok {
			m.add(level{up: upPath(top, abs), pats: parse(readFile(excludeFile(top)))})
			for _, dir := range between(top, abs) {
				m.add(level{up: upPath(dir, abs), pats: parse(readFile(filepath.Join(dir, ".gitignore")))})
			}
		}
	}
	m.add(level{pats: parse(readFile(filepath.Join(root, ".gitignore")))})
	m.fixed = len(m.levels)
	return m
}

// Enter loads the .gitignore of the directory dir (a slash path relative to the walked root)
// and drops the levels of directories the walk has left. The walk calls it for every directory
// it descends into, the root ("") excluded: New loaded that one.
func (m *Matcher) Enter(dir string) {
	if m == nil || dir == "" {
		return
	}
	for len(m.levels) > m.fixed {
		if last := m.levels[len(m.levels)-1]; last.dir == "" || strings.HasPrefix(dir, last.dir+"/") {
			break
		}
		m.levels = m.levels[:len(m.levels)-1]
	}
	m.add(level{dir: dir, pats: parse(readFile(filepath.Join(m.root, filepath.FromSlash(dir), ".gitignore")))})
}

// Ignored reports whether path (a slash path relative to the walked root, isDir for a
// directory) is ignored. The caller must not descend into an ignored directory: as in git,
// nothing below it can be brought back by a negation.
func (m *Matcher) Ignored(path string, isDir bool) bool {
	if m == nil {
		return false
	}
	for _, lv := range slices.Backward(m.levels) {
		rel := path
		switch {
		case lv.up != "":
			rel = lv.up + "/" + path
		case lv.dir != "":
			if !strings.HasPrefix(path, lv.dir+"/") {
				continue
			}
			rel = path[len(lv.dir)+1:]
		}
		for i := len(lv.pats) - 1; i >= 0; i-- {
			p := &lv.pats[i]
			if p.dirOnly && !isDir {
				continue
			}
			if p.match(rel) {
				return !p.negate
			}
		}
	}
	return false
}

// add keeps levels without patterns out, so matching walks as few levels as possible.
func (m *Matcher) add(lv level) {
	if len(lv.pats) > 0 {
		m.levels = append(m.levels, lv)
	}
}

// repoTop returns the nearest directory at or above abs holding .git (a directory in a
// checkout, a file in a linked worktree or a submodule).
func repoTop(abs string) (string, bool) {
	for dir := abs; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// between returns the directories from top down to abs, abs excluded.
func between(top, abs string) []string {
	var dirs []string
	for dir := filepath.Dir(abs); len(dir) >= len(top); dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == top || filepath.Dir(dir) == dir {
			break
		}
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// upPath returns abs relative to dir as a slash path ("" when they are the same).
func upPath(dir, abs string) string {
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// excludeFile returns the info/exclude of the repository at top: in .git, or, when .git is a
// file (a linked worktree or a submodule), in the common directory it points at.
func excludeFile(top string) string {
	gitDir := filepath.Join(top, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		ptr, ok := strings.CutPrefix(string(bytes.TrimSpace(readFile(gitDir))), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(ptr)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(top, gitDir)
		}
		// A worktree's git directory points at the common directory holding info/exclude.
		if common := strings.TrimSpace(string(readFile(filepath.Join(gitDir, "commondir")))); common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			gitDir = filepath.Clean(common)
		}
	}
	return filepath.Join(gitDir, "info", "exclude")
}

// readFile reads path, returning nil for an empty name and for every error: an ignore file
// that cannot be read holds no patterns.
func readFile(path string) []byte {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// parse compiles the lines of an ignore file, in order. Blank lines, comments ("#", escaped
// as "\#") and patterns doublestar rejects are dropped. A leading UTF-8 BOM belongs to the
// file, not to the first pattern, so it is dropped as git's skip_utf8_bom does.
func parse(data []byte) []pattern {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(data) == 0 {
		return nil
	}
	var pats []pattern
	for line := range strings.Lines(string(data)) {
		if p, ok := parseLine(strings.TrimRight(line, "\n\r")); ok {
			pats = append(pats, p)
		}
	}
	return pats
}

// parseLine compiles one line as gitignore(5) reads it: "!" negates, trailing spaces are
// dropped unless escaped, a trailing "/" matches directories only, a "/" anywhere but at the
// end anchors the pattern to the file's directory, and a pattern without one matches a name at
// any depth. Backslash escapes stay as they are: doublestar reads them like git.
func parseLine(line string) (pattern, bool) {
	if line == "" || line[0] == '#' {
		return pattern{}, false
	}
	var p pattern
	if line[0] == '!' {
		p.negate, line = true, line[1:]
	}
	line = trimUnescapedSpace(line)
	if rest, ok := strings.CutSuffix(line, "/"); ok {
		p.dirOnly, line = true, rest
	}
	if line == "" {
		return pattern{}, false
	}
	if strings.Contains(line, "/") {
		p.glob = strings.TrimPrefix(line, "/")
		if !meta(p.glob) {
			p.kind, p.text = kindPath, p.glob
		}
	} else {
		p.glob = "**/" + line
		switch {
		case !meta(line):
			p.kind, p.text = kindName, line
		case !meta(line[1:]) && line[0] == '*' && len(line) > 1:
			p.kind, p.text = kindSuffix, line[1:]
		}
	}
	p.glob = escapeBraces(p.glob)
	// git's trailing "/**" matches everything inside the prefix but not the prefix itself,
	// while doublestar's "**" also matches zero path elements. Requiring one more element
	// keeps a walk from pruning the directory, which would make a later negation
	// ("build/**" then "!build/go.mod") unreachable.
	if strings.HasSuffix(p.glob, "/**") {
		p.glob += "/*"
	}
	if p.kind == kindGlob && !doublestar.ValidatePattern(p.glob) {
		return pattern{}, false
	}
	return p, true
}

// trimUnescapedSpace drops the trailing spaces of a line; a space preceded by an odd number of
// backslashes is escaped and kept, with the escape.
func trimUnescapedSpace(line string) string {
	end := len(line)
	for end > 0 && line[end-1] == ' ' {
		slashes := 0
		for end-1-slashes > 0 && line[end-2-slashes] == '\\' {
			slashes++
		}
		if slashes%2 == 1 {
			break
		}
		end--
	}
	return line[:end]
}
