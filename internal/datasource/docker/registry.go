package dockerds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
)

// repository is a registry repository and how to list its tags.
type repository struct {
	registry  string // as in the canonical name: docker.io, ghcr.io, localhost:5000
	path      string // library/alpine
	host      string // request host: registry-1.docker.io for docker.io
	plainHTTP bool
	pageSize  int // tags/list n, 0 = the registry's default
	maxPages  int // tags/list page bound, 0 = none
}

// url returns the URL of the repository's API endpoint p (manifests/…, blobs/…, tags/list).
func (r *repository) url(p string) string {
	scheme := "https"
	if r.plainHTTP {
		scheme = "http"
	}
	return scheme + "://" + r.host + "/v2/" + r.path + "/" + p
}

var (
	// errNotFound is the error of a manifest or blob the registry does not have (404).
	errNotFound = errors.New("not found")
	// errTooManyPages is the error of a tag listing longer than its page bound.
	errTooManyPages = errors.New("too many pages")
)

// errorResponse is the error of a non-2xx registry response: method, URL, status and the
// `errors` of the body.
type errorResponse struct {
	Method     string
	URL        *url.URL
	StatusCode int
	Errors     errorDetails
}

// errorDetail is an entry of a registry error body's `errors`.
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

type errorDetails []errorDetail

func (e *errorResponse) Error() string {
	msg := http.StatusText(e.StatusCode)
	if len(e.Errors) > 0 {
		msg = e.Errors.Error()
	}
	return fmt.Sprintf("%s %q: response status code %d: %s", e.Method, e.URL, e.StatusCode, msg)
}

// Error joins the entries: code in lower case with spaces, message, detail.
func (d errorDetails) Error() string {
	msgs := make([]string, len(d))
	for i, e := range d {
		code := strings.Map(func(r rune) rune {
			if r == '_' {
				return ' '
			}
			return unicode.ToLower(r)
		}, e.Code)
		switch {
		case e.Message == "":
			msgs[i] = code
		case e.Detail == nil:
			msgs[i] = code + ": " + e.Message
		default:
			msgs[i] = fmt.Sprintf("%s: %s: %v", code, e.Message, e.Detail)
		}
	}
	return strings.Join(msgs, "; ")
}

// maxErrorBytes bounds the error body read from a failed request.
const maxErrorBytes = 8 << 10

// responseError is the error for a non-2xx response.
func responseError(resp *http.Response) error {
	e := &errorResponse{Method: resp.Request.Method, URL: resp.Request.URL, StatusCode: resp.StatusCode}
	var body struct {
		Errors errorDetails `json:"errors"`
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxErrorBytes), &body); err == nil {
		e.Errors = body.Errors
	}
	return e
}

// manifestRequest sends a manifest request for ref (tag or digest).
func (s *Source) manifestRequest(ctx context.Context, r *repository, method, ref string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.url("manifests/"+ref), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	resp, err := s.client.do(req, r)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", ref, errNotFound)
	}
	defer resp.Body.Close()
	return nil, responseError(resp)
}

// resolve returns the digest of the manifest ref (tag or digest) points to from a HEAD; a
// response without Docker-Content-Digest fails unless ref is the digest. As in oras-go, a
// response without a valid Content-Type or a Content-Length fails too: it is not a manifest
// response deps can trust (head then GETs the manifest and computes the digest).
func (s *Source) resolve(ctx context.Context, r *repository, ref string) (string, error) {
	resp, err := s.manifestRequest(ctx, r, http.MethodHead, ref)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if _, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil {
		return "", fmt.Errorf("HEAD %q: invalid Content-Type: %w", resp.Request.URL, err)
	}
	if resp.ContentLength < 0 {
		return "", fmt.Errorf("HEAD %q: unknown Content-Length", resp.Request.URL)
	}
	dg := resp.Header.Get("Docker-Content-Digest")
	if dg == "" {
		if isDigest(ref) {
			return ref, nil
		}
		return "", fmt.Errorf("HEAD %q: missing Docker-Content-Digest header", resp.Request.URL)
	}
	if _, err := imageref.NewDigester(dg); err != nil {
		return "", fmt.Errorf("HEAD %q: Docker-Content-Digest: %w", resp.Request.URL, err)
	}
	if isDigest(ref) && dg != ref {
		return "", fmt.Errorf("HEAD %q: Docker-Content-Digest %s is not the requested digest", resp.Request.URL, dg)
	}
	return dg, nil
}

// isDigest reports whether ref is a digest rather than a tag (tags cannot contain ':').
func isDigest(ref string) bool { return strings.Contains(ref, ":") }

// fetchManifest GETs the manifest of ref (tag or digest), up to maxMetadataBytes. Its media type
// is the response's Content-Type, else the manifest's own mediaType. Its digest is the registry's
// Docker-Content-Digest, else the sha256 of the body; the body is verified against it and
// against ref when ref is a digest.
func (s *Source) fetchManifest(ctx context.Context, r *repository, ref string) (descriptor, []byte, error) {
	resp, err := s.manifestRequest(ctx, r, http.MethodGet, ref)
	if err != nil {
		return descriptor{}, nil, err
	}
	defer resp.Body.Close()
	b, err := readLimited(resp, maxMetadataBytes)
	if err != nil {
		return descriptor{}, nil, err
	}
	desc := descriptor{Digest: resp.Header.Get("Docker-Content-Digest"), Size: int64(len(b))}
	switch {
	case desc.Digest == "" && !isDigest(ref):
		sum := sha256.Sum256(b)
		desc.Digest = "sha256:" + hex.EncodeToString(sum[:])
	case desc.Digest == "":
		desc.Digest = ref
	case isDigest(ref) && desc.Digest != ref:
		return descriptor{}, nil, fmt.Errorf("GET %q: Docker-Content-Digest %s is not the requested digest", resp.Request.URL, desc.Digest)
	}
	if err := verify(desc.Digest, b); err != nil {
		return descriptor{}, nil, fmt.Errorf("GET %q: %w", resp.Request.URL, err)
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && isManifest(mt) {
		desc.MediaType = mt
	} else {
		var m struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(b, &m)
		desc.MediaType = m.MediaType
	}
	return desc, b, nil
}

// fetch reads the content desc points to: a manifest by digest, else a blob. Its size must be
// at most maxMetadataBytes and match desc, and it is verified against desc's digest.
func (s *Source) fetch(ctx context.Context, r *repository, desc descriptor) ([]byte, error) {
	if _, err := imageref.NewDigester(desc.Digest); err != nil {
		return nil, err
	}
	if desc.Size < 0 || desc.Size > maxMetadataBytes {
		return nil, fmt.Errorf("%s: size %d exceeds %d bytes", desc.Digest, desc.Size, maxMetadataBytes)
	}
	var b []byte
	if isManifest(desc.MediaType) {
		_, mb, err := s.fetchManifest(ctx, r, desc.Digest)
		if err != nil {
			return nil, err
		}
		b = mb
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url("blobs/"+desc.Digest), http.NoBody)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.do(req, r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusNotFound:
			return nil, fmt.Errorf("%s: %w", desc.Digest, errNotFound)
		default:
			return nil, responseError(resp)
		}
		if b, err = readLimited(resp, desc.Size); err != nil {
			return nil, err
		}
		if err := verify(desc.Digest, b); err != nil {
			return nil, fmt.Errorf("GET %q: %w", resp.Request.URL, err)
		}
	}
	if int64(len(b)) != desc.Size {
		return nil, fmt.Errorf("%s: size %d, descriptor says %d", desc.Digest, len(b), desc.Size)
	}
	return b, nil
}

// readLimited reads resp's body of at most limit bytes.
func readLimited(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s %q: content length %d exceeds %d bytes", resp.Request.Method, resp.Request.URL, resp.ContentLength, limit)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", resp.Request.Method, resp.Request.URL, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s %q: body exceeds %d bytes", resp.Request.Method, resp.Request.URL, limit)
	}
	return b, nil
}

// verify checks b against digest.
func verify(digest string, b []byte) error {
	h, err := imageref.NewDigester(digest)
	if err != nil {
		return err
	}
	h.Write(b)
	if got := hex.EncodeToString(h.Sum(nil)); !strings.HasSuffix(digest, ":"+got) {
		return fmt.Errorf("content does not match digest %s", digest)
	}
	return nil
}
