// Package checksum resolves the checksum of a new version for deps that pin one next to their
// version (dep.Dep.ChecksumSource): from a GitHub release asset digest, or from a downloaded
// checksum document.
package checksum

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

// maxBody bounds the size of a checksum document.
const maxBody = 1 << 20

// Assets returns the hex digest of a release asset (githubreleases.Source implements it).
type Assets interface {
	AssetDigest(ctx context.Context, name, version, asset string) (string, error)
}

// Source fetches each checksum document once per run; concurrent callers share the result.
type Source struct {
	client *http.Client
	sem    chan struct{}
	assets Assets

	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	once  sync.Once
	first string            // first hex token of the body
	files map[string]string // entry name -> hex
	err   error
}

// New returns the checksum source. sem bounds concurrent requests (nil = unbounded); assets
// may be nil when no github-releases datasource is available.
func New(client *http.Client, sem chan struct{}, assets Assets) datasource.ChecksumSource {
	return &Source{client: client, sem: sem, assets: assets, cache: make(map[string]*entry)}
}

// Checksum returns the checksum of version per d.ChecksumSource.
func (s *Source) Checksum(ctx context.Context, d dep.Dep, version string) (string, error) {
	cs := d.ChecksumSource
	switch {
	case cs == nil:
		return "", errors.New("dependency has no checksum source")
	case cs.Asset != "":
		if d.Datasource != "github-releases" || s.assets == nil {
			return "", fmt.Errorf("checksum asset requires the github-releases datasource, have %q", d.Datasource)
		}
		return s.assets.AssetDigest(ctx, d.Name, version, dep.ExpandVersion(cs.Asset, version))
	case cs.URL == "":
		return "", errors.New("checksum source has neither asset nor url")
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
	s.mu.Lock()
	e := s.cache[u]
	if e == nil {
		e = &entry{}
		s.cache[u] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.first, e.files, e.err = s.fetch(ctx, u) })
	return e
}

func (s *Source) fetch(ctx context.Context, u string) (first string, files map[string]string, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "checksums fetched", "url", u, "entries", len(files), "duration", time.Since(start), "err", err)
	}()
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return "", nil, fmt.Errorf("checksums %s: %w", u, err)
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("checksums %s: %w", u, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", nil, fmt.Errorf("checksums %s: HTTP %d", u, resp.StatusCode)
	}
	first, files, err = parse(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return "", nil, fmt.Errorf("checksums %s: %w", u, err)
	}
	return first, files, nil
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
