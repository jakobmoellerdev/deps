package nodetool

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v4"
)

// pnpmImporter is a project of pnpm-lock.yaml: its direct dependencies by section.
type pnpmImporter struct {
	Dependencies         map[string]pnpmDep `yaml:"dependencies"`
	DevDependencies      map[string]pnpmDep `yaml:"devDependencies"`
	OptionalDependencies map[string]pnpmDep `yaml:"optionalDependencies"`
}

// pnpmLock is the part of pnpm-lock.yaml ReadLocked reads: the importers by directory relative
// to the lock root ("." for the root), or the root project's sections at the top level, as
// single-project lockfiles before version 6 have them.
type pnpmLock struct {
	Importers    map[string]pnpmImporter `yaml:"importers"`
	pnpmImporter `yaml:",inline"`
}

// pnpmDep is the locked version of a direct dependency: `{specifier, version}` (lockfile
// version 6 and later) or the version alone (5.x).
type pnpmDep string

func (d *pnpmDep) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*d = pnpmDep(n.Value)
		return nil
	}
	var e struct {
		Version string `yaml:"version"`
	}
	if err := n.Decode(&e); err != nil {
		return err
	}
	*d = pnpmDep(e.Version)
	return nil
}

// version returns the locked version of name, the first section's that has it: without the
// peer suffix (`1.2.3(react@18.0.0)`, 5.x `1.2.3_react@18.0.0`); "" when it is not a version
// (`link:`, `file:`, an alias's `name@1.2.3`, a tarball URL).
func (p pnpmImporter) version(name string) string {
	for _, s := range []map[string]pnpmDep{p.Dependencies, p.DevDependencies, p.OptionalDependencies} {
		d, ok := s[name]
		if !ok {
			continue
		}
		v, _, _ := strings.Cut(string(d), "(")
		v, _, _ = strings.Cut(v, "_")
		if v == "" || v[0] < '0' || v[0] > '9' {
			return ""
		}
		return v
	}
	return ""
}

// pnpmVersions reads the locked versions of ws from a pnpm-lock.yaml. Only the head of each
// YAML document is parsed: everything before its top-level `packages:` or `snapshots:`, the
// resolved package graph that makes up most of the file. pnpm 11 and later write the
// packageManager and configDependencies into a document of their own before the project's.
func pnpmVersions(r *bufio.Reader, ws []lockWant) ([]string, error) {
	heads, err := pnpmHeads(r)
	if err != nil {
		return nil, err
	}
	locks := make([]pnpmLock, len(heads))
	for i, h := range heads {
		if err := yaml.Unmarshal(h, &locks[i]); err != nil {
			return nil, err
		}
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		for _, lock := range locks {
			imp, ok := lock.Importers[cmp.Or(w.member, ".")]
			if !ok && lock.Importers == nil && w.member == "" {
				imp = lock.pnpmImporter
			}
			if out[i] = imp.version(w.name); out[i] != "" {
				break
			}
		}
	}
	return out, nil
}

// pnpmHeads returns the heads of the YAML documents of r (separated by `---` lines): their lines
// before the first top-level `packages:` or `snapshots:`.
func pnpmHeads(r *bufio.Reader) ([][]byte, error) {
	var heads [][]byte
	var head []byte
	in := true    // within a head
	start := true // at the start of a line
	for {
		line, err := r.ReadSlice('\n')
		if start {
			switch string(bytes.TrimRight(line, " \r\n")) {
			case "---":
				if len(head) > 0 {
					heads = append(heads, head)
				}
				head, in = nil, true
				line = nil
			case "packages:", "snapshots:":
				in = false
			}
		}
		if in {
			head = append(head, line...)
		}
		switch {
		case err == nil:
			start = true
		case errors.Is(err, bufio.ErrBufferFull):
			start = false
		case errors.Is(err, io.EOF):
			if len(head) > 0 {
				heads = append(heads, head)
			}
			return heads, nil
		default:
			return nil, err
		}
	}
}
