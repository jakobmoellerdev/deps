// Package deb lists the versions of Debian and Ubuntu binary packages from the package
// indexes of an APT repository, as Renovate's deb datasource does.
//
// A registry URL is the repository's base URL with the query parameters `suite` (Renovate's
// deprecated `release` is an alias), `components` (comma-separated) and `binaryArch`:
// `https://deb.debian.org/debian?suite=bookworm&components=main,contrib&binaryArch=amd64`.
// Each component's index `<base>/dists/<suite>/<component>/binary-<arch>/Packages.gz` (else
// `Packages.xz`, else the uncompressed `Packages`) is read once per run and stream-scanned;
// only the packages some dep asks for are kept (datasource.Planner). A dep's releases are the
// versions of its package in every component of every registry URL of its source, merged.
package deb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ulikunitz/xz"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "deb"

// Versioning is the default versioning of deb deps.
const Versioning = "deb"

// DefaultRegistry is the registry URL of deps without one (Renovate's default).
const DefaultRegistry = "https://deb.debian.org/debian?suite=stable&components=main,contrib,non-free&binaryArch=amd64"

// indexFiles are the index file names tried in order; a 404 moves to the next.
var indexFiles = []string{"Packages.gz", "Packages.xz", "Packages"}

// ParseRegistry returns the component index URLs (`<base>/dists/<suite>/<component>/binary-<arch>`,
// without the index file name) of registry URL raw, in the order of its components. The
// query parameters other than suite, release, components and binaryArch stay on the URL and
// are appended to every index URL.
func ParseRegistry(raw string) ([]string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("invalid deb registry URL %q: want an http(s) URL", raw)
	}
	q := u.Query()
	suite := q.Get("suite")
	if suite == "" {
		suite = q.Get("release")
	}
	components, arch := q.Get("components"), q.Get("binaryArch")
	switch {
	case suite == "":
		return nil, fmt.Errorf("invalid deb registry URL %q: missing query parameter suite", raw)
	case components == "":
		return nil, fmt.Errorf("invalid deb registry URL %q: missing query parameter components", raw)
	case arch == "":
		return nil, fmt.Errorf("invalid deb registry URL %q: missing query parameter binaryArch", raw)
	}
	for _, k := range []string{"suite", "release", "components", "binaryArch"} {
		q.Del(k)
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	base := strings.TrimRight(u.String(), "/")
	var query string
	if len(q) > 0 {
		query = "?" + q.Encode()
	}
	var out []string
	for c := range strings.SplitSeq(components, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, base+"/dists/"+suite+"/"+c+"/binary-"+arch+query)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid deb registry URL %q: missing query parameter components", raw)
	}
	return out, nil
}

// registries are the registry URLs of d: the whitespace-separated URLs of d.Source, else
// DefaultRegistry.
func registries(d dep.Dep) []string {
	if rs := strings.Fields(d.Source); len(rs) > 0 {
		return rs
	}
	return []string{DefaultRegistry}
}

// Source is the deb datasource.
type Source struct {
	getter httpx.Getter
	auth   *httpx.Auth

	mu      sync.Mutex
	wanted  map[string]map[string]bool      // component URL → package names asked for
	indexes datasource.Memo[string, *index] // by component URL
	late    datasource.Memo[string, []string]
}

// index is one scan of a component's package index.
type index struct {
	wanted map[string]bool
	pkgs   map[string][]string // package → versions in index order, each once
}

// New returns the deb datasource. sem bounds concurrent requests (nil = unbounded); auth
// supplies Basic credentials for the forge's host (a repository proxied by it); credentials in
// a registry URL's userinfo are sent as Basic auth too. Index files are not size-capped: a
// distribution's Packages file runs to tens of MB.
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth) *Source {
	s := &Source{getter: httpx.Getter{Client: client, Sem: sem, MaxBody: -1}, auth: auth, wanted: map[string]map[string]bool{}}
	s.getter.Prepare = s.authorize
	return s
}

// Plan records the packages each component index will be asked for, so one scan keeps all of
// them (datasource.Planner).
func (s *Source) Plan(deps []dep.Dep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range deps {
		for _, r := range registries(d) {
			comps, err := ParseRegistry(r)
			if err != nil {
				continue
			}
			for _, c := range comps {
				s.want(c, d.Name)
			}
		}
	}
}

// want registers pkg for component URL comp; s.mu must be held.
func (s *Source) want(comp, pkg string) {
	w := s.wanted[comp]
	if w == nil {
		w = map[string]bool{}
		s.wanted[comp] = w
	}
	w[pkg] = true
}

// Releases lists the versions of package d.Name in the components of d's registry URLs
// (datasource.Datasource), merged in registry and component order, each once. A component
// whose index fails is skipped when another one lists the package, as in Renovate. The
// indexes still missing are scanned concurrently (see prefetch), so a registry's components
// cost the longest download instead of their sum.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	var comps []string
	for _, r := range registries(d) {
		cs, err := ParseRegistry(r)
		if err != nil {
			return nil, err
		}
		comps = append(comps, cs...)
	}
	if len(comps) > 1 {
		s.prefetch(ctx, comps, d.Name)
	}
	var out []datasource.Release
	var errs []error
	seen := map[string]bool{}
	for _, c := range comps {
		vs, err := s.lookup(ctx, c, d.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, v := range vs {
			if !seen[v] {
				seen[v] = true
				out = append(out, datasource.Release{Version: v})
			}
		}
	}
	switch {
	case len(out) > 0:
		for _, err := range errs {
			slog.DebugContext(ctx, "deb component skipped", "package", d.Name, "err", err)
		}
		return out, nil
	case len(errs) > 0:
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("deb package %s not found in %s", d.Name, strings.Join(registries(d), " "))
}

// prefetch starts the scan of the component indexes of comps that no caller has shared yet,
// one goroutine each (the request pool still bounds them), and waits for them: the default
// Debian `main,contrib,non-free` and Ubuntu's `main,universe,multiverse,restricted` otherwise
// download their indexes one after another, and universe's Packages.gz alone is tens of MB.
// The scans are the same ones the caller's loop would run, memoised under the same keys, so
// its results and errors do not change; a single missing index is left to the caller.
func (s *Source) prefetch(ctx context.Context, comps []string, pkg string) {
	n := 0
	for _, c := range comps {
		if _, ok := s.indexes.Peek(c); !ok {
			n++
		}
	}
	if n < 2 {
		return
	}
	var wg sync.WaitGroup
	for _, c := range comps {
		if _, ok := s.indexes.Peek(c); ok {
			continue
		}
		wg.Go(func() { _, _ = s.lookup(ctx, c, pkg) })
	}
	wg.Wait()
}

// Digest is unsupported: package versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup returns the versions of pkg in the component index comp; nil when it is not listed.
func (s *Source) lookup(ctx context.Context, comp, pkg string) ([]string, error) {
	s.mu.Lock()
	s.want(comp, pkg)
	s.mu.Unlock()
	ix, err := s.indexes.Do(ctx, comp, func() (*index, error) {
		s.mu.Lock()
		ix := &index{wanted: maps.Clone(s.wanted[comp])}
		s.mu.Unlock()
		var err error
		ix.pkgs, err = s.scan(ctx, comp, ix.wanted)
		return ix, err
	})
	if ix.wanted[pkg] {
		return ix.pkgs[pkg], err
	}
	// Asked for after the scan started: scan again for this package alone.
	return s.late.Do(ctx, comp+"\x00"+pkg, func() ([]string, error) {
		pkgs, err := s.scan(ctx, comp, map[string]bool{pkg: true})
		return pkgs[pkg], err
	})
}

// scan reads the first index file of comp that exists and returns the versions of the wanted
// packages it lists.
func (s *Source) scan(ctx context.Context, comp string, wanted map[string]bool) (map[string][]string, error) {
	base, query, _ := strings.Cut(comp, "?")
	if query != "" {
		query = "?" + query
	}
	for _, f := range indexFiles {
		target := base + "/" + f + query
		pkgs, err := s.fetch(ctx, target, f, wanted)
		if errors.Is(err, errNotFound) {
			continue
		}
		slog.DebugContext(ctx, "deb index scanned", "url", httpx.Redact(target), "wanted", len(wanted), "found", len(pkgs), "err", err)
		return pkgs, err
	}
	return nil, fmt.Errorf("deb index %s: no %s", httpx.Redact(base), strings.Join(indexFiles, ", "))
}

var errNotFound = errors.New("not found")

// fetch GETs one index file and scans it, decompressed by the file's extension.
func (s *Source) fetch(ctx context.Context, target, file string, wanted map[string]bool) (map[string][]string, error) {
	var pkgs map[string][]string
	err := s.getter.Stream(ctx, target, func(r io.Reader) error {
		switch {
		case strings.HasSuffix(file, ".gz"):
			zr, err := gzip.NewReader(r)
			if err != nil {
				return err
			}
			defer zr.Close()
			r = zr
		case strings.HasSuffix(file, ".xz"):
			zr, err := xz.NewReader(bufio.NewReaderSize(r, 64<<10))
			if err != nil {
				return err
			}
			r = zr
		}
		found, err := scanIndex(r, wanted)
		pkgs = found
		return err
	})
	switch {
	case errors.Is(err, httpx.ErrNotFound):
		return nil, errNotFound
	case err != nil:
		return nil, err
	}
	return pkgs, nil
}

// authorize sends the forge's Basic credentials for req's host, unless its URL has userinfo.
func (s *Source) authorize(req *http.Request) error {
	if user, pass, ok := s.auth.Static(req.URL.Hostname()); ok && req.URL.User == nil {
		httpx.SetAuth(req, user, pass)
	}
	return nil
}

var (
	fieldPackage = []byte("Package:")
	fieldVersion = []byte("Version:")
	longLine     = []byte(" ") // stands in for a line longer than the read buffer
)

// scanIndex reads a Packages file (deb822 paragraphs separated by blank lines) and returns the
// versions of the wanted packages: each paragraph with a Package and a Version field is one
// release, in file order, each version once per package. Only wanted names and their versions
// are copied out of the read buffer.
func scanIndex(r io.Reader, wanted map[string]bool) (map[string][]string, error) {
	out := map[string][]string{}
	br := bufio.NewReaderSize(r, 64<<10)
	var pkg string // wanted package of the current paragraph; "" = none or not wanted
	var version []byte
	var skip bool // the paragraph's Package is not wanted
	flush := func() {
		if pkg != "" && len(version) > 0 {
			v := string(version)
			if !slices.Contains(out[pkg], v) {
				out[pkg] = append(out[pkg], v)
			}
		}
		pkg, version, skip = "", version[:0], false
	}
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A line longer than the buffer (a long Description) is never a field we read:
			// drop the rest of it and treat it as a continuation line.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			line = longLine
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			flush()
		case skip || line[0] == ' ' || line[0] == '\t':
		case bytes.HasPrefix(line, fieldPackage):
			name := bytes.TrimSpace(line[len(fieldPackage):])
			if wanted[string(name)] {
				pkg = string(name)
			} else {
				skip = true
			}
		case bytes.HasPrefix(line, fieldVersion):
			version = append(version[:0], bytes.TrimSpace(line[len(fieldVersion):])...)
		}
		if errors.Is(err, io.EOF) {
			flush()
			return out, nil
		}
	}
}
