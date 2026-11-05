package dockerds

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oras-project/oras-go/v3/errdef"
	"github.com/oras-project/oras-go/v3/registry/remote"
	"github.com/oras-project/oras-go/v3/registry/remote/errcode"
)

// tagMeta is what a tags/list `manifest` map says about a tag: the digest of the manifest it
// points to (the index digest for multi-platform images, as a HEAD returns it) and when it was
// published.
type tagMeta struct {
	digest string
	time   time.Time // zero when unknown
}

// listTags pages through the tags/list endpoint of repo and passes every tag of the `tags`
// arrays to fn. gcr.io and Artifact Registry (also behind registry.k8s.io) add a `manifest` map
// of digest → {tag, timeCreatedMs, timeUploadedMs, …} to the page; its tags are returned in
// meta, which is nil when no page had one. Pages are decoded as they stream in, never buffered
// whole, and the map's untagged entries (most of a large gcr.io repository) are skipped.
//
// This replaces oras-go's Repository.Tags, which decodes only `tags`. Like it, the request
// carries the repository's pull scope (with the registry's, see pullContext), n is
// repo.TagListPageSize, a page is limited to maxMetadataBytes, next links must stay on the
// origin of the page that sent them, and a next page beyond repo.TagListMaxPages (0 = no
// limit) fails with errdef.ErrTooManyPages; the tags and meta read until then are kept.
func (s *Source) listTags(ctx context.Context, repo *remote.Repository, fn func(tag string)) (map[string]tagMeta, error) {
	ref := repo.Reference()
	ctx = s.pullContext(ctx, repo)
	scheme := "https"
	if repo.Registry.PlainHTTP {
		scheme = "http"
	}
	next := scheme + "://" + ref.Host() + "/v2/" + ref.Repository + "/tags/list"
	if repo.TagListPageSize > 0 {
		next += "?n=" + strconv.Itoa(repo.TagListPageSize)
	}
	var meta map[string]tagMeta
	listed := func(tag string, m tagMeta) {
		if meta == nil {
			meta = map[string]tagMeta{}
		}
		meta[tag] = m
	}
	for page := 0; next != ""; page++ {
		if limit := repo.TagListMaxPages; limit > 0 && page >= limit {
			return meta, fmt.Errorf("tag listing exceeded %d pages: %w", limit, errdef.ErrTooManyPages)
		}
		var err error
		if next, err = s.tagPage(ctx, next, fn, listed); err != nil {
			return nil, err
		}
	}
	return meta, nil
}

// tagPage GETs the tags/list page at url, decodes it into fn and listed, and returns the URL of
// the next page, "" on the last one.
func (s *Source) tagPage(ctx context.Context, url string, fn func(string), listed func(string, tagMeta)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", responseError(resp)
	}
	lr := &io.LimitedReader{R: resp.Body, N: maxMetadataBytes}
	if err := decodeTagPage(lr, fn, listed); err != nil {
		if lr.N == 0 {
			err = fmt.Errorf("page exceeds %d bytes", maxMetadataBytes)
		}
		return "", fmt.Errorf("%s %q: decode: %w", req.Method, resp.Request.URL, err)
	}
	return nextLink(resp)
}

// decodeTagPage streams a tags/list page: every string of `tags` goes to fn, every tag of the
// `manifest` map with its tagMeta to listed; other members are skipped. Either may be null.
func decodeTagPage(r io.Reader, fn func(string), listed func(string, tagMeta)) error {
	// Duplicate-name checking is off: it keeps a name set per object, one per manifest entry.
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := readDelim(dec, '{'); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue() // the quoted name, valid until the next read or peek
		if err != nil {
			return err
		}
		switch string(name) {
		case `"tags"`:
			err = readTags(dec, fn)
		case `"manifest"`:
			err = readManifests(dec, listed)
		default:
			err = dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	return readDelim(dec, '}')
}

// readTags passes each string of the array (or null) at dec to fn.
func readTags(dec *jsontext.Decoder, fn func(string)) error {
	if null, err := readNull(dec); null || err != nil {
		return err
	}
	if err := readDelim(dec, '['); err != nil {
		return err
	}
	for dec.PeekKind() != ']' {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		if tok.Kind() != '"' {
			return fmt.Errorf("tag is %v, not a string", tok.Kind())
		}
		fn(tok.String())
	}
	return readDelim(dec, ']')
}

// readManifests passes every tag of the `manifest` map (or null) at dec (digest → {tag: [...],
// timeCreatedMs, timeUploadedMs, …}; gcr.io sends the times as strings) with its tagMeta to
// listed. Entries without tags cost no allocation: the digest is copied into a reused buffer and
// turned into a string only once the entry turns out to have tags.
func readManifests(dec *jsontext.Decoder, listed func(string, tagMeta)) error {
	if null, err := readNull(dec); null || err != nil {
		return err
	}
	if err := readDelim(dec, '{'); err != nil {
		return err
	}
	var digest []byte
	var tags []string
	addTag := func(t string) { tags = append(tags, t) }
	for dec.PeekKind() != '}' {
		k, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if digest, err = jsontext.AppendUnquote(digest[:0], k); err != nil {
			return err
		}
		if err := readDelim(dec, '{'); err != nil {
			return err
		}
		tags = tags[:0]
		var created, uploaded int64
		for dec.PeekKind() != '}' {
			f, err := dec.ReadValue() // valid until the next read or peek
			if err != nil {
				return err
			}
			switch string(f) {
			case `"tag"`:
				err = readTags(dec, addTag)
			case `"timeCreatedMs"`:
				created, err = readMillis(dec)
			case `"timeUploadedMs"`:
				uploaded, err = readMillis(dec)
			default:
				err = dec.SkipValue()
			}
			if err != nil {
				return err
			}
		}
		if err := readDelim(dec, '}'); err != nil {
			return err
		}
		if len(tags) == 0 {
			continue
		}
		m := tagMeta{digest: string(digest), time: tagTime(created, uploaded)}
		for _, t := range tags {
			listed(t, m)
		}
	}
	return readDelim(dec, '}')
}

// tagTime is the release time of a `manifest` map entry: its creation time (timeCreatedMs, the
// image config's `created`, which other registries' release times come from as well), else its
// upload time. Creation is 0 for indexes and for reproducible builds dated 1970, and negative
// for Go's zero time; the upload is then the earliest time the tag is known to exist, and it
// never makes a release look older than it is, which is what minimumReleaseAge relies on.
func tagTime(created, uploaded int64) time.Time {
	switch {
	case created > 0:
		return time.UnixMilli(created).UTC()
	case uploaded > 0:
		return time.UnixMilli(uploaded).UTC()
	}
	return time.Time{}
}

// readMillis reads a number or numeric string (gcr.io quotes its times) of non-negative
// integer milliseconds; anything else, including negative times, reads as 0.
func readMillis(dec *jsontext.Decoder) (int64, error) {
	v, err := dec.ReadValue()
	if err != nil {
		return 0, err
	}
	b := bytes.Trim(v, `"`)
	if len(b) == 0 || len(b) > 18 { // 18 digits cannot overflow int64
		return 0, nil
	}
	var n int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// readNull consumes a null at dec and reports whether there was one.
func readNull(dec *jsontext.Decoder) (bool, error) {
	if dec.PeekKind() != 'n' {
		return false, nil
	}
	_, err := dec.ReadToken()
	return true, err
}

func readDelim(dec *jsontext.Decoder, d jsontext.Kind) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != d {
		return fmt.Errorf("expected %v, got %v", d, tok.Kind())
	}
	return nil
}

// nextLink returns the next page URL from the Link header of resp ("" without one), resolved
// against the page's URL. As in oras-go, it must have the page's origin, so a registry cannot
// point pagination at another host.
func nextLink(resp *http.Response) (string, error) {
	link := resp.Header.Get("Link")
	if link == "" {
		return "", nil
	}
	end := strings.IndexByte(link, '>')
	if link[0] != '<' || end < 0 {
		return "", fmt.Errorf("invalid next link %q", link)
	}
	u, err := resp.Request.URL.Parse(link[1:end])
	if err != nil {
		return "", err
	}
	if base := resp.Request.URL; u.Scheme != base.Scheme || u.Host != base.Host {
		return "", fmt.Errorf("invalid next link %q: not the same origin as %q", link, base)
	}
	return u.String(), nil
}

// maxErrorBytes bounds the error body read from a failed tags/list request.
const maxErrorBytes = 8 << 10

// responseError is the error for a non-200 response, as oras-go reports it: method, URL,
// status and the `errors` of the body.
func responseError(resp *http.Response) error {
	e := &errcode.ErrorResponse{Method: resp.Request.Method, URL: resp.Request.URL, StatusCode: resp.StatusCode}
	var body struct {
		Errors errcode.Errors `json:"errors"`
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxErrorBytes), &body); err == nil {
		e.Errors = body.Errors
	}
	return e
}
