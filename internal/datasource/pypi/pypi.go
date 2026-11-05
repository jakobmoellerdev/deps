// Package pypi lists the versions of Python packages from PyPI or another Python package
// index.
//
// The primary index of a dependency is d.Source (a simple index URL declared by the manifest),
// else PIP_INDEX_URL, else PyPI; d.ExtraSources and the space-separated PIP_EXTRA_INDEX_URL add
// indexes whose releases are merged with the primary's (a package missing from some of them is
// fine). PyPI itself, also when named by its simple index URL https://pypi.org/simple, is
// asked through its JSON API (`GET <base>/pypi/<name>/json`, base DEPS_PYPI_URL or
// https://pypi.org); any other index through the simple API, preferring JSON (PEP 691, with
// the PEP 700 `versions` list when served) over HTML (PEP 503). With d.PythonConstraint set,
// releases whose files' requires-python does not admit every Python it allows are dropped.
// Credentials in the userinfo of an index URL are sent as basic auth and redacted from errors
// and logs. Names are PEP 503-normalized for lookups.
package pypi

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the datasource name.
const Name = "pypi"

// DefaultURL is the base URL of the PyPI JSON API; DEPS_PYPI_URL overrides it (mirrors).
const DefaultURL = "https://pypi.org"

// pypiSimple is PyPI's simple index; deps asks the JSON API instead when an index names it.
const pypiSimple = "https://pypi.org/simple"

// simpleJSON is the media type of version 1 of the JSON simple API (PEP 691).
const simpleJSON = "application/vnd.pypi.simple.v1+json"

// simpleHTML is the media type of version 1 of the HTML simple API (PEP 503, PEP 691).
const simpleHTML = "application/vnd.pypi.simple.v1+html"

// simpleAccept asks a simple index for JSON, else HTML (PEP 691's recommended header).
const simpleAccept = simpleJSON + ", " + simpleHTML + ";q=0.2, text/html;q=0.01"

// errNotAcceptable is returned by get on HTTP 406: the index has no representation for Accept.
var errNotAcceptable = errors.New("not acceptable")

// errNotFound is wrapped by get's error on HTTP 404.
var errNotFound = errors.New("not found")

// Source fetches each project once per (index, normalized name) per run and merges and
// filters it once per (indexes, name, Python constraint); concurrent callers share the
// results, and ReleaseTime and SourceRepo reuse them.
type Source struct {
	client *http.Client
	sem    chan struct{}
	getenv func(string) string
	base   string // JSON API base URL without trailing slash

	mu       sync.Mutex
	projects map[string]*entry  // index ("" = PyPI JSON API) + "\x00" + normalized name
	views    map[string]*entry  // indexes + "\x00" + normalized name + "\x00" + Python constraint
	admits   map[[2]string]bool // (project Python, requires-python) -> every Python of the project admitted
}

// project is what deps keeps of a project document.
type project struct {
	rels   []datasource.Release
	times  map[string]time.Time // versionKey -> earliest upload time
	python map[string]string    // versionKey -> requires-python shared by all files not yanked
	repo   string               // source repository URL as published (JSON API only)
}

type entry struct {
	once sync.Once
	project
	err error
}

// New returns the pypi datasource. sem bounds concurrent requests and is shared with the
// other datasources; getenv supplies PIP_INDEX_URL, PIP_EXTRA_INDEX_URL and DEPS_PYPI_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{
		client: client, sem: sem, getenv: getenv,
		base:     strings.TrimSuffix(cmp.Or(getenv("DEPS_PYPI_URL"), DefaultURL), "/"),
		projects: make(map[string]*entry),
		views:    make(map[string]*entry),
		admits:   make(map[[2]string]bool),
	}
}

// Releases lists the versions of d.Name that have at least one file that is not yanked, as
// published, across d's indexes (primary first, each version once), without those whose
// requires-python excludes part of d.PythonConstraint. The slice is shared between callers
// and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	e, err := s.view(ctx, d)
	if err != nil {
		return nil, err
	}
	return e.rels, e.err
}

// ReleaseTime implements datasource.Timer with the earliest upload time of version's files on
// any of d's indexes; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	e, err := s.view(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return e.times[versionKey(version)], e.err
}

// SourceRepo implements datasource.SourceRepo from the JSON API's `info.project_urls` and
// `info.home_page` (of the first index that has them). They describe the latest release
// whatever the version; simple indexes publish no project URLs, so their packages have none.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	e, err := s.view(ctx, d)
	if err != nil {
		return "", err
	}
	return e.repo, e.err
}

// Digest is unsupported: Python versions are not digest-pinned.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) {
	return "", errors.New("pypi: digests are not supported")
}

// view returns the project of d merged across its indexes and filtered by its Python
// constraint, built once per (indexes, normalized name, constraint).
func (s *Source) view(ctx context.Context, d dep.Dep) (*entry, error) {
	idxs, err := s.indexes(d)
	if err != nil {
		return nil, err
	}
	name := pyreq.Normalize(d.Name)
	key := strings.Join(idxs, "\n") + "\x00" + name + "\x00" + d.PythonConstraint
	s.mu.Lock()
	v := s.views[key]
	if v == nil {
		v = &entry{}
		s.views[key] = v
	}
	s.mu.Unlock()
	v.once.Do(func() {
		es := make([]*entry, len(idxs))
		if len(idxs) == 1 {
			es[0] = s.project(ctx, idxs[0], d.Name, name)
		} else {
			var wg sync.WaitGroup
			for i, idx := range idxs {
				wg.Go(func() { es[i] = s.project(ctx, idx, d.Name, name) })
			}
			wg.Wait()
		}
		v.project, v.err = merge(es)
		if v.err == nil && d.PythonConstraint != "" {
			v.project = s.filterPython(ctx, v.project, name, d.PythonConstraint)
		}
	})
	return v, nil
}

// project returns the fetched project normalized name (written is the dep name) on idx,
// fetching it once per (index, normalized name).
func (s *Source) project(ctx context.Context, idx, written, name string) *entry {
	key := idx + "\x00" + name
	s.mu.Lock()
	e := s.projects[key]
	if e == nil {
		e = &entry{}
		s.projects[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		if idx == "" {
			e.project, e.err = s.fetchJSON(ctx, written, name)
		} else {
			e.project, e.err = s.fetchSimple(ctx, idx, written, name)
		}
	})
	return e
}

// merge unions the projects of es (in index order): each version once, as first listed, with
// the earliest upload time; requires-python is kept from the first index listing the
// version. An index without the project is skipped unless all are; any other error fails.
func merge(es []*entry) (project, error) {
	var found []*entry
	for _, e := range es {
		switch {
		case e.err == nil:
			found = append(found, e)
		case !errors.Is(e.err, errNotFound):
			return project{}, e.err
		}
	}
	switch len(found) {
	case 0:
		return project{}, es[0].err
	case 1:
		return found[0].project, nil
	}
	p := project{times: map[string]time.Time{}, python: map[string]string{}}
	listed := map[string]bool{}
	for _, e := range found {
		p.repo = cmp.Or(p.repo, e.repo)
		for _, r := range e.rels {
			k := versionKey(r.Version)
			if !listed[k] {
				listed[k] = true
				p.rels = append(p.rels, r)
				if py, ok := e.python[k]; ok {
					p.python[k] = py
				}
			}
		}
		for k, t := range e.times {
			if cur, ok := p.times[k]; !ok || t.Before(cur) {
				p.times[k] = t
			}
		}
	}
	return p, nil
}

// filterPython drops the releases of p (of project name) whose requires-python does not
// admit every Python version of the project constraint python. An invalid constraint
// filters nothing; neither does an invalid requires-python.
func (s *Source) filterPython(ctx context.Context, p project, name, python string) project {
	if _, err := versioning.PEP440Subset(python, ""); err != nil {
		slog.WarnContext(ctx, "pypi: ignoring the project's Python constraint", "name", name, "err", err)
		return p
	}
	var kept []datasource.Release
	var dropped []string
	for i, r := range p.rels {
		if req, ok := p.python[versionKey(r.Version)]; !ok || s.admitted(python, req) {
			if kept != nil {
				kept = append(kept, r)
			}
			continue
		}
		if kept == nil {
			kept = append(make([]datasource.Release, 0, len(p.rels)-1), p.rels[:i]...)
		}
		dropped = append(dropped, r.Version)
	}
	if dropped == nil {
		return p
	}
	slog.DebugContext(ctx, "pypi releases dropped by requires-python", "name", name, "python", python, "versions", dropped)
	p.rels = kept
	return p
}

// admitted reports whether requires-python req admits every Python version of the project
// constraint python, memoized; true when req is invalid.
func (s *Source) admitted(python, req string) bool {
	key := [2]string{python, req}
	s.mu.Lock()
	ok, done := s.admits[key]
	s.mu.Unlock()
	if done {
		return ok
	}
	ok, err := versioning.PEP440Subset(python, req)
	ok = ok || err != nil
	s.mu.Lock()
	s.admits[key] = ok
	s.mu.Unlock()
	return ok
}

// indexes returns d's index (see index) followed by its extra indexes, d.ExtraSources and
// then the space-separated PIP_EXTRA_INDEX_URL, without duplicates.
func (s *Source) indexes(d dep.Dep) ([]string, error) {
	idx, err := s.index(d)
	if err != nil {
		return nil, err
	}
	idxs := []string{idx}
	extra := strings.Fields(s.getenv("PIP_EXTRA_INDEX_URL"))
	for _, raw := range slices.Concat(d.ExtraSources, extra) {
		x, err := indexURL(raw, d.Name)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(idxs, x) {
			idxs = append(idxs, x)
		}
	}
	return idxs, nil
}

// index returns the primary simple index URL of d with a trailing slash: d.Source, else
// PIP_INDEX_URL; "" selects the PyPI JSON API (no index configured, or PyPI's simple index).
func (s *Source) index(d dep.Dep) (string, error) {
	return indexURL(cmp.Or(d.Source, s.getenv("PIP_INDEX_URL")), d.Name)
}

// indexURL validates the simple index URL raw (of a dep named name, for errors) and returns
// it with a trailing slash, or "" for PyPI's simple index or an empty raw.
func indexURL(raw, name string) (string, error) {
	idx := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if idx == "" || idx == pypiSimple {
		return "", nil
	}
	// The URL is not part of the error: it may hold credentials and does not parse to redact them.
	if u, err := url.Parse(idx); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("pypi: invalid index URL for package %s", name)
	}
	return idx + "/", nil
}

// normalizedIs reports whether pyreq.Normalize(name) == norm without allocating.
func normalizedIs(name, norm string) bool {
	j := 0
	next := func(c byte) bool {
		if j >= len(norm) || norm[j] != c {
			return false
		}
		j++
		return true
	}
	sep := false
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '-' || c == '_' || c == '.':
			sep = true
			continue
		case 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		}
		if sep && j > 0 && !next('-') || !next(c) {
			return false
		}
		sep = false
	}
	return j == len(norm)
}

// versionKey is the PEP 440 normalized form of v, or v lowercased when it is no PEP 440
// version; filenames and the `versions` list are matched by it.
func versionKey(v string) string {
	if n, ok := versioning.NormalizePEP440(v); ok {
		return n
	}
	return strings.ToLower(v)
}

// fetchJSON fetches the PyPI JSON API document of the project normalized name (written is
// the dep name, for errors).
func (s *Source) fetchJSON(ctx context.Context, written, name string) (p project, err error) {
	target := s.base + "/pypi/" + url.PathEscape(name) + "/json"
	start, yanked := time.Now(), 0
	defer func() {
		slog.DebugContext(ctx, "pypi project fetched", "name", name, "index", redact(s.base), "api", "json",
			"versions", len(p.rels), "yanked", yanked, "repository", p.repo, "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return project{}, err
	}
	defer release()
	resp, err := s.get(ctx, target, written, "application/json")
	if err != nil {
		return project{}, err
	}
	defer closeBody(resp)
	p, yanked, err = decodeJSON(resp.Body)
	if err != nil {
		return project{}, fmt.Errorf("pypi %s: malformed response: %w", redact(target), err)
	}
	return p, nil
}

// fetchSimple fetches the simple API page of the project normalized name from idx, as JSON
// (PEP 691) or HTML (PEP 503), whichever the index serves; an index answering 406 to the
// negotiating Accept header is asked for text/html alone.
func (s *Source) fetchSimple(ctx context.Context, idx, written, name string) (p project, err error) {
	target := idx + url.PathEscape(name) + "/"
	start, yanked, api := time.Now(), 0, "simple"
	defer func() {
		slog.DebugContext(ctx, "pypi project fetched", "name", name, "index", redact(idx), "api", api,
			"versions", len(p.rels), "yanked", yanked, "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return project{}, err
	}
	defer release()
	resp, err := s.getSimple(ctx, target, written)
	if errors.Is(err, errNotAcceptable) {
		return project{}, fmt.Errorf("%s serves neither the JSON nor the HTML simple API", redact(idx))
	}
	if err != nil {
		return project{}, err
	}
	defer closeBody(resp)
	ct := resp.Header.Get("Content-Type")
	switch mt, _, _ := mime.ParseMediaType(ct); mt {
	case simpleJSON:
		api = "simple-json"
		p, yanked, err = decodeSimple(resp.Body, name)
	case simpleHTML, "text/html":
		api = "simple-html"
		p, yanked, err = decodeHTML(resp.Body, resp.Request.URL, name)
	default:
		return project{}, fmt.Errorf("pypi %s: unsupported content type %q (want %s or HTML)", redact(target), ct, simpleJSON)
	}
	if err != nil {
		return project{}, fmt.Errorf("pypi %s: malformed response: %w", redact(target), err)
	}
	return p, nil
}

// getSimple gets the simple API page target as JSON or HTML (simpleAccept), asking for
// text/html alone when the index answers 406 to the negotiating Accept header.
func (s *Source) getSimple(ctx context.Context, target, written string) (*http.Response, error) {
	resp, err := s.get(ctx, target, written, simpleAccept)
	if !errors.Is(err, errNotAcceptable) {
		return resp, err
	}
	return s.get(ctx, target, written, "text/html")
}

// get sends a GET for the project document target and fails on a non-2xx status. Userinfo of
// target is sent as basic auth by net/http.
func (s *Source) get(ctx context.Context, target, name, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok { // its message holds the URL with the password
			err = ue.Err
		}
		return nil, fmt.Errorf("pypi %s: %w", redact(target), err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req) // a *url.Error carries the URL without its password
	if err != nil {
		return nil, fmt.Errorf("pypi %s: %w", redact(target), err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		closeBody(resp)
		return nil, fmt.Errorf("package %s %w", name, errNotFound)
	case resp.StatusCode == http.StatusNotAcceptable:
		closeBody(resp)
		return nil, errNotAcceptable
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		closeBody(resp)
		return nil, fmt.Errorf("pypi %s: HTTP %d", redact(target), resp.StatusCode)
	}
	return resp, nil
}

// redact returns raw with the password of its userinfo masked.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	return u.Redacted()
}

// decodeJSON streams a PyPI JSON API document. `releases` maps each version to its files; one
// file object is buffered at a time, which keeps projects with thousands of releases (boto3)
// cheap. A version is dropped when it has no files or all of them are yanked.
// Duplicate-name checking is off: it keeps a name set per object.
func decodeJSON(r io.Reader) (p project, yanked int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelim(dec, '{'); err != nil {
		return project{}, 0, err
	}
	p.times, p.python = map[string]time.Time{}, map[string]string{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return project{}, 0, err
		}
		switch tok.String() {
		case "info":
			var info struct {
				HomePage    string            `json:"home_page"`
				ProjectURLs map[string]string `json:"project_urls"`
			}
			if err := json.UnmarshalDecode(dec, &info); err != nil {
				return project{}, 0, err
			}
			p.repo = sourceRepo(info.ProjectURLs, info.HomePage)
		case "releases":
			if yanked, err = decodeReleases(dec, &p); err != nil {
				return project{}, 0, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return project{}, 0, err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return project{}, 0, err
	}
	return p, yanked, nil
}

// decodeReleases reads the `releases` object (version -> files) into p.
func decodeReleases(dec *jsontext.Decoder, p *project) (yanked int, err error) {
	if err := expectDelim(dec, '{'); err != nil {
		return 0, err
	}
	var f releaseFile
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, err
		}
		v := tok.String() // tokens are only valid until the next read
		if err := expectDelim(dec, '['); err != nil {
			return 0, err
		}
		files, allYanked, first, py := 0, true, time.Time{}, pyReq{}
		for dec.PeekKind() != ']' {
			if err := decodeFile(dec, &f); err != nil {
				return 0, err
			}
			files++
			allYanked = allYanked && f.Yanked
			first = earliest(first, f.Upload)
			if !f.Yanked {
				py.add(f.RequiresPython)
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return 0, err
		}
		switch {
		case files == 0:
			continue
		case allYanked:
			yanked++
			continue
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
		k := versionKey(v)
		if !first.IsZero() {
			p.times[k] = first
		}
		if spec, ok := py.get(); ok {
			p.python[k] = spec
		}
	}
	_, err = dec.ReadToken()
	return yanked, err
}

// releaseFile is what deps keeps of one file of a release (unnamed, as in unmarshal errors).
type releaseFile = struct {
	Yanked         bool   `json:"yanked"`
	Upload         string `json:"upload_time_iso_8601"`
	RequiresPython string `json:"requires_python"`
}

// decodeFile reads one file object of a release into f, which it resets first; f is reused so
// that unmarshaling its fields does not allocate. The kept members are unmarshaled one by one
// and the others consumed with ReadValue: unmarshaling the whole object into a struct looks up
// every unknown name (also case-folded) and skips nested values (`digests`) token by token.
func decodeFile(dec *jsontext.Decoder, f *releaseFile) error {
	*f = releaseFile{}
	if dec.PeekKind() != '{' {
		return json.UnmarshalDecode(dec, f) // null, or the unmarshal error
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return err
		}
		switch name.String() {
		case "yanked":
			err = json.UnmarshalDecode(dec, &f.Yanked)
		case "upload_time_iso_8601":
			err = json.UnmarshalDecode(dec, &f.Upload)
		case "requires_python":
			err = json.UnmarshalDecode(dec, &f.RequiresPython)
		default:
			_, err = dec.ReadValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// pyReq accumulates the requires-python of a version's files that are not yanked; it has a
// value when they all share one.
type pyReq struct {
	spec        string
	seen, mixed bool
}

func (r *pyReq) add(spec string) {
	switch spec = strings.TrimSpace(spec); {
	case spec == "":
		r.mixed = true
	case !r.seen:
		r.spec, r.seen = spec, true
	case r.spec != spec:
		r.mixed = true
	}
}

func (r pyReq) get() (string, bool) { return r.spec, r.seen && !r.mixed }

// simpleFile is a file listed on a simple index page. decodeSimple fills Filename, Yanked,
// RequiresPython and Upload; decodeHTMLFiles all fields it finds.
type simpleFile struct {
	Filename       string
	SHA256         string // from the URL's `#sha256=` fragment
	RequiresPython string
	Upload         string // RFC 3339
	Yanked         bool
}

// simpleVersion collects the files of one version on a simple index page.
type simpleVersion struct {
	raw           string // as in the first filename
	files, yanked int
	first         time.Time
	py            pyReq
}

// simplePage maps the files of a simple index page of the project name to versions.
type simplePage struct {
	name  string
	byKey map[string]*simpleVersion
	order []string // keys in order of appearance
}

func newSimplePage(name string) *simplePage {
	return &simplePage{name: name, byKey: map[string]*simpleVersion{}}
}

func (pg *simplePage) add(f simpleFile) {
	raw, ok := fileVersion(f.Filename, pg.name)
	if !ok {
		return
	}
	k := versionKey(raw)
	sv := pg.byKey[k]
	if sv == nil {
		sv = &simpleVersion{raw: raw}
		pg.byKey[k] = sv
		pg.order = append(pg.order, k)
	}
	sv.files++
	if f.Yanked {
		sv.yanked++
	} else {
		sv.py.add(f.RequiresPython)
	}
	sv.first = earliest(sv.first, f.Upload)
}

// project returns the versions (nil: those of the filenames in order of appearance) that
// have a file that is not yanked, and the number of yanked ones.
func (pg *simplePage) project(versions []string) (p project, yanked int) {
	if versions == nil {
		versions = make([]string, len(pg.order))
		for i, k := range pg.order {
			versions[i] = pg.byKey[k].raw
		}
	}
	p.times, p.python = map[string]time.Time{}, map[string]string{}
	for _, v := range versions {
		k := versionKey(v)
		sv := pg.byKey[k]
		switch {
		case sv == nil:
			continue
		case sv.yanked == sv.files:
			yanked++
			continue
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
		if !sv.first.IsZero() {
			p.times[k] = sv.first
		}
		if spec, ok := sv.py.get(); ok {
			p.python[k] = spec
		}
	}
	return p, yanked
}

// decodeSimple streams a JSON simple API page of the project normalized name. Files are mapped
// to versions by filename. With a PEP 700 `versions` list the versions are those, in its
// order; otherwise the versions of the filenames in order of appearance. A version is dropped
// when it has no files or all of them are yanked (`yanked` true or a reason).
func decodeSimple(r io.Reader, name string) (p project, yanked int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelim(dec, '{'); err != nil {
		return project{}, 0, err
	}
	pg := newSimplePage(name)
	var versions []string
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return project{}, 0, err
		}
		switch tok.String() {
		case "files":
			if err := expectDelim(dec, '['); err != nil {
				return project{}, 0, err
			}
			for dec.PeekKind() != ']' {
				var f struct {
					Filename       string         `json:"filename"`
					Yanked         jsontext.Value `json:"yanked"`
					Upload         string         `json:"upload-time"`
					RequiresPython string         `json:"requires-python"`
				}
				if err := json.UnmarshalDecode(dec, &f); err != nil {
					return project{}, 0, err
				}
				k := f.Yanked.Kind()
				pg.add(simpleFile{Filename: f.Filename, Upload: f.Upload, RequiresPython: f.RequiresPython, Yanked: k == 't' || k == '"'})
			}
			if _, err := dec.ReadToken(); err != nil {
				return project{}, 0, err
			}
		case "versions":
			versions = []string{}
			if err := json.UnmarshalDecode(dec, &versions); err != nil {
				return project{}, 0, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return project{}, 0, err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return project{}, 0, err
	}
	p, yanked = pg.project(versions)
	return p, yanked, nil
}

// decodeHTML reads an HTML simple API page (PEP 503) of the project normalized name served at
// base. Versions are those of the filenames in order of appearance; a version is dropped when
// all of its files are yanked (PEP 592 `data-yanked`).
func decodeHTML(r io.Reader, base *url.URL, name string) (p project, yanked int, err error) {
	pg := newSimplePage(name)
	if err := decodeHTMLFiles(r, base, pg.add); err != nil {
		return project{}, 0, err
	}
	p, yanked = pg.project(nil)
	return p, yanked, nil
}

// decodeHTMLFiles calls fn for each anchor of an HTML simple API page served at base (nil:
// href is left relative): the filename is the anchor text (else the last path segment of
// href resolved against base), SHA256 comes from href, Yanked from the presence of `data-yanked`,
// RequiresPython from `data-requires-python` and Upload from the non-standard
// `data-upload-time` (download.pytorch.org). Entities are decoded.
func decodeHTMLFiles(r io.Reader, base *url.URL, fn func(simpleFile)) error {
	var sb strings.Builder
	if _, err := io.Copy(&sb, r); err != nil {
		return err
	}
	s := sb.String()
	for i := 0; ; {
		j := strings.IndexByte(s[i:], '<')
		if j < 0 {
			return nil
		}
		i += j + 1
		if i+1 >= len(s) || s[i]|0x20 != 'a' || !htmlSpace(s[i+1]) && s[i+1] != '>' {
			continue
		}
		var f simpleFile
		var href string
		i = htmlAttrs(s, i+1, func(k, v string) {
			switch k {
			case "href":
				href = v
			case "data-yanked":
				f.Yanked = true
			case "data-requires-python":
				f.RequiresPython = v
			case "data-upload-time":
				f.Upload = v
			}
		})
		end := indexFold(s[i:], "</a")
		if end < 0 {
			end = len(s) - i
		}
		f.Filename = strings.TrimSpace(html.UnescapeString(s[i : i+end]))
		i += end
		if u, err := url.Parse(href); err == nil {
			if v, ok := strings.CutPrefix(u.Fragment, "sha256="); ok {
				f.SHA256 = v
			}
			if f.Filename == "" {
				if base != nil {
					u = base.ResolveReference(u)
				}
				f.Filename = path.Base(u.Path)
			}
		}
		fn(f)
	}
}

// htmlAttrs reads the attributes of the tag whose name ends at i, calling fn with each
// lowercased name and its entity-decoded value ("" without one), and returns the offset after
// the closing `>`.
func htmlAttrs(s string, i int, fn func(k, v string)) int {
	for i < len(s) {
		for i < len(s) && (htmlSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '>' {
			return i + 1
		}
		j := i
		for j < len(s) && !htmlSpace(s[j]) && s[j] != '=' && s[j] != '>' && s[j] != '/' {
			j++
		}
		k := strings.ToLower(s[i:j])
		for j < len(s) && htmlSpace(s[j]) {
			j++
		}
		if j >= len(s) || s[j] != '=' {
			fn(k, "")
			i = j
			continue
		}
		j++
		for j < len(s) && htmlSpace(s[j]) {
			j++
		}
		var v string
		switch {
		case j < len(s) && (s[j] == '"' || s[j] == '\''):
			e := strings.IndexByte(s[j+1:], s[j])
			if e < 0 {
				e = len(s) - j - 1
			}
			v, i = s[j+1:j+1+e], min(j+2+e, len(s))
		default:
			e := j
			for e < len(s) && !htmlSpace(s[e]) && s[e] != '>' {
				e++
			}
			v, i = s[j:e], e
		}
		fn(k, html.UnescapeString(v))
	}
	return len(s)
}

func htmlSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// indexFold is strings.Index for an ASCII lower-case sub, ignoring the case of s.
func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// sdistExts are the source distribution extensions, longest first.
var sdistExts = []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".zip", ".tar"}

// fileVersion returns the version in a distribution filename of the project normalized name:
// the second dash-separated field of wheels (`{name}-{ver}(-{build})?-{py}-{abi}-{plat}.whl`)
// and eggs (`{name}-{ver}(-{py}(-{plat})?)?.egg`), whose names have no dashes; for sdists
// (`{name}-{ver}.tar.gz`, where legacy names keep their dashes) the part after the longest
// prefix that normalizes to name, else after the last dash. Other files (`.exe`, `.msi`,
// `.rpm`) have no version.
func fileVersion(filename, name string) (string, bool) {
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".whl") || strings.HasSuffix(lower, ".egg") {
		fields := strings.SplitN(filename[:len(filename)-4], "-", 3)
		if len(fields) < 2 || fields[1] == "" {
			return "", false
		}
		return fields[1], true
	}
	i := slices.IndexFunc(sdistExts, func(ext string) bool { return strings.HasSuffix(lower, ext) })
	if i < 0 {
		return "", false
	}
	base := filename[:len(filename)-len(sdistExts[i])]
	dash := strings.LastIndexByte(base, '-')
	for j := len(base) - 1; j > 0; j-- {
		if base[j] == '-' && normalizedIs(base[:j], name) {
			dash = j
			break
		}
	}
	if dash < 0 || dash+1 == len(base) {
		return "", false
	}
	return base[dash+1:], true
}

// earliest returns the earlier of cur and the RFC 3339 time s; unparsable times are ignored.
func earliest(cur time.Time, s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || (!cur.IsZero() && !t.Before(cur)) {
		return cur
	}
	return t
}

// repoKeys are the project URL labels naming the source repository, by preference, as
// compared by urlLabel. The first three may be on any host, the others only on a forge.
var repoKeys = []string{"source", "sourcecode", "repository", "code", "github", "homepage"}

// forges are the hosts whose URLs are taken as repositories from labels that may point
// elsewhere (a homepage).
var forges = []string{"github.com", "gitlab.com", "codeberg.org"}

// sourceRepo picks the repository URL from project_urls (see repoKeys), else home_page when it
// is on a forge; "" when none qualifies.
func sourceRepo(urls map[string]string, home string) string {
	labels := make(map[string]string, len(urls))
	for _, k := range slices.Sorted(maps.Keys(urls)) {
		if l := urlLabel(k); labels[l] == "" {
			labels[l] = strings.TrimSpace(urls[k])
		}
	}
	for i, k := range repoKeys {
		if u := labels[k]; u != "" && (i < 3 || onForge(u)) {
			return u
		}
	}
	if home = strings.TrimSpace(home); onForge(home) {
		return home
	}
	return ""
}

// urlLabel folds a project URL label for comparison: lowercase without spaces, dashes and
// underscores (`Source Code` → `sourcecode`).
func urlLabel(k string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '_' {
			return -1
		}
		return r
	}, strings.ToLower(k))
}

// onForge reports whether raw is an http(s) URL on one of forges.
func onForge(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return slices.Contains(forges, strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
}

func expectDelim(dec *jsontext.Decoder, d jsontext.Kind) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != d {
		return fmt.Errorf("expected %v, got %v", d, tok.Kind())
	}
	return nil
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
