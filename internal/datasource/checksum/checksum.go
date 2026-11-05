// Package checksum resolves the checksum of a new version for deps that pin one next to their
// version (dep.Dep.ChecksumSource): from a GitHub release asset digest, from a downloaded
// checksum document, or by hashing the downloaded artifact.
package checksum

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// maxBody bounds the size of a checksum document, maxArtifact that of a hashed artifact.
const (
	maxBody     = 1 << 20
	maxArtifact = 256 << 20
)

// Assets returns the hex digest of a release asset (githubreleases.Source implements it).
type Assets interface {
	AssetDigest(ctx context.Context, name, version, asset string) (string, error)
}

// ArtifactOpener downloads an artifact of a dep the way its datasource does (repository
// credentials, mirrors): it returns the body of a successful response, which the caller
// closes. mavends.Source implements it.
type ArtifactOpener interface {
	OpenArtifact(ctx context.Context, d dep.Dep, url string) (io.ReadCloser, error)
}

// Source fetches each checksum document and hashes each artifact once per run; concurrent
// callers share the result.
type Source struct {
	client  *http.Client
	sem     chan struct{}
	assets  Assets
	openers map[string]ArtifactOpener

	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	once  sync.Once
	first string            // first hex token of the body; an artifact's digest
	files map[string]string // entry name -> hex
	err   error
}

// New returns the checksum source. sem bounds concurrent requests (nil = unbounded); assets
// may be nil when no github-releases datasource is available. openers download the artifacts
// of deps by datasource name (nil = all with client).
func New(client *http.Client, sem chan struct{}, assets Assets, openers map[string]ArtifactOpener) datasource.ChecksumSource {
	return &Source{client: client, sem: sem, assets: assets, openers: openers, cache: make(map[string]*entry)}
}

// Checksum returns the checksum of version per d.ChecksumSource. Templates expand version;
// the github-releases asset is looked up in the release named release.
func (s *Source) Checksum(ctx context.Context, d dep.Dep, version, release string) (string, error) {
	cs := d.ChecksumSource
	switch {
	case cs == nil:
		return "", errors.New("dependency has no checksum source")
	case cs.Asset != "":
		if d.Datasource != "github-releases" || s.assets == nil {
			return "", fmt.Errorf("checksum asset requires the github-releases datasource, have %q", d.Datasource)
		}
		return s.assets.AssetDigest(ctx, d.Name, release, dep.ExpandVersion(cs.Asset, version))
	case cs.Artifact != "":
		return s.artifactDigest(ctx, d, dep.ExpandVersion(cs.Artifact, version))
	case cs.URL == "":
		return "", errors.New("checksum source has neither asset, url nor artifact")
	}
	u := dep.ExpandVersion(cs.URL, version)
	e := s.document(ctx, u)
	if e.err != nil {
		return "", e.err
	}
	if cs.File == "" {
		if e.first == "" {
			return "", fmt.Errorf("no checksum in %s", u)
		}
		return e.first, nil
	}
	file := dep.ExpandVersion(cs.File, version)
	if h, ok := e.files[file]; ok {
		return h, nil
	}
	return "", fmt.Errorf("no checksum for %s in %s", file, u)
}

func (s *Source) document(ctx context.Context, u string) *entry {
	return s.once(u, func(e *entry) { e.first, e.files, e.err = s.fetch(ctx, u) })
}

// once runs fill for the cache entry key on first use and returns the entry.
func (s *Source) once(key string, fill func(*entry)) *entry {
	s.mu.Lock()
	e := s.cache[key]
	if e == nil {
		e = &entry{}
		s.cache[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { fill(e) })
	return e
}

// artifactDigest returns the hex digest of the artifact at u: sha256 for a 64-hex pinned
// checksum, sha512 for a 128-hex one.
func (s *Source) artifactDigest(ctx context.Context, d dep.Dep, u string) (string, error) {
	var algo string
	var newHash func() hash.Hash
	switch len(d.Checksum) {
	case 64:
		algo, newHash = "sha256", sha256.New
	case 128:
		algo, newHash = "sha512", sha512.New
	default:
		return "", fmt.Errorf("artifact %s: pinned checksum is neither sha256 nor sha512 hex", u)
	}
	e := s.once(algo+" "+u, func(e *entry) { e.first, e.err = s.hash(ctx, d, u, newHash()) })
	return e.first, e.err
}

// hash streams the artifact at u through h, up to maxArtifact bytes, downloaded by the
// opener of d's datasource, else with the client.
func (s *Source) hash(ctx context.Context, d dep.Dep, u string, h hash.Hash) (sum string, err error) {
	start := time.Now()
	var n int64
	defer func() {
		slog.DebugContext(ctx, "artifact hashed", "url", u, "bytes", n, "duration", time.Since(start), "err", err)
	}()
	var body io.Reader
	var release func()
	if o := s.openers[d.Datasource]; o != nil {
		body, release, err = s.open(ctx, o, d, u)
	} else {
		body, release, err = s.get(ctx, u)
	}
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", u, err)
	}
	defer release()
	if n, err = io.Copy(h, io.LimitReader(body, maxArtifact+1)); err != nil {
		return "", fmt.Errorf("artifact %s: %w", u, err)
	}
	if n > maxArtifact {
		return "", fmt.Errorf("artifact %s: larger than %d bytes", u, maxArtifact)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// open opens u with o within the semaphore; release closes the body and frees the slot.
func (s *Source) open(ctx context.Context, o ArtifactOpener, d dep.Dep, u string) (body io.Reader, release func(), err error) {
	done, err := s.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	rc, err := o.OpenArtifact(ctx, d, u)
	if err != nil {
		done()
		return nil, nil, err
	}
	return rc, func() { _ = rc.Close(); done() }, nil
}

// acquire takes a semaphore slot; done frees it.
func (s *Source) acquire(ctx context.Context) (done func(), err error) {
	if s.sem == nil {
		return func() {}, nil
	}
	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Source) fetch(ctx context.Context, u string) (first string, files map[string]string, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "checksums fetched", "url", u, "entries", len(files), "duration", time.Since(start), "err", err)
	}()
	body, release, err := s.get(ctx, u)
	if err != nil {
		return "", nil, fmt.Errorf("checksums %s: %w", u, err)
	}
	defer release()
	first, files, err = parse(io.LimitReader(body, maxBody+1))
	if err != nil {
		return "", nil, fmt.Errorf("checksums %s: %w", u, err)
	}
	return first, files, nil
}

// get requests u within the semaphore and returns the body of a 2xx response; release
// drains and closes it and frees the semaphore slot.
func (s *Source) get(ctx context.Context, u string) (body io.Reader, release func(), err error) {
	done, err := s.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		done()
		return nil, nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		done()
		return nil, nil, err
	}
	release = func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		done()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		release()
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, release, nil
}

// parse reads a sha256sum-style list ("<hex>  <name>" or "<hex> *<name>") line by line. first
// is the first 64/128-hex whitespace-separated token anywhere in the body (bare digest files).
func parse(r io.Reader) (first string, files map[string]string, err error) {
	files = make(map[string]string)
	br := bufio.NewReader(r)
	read := 0
	for {
		line, rerr := br.ReadString('\n')
		read += len(line)
		if read > maxBody {
			return "", nil, fmt.Errorf("larger than %d bytes", maxBody)
		}
		line = strings.TrimRight(line, "\r\n")
		if first == "" {
			for f := range strings.FieldsSeq(line) {
				if isHex(f) {
					first = strings.ToLower(f)
					break
				}
			}
		}
		if h, name, ok := strings.Cut(line, " "); ok && isHex(h) {
			name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
			if _, dup := files[name]; name != "" && !dup {
				files[name] = strings.ToLower(h)
			}
		}
		if rerr == io.EOF {
			return first, files, nil
		}
		if rerr != nil {
			return "", nil, rerr
		}
	}
}

func isHex(s string) bool {
	if len(s) != 64 && len(s) != 128 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}
