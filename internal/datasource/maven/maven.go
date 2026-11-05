// Package mavends lists the versions of Maven artifacts from the maven-metadata.xml of one or
// more Maven repositories (Maven Central by default).
//
// The repositories of a dep come from its Source. Deps of the maven and maven-wrapper
// managers also apply Maven's settings.xml (the nearest in-repository one over the user's):
// mirrors, server credentials and active-profile repositories. Deps of the gradle manager
// authenticate named repositories with Gradle's `<name>Username`/`<name>Password` properties.
package mavends

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "maven"

// Central is Maven Central; DEPS_MAVEN_CENTRAL_URL replaces it (mirrors).
const Central = "https://repo.maven.apache.org/maven2/"

// centralID is the repository id of Maven Central.
const centralID = "central"

// maxMetadata bounds the size of a maven-metadata.xml document.
const maxMetadata = 16 << 20

// Source fetches each (repository, credentials, artifact) metadata document once per run and
// parses each settings file once.
type Source struct {
	client  *http.Client
	sem     chan struct{}
	getenv  func(string) string
	central string

	times datasource.Memo[reqKey, time.Time]

	mu       sync.Mutex
	meta     map[reqKey]*metaEntry
	poms     map[reqKey]*pomEntry
	resolved map[string][]repository   // dep key -> repositories of its Releases lookup
	settings map[string]*settingsEntry // in-repository settings path ("" = none) -> effective settings
	nearest  map[string]*nearestEntry  // pom directory -> nearest in-repository settings path
	files    map[string]*fileEntry     // settings file -> parsed

	masterOnce sync.Once
	master     string
	masterErr  error

	gradleOnce  sync.Once
	gradleProps map[string]string
	gradleErr   error
	gradleAuths map[string]*auth // repository name -> credentials (nil = none)
}

// reqKey identifies a cached request: repository URL, credentials and artifact name or path.
type reqKey struct {
	repo string
	auth *auth
	name string
}

// repository is one place a dep is looked up, after mirroring, with its credentials.
type repository struct {
	id, url string
	auth    *auth    // nil = anonymous
	orig    []string // the URLs as declared (before the Central override and mirroring) mapped to url
}

// auth is the credentials of a repository: Basic auth unless user and pass are empty, plus
// headers. err is set when they could not be read (an undecryptable password).
type auth struct {
	user, pass string
	headers    []header
	err        error
}

type header struct{ name, value string }

type metaEntry struct {
	once     sync.Once
	versions []string
	notFound bool
	err      error
}

// New returns the maven datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_MAVEN_CENTRAL_URL, the settings.xml locations and expansions, and Gradle
// credentials.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	central := Central
	if v := getenv("DEPS_MAVEN_CENTRAL_URL"); v != "" {
		central = withSlash(v)
	}
	return &Source{
		client: stripHeadersOnRedirect(client), sem: sem, getenv: getenv, central: central,
		meta: map[reqKey]*metaEntry{}, poms: map[reqKey]*pomEntry{},
		resolved: map[string][]repository{}, settings: map[string]*settingsEntry{},
		nearest: map[string]*nearestEntry{}, files: map[string]*fileEntry{}, gradleAuths: map[string]*auth{},
	}
}

// parseSource returns the repositories `[<id>=]<url>` of a dep Source, Maven Central when
// empty. Central is replaced by its DEPS_MAVEN_CENTRAL_URL override.
func (s *Source) parseSource(src string) []repository {
	fields := strings.Fields(src)
	if len(fields) == 0 {
		return []repository{{id: centralID, url: s.central, orig: []string{Central}}}
	}
	repos := make([]repository, 0, len(fields))
	for _, f := range fields {
		id, u, ok := strings.Cut(f, "=")
		if !ok || strings.Contains(id, "/") {
			id, u = "", f
		}
		u = withSlash(u)
		r := repository{id: id, url: u, orig: []string{u}}
		if u == Central {
			r.url = s.central
		}
		repos = append(repos, r)
	}
	return repos
}

// depKey identifies what the repositories of d depend on.
func depKey(d dep.Dep) string {
	return d.Manager + "\x00" + d.DepType + "\x00" + d.File + "\x00" + d.Source
}

// repositories returns the effective repositories of d, in lookup order. dir is the absolute
// directory of d.File ("" = no in-repository settings). maven and maven-wrapper deps get the
// active-profile repositories of settings.xml before Maven Central, are mirrored and use the
// credentials of the (mirror's) server id; gradle deps use Gradle's credentials of the
// repository name.
func (s *Source) repositories(d dep.Dep, dir string) ([]repository, error) {
	repos := s.parseSource(d.Source)
	switch d.Manager {
	case "maven", "maven-wrapper":
		st, err := s.settingsFor(dir, datasource.RootOf(d.File, dir))
		if err != nil {
			return nil, err
		}
		if st == nil {
			break
		}
		extra := st.repos
		if d.DepType == "build" { // plugins resolve from <pluginRepositories>
			extra = st.pluginRepos
		}
		repos = insertBeforeCentral(repos, extra, s.central)
		for i, r := range repos {
			if m, ok := st.mirrorFor(r); ok {
				r = repository{id: m.id, url: m.url, orig: r.orig}
			}
			if a := st.servers[r.id]; a != nil {
				if a.err != nil {
					return nil, a.err
				}
				r.auth = a
			}
			repos[i] = r
		}
	case "gradle":
		for i, r := range repos {
			if r.id == "" {
				continue
			}
			a, err := s.gradleAuth(r.id)
			if err != nil {
				return nil, err
			}
			repos[i].auth = a
		}
	}
	// Mirroring may map several repositories to one.
	out := repos[:0]
	for _, r := range repos {
		if i := slices.IndexFunc(out, func(o repository) bool { return o.url == r.url && o.auth == r.auth }); i >= 0 {
			out[i].orig = append(out[i].orig, r.orig...)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// insertBeforeCentral inserts the repositories extra (except ids already present) before the
// first Maven Central entry of repos, else at the end.
func insertBeforeCentral(repos, extra []repository, central string) []repository {
	if len(extra) == 0 {
		return repos
	}
	at := slices.IndexFunc(repos, func(r repository) bool {
		return r.id == centralID || r.url == central || r.url == Central
	})
	if at < 0 {
		at = len(repos)
	}
	add := make([]repository, 0, len(extra))
	for _, e := range extra {
		if e.id == "" || !slices.ContainsFunc(repos, func(r repository) bool { return r.id == e.id }) {
			add = append(add, e)
		}
	}
	return slices.Insert(repos, at, add...)
}

// located returns the repositories of d's earlier Releases lookup, else those without
// in-repository settings.
func (s *Source) located(d dep.Dep) ([]repository, error) {
	s.mu.Lock()
	repos, ok := s.resolved[depKey(d)]
	s.mu.Unlock()
	if ok {
		return repos, nil
	}
	return s.repositories(d, "")
}

// Releases merges the versions listed by every repository of d, in repository order. dir is
// the absolute directory of d.File. A repository without the artifact (404) contributes
// nothing; other failures fail the lookup only when no repository has the artifact.
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	base, _, err := artifactPath(d.Name)
	if err != nil {
		return nil, err
	}
	repos, err := s.repositories(d, dir)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.resolved[depKey(d)] = repos
	s.mu.Unlock()
	var (
		rels  []datasource.Release
		seen  = map[string]bool{}
		found bool
		errs  []error
	)
	for _, r := range repos {
		e := s.metadata(ctx, r, d.Name, base)
		switch {
		case e.err != nil:
			errs = append(errs, e.err)
			continue
		case e.notFound:
			continue
		}
		found = true
		for _, v := range e.versions {
			if !seen[v] {
				seen[v] = true
				rels = append(rels, datasource.Release{Version: v})
			}
		}
	}
	switch {
	case found:
		if len(errs) > 0 {
			slog.DebugContext(ctx, "maven repositories failed", "name", d.Name, "err", errors.Join(errs...))
		}
		return rels, nil
	case len(errs) > 0:
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("artifact %s not found", d.Name)
}

// ReleaseTime implements datasource.Timer with the Last-Modified header of the version's POM
// in the first repository listing it; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	base, artifact, err := artifactPath(d.Name)
	if err != nil {
		return time.Time{}, err
	}
	repos, err := s.located(d)
	if err != nil {
		return time.Time{}, err
	}
	for _, r := range repos {
		e := s.metadata(ctx, r, d.Name, base)
		if e.err != nil || e.notFound || !slices.Contains(e.versions, version) {
			continue
		}
		return s.pomTime(ctx, r, base+version+"/"+artifact+"-"+version+".pom")
	}
	return time.Time{}, nil
}

// OpenArtifact GETs the artifact at u with the credentials of the repository of d that u is
// below: u is matched against each repository's declared URLs (rewritten to the mirror, as
// for Releases) and its effective URL, longest prefix first; other URLs are fetched
// anonymously. The semaphore slot is held until the response headers arrive; the caller
// reads and closes the body. A non-2xx status is an error.
func (s *Source) OpenArtifact(ctx context.Context, d dep.Dep, u string) (io.ReadCloser, error) {
	repos, err := s.located(d)
	if err != nil {
		return nil, err
	}
	target, best := u, 0
	var a *auth
	match := func(prefix string, r repository) {
		if len(prefix) > best && strings.HasPrefix(u, prefix) {
			target, best, a = r.url+u[len(prefix):], len(prefix), r.auth
		}
	}
	for _, r := range repos {
		for _, o := range r.orig {
			match(o, r)
		}
		match(r.url, r)
	}
	resp, err := s.do(ctx, http.MethodGet, target, a)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		closeBody(resp)
		return nil, fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	slog.DebugContext(ctx, "maven artifact opened", "url", target, "authenticated", a != nil)
	return resp.Body, nil
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// artifactPath maps `group:artifact` to `group/path/artifact/` and the artifact. A name
// without ":" is a Gradle plugin id, looked up as its marker artifact `<id>:<id>.gradle.plugin`.
func artifactPath(name string) (base, artifact string, err error) {
	group, artifact, ok := strings.Cut(name, ":")
	if !ok {
		artifact = name + ".gradle.plugin"
	}
	if group == "" || artifact == "" || strings.ContainsAny(name, "/ ") || strings.Contains(artifact, ":") {
		return "", "", fmt.Errorf("%s: name %q is neither group:artifact nor a plugin id", Name, name)
	}
	return strings.ReplaceAll(group, ".", "/") + "/" + artifact + "/", artifact, nil
}

func (s *Source) metadata(ctx context.Context, r repository, name, base string) *metaEntry {
	k := reqKey{r.url, r.auth, name}
	s.mu.Lock()
	e := s.meta[k]
	if e == nil {
		e = &metaEntry{}
		s.meta[k] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		start := time.Now()
		e.versions, e.notFound, e.err = s.fetchMetadata(ctx, r.url+base+"maven-metadata.xml", r.auth)
		slog.DebugContext(ctx, "maven metadata fetched", "name", name, "repository", r.url, "authenticated", r.auth != nil,
			"versions", len(e.versions), "notFound", e.notFound, "duration", time.Since(start), "err", e.err)
	})
	return e
}

func (s *Source) fetchMetadata(ctx context.Context, u string, a *auth) (versions []string, notFound bool, err error) {
	resp, err := s.do(ctx, http.MethodGet, u, a)
	if err != nil {
		return nil, false, err
	}
	defer closeBody(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	var m struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	dec := xml.NewDecoder(io.LimitReader(resp.Body, maxMetadata))
	// plugins.gradle.org declares US-ASCII, other repositories ISO-8859-1; versions are ASCII.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&m); err != nil {
		return nil, false, fmt.Errorf("GET %s: malformed metadata: %w", u, err)
	}
	versions = make([]string, 0, len(m.Versions))
	for _, v := range m.Versions {
		if v = strings.TrimSpace(v); v != "" {
			versions = append(versions, v)
		}
	}
	return versions, false, nil
}

// pomTime HEADs the POM at path below repository r once per run.
func (s *Source) pomTime(ctx context.Context, r repository, path string) (time.Time, error) {
	return s.times.Do(reqKey{r.url, r.auth, path}, func() (time.Time, error) {
		u := r.url + path
		resp, err := s.do(ctx, http.MethodHead, u, r.auth)
		if err != nil {
			return time.Time{}, err
		}
		closeBody(resp)
		var t time.Time
		if resp.StatusCode == http.StatusOK {
			// A missing or malformed header leaves the time unknown.
			t, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
		}
		slog.DebugContext(ctx, "maven release time", "url", u, "status", resp.StatusCode, "time", t)
		return t, nil
	})
}

// authHeadersKey carries the credential header names of a request to the redirect policy.
type authHeadersKey struct{}

// do sends one request holding a semaphore slot, authenticated with a (nil = anonymous); the
// caller closes the body.
func (s *Source) do(ctx context.Context, method, u string, a *auth) (*http.Response, error) {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	if a != nil && len(a.headers) > 0 {
		ctx = context.WithValue(ctx, authHeadersKey{}, a.headers)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if a != nil {
		if a.user != "" || a.pass != "" {
			req.SetBasicAuth(a.user, a.pass)
		}
		for _, h := range a.headers {
			req.Header.Set(h.name, h.value)
		}
	}
	return s.client.Do(req)
}

// stripHeadersOnRedirect returns a copy of c that drops a request's credential headers when a
// redirect leaves its host (net/http does so for Authorization only).
func stripHeadersOnRedirect(c *http.Client) *http.Client {
	cp := *c
	next := c.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if hs, ok := req.Context().Value(authHeadersKey{}).([]header); ok && req.URL.Host != via[0].URL.Host {
			for _, h := range hs {
				req.Header.Del(h.name)
			}
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &cp
}

func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

func withSlash(u string) string {
	if strings.HasSuffix(u, "/") {
		return u
	}
	return u + "/"
}
