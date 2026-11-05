package nodetool

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
)

// bunVersions reads the locked versions of ws from a bun.lock (bun's text lockfile, JSON with
// trailing commas). `workspaces` maps each member directory to its package name (bun writes it
// before `packages`), and `packages` keys a member's own copy of a dependency
// `<member name>/<name>` and the hoisted one `<name>`; each entry is an array whose first element
// is `<name>@<version>`. Only the wanted entries are decoded.
func bunVersions(r *bufio.Reader, ws []lockWant) ([]string, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(jsonc.Strip(src)), lenient)
	if err := enterObject(dec); err != nil {
		return nil, err
	}
	var members map[string]struct {
		Name string `json:"name"`
	}
	versions := map[string]string{}
	// keys are the packages entries of each want, in resolution order.
	keys := func(w lockWant) []string {
		if m := members[w.member]; w.member != "" && m.Name != "" {
			return []string{m.Name + "/" + w.name, w.name}
		}
		return []string{w.name}
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		switch tok.String() {
		case "workspaces":
			if err := json.UnmarshalDecode(dec, &members, lenient); err != nil {
				return nil, fmt.Errorf("workspaces: %w", err)
			}
		case "packages":
			wanted := map[string]bool{}
			for _, w := range ws {
				for _, k := range keys(w) {
					wanted[k] = true
				}
			}
			if err := bunEntries(dec, wanted, versions); err != nil {
				return nil, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return nil, err
			}
		}
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		for _, k := range keys(w) {
			if v := versions[k]; v != "" {
				out[i] = v
				break
			}
		}
	}
	return out, nil
}

// bunEntries reads the packages object of a bun.lock and records the version of each entry
// named in keys: the part after the last @ of its first element, when it is a version (not
// `workspace:`, `file:`, `github:` and the like).
func bunEntries(dec *jsontext.Decoder, keys map[string]bool, versions map[string]string) error {
	if err := enterObject(dec); err != nil {
		return fmt.Errorf("packages: %w", err)
	}
	for dec.PeekKind() != '}' {
		k, ok, err := wantedName(dec, keys)
		if err != nil {
			return err
		}
		if !ok {
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		var e []jsontext.Value
		if err := json.UnmarshalDecode(dec, &e, lenient); err != nil {
			return fmt.Errorf("packages[%q]: %w", k, err)
		}
		var id string
		if len(e) == 0 || json.Unmarshal(e[0], &id) != nil {
			return fmt.Errorf("packages[%q]: want [\"<name>@<version>\", …]", k)
		}
		if i := strings.LastIndexByte(id, '@'); i > 0 && i+1 < len(id) && id[i+1] >= '0' && id[i+1] <= '9' {
			versions[k] = id[i+1:]
		}
	}
	_, err := dec.ReadToken()
	return err
}
