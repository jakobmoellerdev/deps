package config

import (
	"strings"
	"testing"
)

func TestParseKeyDocs(t *testing.T) {
	src := "<!--\nText before the first section is ignored.\n-->\n" +
		"## Config\n\nThe file.\n\n" +
		"## Config.pin\n\nPin text.\n\n```yaml\n## not a heading inside a fence\npin: digest\n```\n\n" +
		"## Rule.allow\nAllow text.\n"
	docs, err := parseKeyDocs(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"Config": {"": "The file.", "pin": "Pin text.\n\n```yaml\n## not a heading inside a fence\npin: digest\n```"},
		"Rule":   {"allow": "Allow text."},
	}
	for def, keys := range want {
		for k, v := range keys {
			if docs[def][k] != v {
				t.Errorf("%s.%s = %q, want %q", def, k, docs[def][k], v)
			}
		}
	}
	if len(docs) != 2 || len(docs["Config"]) != 2 || len(docs["Rule"]) != 1 {
		t.Errorf("docs = %v", docs)
	}
}

func TestParseKeyDocsErrors(t *testing.T) {
	for src, want := range map[string]string{
		"## Config.pin\n\n":                     "Config.pin: empty section",
		"## Config.pin\na\n## Config.pin\nb\n":  "Config.pin: duplicate section",
		"## Config pin\na\n":                    `invalid section "Config pin"`,
		"## .pin\na\n":                          `invalid section ".pin"`,
		"## `Config`\na\n":                      "invalid section",
		"## Config.pin\n```\nunterminated\n":    "Config.pin: unterminated code fence",
		"## Config.a\nx\n## Config.b\n\n## C\n": "Config.b: empty section",
	} {
		if _, err := parseKeyDocs(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
	}
}

// The embedded key docs parse; that each section names an existing key is checked where they
// are attached to the schema (cmd/deps).
func TestKeyDocs(t *testing.T) {
	docs, err := KeyDocs()
	if err != nil {
		t.Fatal(err)
	}
	if docs["Config"][""] == "" || docs["Rule"]["allow"] == "" || docs["CustomDatasource"]["versions"] == "" {
		t.Errorf("missing sections: %v", docs)
	}
}

func TestIgnoredByDefault(t *testing.T) {
	for _, d := range DepTypes {
		want := d == "indirect" || d == "go" || d == "peerDependencies" || d == "engines"
		if IgnoredByDefault(d) != want {
			t.Errorf("IgnoredByDefault(%q) = %v", d, !want)
		}
	}
}
