package nuget

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// credential is the username and password of a package source.
type credential struct{ user, pass string }

// configCreds caches the packageSourceCredentials of the nuget.config files above each
// manifest directory.
type configCreds struct {
	mu     sync.Mutex
	dirs   map[string]*credsEntry           // absolute manifest directory -> its merged credentials
	byFile map[string]map[string]credential // dep file (slash, relative) -> its merged credentials
}

type credsEntry struct {
	once  sync.Once
	creds map[string]credential // lower-case source key -> credential
}

// load returns the packageSourceCredentials that apply to the manifest directory dir: those of
// every nuget.config from root down to dir, nearer files overriding a source's entry (as NuGet
// merges them). It remembers them for file, the dep's manifest. Unreadable or malformed files
// are left out (the manager reports them).
func (c *configCreds) load(file, dir, root string, getenv func(string) string) map[string]credential {
	c.mu.Lock()
	if c.dirs == nil {
		c.dirs, c.byFile = map[string]*credsEntry{}, map[string]map[string]credential{}
	}
	e := c.dirs[dir]
	if e == nil {
		e = &credsEntry{}
		c.dirs[dir] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.creds = readCreds(dir, root, getenv) })
	c.mu.Lock()
	c.byFile[file] = e.creds
	c.mu.Unlock()
	return e.creds
}

// of returns the credentials remembered for the dep file file (nil when none were loaded).
func (c *configCreds) of(file string) map[string]credential {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byFile[file]
}

// readCreds merges the packageSourceCredentials of the nuget.config files of dir and its
// parents up to root.
func readCreds(dir, root string, getenv func(string) string) map[string]credential {
	var chain []string
	for cur := filepath.Clean(dir); ; {
		if p := configIn(cur); p != "" {
			chain = append(chain, p)
		}
		if root == "" || cur == filepath.Clean(root) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	out := map[string]credential{}
	for _, p := range slices.Backward(chain) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		creds, cleared, err := parseCreds(b, getenv)
		if err != nil {
			slog.Debug("nuget.config credentials not read", "path", p, "err", err)
			continue
		}
		if cleared {
			clear(out)
		}
		maps.Copy(out, creds)
	}
	return out
}

// configIn returns the nuget.config of dir (the name case-insensitive), "" when there is none.
func configIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("nuget.config lookup", "dir", dir, "err", err)
		}
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), "nuget.config") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

type credsXML struct {
	Creds struct {
		Sources []struct {
			XMLName xml.Name
			Entries []struct {
				Key   string `xml:"key,attr"`
				Value string `xml:"value,attr"`
			} `xml:"add"`
		} `xml:",any"`
	} `xml:"packageSourceCredentials"`
}

// parseCreds reads the `<packageSourceCredentials>` of a nuget.config: per source key (the
// element name, XML-encoded as NuGet writes it: `_x0020_` for a space) `Username` and
// `ClearTextPassword`, `%VAR%` references expanded from the environment. A source whose
// password is the encrypted `Password` (Windows DPAPI, unreadable elsewhere) or names an unset
// variable is left out with a warning. cleared reports a `<clear/>`, which drops the
// credentials of farther files.
func parseCreds(src []byte, getenv func(string) string) (creds map[string]credential, cleared bool, err error) {
	var x credsXML
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&x); err != nil {
		return nil, false, err
	}
	out := map[string]credential{}
	for _, s := range x.Creds.Sources {
		key := decodeName(s.XMLName.Local)
		if strings.EqualFold(key, "clear") {
			clear(out)
			cleared = true
			continue
		}
		var c credential
		var clearText, encrypted bool
		for _, e := range s.Entries {
			switch strings.ToLower(e.Key) {
			case "username":
				c.user = e.Value
			case "cleartextpassword":
				c.pass, clearText = e.Value, true
			case "password":
				encrypted = true
			}
		}
		if !clearText {
			if encrypted {
				slog.Warn("nuget.config: encrypted Password of a package source is Windows-only (DPAPI); use ClearTextPassword or NuGetPackageSourceCredentials_<key>", "source", key)
			}
			continue
		}
		var unset []string
		c.user, unset = expand(c.user, getenv, unset)
		c.pass, unset = expand(c.pass, getenv, unset)
		if len(unset) > 0 {
			slog.Warn("nuget.config: credentials of a package source reference unset variables", "source", key, "vars", unset)
			continue
		}
		out[strings.ToLower(key)] = c
	}
	return out, cleared, nil
}

var (
	encodedRe = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)
	envVarRe  = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
)

// decodeName undoes XmlConvert.EncodeLocalName (`_xHHHH_` for characters not allowed in
// element names), which NuGet applies to source keys.
func decodeName(s string) string {
	return encodedRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseUint(m[2:6], 16, 16)
		return string(rune(n))
	})
}

// expand replaces `%VAR%` in s from the environment, appending unset names to unset.
func expand(s string, getenv func(string) string, unset []string) (string, []string) {
	s = envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		v := getenv(name)
		if v == "" {
			unset = append(unset, name)
		}
		return v
	})
	return s, unset
}
