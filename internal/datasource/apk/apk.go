// Package apk lists the versions of Alpine Linux packages (and of other apk repositories such as
// Wolfi) from the APKINDEX.tar.gz of the repositories a dependency names in its registry URLs:
// each index is fetched once per run and shared by every package looked up in it.
package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the datasource name (Renovate's).
const Name = "apk"

// Source is the apk datasource.
type Source struct {
	getter httpx.Getter

	indexes datasource.Memo[string, index] // by APKINDEX.tar.gz URL
}

// index maps the package names of one APKINDEX to their versions.
type index map[string][]entry

// entry is one package version of an index and its build time (zero when unknown).
type entry struct {
	version string
	built   time.Time
}

// New returns the apk datasource. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}}
}

// Releases lists every version of the package d.Name in the indexes of d.Source, oldest first
// in apk order, each version once. The result is fresh.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	entries, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	rels := make([]datasource.Release, len(entries))
	for i, e := range entries {
		rels[i] = datasource.Release{Version: e.version}
	}
	return rels, nil
}

// ReleaseTime implements datasource.Timer with the build time of version (the index's `t:`
// field, Renovate's buildDate); the earliest when several indexes list it, zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	entries, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	for _, e := range entries {
		if e.version == version {
			return e.built, nil
		}
	}
	return time.Time{}, nil
}

// Digest is unsupported: apk package versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup returns the versions of d.Name across the indexes of d.Source, sorted in apk order
// with duplicates merged (keeping the earliest build time).
func (s *Source) lookup(ctx context.Context, d dep.Dep) ([]entry, error) {
	urls, err := IndexURLs(d.Source)
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, u := range urls {
		idx, err := s.indexes.Do(ctx, u, func() (index, error) { return s.fetch(ctx, u) })
		if err != nil {
			return nil, err
		}
		out = append(out, idx[d.Name]...)
	}
	if len(out) == 0 {
		shown := make([]string, len(urls))
		for i, u := range urls {
			shown[i] = httpx.Redact(u)
		}
		return nil, fmt.Errorf("package %q not found in %s", d.Name, strings.Join(shown, ", "))
	}
	scheme, _ := versioning.Get("apk")
	slices.SortStableFunc(out, func(a, b entry) int {
		if c := scheme.Compare(a.version, b.version); c != 0 {
			return c
		}
		return strings.Compare(a.version, b.version)
	})
	merged := out[:1]
	for _, e := range out[1:] {
		last := &merged[len(merged)-1]
		if e.version != last.version {
			merged = append(merged, e)
			continue
		}
		if !e.built.IsZero() && (last.built.IsZero() || e.built.Before(last.built)) {
			last.built = e.built
		}
	}
	return merged, nil
}

// IndexURLs returns the APKINDEX.tar.gz URLs of registry, whitespace-separated registry URLs
// in Renovate's form `<base>?arch=<arch>[&branch=<branch>][&components=<c1>,<c2>]`, each
// expanding to `<base>[/<branch>]/<component>/<arch>/APKINDEX.tar.gz` per component (one
// index without components), or a URL without query naming the index or its directory.
func IndexURLs(registry string) ([]string, error) {
	var out []string
	for raw := range strings.FieldsSeq(registry) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("invalid apk registry URL %q", httpx.Redact(raw))
		}
		q := u.Query()
		u.RawQuery, u.Fragment = "", ""
		base := strings.TrimSuffix(u.String(), "/")
		if len(q) == 0 {
			if !strings.HasSuffix(base, ".tar.gz") {
				base += "/APKINDEX.tar.gz"
			}
			out = append(out, base)
			continue
		}
		for k := range q {
			if k != "arch" && k != "branch" && k != "components" {
				return nil, fmt.Errorf("apk registry URL %q: unknown parameter %q (want arch, branch, components)", httpx.Redact(raw), k)
			}
		}
		arch := q.Get("arch")
		if arch == "" {
			return nil, fmt.Errorf("apk registry URL %q: missing arch parameter", httpx.Redact(raw))
		}
		if b := q.Get("branch"); b != "" {
			base += "/" + url.PathEscape(b)
		}
		comps := []string{""}
		if c := q.Get("components"); c != "" {
			comps = strings.Split(c, ",")
		}
		for _, c := range comps {
			p := base
			if c = strings.TrimSpace(c); c != "" {
				p += "/" + url.PathEscape(c)
			}
			out = append(out, p+"/"+url.PathEscape(arch)+"/APKINDEX.tar.gz")
		}
	}
	if len(out) == 0 {
		return nil, errors.New("apk needs a registry URL (registryUrl), e.g. https://dl-cdn.alpinelinux.org/alpine?branch=v3.22&components=main,community&arch=x86_64")
	}
	return out, nil
}

// fetch downloads and parses one APKINDEX.tar.gz.
func (s *Source) fetch(ctx context.Context, u string) (index, error) {
	start := time.Now()
	var idx index
	err := s.getter.Stream(ctx, u, func(r io.Reader) error {
		var err error
		idx, err = parseArchive(r)
		return err
	})
	slog.DebugContext(ctx, "apk index", "url", httpx.Redact(u), "packages", len(idx), "duration", time.Since(start), "err", err)
	return idx, err
}

// parseArchive reads the APKINDEX member of an APKINDEX.tar.gz: gzip streams (the signature,
// then the index) of tar members without end-of-archive blocks, so one tar reader over the
// multistream gzip reader sees them all.
func parseArchive(r io.Reader) (index, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no APKINDEX in archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "APKINDEX" {
			return parseIndex(tr)
		}
	}
}

// parseIndex parses APKINDEX text: records of `<key>:<value>` lines separated by blank lines;
// P is the package name, V its version and t its build time (Unix seconds).
func parseIndex(r io.Reader) (index, error) {
	idx := index{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var name, version string
	var built time.Time
	flush := func() {
		if name != "" && version != "" {
			idx[name] = append(idx[name], entry{version: version, built: built})
		}
		name, version, built = "", "", time.Time{}
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < 2 || line[1] != ':' {
			if len(line) == 0 {
				flush()
			}
			continue
		}
		val := line[2:]
		switch line[0] {
		case 'P':
			name = string(val)
		case 'V':
			version = string(val)
		case 't':
			if ts, err := strconv.ParseInt(string(bytes.TrimSpace(val)), 10, 64); err == nil && ts > 0 {
				built = time.Unix(ts, 0).UTC()
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return idx, nil
}
