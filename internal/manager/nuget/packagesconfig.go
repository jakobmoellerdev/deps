package nuget

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// packagesConfig is the legacy NuGet manifest of a project (packages.config, next to it).
const packagesConfig = "packages.config"

// isPackagesConfig reports whether the slash path is a packages.config (any case).
func isPackagesConfig(p string) bool { return strings.EqualFold(path.Base(p), packagesConfig) }

// parsePackagesConfig returns a nuget item per `<package id="…" version="…">` child of the
// `<packages>` root; a file with another root yields nothing.
func parsePackagesConfig(src []byte) ([]item, error) {
	bom := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	dec := xml.NewDecoder(bytes.NewReader(src[bom:]))
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
			return r, nil
		}
		return nil, errors.New("unsupported encoding " + label)
	}
	ln := newLines(src)
	var items []item
	depth := 0
	for {
		start := bom + int(dec.InputOffset())
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		end := bom + int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && t.Name.Local != "packages" {
				return nil, nil
			}
			if depth == 1 && t.Name.Local == "package" {
				attrs := attrSpans(src, start, end, t.Attr, ln)
				id, okID := attrs["id"]
				if v, okV := attrs["version"]; okID && okV {
					items = append(items, item{depType: DepTypeNuGet, name: id, version: &v})
				}
			}
			depth++ // RawToken closes a self-closing element with an EndElement too
		case xml.EndElement:
			depth--
		}
	}
	return items, nil
}

// packagesFolder matches a path into the solution's packages folder (`..\packages\`), which
// the projects of a packages.config reference installed packages by (HintPath, Import).
var packagesFolder = regexp.MustCompile(`(?i)[\\/]packages[\\/]`)

// FileEdits implements manager.FileEditor: an update of a packages.config package also moves
// the `packages\<id>.<version>\` paths (HintPath, Import, Error conditions) of the projects
// next to it to the new version, as Update-Package does.
func (*nugetManager) FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error) {
	d := u.Dep
	if !isPackagesConfig(d.File) || u.NewVersion == d.Version || d.Version == "" {
		return nil, nil
	}
	dir := path.Dir(d.File)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(d.Name + "." + d.Version)
	var out map[string][]patch.Edit
	for _, e := range entries {
		if e.IsDir() || !isProject(e.Name()) {
			continue
		}
		rel := path.Join(dir, e.Name())
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		var edits []patch.Edit
		for _, m := range packagesFolder.FindAllIndex(src, -1) {
			s := m[1]
			e := s + len(needle)
			if e >= len(src) || strings.ToLower(string(src[s:e])) != needle || src[e] != '\\' && src[e] != '/' {
				continue
			}
			vs := s + len(d.Name) + 1
			edits = append(edits, patch.Edit{Start: vs, End: e, Old: string(src[vs:e]), New: u.NewVersion})
		}
		if edits != nil {
			if out == nil {
				out = map[string][]patch.Edit{}
			}
			out[rel] = edits
		}
	}
	return out, nil
}
