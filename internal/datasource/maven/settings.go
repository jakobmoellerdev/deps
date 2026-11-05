package mavends

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// repoSettingsFiles are the in-repository settings looked for in each directory, in order.
var repoSettingsFiles = [...]string{".mvn/settings.xml", ".m2/settings.xml", "settings.xml"}

// settings is the subset of Maven's settings.xml deps applies: mirrors, server credentials and
// the repositories of the active profiles, with ${env.NAME} / ${NAME} expanded.
type settings struct {
	mirrors     []mirror
	servers     map[string]*auth
	repos       []repository // active profiles' <repositories>
	pluginRepos []repository // active profiles' <pluginRepositories>
}

type mirror struct{ id, url, mirrorOf string }

type settingsEntry struct {
	once sync.Once
	s    *settings
	err  error
}

type settingsXML struct {
	Mirrors []struct {
		ID       string `xml:"id"`
		URL      string `xml:"url"`
		MirrorOf string `xml:"mirrorOf"`
	} `xml:"mirrors>mirror"`
	Servers []struct {
		ID       string `xml:"id"`
		Username string `xml:"username"`
		Password string `xml:"password"`
		Headers  []struct {
			Name  string `xml:"name"`
			Value string `xml:"value"`
		} `xml:"configuration>httpHeaders>property"`
	} `xml:"servers>server"`
	Profiles []struct {
		ID              string    `xml:"id"`
		ActiveByDefault string    `xml:"activation>activeByDefault"`
		Repos           []repoXML `xml:"repositories>repository"`
		PluginRepos     []repoXML `xml:"pluginRepositories>pluginRepository"`
	} `xml:"profiles>profile"`
	ActiveProfiles []string `xml:"activeProfiles>activeProfile"`
}

type repoXML struct {
	ID  string `xml:"id"`
	URL string `xml:"url"`
}

// userSettingsPath is $DEPS_MAVEN_SETTINGS, else $HOME/.m2/settings.xml ("" without HOME).
func (s *Source) userSettingsPath() string {
	if p := s.getenv("DEPS_MAVEN_SETTINGS"); p != "" {
		return p
	}
	if h := s.getenv("HOME"); h != "" {
		return filepath.Join(h, ".m2", "settings.xml")
	}
	return ""
}

// settingsFor returns the effective settings of the pom directory dir: the nearest
// in-repository settings file (walking up to root) over the user settings, nil when neither
// exists. dir "" uses the user settings alone.
func (s *Source) settingsFor(dir, root string) (*settings, error) {
	p := ""
	if dir != "" {
		var err error
		if p, err = s.nearestSettings(dir, root); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	e := s.settings[p]
	if e == nil {
		e = &settingsEntry{}
		s.settings[p] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		user, err := s.loadFile(s.userSettingsPath())
		if err != nil {
			e.err = err
			return
		}
		var local *settingsXML
		if p != "" {
			if local, e.err = s.loadFile(p); e.err != nil {
				return
			}
		}
		e.s = s.merge(local, user)
	})
	return e.s, e.err
}

// nearestSettings returns the first of repoSettingsFiles found walking up from dir to root,
// "" when there is none; looked up once per directory.
func (s *Source) nearestSettings(dir, root string) (string, error) {
	s.mu.Lock()
	e := s.nearest[dir]
	if e == nil {
		e = &nearestEntry{}
		s.nearest[dir] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		for cur := dir; ; {
			for _, name := range repoSettingsFiles {
				p := filepath.Join(cur, filepath.FromSlash(name))
				fi, err := os.Stat(p)
				switch {
				case err == nil && !fi.IsDir():
					e.path = p
					return
				case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission):
					e.err = err
					return
				}
			}
			parent := filepath.Dir(cur)
			if cur == root || parent == cur {
				return
			}
			cur = parent
		}
	})
	return e.path, e.err
}

type nearestEntry struct {
	once sync.Once
	path string
	err  error
}

// loadFile parses the settings file p once; nil when p is "" or missing.
func (s *Source) loadFile(p string) (*settingsXML, error) {
	if p == "" {
		return nil, nil
	}
	s.mu.Lock()
	e := s.files[p]
	if e == nil {
		e = &fileEntry{}
		s.files[p] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		b, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return
		case err != nil:
			e.err = err
			return
		}
		x := &settingsXML{}
		if err := xml.Unmarshal(b, x); err != nil {
			e.err = fmt.Errorf("%s: %w", p, err)
			return
		}
		e.x = x
		slog.Debug("maven settings loaded", "path", p, "mirrors", len(x.Mirrors), "servers", len(x.Servers), "profiles", len(x.Profiles))
	})
	return e.x, e.err
}

type fileEntry struct {
	once sync.Once
	x    *settingsXML
	err  error
}

var propRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// expand replaces ${env.NAME} and ${NAME} with the environment value (empty when unset).
func (s *Source) expand(v string) string {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "${") {
		return v
	}
	return propRe.ReplaceAllStringFunc(v, func(m string) string {
		return s.getenv(strings.TrimPrefix(m[2:len(m)-1], "env."))
	})
}

// merge combines local (dominant) and user settings per element id: mirrors, servers and
// profiles of local shadow user ones with the same id; active profiles are the union.
func (s *Source) merge(local, user *settingsXML) *settings {
	if local == nil && user == nil {
		return nil
	}
	st := &settings{servers: map[string]*auth{}}
	mirrorSeen := map[string]bool{}
	profileSeen := map[string]bool{}
	active := map[string]bool{}
	for _, x := range [...]*settingsXML{local, user} {
		if x == nil {
			continue
		}
		for _, id := range x.ActiveProfiles {
			active[strings.TrimSpace(id)] = true
		}
	}
	for _, x := range [...]*settingsXML{local, user} {
		if x == nil {
			continue
		}
		for _, m := range x.Mirrors {
			id := s.expand(m.ID)
			if mirrorSeen[id] {
				continue
			}
			mirrorSeen[id] = true
			if u := s.expand(m.URL); u != "" {
				st.mirrors = append(st.mirrors, mirror{id: id, url: withSlash(u), mirrorOf: s.expand(m.MirrorOf)})
			}
		}
		for _, sv := range x.Servers {
			id := s.expand(sv.ID)
			if _, ok := st.servers[id]; ok {
				continue
			}
			a := &auth{user: s.expand(sv.Username)}
			if a.pass, a.err = s.decryptValue(s.expand(sv.Password)); a.err != nil {
				a.err = fmt.Errorf("maven settings server %s password: %w", id, a.err)
			}
			for _, h := range sv.Headers {
				if n := s.expand(h.Name); n != "" {
					a.headers = append(a.headers, header{n, s.expand(h.Value)})
				}
			}
			st.servers[id] = a
		}
		for _, p := range x.Profiles {
			id := s.expand(p.ID)
			if profileSeen[id] {
				continue
			}
			profileSeen[id] = true
			if !active[id] && strings.TrimSpace(p.ActiveByDefault) != "true" {
				continue
			}
			st.repos = s.appendRepos(st.repos, p.Repos)
			st.pluginRepos = s.appendRepos(st.pluginRepos, p.PluginRepos)
		}
	}
	return st
}

func (s *Source) appendRepos(dst []repository, rs []repoXML) []repository {
	for _, r := range rs {
		if u := withSlash(s.expand(r.URL)); u != "/" {
			dst = append(dst, repository{id: s.expand(r.ID), url: u, orig: []string{u}})
		}
	}
	return dst
}

// mirrorFor returns the mirror of repository r: the first whose mirrorOf is r's id, else the
// first whose mirrorOf pattern matches (Maven's DefaultMirrorSelector).
func (st *settings) mirrorFor(r repository) (mirror, bool) {
	if r.id != "" {
		for _, m := range st.mirrors {
			if m.mirrorOf == r.id {
				return m, true
			}
		}
	}
	for _, m := range st.mirrors {
		if matchMirrorOf(m.mirrorOf, r) {
			return m, true
		}
	}
	return mirror{}, false
}

// matchMirrorOf implements Maven's mirrorOf syntax: `*`, `external:*` (not localhost or
// file:), `external:http:*` (external over plain HTTP), comma-separated ids, and `!id`
// exclusions. Explicit ids and exclusions decide at once; wildcards keep looking for an
// exclusion. Whitespace around list items is not trimmed, as in Maven.
func matchMirrorOf(pattern string, r repository) bool {
	if pattern == "*" || (r.id != "" && pattern == r.id) {
		return true
	}
	matched := false
	for item := range strings.SplitSeq(pattern, ",") {
		switch {
		case len(item) > 1 && item[0] == '!':
			if item[1:] == r.id {
				return false
			}
		case item != "" && item == r.id:
			return true
		case item == "*":
			matched = true
		case item == "external:*":
			matched = matched || isExternal(r.url)
		case item == "external:http:*":
			matched = matched || (isExternal(r.url) && strings.HasPrefix(strings.ToLower(r.url), "http:"))
		}
	}
	return matched
}

// isExternal reports whether a repository URL is neither file: nor on localhost.
func isExternal(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || strings.EqualFold(u.Scheme, "file") {
		return false
	}
	h := u.Hostname()
	return h != "localhost" && h != "127.0.0.1"
}
