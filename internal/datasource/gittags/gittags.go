// Package gittags lists a remote repository's tags over the git smart-HTTP protocol, without a git process.
//
// Protocol v2 `ls-refs` with `ref-prefix refs/tags/` and `peel` is used when the server advertises it,
// so only tag refs cross the wire; otherwise the v0 ref advertisement is parsed. Responses are parsed
// as a pkt-line stream.
package gittags

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Tag is a remote tag and the commit it points to (peeled for annotated tags).
type Tag struct{ Name, Commit string }

// Lister fetches tags once per repository URL per run; concurrent callers share the result.
type Lister struct {
	client *http.Client
	auth   *httpx.Auth
	sem    chan struct{}

	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	once sync.Once
	tags []Tag
	rels []datasource.Release
	err  error
}

// New returns a Lister. sem bounds concurrent requests and is shared with other datasources;
// auth may be nil for anonymous access.
func New(client *http.Client, auth *httpx.Auth, sem chan struct{}) *Lister {
	return &Lister{client: client, auth: auth, sem: sem, cache: make(map[string]*entry)}
}

// Tags returns the tags of the repository at repoURL (e.g. https://github.com/actions/checkout).
// The returned slice is shared between callers and must not be modified.
func (l *Lister) Tags(ctx context.Context, repoURL string) ([]Tag, error) {
	e := l.get(ctx, repoURL)
	return e.tags, e.err
}

// Releases implements datasource.Datasource: the tags of d.Source, each with its commit as
// digest. The slice is shared between callers and must not be modified.
func (l *Lister) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	e := l.get(ctx, d.Source)
	return e.rels, e.err
}

// Digest returns the commit of tag version in d.Source.
func (l *Lister) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	e := l.get(ctx, d.Source)
	if e.err != nil {
		return "", e.err
	}
	for _, t := range e.tags {
		if t.Name == version {
			return t.Commit, nil
		}
	}
	return "", fmt.Errorf("tag %s not found upstream", version)
}

func (l *Lister) get(ctx context.Context, repoURL string) *entry {
	repoURL = strings.TrimSuffix(repoURL, "/")
	l.mu.Lock()
	e := l.cache[repoURL]
	if e == nil {
		e = &entry{}
		l.cache[repoURL] = e
	}
	l.mu.Unlock()
	e.once.Do(func() {
		e.tags, e.err = l.fetch(ctx, repoURL)
		e.rels = make([]datasource.Release, len(e.tags))
		for i, t := range e.tags {
			e.rels[i] = datasource.Release{Version: t.Name, Digest: t.Commit}
		}
	})
	return e
}

// session carries the credentials of one repository fetch across its requests.
type session struct {
	l           *Lister
	url         string
	host        string // host[:port] for the credential helper
	user, pass  string
	hasCreds    bool
	triedHelper bool
}

func (l *Lister) fetch(ctx context.Context, repoURL string) (tags []Tag, err error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return nil, fmt.Errorf("ls-refs %s: %w", repoURL, err)
	}
	start, protocol := time.Now(), "v0"
	defer func() {
		slog.DebugContext(ctx, "tags listed", "repo", repoURL, "protocol", protocol, "tags", len(tags), "duration", time.Since(start), "err", err)
	}()
	s := &session{l: l, url: repoURL, host: u.Host}
	s.user, s.pass, s.hasCreds = l.auth.Static(u.Hostname())

	tags, caps, err := s.advertisement(ctx)
	if err != nil || caps == nil {
		return tags, err
	}
	protocol = "v2"
	return s.lsRefs(ctx, caps)
}

// v2caps is what deps needs from a protocol v2 capability advertisement.
type v2caps struct {
	agent        bool
	objectFormat string
}

// advertisement performs the info/refs request. For a v2 server it returns the capabilities;
// for a v0 server it returns the tags parsed from the ref advertisement.
func (s *session) advertisement(ctx context.Context) ([]Tag, *v2caps, error) {
	if err := s.l.acquire(ctx); err != nil {
		return nil, nil, err
	}
	defer s.l.release()
	resp, err := s.do(ctx, http.MethodGet, s.url+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, nil, err
	}
	defer closeBody(resp)
	p := newPktReader(resp.Body)

	line, kind, err := p.next()
	if err != nil {
		return nil, nil, s.wrap(err)
	}
	if kind == pktData && bytes.HasPrefix(line, []byte("# service=")) {
		if _, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
		if kind != pktFlush {
			return nil, nil, s.wrap(errMalformed)
		}
		if line, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
	}
	if kind == pktData && string(line) == "version 2" {
		caps := &v2caps{}
		for {
			line, kind, err = p.next()
			if err != nil {
				return nil, nil, s.wrap(err)
			}
			if kind == pktFlush {
				return nil, caps, nil
			}
			if kind != pktData {
				return nil, nil, s.wrap(errMalformed)
			}
			k, v, _ := bytes.Cut(line, []byte{'='})
			switch string(k) {
			case "agent":
				caps.agent = true
			case "object-format":
				caps.objectFormat = string(v)
			}
		}
	}

	// Protocol v0 advertisement: "<oid> <ref>[\0caps]" lines, then a flush.
	var tags []Tag
	for first := true; kind == pktData; first = false {
		if first {
			if i := bytes.IndexByte(line, 0); i >= 0 {
				line = line[:i]
			}
		}
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return nil, nil, fmt.Errorf("ls-refs %s: %s", s.url, line[4:])
		}
		if oid, ref, ok := bytes.Cut(line, []byte{' '}); ok && bytes.HasPrefix(ref, []byte("refs/tags/")) {
			name := ref[len("refs/tags/"):]
			if base, peeled := bytes.CutSuffix(name, []byte("^{}")); peeled {
				setCommit(tags, base, string(oid))
			} else {
				str := string(line) // one allocation per tag; name and commit slice into it
				tags = append(tags, Tag{Name: str[len(oid)+1+len("refs/tags/"):], Commit: str[:len(oid)]})
			}
		}
		if line, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
	}
	if kind != pktFlush {
		return nil, nil, s.wrap(errMalformed)
	}
	return tags, nil, nil
}

// setCommit overwrites the commit of tag name; the peeled line usually directly follows the tag.
func setCommit(tags []Tag, name []byte, commit string) {
	for i := len(tags) - 1; i >= 0; i-- {
		if tags[i].Name == string(name) {
			tags[i].Commit = commit
			return
		}
	}
}

// lsRefs runs the protocol v2 ls-refs command restricted to tags.
func (s *session) lsRefs(ctx context.Context, caps *v2caps) ([]Tag, error) {
	body := make([]byte, 0, 128)
	body = appendPkt(body, "command=ls-refs\n")
	if caps.agent {
		body = appendPkt(body, "agent="+httpx.UserAgent()+"\n")
	}
	if caps.objectFormat != "" {
		body = appendPkt(body, "object-format="+caps.objectFormat+"\n")
	}
	body = append(body, "0001"...)
	body = appendPkt(body, "peel\n")
	body = appendPkt(body, "ref-prefix refs/tags/\n")
	body = append(body, "0000"...)

	if err := s.l.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.l.release()
	resp, err := s.do(ctx, http.MethodPost, s.url+"/git-upload-pack", body)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	p := newPktReader(resp.Body)

	var tags []Tag
	for {
		line, kind, err := p.next()
		if err != nil {
			return nil, s.wrap(err)
		}
		switch kind {
		case pktFlush, pktResponseEnd:
			return tags, nil
		case pktDelim:
			return nil, s.wrap(errMalformed)
		}
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return nil, fmt.Errorf("ls-refs %s: %s", s.url, line[4:])
		}
		// "<oid> refs/tags/<name>[ <attr>...]" with attr "peeled:<oid>".
		str := string(line) // one allocation per tag; name and commit slice into it
		oid, rest, ok := strings.Cut(str, " ")
		if !ok {
			return nil, s.wrap(errMalformed)
		}
		ref, attrs, _ := strings.Cut(rest, " ")
		name, ok := strings.CutPrefix(ref, "refs/tags/")
		if !ok {
			continue
		}
		commit := oid
		for attrs != "" {
			var a string
			a, attrs, _ = strings.Cut(attrs, " ")
			if peeled, ok := strings.CutPrefix(a, "peeled:"); ok {
				commit = peeled
			}
		}
		tags = append(tags, Tag{Name: name, Commit: commit})
	}
}

// do sends one request with the session's credentials. On a 401 without credentials it asks the
// git credential helper and retries once. Non-2xx responses become errors.
func (s *session) do(ctx context.Context, method, target string, body []byte) (*http.Response, error) {
	for {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, r)
		if err != nil {
			return nil, fmt.Errorf("ls-refs %s: %w", s.url, err)
		}
		req.Header.Set("Git-Protocol", "version=2")
		req.Header.Set("User-Agent", httpx.UserAgent())
		if body != nil {
			req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
			req.Header.Set("Accept", "application/x-git-upload-pack-result")
		}
		if s.hasCreds {
			req.SetBasicAuth(s.user, s.pass)
		}
		resp, err := s.l.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ls-refs %s: %w", s.url, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && !s.hasCreds && !s.triedHelper {
			closeBody(resp)
			s.triedHelper = true
			if s.user, s.pass, s.hasCreds = s.l.auth.Helper(ctx, s.host); s.hasCreds {
				continue
			}
			return nil, fmt.Errorf("ls-refs %s: HTTP %d", s.url, resp.StatusCode)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			closeBody(resp)
			return nil, fmt.Errorf("ls-refs %s: HTTP %d", s.url, resp.StatusCode)
		}
		return resp, nil
	}
}

func (s *session) wrap(err error) error {
	if errors.Is(err, errMalformed) {
		return fmt.Errorf("ls-refs %s: malformed pkt-line", s.url)
	}
	return fmt.Errorf("ls-refs %s: %w", s.url, err)
}

func (l *Lister) acquire(ctx context.Context) error {
	if l.sem == nil {
		return nil
	}
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Lister) release() {
	if l.sem != nil {
		<-l.sem
	}
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

var errMalformed = errors.New("malformed pkt-line")

// Packet kinds returned by pktReader.next.
const (
	pktFlush       = 0 // "0000"
	pktDelim       = 1 // "0001"
	pktResponseEnd = 2 // "0002"
	pktData        = 3
)

const maxPktLen = 65520

type pktReader struct {
	r   *bufio.Reader
	buf []byte
}

func newPktReader(r io.Reader) *pktReader { return &pktReader{r: bufio.NewReader(r)} }

// next returns the next packet. Data payloads have one trailing newline removed and are only
// valid until the following call.
func (p *pktReader) next() ([]byte, int, error) {
	hdr, err := p.r.Peek(4)
	if err != nil {
		return nil, 0, eofMalformed(err)
	}
	n := 0
	for _, c := range hdr {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return nil, 0, errMalformed
		}
		n = n<<4 | int(d)
	}
	_, _ = p.r.Discard(4)
	switch {
	case n <= pktResponseEnd:
		return nil, n, nil
	case n < 4 || n > maxPktLen:
		return nil, 0, errMalformed
	}
	n -= 4
	var b []byte
	if n <= p.r.Size() {
		if b, err = p.r.Peek(n); err != nil {
			return nil, 0, eofMalformed(err)
		}
		_, _ = p.r.Discard(n)
	} else {
		if cap(p.buf) < n {
			p.buf = make([]byte, n)
		}
		b = p.buf[:n]
		if _, err = io.ReadFull(p.r, b); err != nil {
			return nil, 0, eofMalformed(err)
		}
	}
	return bytes.TrimSuffix(b, []byte{'\n'}), pktData, nil
}

func eofMalformed(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errMalformed
	}
	return err
}

func appendPkt(b []byte, payload string) []byte {
	const hex = "0123456789abcdef"
	n := len(payload) + 4
	b = append(b, hex[n>>12&0xf], hex[n>>8&0xf], hex[n>>4&0xf], hex[n&0xf])
	return append(b, payload...)
}
