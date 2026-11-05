package piprequirements

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// source is what a requirements file contributes to the compile graph: the header command of
// a compiled file, or the requirements and includes of an input.
type source struct {
	compiled bool     // the file has a pip-compile or uv header
	base     string   // regenerable header: the command's directory, slash path relative to root
	inputs   []string // regenerable header: the command's sources, relative to root; nil = not regenerable
	includes []string // `-r` files, relative to root
	names    []string // normalized names of every requirement
	pins     map[string]string
}

// newSource builds the source of file (a slash path relative to root) from its requirements.
// exists reports whether a slash path relative to root exists.
func newSource(file string, src []byte, reqs []Hashed, exists func(string) bool) source {
	var s source
	for _, r := range reqs {
		s.names = append(s.names, pyreq.Normalize(r.Dep.Name))
	}
	for _, ref := range References(src) {
		if ref.Kind == RefRequirement {
			s.includes = append(s.includes, path.Join(path.Dir(file), ref.Path))
		}
	}
	h, ok := ParseHeader(src)
	if !ok {
		return s
	}
	s.compiled = true
	s.pins = map[string]string{}
	for _, r := range reqs {
		if v, ok := strings.CutPrefix(r.Dep.Range, "=="); ok && r.Dep.Skip == "" {
			s.pins[pyreq.Normalize(r.Dep.Name)] = v
		}
	}
	base, _, err := h.Base(file)
	if err != nil {
		return s
	}
	srcs, err := h.Sources(func(n string) bool { return exists(path.Join(base, n)) })
	if err != nil {
		return s
	}
	s.base = base
	s.inputs = make([]string, 0, len(srcs))
	for _, in := range srcs {
		p := path.Join(base, filepath.ToSlash(in))
		if strings.Contains(in, "://") || path.IsAbs(in) || !within(base, p) {
			return source{compiled: true, names: s.names, includes: s.includes, pins: s.pins}
		}
		s.inputs = append(s.inputs, p)
	}
	return s
}

// within reports whether the clean slash path p is dir or below it.
func within(dir, p string) bool {
	if dir == "." {
		return p != ".." && !strings.HasPrefix(p, "../")
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// closure returns the requirements files a compiled file (out, with source o) is compiled
// from: its inputs and the files they include with `-r`, transitively. ok is false when one
// of them is not a requirements file the manager reads (lookup fails: pyproject.toml, setup.py,
// a name the manager does not match), is itself compiled, lies outside the command's
// directory, or in a directory that is not out's own or below it (the compiled files of an
// input are found by looking upward from it).
func closure(out string, o source, lookup func(string) (source, bool)) (files []string, ok bool) {
	if o.inputs == nil {
		return nil, false
	}
	outDir := path.Dir(out)
	queue := slices.Clone(o.inputs)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if slices.Contains(files, f) {
			continue
		}
		s, ok := lookup(f)
		if !ok || s.compiled || f == out || !within(o.base, f) || !within(outDir, path.Dir(f)) {
			return nil, false
		}
		files = append(files, f)
		queue = append(queue, s.includes...)
	}
	return files, true
}

// graph is the compile graph of a set of requirements files: the compiled files whose inputs
// are all requirements files of the set, and the inputs they share.
type graph struct {
	members map[string][]string // handled compiled file -> its closure
	outputs map[string][]string // input -> the handled compiled files whose closure has it, sorted
}

// newGraph builds the graph of srcs (by slash path relative to root). A compiled file is
// handled when its closure is (see closure), all compiled files naming one of its inputs are
// handled too and they all run in the same directory; the others are left out, as their
// inputs' requirements would be both direct deps and pins of a file left as it is.
func newGraph(srcs map[string]source) graph {
	lookup := func(p string) (source, bool) {
		s, ok := srcs[p]
		return s, ok
	}
	members := map[string][]string{}
	named := map[string][]string{} // every compiled file -> the inputs it names, as far as known
	for f, s := range srcs {
		if !s.compiled {
			continue
		}
		if files, ok := closure(f, s, lookup); ok {
			members[f] = files
		}
		named[f] = closureOf(f, s, lookup)
	}
	namers := invert(named)
	for changed := true; changed; {
		changed = false
		for _, outs := range namers {
			ok := true
			for _, o := range outs {
				ok = ok && members[o] != nil && srcs[o].base == srcs[outs[0]].base
			}
			if ok {
				continue
			}
			for _, o := range outs {
				if members[o] != nil {
					delete(members, o)
					changed = true
				}
			}
		}
	}
	return graph{members: members, outputs: invert(members)}
}

// closureOf is the closure of a compiled file as far as it can be followed, whether or not it
// is handled (nil when its inputs are unknown).
func closureOf(out string, o source, lookup func(string) (source, bool)) []string {
	var files []string
	queue := slices.Clone(o.inputs)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if slices.Contains(files, f) || f == out {
			continue
		}
		files = append(files, f)
		if s, ok := lookup(f); ok {
			queue = append(queue, s.includes...)
		}
	}
	return files
}

// invert maps each input of members to the compiled files naming it, sorted.
func invert(members map[string][]string) map[string][]string {
	out := map[string][]string{}
	for o, files := range members {
		for _, f := range files {
			out[f] = append(out[f], o)
		}
	}
	for _, outs := range out {
		slices.Sort(outs)
	}
	return out
}

// link classifies the deps of one file (all of them have File == file) by the graph: a
// handled compiled file keeps only the pins no input of it names, as DepTypeIndirect; an input
// keeps its requirements with the lowest version its compiled files pin as Locked; any other
// `.in` file, which no handled compiled file names, yields none.
func (g graph) link(file string, deps []dep.Dep, srcs map[string]source) []dep.Dep {
	if files, ok := g.members[file]; ok {
		direct := map[string]bool{}
		for _, f := range files {
			for _, n := range srcs[f].names {
				direct[n] = true
			}
		}
		deps = slices.DeleteFunc(deps, func(d dep.Dep) bool { return direct[pyreq.Normalize(d.Name)] })
		for i := range deps {
			deps[i].DepType = DepTypeIndirect
		}
		return deps
	}
	if outs, ok := g.outputs[file]; ok {
		pep440, _ := versioning.Get(pyreq.Versioning)
		for i := range deps {
			d := &deps[i]
			if d.Skip != "" {
				continue
			}
			n := pyreq.Normalize(d.Name)
			for _, o := range outs {
				if v, ok := srcs[o].pins[n]; ok && pep440.Valid(v) && (d.Locked == "" || pep440.Compare(v, d.Locked) < 0) {
					d.Locked = v
				}
			}
		}
		return deps
	}
	if isInput(file) {
		return nil
	}
	return deps
}

// isInput reports whether the slash path is a pip-compile input file (`*.in`), which only
// counts as a requirements file when a compiled file names it.
func isInput(p string) bool { return strings.HasSuffix(p, ".in") }

// CompiledOutputs returns the compiled requirements files (slash paths relative to the
// absolute root, sorted) whose header command reads the requirements file input, directly or
// through `-r` includes, and the directory the commands run in. It applies the graph rules of
// Link to the files on disk: the compiled files are looked for in input's directory and its
// ancestors; ok is false when there is none or they run in different directories.
func CompiledOutputs(root, input string) (outputs []string, base string, ok bool) {
	srcs := map[string]source{}
	exists := func(p string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		return err == nil
	}
	lookup := func(p string) (source, bool) {
		if s, ok := srcs[p]; ok {
			return s, true
		}
		if !match(p) {
			return source{}, false
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return source{}, false
		}
		s := newSource(p, b, nil, exists)
		srcs[p] = s
		return s, true
	}
	if _, ok := lookup(input); !ok {
		return nil, "", false
	}
	for dir := path.Dir(input); ; dir = path.Dir(dir) {
		entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		for _, e := range entries {
			p := path.Join(dir, e.Name())
			if !e.Type().IsRegular() || isInput(p) || !match(p) {
				continue
			}
			s, ok := lookup(p)
			if !ok || !s.compiled {
				continue
			}
			if files, ok := closure(p, s, lookup); ok && slices.Contains(files, input) {
				if len(outputs) > 0 && s.base != base {
					return nil, "", false
				}
				outputs, base = append(outputs, p), s.base
			}
		}
		if dir == "." {
			break
		}
	}
	slices.Sort(outputs)
	return outputs, base, len(outputs) > 0
}
