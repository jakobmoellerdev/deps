package pypi

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// Hashes returns the sha256 digests (lowercase hex, sorted, unique) of the files of version of
// d.Name, as pip's `--hash` options pin them: from the JSON API
// (`GET <base>/pypi/<name>/<version>/json`, `urls[].digests.sha256`) for PyPI, else from the
// files of the version on the simple index page (JSON `hashes.sha256` or HTML `#sha256=`).
// d's indexes are tried in order (primary, then the extra ones Releases merges) and the
// digests of the first index holding the version win. Files without a sha256 digest are left
// out; a version no index has any file of is an error.
func (s *Source) Hashes(ctx context.Context, d dep.Dep, version string) (hashes []string, err error) {
	idxs, err := s.indexes(d)
	if err != nil {
		return nil, err
	}
	name := pyreq.Normalize(d.Name)
	start := time.Now()
	var from string // index the digests came from, for the log
	defer func() {
		slog.DebugContext(ctx, "pypi hashes fetched", "name", name, "version", version, "index", httpx.Redact(cmp.Or(from, s.base)),
			"hashes", len(hashes), "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	var missing error // the first index without the package, when no index had it
	for _, idx := range idxs {
		var h []string
		if idx == "" {
			h, err = s.jsonHashes(ctx, d.Name, name, version)
		} else {
			h, err = s.simpleHashes(ctx, idx, d.Name, name, version)
		}
		switch {
		case errors.Is(err, errNotFound):
			if missing == nil {
				missing = err
			}
			continue
		case err != nil:
			return nil, err
		case len(h) == 0:
			missing = nil // the package is published here, just not this version
			continue
		}
		slices.Sort(h)
		from, hashes = idx, slices.Compact(h)
		return hashes, nil
	}
	if missing != nil {
		return nil, missing
	}
	return nil, fmt.Errorf("pypi: %s %s has no files with a sha256 digest", d.Name, version)
}

// jsonHashes reads `urls[].digests.sha256` of the JSON API document of one release.
func (s *Source) jsonHashes(ctx context.Context, written, name, version string) ([]string, error) {
	target := s.base + "/pypi/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/json"
	resp, err := s.get(ctx, target, written+" "+version, "application/json")
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	var doc struct {
		URLs []struct {
			Digests struct {
				SHA256 string `json:"sha256"`
			} `json:"digests"`
		} `json:"urls"`
	}
	if err := json.UnmarshalRead(resp.Body, &doc); err != nil {
		return nil, fmt.Errorf("pypi %s: malformed response: %w", httpx.Redact(target), err)
	}
	var out []string
	for _, u := range doc.URLs {
		if h := strings.ToLower(u.Digests.SHA256); validSHA256(h) {
			out = append(out, h)
		}
	}
	return out, nil
}

// simpleHashes reads the sha256 digests of the files of version on the simple index page of
// the project: `hashes.sha256` (JSON, PEP 691) or the `#sha256=` URL fragment (HTML, PEP 503).
func (s *Source) simpleHashes(ctx context.Context, idx, written, name, version string) ([]string, error) {
	target := idx + url.PathEscape(name) + "/"
	resp, err := s.getSimple(ctx, target, written)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	want := versionKey(version)
	var out []string
	add := func(filename, sha256 string) {
		v, ok := fileVersion(filename, name)
		if h := strings.ToLower(sha256); ok && versionKey(v) == want && validSHA256(h) {
			out = append(out, h)
		}
	}
	switch mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt {
	case simpleJSON:
		var doc struct {
			Files []struct {
				Filename string            `json:"filename"`
				Hashes   map[string]string `json:"hashes"`
			} `json:"files"`
		}
		if err := json.UnmarshalRead(resp.Body, &doc); err != nil {
			return nil, fmt.Errorf("pypi %s: malformed response: %w", httpx.Redact(target), err)
		}
		for _, f := range doc.Files {
			add(f.Filename, f.Hashes["sha256"])
		}
	case simpleHTML, "text/html":
		if err := decodeHTMLFiles(resp.Body, resp.Request.URL, func(f simpleFile) { add(f.Filename, f.SHA256) }); err != nil {
			return nil, fmt.Errorf("pypi %s: malformed response: %w", httpx.Redact(target), err)
		}
	default:
		return nil, fmt.Errorf("pypi %s: unexpected content type %q", httpx.Redact(target), resp.Header.Get("Content-Type"))
	}
	return out, nil
}

// validSHA256 reports whether h is 64 lowercase hex digits.
func validSHA256(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range []byte(h) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
