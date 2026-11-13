package gomod

import (
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// importIndex answers "which .go files of this module import this module path?" for the
// whole module after one pass over its files, so that a group rewriting several module
// paths does not read every file once per path.
//
// The import paths are deduplicated and sorted, and a path's files are a slice of one
// shared []int32, so the index costs the distinct import path strings plus 4 bytes per
// (path, file) pair - not a copy of the sources.
type importIndex struct {
	files []string // absolute paths of the module's .go files, in walk order
	paths []string // the distinct import paths of those files, sorted
	at    []int32  // paths[i]'s files are ids[at[i]:at[i+1]], ascending
	ids   []int32  // indices into files
	// unparsed are the files the parser rejected; they are candidates for every path, so
	// that a broken file mentioning the module still reports its parse error as before.
	unparsed []int32
}

// buildIndex indexes the import paths of the module's .go files.
func buildIndex(files []string) (*importIndex, error) {
	ix := &importIndex{files: files}
	type pair struct{ path, file int32 }
	var pairs []pair
	ids := make(map[string]int32, len(files))
	var buf []byte // reused: only the import paths are kept
	fset := token.NewFileSet()
	for i, p := range files {
		src, err := readFile(p, buf)
		buf = src
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			ix.unparsed = append(ix.unparsed, int32(i))
			continue
		}
		for _, is := range f.Imports {
			imp, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				continue
			}
			id, ok := ids[imp]
			if !ok {
				// Unquote returns a substring of src, which the next file overwrites.
				imp = strings.Clone(imp)
				id = int32(len(ix.paths))
				ids[imp] = id
				ix.paths = append(ix.paths, imp)
			}
			pairs = append(pairs, pair{id, int32(i)})
		}
	}
	// Sort the paths for the prefix search of candidates, and group the pairs by path in
	// that order; pairs come in file order, so every group stays ascending.
	rank := make([]int32, len(ix.paths))
	for r, id := range order(ix.paths) {
		rank[id] = int32(r)
	}
	sorted := make([]string, len(ix.paths))
	for id, r := range rank {
		sorted[r] = ix.paths[id]
	}
	ix.paths = sorted
	ix.at = make([]int32, len(ix.paths)+1)
	for _, p := range pairs {
		ix.at[rank[p.path]+1]++
	}
	for i := 1; i < len(ix.at); i++ {
		ix.at[i] += ix.at[i-1]
	}
	ix.ids = make([]int32, len(pairs))
	fill := slices.Clone(ix.at[:len(ix.paths)])
	for _, p := range pairs {
		r := rank[p.path]
		ix.ids[fill[r]] = p.file
		fill[r]++
	}
	return ix, nil
}

// order returns the indices of paths sorted by path.
func order(paths []string) []int32 {
	idx := make([]int32, len(paths))
	for i := range idx {
		idx[i] = int32(i)
	}
	slices.SortFunc(idx, func(a, b int32) int { return strings.Compare(paths[a], paths[b]) })
	return idx
}

// importers returns the absolute paths of the .go files that import the module path mod or
// one of its packages, in walk order, plus the files that could not be parsed.
func (ix *importIndex) importers(mod string) []string {
	var cand []int32
	if i, ok := slices.BinarySearch(ix.paths, mod); ok {
		cand = append(cand, ix.ids[ix.at[i]:ix.at[i+1]]...)
	}
	pkgs := mod + "/"
	i, _ := slices.BinarySearch(ix.paths, pkgs)
	for ; i < len(ix.paths) && strings.HasPrefix(ix.paths[i], pkgs); i++ {
		cand = append(cand, ix.ids[ix.at[i]:ix.at[i+1]]...)
	}
	cand = append(cand, ix.unparsed...)
	slices.Sort(cand)
	cand = slices.Compact(cand)
	out := make([]string, len(cand))
	for k, id := range cand {
		out[k] = ix.files[id]
	}
	return out
}
