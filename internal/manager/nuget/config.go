package nuget

import (
	"bytes"
	"encoding/xml"
	"io"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
)

// defaultFeed is nuget.org, the package source NuGet starts from (its user-wide default
// configuration) before the repository's nuget.config files.
var defaultFeed = feed{key: "nuget.org", url: "https://api.nuget.org/v3/index.json"}

// feed is a package source: its nuget.config key and URL.
type feed struct{ key, url string }

// nugetConfig is what a nuget.config says about package sources.
type nugetConfig struct {
	clear    bool   // <packageSources><clear/>: the sources of farther configs are dropped
	sources  []feed // <add>; a feed with url "" is a <remove>
	disabled map[string]bool
	// mapping is packageSourceMapping: lower-case source key → package id patterns;
	// clearMapping drops the mappings of farther configs.
	mapping      map[string][]string
	clearMapping bool
}

type configXML struct {
	Sources struct {
		Entries []entryXML `xml:",any"`
	} `xml:"packageSources"`
	Disabled struct {
		Entries []entryXML `xml:"add"`
	} `xml:"disabledPackageSources"`
	Mapping struct {
		Clear   *struct{} `xml:"clear"`
		Sources []struct {
			Key      string `xml:"key,attr"`
			Packages []struct {
				Pattern string `xml:"pattern,attr"`
			} `xml:"package"`
		} `xml:"packageSource"`
	} `xml:"packageSourceMapping"`
}

type entryXML struct {
	XMLName xml.Name
	Key     string `xml:"key,attr"`
	Value   string `xml:"value,attr"`
}

// parseConfig reads a nuget.config (`<configuration>`).
func parseConfig(src []byte) (*nugetConfig, error) {
	var x configXML
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&x); err != nil {
		return nil, err
	}
	c := &nugetConfig{disabled: map[string]bool{}, mapping: map[string][]string{}, clearMapping: x.Mapping.Clear != nil}
	for _, e := range x.Sources.Entries {
		switch e.XMLName.Local {
		case "clear":
			c.clear, c.sources = true, nil
		case "add":
			if e.Key != "" && e.Value != "" {
				c.sources = append(c.sources, feed{key: e.Key, url: e.Value})
			}
		case "remove":
			c.sources = append(c.sources, feed{key: e.Key})
		}
	}
	for _, e := range x.Disabled.Entries {
		c.disabled[strings.ToLower(e.Key)] = strings.EqualFold(e.Value, "true")
	}
	for _, s := range x.Mapping.Sources {
		var pats []string
		for _, p := range s.Packages {
			if p.Pattern != "" {
				pats = append(pats, p.Pattern)
			}
		}
		c.mapping[strings.ToLower(s.Key)] = pats
	}
	return c, nil
}

// configNames are the file names NuGet reads a configuration from in each directory.
func isConfig(p string) bool { return strings.EqualFold(path.Base(p), "nuget.config") }

// sourcesFor returns the effective package sources of the directory dir (slash, relative to
// the root): nuget.org, then the nuget.config files from the repository root down to dir
// applied in turn, as NuGet merges them (nearer files win). It returns the enabled http(s)
// sources, the merged packageSourceMapping (nil = none) and whether any source was dropped
// for not being http(s) (local folders, `%VAR%` URLs).
func sourcesFor(configs map[string]*nugetConfig, dir string) (feeds []feed, mapping map[string][]string, dropped bool) {
	var chain []*nugetConfig
	for d := dir; ; d = path.Dir(d) {
		if c := configs[d]; c != nil {
			chain = append(chain, c)
		}
		if d == "." || d == "/" {
			break
		}
	}
	feeds = []feed{defaultFeed}
	disabled := map[string]bool{}
	for _, c := range slices.Backward(chain) {
		if c.clear {
			feeds = nil
		}
		for _, f := range c.sources {
			i := slices.IndexFunc(feeds, func(g feed) bool { return strings.EqualFold(g.key, f.key) })
			switch {
			case f.url == "" && i >= 0:
				feeds = slices.Delete(feeds, i, i+1)
			case f.url == "":
			case i >= 0:
				feeds[i].url = f.url
			default:
				feeds = append(feeds, f)
			}
		}
		maps.Copy(disabled, c.disabled)
		if c.clearMapping {
			mapping = nil
		}
		for k, v := range c.mapping {
			if mapping == nil {
				mapping = map[string][]string{}
			}
			mapping[k] = v
		}
	}
	out := feeds[:0:0]
	for _, f := range feeds {
		if disabled[strings.ToLower(f.key)] {
			continue
		}
		if u, err := url.Parse(f.url); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Contains(f.url, "%") {
			dropped = true
			continue
		}
		out = append(out, f)
	}
	return out, mapping, dropped
}

// mapped returns the feeds of feeds that packageSourceMapping allows for the package id: those
// whose patterns hold the most specific match of id (an exact id beats the longest `prefix*`,
// `*` is the shortest prefix). With no matching pattern no feed is allowed.
func mapped(feeds []feed, mapping map[string][]string, id string) []feed {
	if mapping == nil {
		return feeds
	}
	best := -1 // the matching pattern's specificity: its length, exact ids above any prefix
	var out []feed
	for _, f := range feeds {
		score := -1
		for _, p := range mapping[strings.ToLower(f.key)] {
			s := -1
			switch prefix, ok := strings.CutSuffix(p, "*"); {
			case ok && len(id) >= len(prefix) && strings.EqualFold(id[:len(prefix)], prefix):
				s = len(prefix)
			case !ok && strings.EqualFold(p, id):
				s = 1 << 30
			}
			score = max(score, s)
		}
		switch {
		case score < 0 || score < best:
		case score > best:
			best, out = score, append(out[:0], f)
		default:
			out = append(out, f)
		}
	}
	return out
}

// sourceString renders feeds as dep.Dep.Source for the nuget datasource: whitespace-separated
// `<key>=<url>` entries with query-escaped keys; "" when it is nuget.org alone.
func sourceString(feeds []feed) string {
	if len(feeds) == 1 && feeds[0].url == defaultFeed.url {
		return ""
	}
	parts := make([]string, len(feeds))
	for i, f := range feeds {
		parts[i] = url.QueryEscape(f.key) + "=" + f.url
	}
	return strings.Join(parts, " ")
}
