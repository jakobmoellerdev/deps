package bazel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// readBazelrc returns the registries the `.bazelrc` of the workspace directory ws names, in
// order and without duplicates: the values of `--registry=<url>` and `--registry <url>` on
// every command line without a `:config` (`common`, `build`, …, not `startup`), following
// `import` and `try-import` of files inside root (the scanned root), with %workspace%
// expanded. A workspace without a .bazelrc has none, so the Bazel Central Registry is used.
func readBazelrc(ws, root string) ([]string, error) {
	var regs []string
	seen := map[string]bool{}
	err := readRC(filepath.Join(ws, ".bazelrc"), ws, root, seen, &regs)
	return regs, err
}

func readRC(file, ws, root string, seen map[string]bool, regs *[]string) error {
	file = filepath.Clean(file)
	if seen[file] || !inside(root, file) {
		return nil
	}
	seen[file] = true
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range rcLines(string(b)) {
		words := rcWords(line)
		if len(words) == 0 {
			continue
		}
		switch cmd := words[0]; {
		case cmd == "import" || cmd == "try-import":
			if len(words) == 2 {
				p := filepath.FromSlash(expandWorkspace(words[1], ws))
				if !filepath.IsAbs(p) {
					p = filepath.Join(filepath.Dir(file), p)
				}
				if err := readRC(p, ws, root, seen, regs); err != nil {
					return err
				}
			}
		case cmd == "startup" || strings.Contains(cmd, ":"):
		default:
			for i := 1; i < len(words); i++ {
				v, ok := strings.CutPrefix(words[i], "--registry=")
				if !ok && words[i] == "--registry" && i+1 < len(words) {
					i++
					v, ok = words[i], true
				}
				if ok && v != "" {
					v = expandWorkspace(v, ws)
					if !slices.Contains(*regs, v) {
						*regs = append(*regs, v)
					}
				}
			}
		}
	}
	return nil
}

// inside reports whether the absolute path p is root or below it.
func inside(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// rcLines splits a bazelrc into logical lines: a backslash at the end of a line continues it
// on the next one.
func rcLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\\\n", "")
	return strings.Split(s, "\n")
}

// rcWords splits a bazelrc line into words as Bazel does: separated by blanks, single and
// double quotes group (and are dropped), a backslash escapes the next byte, and a `#` outside
// quotes starts a comment.
func rcWords(line string) []string {
	var words []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			in = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '#':
			i = len(line)
		case c == ' ' || c == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}
