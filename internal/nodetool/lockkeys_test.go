package nodetool

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"strings"
	"testing"
)

// lockedVersionsToken is lockedVersions as it read every entry name with ReadToken;
// lockedVersions must return the same for every input.
func lockedVersionsToken(r *bufio.Reader, keys map[string]bool) (int, map[string]string, error) {
	dec := jsontext.NewDecoder(r, lenient)
	if err := enterObject(dec); err != nil {
		return 0, nil, err
	}
	lockVersion := 0
	versions := map[string]string{}
	var v1 map[string]string
	entries := func(section, prefix string, out map[string]string) error {
		if err := enterObject(dec); err != nil {
			return fmt.Errorf("%s: %w", section, err)
		}
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return err
			}
			k := tok.String()
			if !keys[prefix+k] || prefix != "" && strings.Contains(k, "/node_modules/") {
				if err := dec.SkipValue(); err != nil {
					return err
				}
				continue
			}
			var e struct {
				Version string `json:"version"`
				Link    bool   `json:"link"`
			}
			if err := json.UnmarshalDecode(dec, &e, lenient); err != nil {
				return fmt.Errorf("%s[%q]: %w", section, k, err)
			}
			if !e.Link && e.Version != "" {
				out[prefix+k] = e.Version
			}
		}
		_, err := dec.ReadToken()
		return err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, nil, err
		}
		switch k := tok.String(); {
		case k == "lockfileVersion":
			if err := json.UnmarshalDecode(dec, &lockVersion); err != nil {
				return 0, nil, fmt.Errorf("lockfileVersion: %w", err)
			}
		case k == "packages":
			if err := entries(k, "", versions); err != nil {
				return 0, nil, err
			}
		case k == "dependencies" && lockVersion < 2:
			v1 = map[string]string{}
			if err := entries(k, "node_modules/", v1); err != nil {
				return 0, nil, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return 0, nil, err
			}
		}
	}
	if lockVersion < 2 && v1 != nil {
		versions = v1
	}
	return lockVersion, versions, nil
}

var parityKeys = map[string]bool{"node_modules/a": true, "node_modules/é": true, "x/node_modules/a": true, "": true}

func checkLockedVersionsParity(t *testing.T, src []byte) {
	t.Helper()
	gv, gm, gerr := lockedVersions(bufio.NewReader(bytes.NewReader(src)), parityKeys)
	wv, wm, werr := lockedVersionsToken(bufio.NewReader(bytes.NewReader(src)), parityKeys)
	if gv != wv || !maps.Equal(gm, wm) || fmt.Sprint(gerr) != fmt.Sprint(werr) {
		t.Fatalf("lockedVersions(%q) = %d, %v, %v; want %d, %v, %v", src, gv, gm, gerr, wv, wm, werr)
	}
}

func TestLockedVersionsParity(t *testing.T) {
	src, _ := benchLock(200)
	for _, s := range append(lockParitySeeds, string(src)) {
		checkLockedVersionsParity(t, []byte(s))
	}
}

var lockParitySeeds = []string{
	`{"lockfileVersion":3,"packages":{"":{"version":"1.0.0"},"node_modules/a":{"version":"1.2.3"},"node_modules/b":{"version":"2.0.0"}}}`,
	`{"lockfileVersion":3,"packages":{"node_modules/\u0061":{"version":"1.0.0"},"node_modules/\u00e9":{"version":"2.0.0"},"x\/node_modules\/a":{"link":true,"version":"3"}}}`,
	`{"lockfileVersion":2,"packages":{"node_modules/a":{"version":"1"},"node_modules/a":{"version":"2"}}}`,
	`{"lockfileVersion":3,"packages":{1:{}}}`,
	`{"lockfileVersion":3,"packages":{"node_modules/a":}}`,
	`{"lockfileVersion":3,"packages":{"node_modules/a":{"version":1}}}`,
	`{"lockfileVersion":3,"packages":{"node_modules/a"`,
	`{"lockfileVersion":3,"packages":{"bad \x":{}}}`,
	`{"lockfileVersion":1,"dependencies":{"a":{"version":"1.0.0"},"\u00e9":{"version":"2.0.0"},"b":{"version":"3.0.0"}}}`,
	`{"lockfileVersion":2,"dependencies":{"a":{"version":"1.0.0"}},"packages":{"node_modules/a":{"version":"2.0.0"}}}`,
	`{"dependencies":{"a":{"version":"1.0.0"}},"lockfileVersion":2,"packages":{}}`,
	`{"lockfileVersion":1,"dependencies":{"a":{"version":1}}}`,
	`{"lockfileVersion":1,"dependencies":[]}`,
	`{"packages":[]}`,
	`[]`,
	`{"packages":{{`,
}

func FuzzLockedVersionsParity(f *testing.F) {
	for _, s := range lockParitySeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(checkLockedVersionsParity)
}
