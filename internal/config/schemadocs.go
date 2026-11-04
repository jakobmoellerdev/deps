package config

import (
	_ "embed"
	"fmt"
	"strings"
)

// schemaDocs holds the long descriptions of the config keys; see the comment at its top.
//
//go:embed schema.md
var schemaDocs string

// KeyDocs returns the long descriptions of the config keys from schema.md, by schema
// definition name ("Config" for the top level) and key; the key "" describes the definition
// itself. They extend the short jsonschema_description tags in the schema and the generated
// configuration reference.
func KeyDocs() (map[string]map[string]string, error) {
	return parseKeyDocs(schemaDocs)
}

// parseKeyDocs splits src into "## <Definition>[.<key>]" sections. Text before the first
// section (the leading comment) is ignored, as are "## " lines inside code fences.
func parseKeyDocs(src string) (map[string]map[string]string, error) {
	docs := map[string]map[string]string{}
	var def, key string
	var body []string
	inSection, fence := false, false
	flush := func() error {
		if !inSection {
			return nil
		}
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text == "" {
			return fmt.Errorf("schema docs: %s: empty section", sectionName(def, key))
		}
		if _, dup := docs[def][key]; dup {
			return fmt.Errorf("schema docs: %s: duplicate section", sectionName(def, key))
		}
		if docs[def] == nil {
			docs[def] = map[string]string{}
		}
		docs[def][key] = text
		return nil
	}
	for line := range strings.SplitSeq(src, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
		}
		if fence || !strings.HasPrefix(line, "## ") {
			body = append(body, line)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(line[3:])
		def, key, _ = strings.Cut(name, ".")
		if def == "" || strings.ContainsAny(name, " `") {
			return nil, fmt.Errorf("schema docs: invalid section %q (want <Definition> or <Definition>.<key>)", name)
		}
		body, inSection = nil, true
	}
	if fence {
		return nil, fmt.Errorf("schema docs: %s: unterminated code fence", sectionName(def, key))
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return docs, nil
}

func sectionName(def, key string) string {
	if key == "" {
		return def
	}
	return def + "." + key
}
