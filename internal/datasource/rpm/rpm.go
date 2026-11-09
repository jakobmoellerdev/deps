// Package rpm lists the versions of RPM packages from yum/dnf repositories (Renovate's `rpm`
// datasource): the repository's `repodata/repomd.xml` names its primary metadata file, which
// lists every package with its version and release. Each repository is read once per run and
// stream-scanned; only the packages some dep asks for are kept (datasource.Planner), and a
// package asked for after the scan started costs one more scan.
package rpm

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "rpm"

// Versioning is the versioning scheme of RPM versions (`[epoch:]version-release`).
const Versioning = "rpm"

// Source is the rpm datasource.
type Source struct {
	getter httpx.Getter

	mu      sync.Mutex
	wanted  map[string]map[string]bool     // repository URL (without /repodata) → package names asked for
	repos   datasource.Memo[string, *repo] // by repository URL (without /repodata)
	late    datasource.Memo[string, *pkg]  // by repository URL and package asked for after its scan
	lookups datasource.Memo[string, *pkg]  // by dep source and package name
}

// repo is the index of one repository's primary metadata: the releases of the wanted packages.
type repo struct {
	wanted map[string]bool
	pkgs   map[string]*pkg
}

// pkg is what a repository lists for a package name: each version once (the builds of every
// architecture merged) and the earliest time a build of it was added to the repository.
type pkg struct {
	rels  []datasource.Release
	times []time.Time // parallel to rels; zero = unknown
}

// New returns the rpm datasource. sem bounds concurrent requests (nil = unbounded). Credentials
// in a repository URL (`https://user:token@host/…`) are sent as basic auth and redacted in
// errors. The primary metadata is not size-capped: a distribution's runs to hundreds of MB.
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem, MaxBody: -1}, wanted: map[string]map[string]bool{}}
}

// Plan records the packages each repository will be asked for, so one scan keeps all of them
// (datasource.Planner).
func (s *Source) Plan(deps []dep.Dep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range deps {
		for u := range strings.FieldsSeq(d.Source) {
			s.want(repoBase(u), d.Name)
		}
	}
}

// want registers name for the repository base; s.mu must be held.
func (s *Source) want(base, name string) {
	w := s.wanted[base]
	if w == nil {
		w = map[string]bool{}
		s.wanted[base] = w
	}
	w[name] = true
}

// Releases lists the versions of d.Name in the repositories of d.Source (whitespace-separated
// repository URLs, Renovate's registryUrls, each the repository root or its `repodata/`
// directory), merged as Renovate's `merge` registry strategy does: a distribution's release
// and updates repositories together. A version is `[<epoch>:]<ver>-<rel>` (`<ver>` when the
// package has no release; the epoch when it is not 0), so the rpm versioning orders an epoch
// bump as dnf does; it writes candidates in the file's form (without the epoch when the file
// has none, as Renovate writes them). The result must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the earliest `<time file>` (when the build was
// added to the repository) of the version's builds. A version without an epoch also matches
// a listed version with one (the rpm versioning writes candidates without it).
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	var t time.Time
	for i, r := range p.rels {
		if r.Version == version {
			return p.times[i], nil
		}
		if _, rest, ok := strings.Cut(r.Version, ":"); ok && rest == version && t.IsZero() {
			t = p.times[i]
		}
	}
	return t, nil
}

// Digest is unsupported: RPM versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup returns the merged listing of d's package, merged once per dep source and package:
// Releases and the ReleaseTime of every candidate share it.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	return s.lookups.Do(ctx, d.Source+"\x00"+d.Name, func() (*pkg, error) { return s.merge(ctx, d) })
}

// merge merges the listings of d's package in every repository of d.Source.
func (s *Source) merge(ctx context.Context, d dep.Dep) (*pkg, error) {
	urls := strings.Fields(d.Source)
	if len(urls) == 0 {
		return nil, fmt.Errorf("rpm package %s: no repository: set registryUrl to the repository's repodata/ URL", d.Name)
	}
	var found []*pkg
	for _, u := range urls {
		p, err := s.find(ctx, repoBase(u), d.Name)
		if err != nil {
			return nil, err
		}
		if p != nil {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		redacted := make([]string, len(urls))
		for i, u := range urls {
			redacted[i] = httpx.Redact(u)
		}
		return nil, fmt.Errorf("rpm package %s not found in %s", d.Name, strings.Join(redacted, ", "))
	case 1:
		return found[0], nil
	}
	merged := &pkg{}
	for _, p := range found {
		for i, r := range p.rels {
			merged.add(r.Version, p.times[i])
		}
	}
	return merged, nil
}

// find returns the listing of name in the repository base; nil when the repository does not
// list it.
func (s *Source) find(ctx context.Context, base, name string) (*pkg, error) {
	s.mu.Lock()
	s.want(base, name)
	s.mu.Unlock()
	r, err := s.repos.Do(ctx, base, func() (*repo, error) {
		s.mu.Lock()
		wanted := maps.Clone(s.wanted[base])
		s.mu.Unlock()
		return s.fetch(ctx, base, wanted)
	})
	if err != nil {
		return nil, err
	}
	if r.wanted[name] {
		return r.pkgs[name], nil
	}
	// Asked for after the scan started: scan again for this package alone.
	return s.late.Do(ctx, base+"\x00"+name, func() (*pkg, error) {
		r, err := s.fetch(ctx, base, map[string]bool{name: true})
		if err != nil {
			return nil, err
		}
		return r.pkgs[name], nil
	})
}

// add records version with the build time t, keeping the earliest known time of a version.
func (p *pkg) add(version string, t time.Time) {
	for i, r := range p.rels {
		if r.Version == version {
			if !t.IsZero() && (p.times[i].IsZero() || t.Before(p.times[i])) {
				p.times[i] = t
			}
			return
		}
	}
	p.rels = append(p.rels, datasource.Release{Version: version})
	p.times = append(p.times, t)
}

// repoBase returns the repository root of a registry URL: without trailing slashes and
// without a final `/repodata` (Renovate's registryUrl is the repodata directory; dnf's
// baseurl is the root).
func repoBase(u string) string {
	u = strings.TrimRight(u, "/")
	return strings.TrimSuffix(u, "/repodata")
}

// repomd is the part of `repodata/repomd.xml` deps reads.
type repomd struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
			Base string `xml:"http://www.w3.org/XML/1998/namespace base,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

// maxRepomd bounds the size of repomd.xml, a few KB in practice.
const maxRepomd = 4 << 20

// fetch reads base's repomd.xml, then streams the primary metadata it names into an index of
// the wanted packages.
func (s *Source) fetch(ctx context.Context, base string, wanted map[string]bool) (r *repo, err error) {
	start := time.Now()
	defer func() {
		n := 0
		if r != nil {
			n = len(r.pkgs)
		}
		slog.DebugContext(ctx, "rpm repository indexed", "repo", httpx.Redact(base), "packages", n, "duration", time.Since(start), "err", err)
	}()
	var md repomd
	g := s.getter
	g.MaxBody = maxRepomd
	if err := g.Stream(ctx, base+"/repodata/repomd.xml", func(body io.Reader) error {
		return xml.NewDecoder(body).Decode(&md)
	}); err != nil {
		return nil, err
	}
	primary := ""
	for _, d := range md.Data {
		if d.Type == "primary" && d.Location.Href != "" {
			primary, err = resolve(base+"/", d.Location.Base, d.Location.Href)
			if err != nil {
				return nil, fmt.Errorf("rpm %s: %w", httpx.Redact(base+"/repodata/repomd.xml"), err)
			}
			break
		}
	}
	if primary == "" {
		return nil, fmt.Errorf("rpm %s: no primary metadata", httpx.Redact(base+"/repodata/repomd.xml"))
	}
	r = &repo{wanted: wanted, pkgs: map[string]*pkg{}}
	err = s.getter.Stream(ctx, primary, func(body io.Reader) error {
		plain, closeFn, err := decompress(body)
		if err != nil {
			return err
		}
		defer closeFn()
		return scanPrimary(plain, r.wants, r.add)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// wants reports whether name is a wanted package of the repository.
func (r *repo) wants(name []byte) bool { return r.wanted[string(name)] }

func (r *repo) add(name []byte, version string, t time.Time) {
	p := r.pkgs[string(name)]
	if p == nil {
		p = &pkg{}
		r.pkgs[string(name)] = p
	}
	p.add(version, t)
}

// resolve resolves href against the location's xml:base (a mirror's absolute URL) when set,
// else against the repository root base.
func resolve(base, xmlBase, href string) (string, error) {
	if xmlBase != "" {
		base = strings.TrimRight(xmlBase, "/") + "/"
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	h, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(h).String(), nil
}

// Magic numbers of the compressions repositories use for primary.xml (createrepo_c's gz, zst,
// xz and bz2); anything else is read as plain XML.
var (
	gzipMagic  = []byte{0x1f, 0x8b}
	zstdMagic  = []byte{0x28, 0xb5, 0x2f, 0xfd}
	xzMagic    = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	bzip2Magic = []byte("BZh")
)

// decompress returns the decompressed stream of body, detected by its magic number (file
// names lie: some servers transparently decompress), and a function releasing the decoder.
func decompress(body io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReaderSize(body, 64<<10)
	head, _ := br.Peek(len(xzMagic))
	switch {
	case bytes.HasPrefix(head, gzipMagic):
		z, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		return z, func() { _ = z.Close() }, nil
	case bytes.HasPrefix(head, zstdMagic):
		z, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, err
		}
		return z, z.Close, nil
	case bytes.HasPrefix(head, xzMagic):
		z, err := xz.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		return z, func() {}, nil
	case bytes.HasPrefix(head, bzip2Magic):
		return bzip2.NewReader(br), func() {}, nil
	}
	return br, func() {}, nil
}

// scanPrimary streams primary.xml and calls add for each `<package type="rpm">` whose `<name>`
// want accepts, with its name, its version (versionOf) and its `<time file>`. It reads tags
// with a byte scanner instead of encoding/xml: a distribution's primary.xml is hundreds of
// MB, of which only these three elements matter, and tens of thousands of packages, of which
// a run asks for a few. Element texts other than the name are skipped unread; attribute
// values and the name are unescaped. The version is built at the end of a package, only for
// a wanted one, so unwanted packages allocate nothing.
func scanPrimary(r io.Reader, want func(name []byte) bool, add func(name []byte, version string, t time.Time)) error {
	s := tagScanner{br: bufio.NewReaderSize(r, 64<<10)}
	if _, err := s.until('<', false); err != nil {
		return fmt.Errorf("no XML: %w", err)
	}
	var (
		in         bool // inside a package of type rpm
		name       []byte
		version    []byte // the package's last `<version …>` tag, when hasVersion
		hasVersion bool
		added      time.Time
	)
	for {
		tag, err := s.until('>', true)
		if err != nil {
			return unexpectedEOF(err)
		}
		switch elem := element(tag); {
		case string(elem) == "/metadata":
			return nil
		case string(elem) == "package":
			typ, _ := attrRaw(tag, "type")
			in = string(typ) == "rpm" || bytes.IndexByte(typ, '&') >= 0 && html.UnescapeString(string(typ)) == "rpm"
			name, version, hasVersion, added = name[:0], version[:0], false, time.Time{}
		case string(elem) == "/package":
			if in && len(name) > 0 && hasVersion && want(name) {
				if v := versionOf(version); v != "" {
					add(name, v, added)
				}
			}
			in = false
		case in && string(elem) == "name" && tag[len(tag)-1] != '/':
			text, err := s.until('<', true)
			if err != nil {
				return unexpectedEOF(err)
			}
			name = append(name[:0], bytes.TrimSpace(text)...)
			if bytes.IndexByte(name, '&') >= 0 {
				name = []byte(html.UnescapeString(string(name)))
			}
			continue // the text up to '<' is consumed
		case in && string(elem) == "version":
			version, hasVersion = append(version[:0], tag...), true
		case in && string(elem) == "time":
			file, _ := attrRaw(tag, "file")
			if bytes.IndexByte(file, '&') >= 0 {
				file = []byte(html.UnescapeString(string(file)))
			}
			if n, err := strconv.ParseInt(string(file), 10, 64); err == nil && n > 0 {
				added = time.Unix(n, 0).UTC()
			}
		}
		if _, err := s.until('<', false); err != nil {
			return unexpectedEOF(err)
		}
	}
}

// versionOf returns the version of a `<version epoch ver rel>` tag's content:
// `epoch:ver-rel`, without `epoch:` when it is 0 or absent and `-rel` without a release; ""
// without a ver.
func versionOf(tag []byte) string {
	ver, _ := attrOK(tag, "ver")
	if ver == "" {
		return ""
	}
	if epoch, _ := attrOK(tag, "epoch"); epoch != "" && strings.TrimLeft(epoch, "0") != "" {
		ver = epoch + ":" + ver
	}
	if rel, _ := attrOK(tag, "rel"); rel != "" {
		return ver + "-" + rel
	}
	return ver
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// tagScanner reads delimited runs of a stream without copying unless a run spans buffers.
type tagScanner struct {
	br  *bufio.Reader
	buf []byte
}

// until reads up to and including delim and returns the bytes before it (valid until the next
// call); with keep false they are discarded and nil is returned.
func (s *tagScanner) until(delim byte, keep bool) ([]byte, error) {
	s.buf = s.buf[:0]
	for {
		b, err := s.br.ReadSlice(delim)
		if errors.Is(err, bufio.ErrBufferFull) {
			if keep {
				s.buf = append(s.buf, b...)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !keep {
			return nil, nil
		}
		b = b[:len(b)-1]
		if len(s.buf) == 0 {
			return b, nil
		}
		s.buf = append(s.buf, b...)
		return s.buf, nil
	}
}

// element returns the element name of a tag's content (`version epoch="0" …/` → version,
// `/package` → /package), without a namespace prefix check: primary.xml's own elements are
// unprefixed and `rpm:`/`file` elements have other names.
func element(tag []byte) []byte {
	start := 0
	if len(tag) > 0 && tag[0] == '/' { // a closing tag
		start = 1
	}
	end := start
	for end < len(tag) && !isSpace(tag[end]) && tag[end] != '/' {
		end++
	}
	return tag[:end]
}

// attrOK returns the unescaped value of the attribute key of a tag's content and whether
// the tag has it.
func attrOK(tag []byte, key string) (string, bool) {
	v, ok := attrRaw(tag, key)
	if bytes.IndexByte(v, '&') >= 0 {
		return html.UnescapeString(string(v)), ok
	}
	return string(v), ok
}

// attrRaw returns the value of the attribute key of a tag's content as written (escaped,
// aliasing tag) and whether the tag has it.
func attrRaw(tag []byte, key string) ([]byte, bool) {
	for i := 0; i < len(tag); {
		k := bytes.Index(tag[i:], []byte(key))
		if k < 0 {
			return nil, false
		}
		k += i
		i = k + len(key)
		if k == 0 || !isSpace(tag[k-1]) {
			continue
		}
		j := i
		for j < len(tag) && isSpace(tag[j]) {
			j++
		}
		if j >= len(tag) || tag[j] != '=' {
			continue
		}
		j++
		for j < len(tag) && isSpace(tag[j]) {
			j++
		}
		if j >= len(tag) || tag[j] != '"' && tag[j] != '\'' {
			continue
		}
		q := tag[j]
		e := bytes.IndexByte(tag[j+1:], q)
		if e < 0 {
			return nil, false
		}
		return tag[j+1 : j+1+e], true
	}
	return nil, false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
