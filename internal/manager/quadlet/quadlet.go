// Package quadlet extracts the container images of Podman Quadlet units (`*.container`,
// `*.image`, `*.volume`).
package quadlet

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "quadlet"

// TypeImage is the dep type of every quadlet image (Renovate's).
const TypeImage = "image"

// Skip reasons.
const (
	skipUnit      = "quadlet unit"
	skipTransport = "local transport"
	skipLocal     = "local image"
)

// suffixes are the unit file types Renovate's managerFilePatterns match.
var suffixes = []string{".container", ".image", ".volume"}

// sections maps the unit sections whose Image= key names an image to the suffixes that make
// the value a reference to another quadlet unit (`Image=build.build`, `Image=app.image`)
// instead of an image.
var sections = map[string][]string{
	"Container": {".image", ".build"},
	"Image":     nil,
	"Volume":    {".image"},
}

// localTransports are the containers-transports(5) prefixes that read no registry.
var localTransports = []string{"dir:", "docker-archive:", "oci-archive:", "oci:", "containers-storage:", "sif:"}

// registryTransports are the transport prefixes of registry images, which are not part of the
// image reference.
var registryTransports = []string{"docker://", "docker-daemon:"}

type quadletManager struct{}

// New returns the quadlet manager.
func New() manager.Manager { return quadletManager{} }

func (quadletManager) Name() string { return Name }

// Match reports whether path is a `<name>.container`, `<name>.image` or `<name>.volume` unit.
func (quadletManager) Match(path string) bool {
	for _, s := range suffixes {
		if len(path) > len(s) && strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (quadletManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: suffixes}
}

// image is the last Image= value of a section: as in Quadlet, a later assignment wins.
type image struct {
	line, off int    // 1-based line, byte offset of value in src (-1: continued line)
	value     string // the value as written, without blanks and quotes
}

// Extract returns the image of the [Container], [Image] and [Volume] sections of src.
func (quadletManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	found := map[string]image{}
	var order []string
	section, cont := "", false
	for line, start := 1, 0; start < len(src); line++ {
		end := len(src)
		if i := bytes.IndexByte(src[start:], '\n'); i >= 0 {
			end = start + i
		}
		l := bytes.TrimRight(src[start:end], "\r")
		lineStart := start
		start = end + 1
		trimmed := bytes.TrimLeft(l, " \t")
		continued := cont
		cont = bytes.HasSuffix(l, []byte(`\`))
		switch {
		case continued:
			continue // the rest of the previous line's value
		case len(trimmed) == 0 || trimmed[0] == '#' || trimmed[0] == ';':
			cont = false // a comment ends with its line
			continue
		case trimmed[0] == '[':
			section = ""
			if i := bytes.IndexByte(trimmed, ']'); i > 0 {
				section = string(trimmed[1:i])
			}
			continue
		}
		key, value, ok := bytes.Cut(trimmed, []byte("="))
		if _, want := sections[section]; !ok || !want || string(bytes.TrimSpace(key)) != "Image" {
			continue
		}
		off := lineStart + len(l) - len(trimmed) + len(key) + 1
		lead := len(value) - len(bytes.TrimLeft(value, " \t"))
		value = bytes.TrimSpace(value)
		off += lead
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value, off = value[1:len(value)-1], off+1
		}
		img := image{line: line, off: off, value: string(value)}
		if bytes.HasSuffix(value, []byte(`\`)) {
			// A continued line: the value spans lines, its bytes are not one token.
			img.off, img.value = -1, strings.TrimSuffix(img.value, `\`)
		}
		if _, seen := found[section]; !seen {
			order = append(order, section)
		}
		found[section] = img
	}
	var deps []dep.Dep
	for _, s := range order {
		img := found[s]
		if img.value == "" {
			continue // Image= resets the key
		}
		deps = append(deps, newDep(file, img, sections[s], env.RegistryAliases))
	}
	return deps, nil
}

// newDep returns the dep of img; unitSuffixes are the suffixes of quadlet unit references in
// its section.
func newDep(file string, img image, unitSuffixes []string, aliases map[string]string) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeImage, File: file, Line: img.line, Name: img.value}
	ref, off := img.value, img.off
	for _, s := range unitSuffixes {
		if strings.HasSuffix(ref, s) {
			d.Skip = skipUnit
			return d
		}
	}
	for _, t := range localTransports {
		if strings.HasPrefix(ref, t) {
			d.Skip = skipTransport
			return d
		}
	}
	for _, t := range registryTransports {
		if r, ok := strings.CutPrefix(ref, t); ok {
			ref = r
			if off >= 0 {
				off += len(t)
			}
			break
		}
	}
	d.Name = ref
	if strings.HasPrefix(ref, "localhost/") {
		// Podman names the images it builds localhost/<name>: no registry has them.
		d.Skip = skipLocal
		return d
	}
	docker.ParseImage(&d, off, ref, aliases)
	return d
}

// Edits rewrites image references like the docker manager.
func (quadletManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (quadletManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	return docker.Rename(d, name)
}
