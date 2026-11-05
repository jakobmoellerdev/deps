package dockerds

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oras-project/oras-go/v3/errdef"
	"github.com/oras-project/oras-go/v3/registry/remote/errcode"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// gcrTagList is a tags/list page as gcr.io and Artifact Registry send it: members sorted, the
// `manifest` map before `tags`, times as strings in milliseconds. 1.0 is an image with both
// times, 2.0/latest an index (timeCreatedMs 0), 3.0 has Go's zero time as creation, 4.0 numeric
// times, an untagged entry is skipped, and 5.0 is missing from the map.
const gcrTagList = `{"child":[],"manifest":{` +
	`"sha256:1111":{"imageSizeBytes":"10","layerId":"","mediaType":"application/vnd.docker.distribution.manifest.v2+json","tag":["1.0"],"timeCreatedMs":"1767225600000","timeUploadedMs":"1767229200000"},` +
	`"sha256:2222":{"imageSizeBytes":"0","mediaType":"application/vnd.oci.image.index.v1+json","tag":["2.0","latest"],"timeCreatedMs":"0","timeUploadedMs":"1767312000000"},` +
	`"sha256:3333":{"mediaType":"application/vnd.oci.image.manifest.v1+json","tag":["3.0"],"timeCreatedMs":"-62135596800000","timeUploadedMs":"1767398400000"},` +
	`"sha256:4444":{"tag":["4.0"],"timeCreatedMs":1767484800000,"timeUploadedMs":1767488400000},` +
	`"sha256:9999":{"tag":[],"timeCreatedMs":"1","timeUploadedMs":"2"}` +
	`},"name":"img","tags":["1.0","2.0","3.0","4.0","5.0","latest"]}`

// TestListedDigestsAndTimes checks that a tags/list page with a `manifest` map answers Releases
// digests, Digest and ReleaseTime without manifest or blob requests; only the tag missing from
// the map is looked up.
func TestListedDigestsAndTimes(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/v2/":
		case "/v2/img/tags/list":
			fmt.Fprint(w, gcrTagList)
		default:
			registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		}
	}))
	t.Cleanup(srv.Close)
	count := func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[key]
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	s := newSource(srv.Client(), nil, anon)
	d := dep.Dep{Name: host + "/img"}

	// Digest before any listing does not list: the engine resolves non-version tags this way.
	if _, err := s.Digest(t.Context(), d, "latest"); err == nil {
		t.Error("Digest(latest) before listing: want the HEAD's 404")
	}
	if n := count("GET /v2/img/tags/list"); n != 0 {
		t.Errorf("Digest listed tags: %d requests", n)
	}

	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rels {
		got = append(got, r.Version+"@"+r.Digest)
	}
	if want := "1.0@sha256:1111,2.0@sha256:2222,3.0@sha256:3333,4.0@sha256:4444,5.0@,latest@sha256:2222"; strings.Join(got, ",") != want {
		t.Errorf("releases = %v, want %s", got, want)
	}

	ms := func(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
	for _, c := range []struct {
		version, digest string
		time            time.Time
	}{
		{"1.0", "sha256:1111", ms(1767225600000)}, // created
		{"2.0", "sha256:2222", ms(1767312000000)}, // index: uploaded
		{"latest", "sha256:2222", ms(1767312000000)},
		{"3.0", "sha256:3333", ms(1767398400000)}, // zero creation time: uploaded
		{"4.0", "sha256:4444", ms(1767484800000)}, // numeric times
	} {
		if got, err := s.Digest(t.Context(), d, c.version); err != nil || got != c.digest {
			t.Errorf("Digest(%s) = %q, %v; want %q", c.version, got, err, c.digest)
		}
		if got, err := s.ReleaseTime(t.Context(), d, c.version); err != nil || !got.Equal(c.time) {
			t.Errorf("ReleaseTime(%s) = %v, %v; want %v", c.version, got, err, c.time)
		}
	}
	if n := count("HEAD /v2/img/manifests/latest"); n != 1 {
		t.Errorf("HEAD latest = %d requests, want 1 (before listing only)", n)
	}
	for _, key := range []string{"HEAD /v2/img/manifests/1.0", "GET /v2/img/manifests/1.0", "GET /v2/img/manifests/2.0"} {
		if n := count(key); n != 0 {
			t.Errorf("%s: %d requests, want 0", key, n)
		}
	}

	// 5.0 is not in the map: Digest and ReleaseTime ask the registry as before.
	if _, err := s.Digest(t.Context(), d, "5.0"); err == nil || err.Error() != "tag 5.0 not found upstream" {
		t.Errorf("Digest(5.0) err = %v", err)
	}
	if _, err := s.ReleaseTime(t.Context(), d, "5.0"); err == nil {
		t.Error("ReleaseTime(5.0): want the manifest GET's error")
	}
	if count("HEAD /v2/img/manifests/5.0") != 1 || count("GET /v2/img/manifests/5.0") != 1 {
		t.Errorf("5.0 lookups: HEAD %d, GET %d; want 1 each", count("HEAD /v2/img/manifests/5.0"), count("GET /v2/img/manifests/5.0"))
	}
}

func TestDecodeTagPage(t *testing.T) {
	for _, c := range []struct {
		name, body, tags, meta, err string
	}{
		{"plain", `{"name":"img","tags":["a","b"]}`, "a,b", "", ""},
		{"null tags", `{"name":"img","tags":null}`, "", "", ""},
		{"null manifest", `{"manifest":null,"tags":["a"]}`, "a", "", ""},
		{"null tag list", `{"manifest":{"sha256:1":{"tag":null,"timeUploadedMs":"5"}},"tags":["a"]}`, "a", "", ""},
		{"tags first", `{"tags":["a"],"manifest":{"sha256:1":{"tag":["a"],"timeUploadedMs":"5"}}}`, "a", "a=sha256:1@5", ""},
		{"unknown members", `{"x":{"y":[1,{}]},"manifest":{"sha256:1":{"z":[null],"tag":["a"],"timeCreatedMs":"7"}},"tags":["a"]}`, "a", "a=sha256:1@7", ""},
		{"bad times", `{"manifest":{"sha256:1":{"tag":["a"],"timeCreatedMs":"x1","timeUploadedMs":1.5}}}`, "", "a=sha256:1@0", ""},
		{"not an object", `["a"]`, "", "", "expected {, got ["},
		{"tag not a string", `{"tags":[1]}`, "", "", "tag is number, not a string"},
		{"truncated", `{"tags":["a"`, "", "", "unexpected EOF"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var tags, got []string
			listed := func(tag string, m tagMeta) {
				var ms int64 // zero time: unknown
				if !m.time.IsZero() {
					ms = m.time.UnixMilli()
				}
				got = append(got, fmt.Sprintf("%s=%s@%d", tag, m.digest, ms))
			}
			err := decodeTagPage(strings.NewReader(c.body), func(s string) { tags = append(tags, s) }, listed)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(tags, ",") != c.tags || strings.Join(got, ",") != c.meta {
				t.Errorf("tags = %v, meta = %v; want %s, %s", tags, got, c.tags, c.meta)
			}
		})
	}
}

func TestTagTime(t *testing.T) {
	for _, c := range []struct {
		created, uploaded int64
		want              time.Time
	}{
		{1000, 2000, time.UnixMilli(1000).UTC()},
		{0, 2000, time.UnixMilli(2000).UTC()},
		{-5, 2000, time.UnixMilli(2000).UTC()},
		{0, 0, time.Time{}},
	} {
		if got := tagTime(c.created, c.uploaded); !got.Equal(c.want) {
			t.Errorf("tagTime(%d, %d) = %v, want %v", c.created, c.uploaded, got, c.want)
		}
	}
}

// TestTagListErrors checks error responses keep oras-go's errcode.ErrorResponse and that next
// links to another origin are refused.
func TestTagListErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
		case "/v2/hop/tags/list":
			w.Header().Set("Link", `<https://elsewhere.example/v2/hop/tags/list?last=a>; rel="next"`)
			fmt.Fprint(w, `{"tags":["a"]}`)
		case "/v2/garbled/tags/list":
			w.Header().Set("Link", `https://elsewhere.example/x>`)
			fmt.Fprint(w, `{"tags":["a"]}`)
		default:
			registryError(w, http.StatusNotFound, "NAME_UNKNOWN")
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	s := newSource(srv.Client(), nil, anon)

	_, err := s.Releases(t.Context(), dep.Dep{Name: host + "/missing"}, "")
	var resp *errcode.ErrorResponse
	if !errors.As(err, &resp) || resp.StatusCode != http.StatusNotFound || len(resp.Errors) != 1 || resp.Errors[0].Code != "NAME_UNKNOWN" {
		t.Errorf("missing repo err = %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: host + "/hop"}, ""); err == nil || !strings.Contains(err.Error(), "not the same origin") {
		t.Errorf("cross-origin link err = %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: host + "/garbled"}, ""); err == nil || !strings.Contains(err.Error(), "invalid next link") {
		t.Errorf("garbled link err = %v", err)
	}
}

// TestTagListMaxPages checks a next page beyond repo.TagListMaxPages fails with
// errdef.ErrTooManyPages as with oras-go's Repository.Tags, keeping what was read.
func TestTagListMaxPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last := r.URL.Query().Get("last")
		w.Header().Set("Link", fmt.Sprintf(`<%s/v2/img/tags/list?last=%s1>; rel="next"`, srv.URL, last))
		fmt.Fprintf(w, `{"manifest":{"sha256:%[1]s":{"tag":["%[1]s1"]}},"tags":["%[1]s1"]}`, last)
	}))
	t.Cleanup(srv.Close)
	s := newSource(srv.Client(), nil, anon)
	repo, err := s.repository(strings.TrimPrefix(srv.URL, "http://") + "/img")
	if err != nil {
		t.Fatal(err)
	}
	repo.TagListMaxPages = 2
	var tags []string
	meta, err := s.listTags(t.Context(), repo, func(tag string) { tags = append(tags, tag) })
	if !errors.Is(err, errdef.ErrTooManyPages) || strings.Join(tags, ",") != "1,11" || len(meta) != 2 {
		t.Errorf("tags = %v, meta = %v, err = %v", tags, meta, err)
	}
}

// TestTagListTimeout: a registry that does not answer a tags/list request in time
// (docker.elastic.co takes 45-55 s per page) fails the lookup with the transport's timeout, once
// per repository for all its deps. It stays an error, not a skip: a dep without a lookup yields
// no PR group, and the bot closes obsolete PRs only when a run has no errors, so a transient
// timeout must not make the dep's open PR look obsolete.
func TestTagListTimeout(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 50 * time.Millisecond
	s := newSource(&http.Client{Transport: tr}, nil, anon)
	d := dep.Dep{Name: strings.TrimPrefix(srv.URL, "http://") + "/elasticsearch/elasticsearch"}

	for range 2 {
		_, err := s.Releases(t.Context(), d, "")
		var nerr net.Error
		if !errors.As(err, &nerr) || !nerr.Timeout() || errors.Is(err, datasource.ErrVersionNotFound) {
			t.Errorf("Releases err = %v; want the timeout", err)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("tags/list requests = %d; want 1", n)
	}
}

// BenchmarkReleaseTime measures ReleaseTime of a listed repository's tag the listing has no
// time for (5.0), answered from the cached config fetch: the per-candidate cost after the
// first lookup.
func BenchmarkReleaseTime(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/img/tags/list" {
			fmt.Fprint(w, gcrTagList)
		}
	}))
	b.Cleanup(srv.Close)
	s := newSource(srv.Client(), nil, anon)
	d := dep.Dep{Name: strings.TrimPrefix(srv.URL, "http://") + "/img"}
	if _, err := s.Releases(b.Context(), d, ""); err != nil {
		b.Fatal(err)
	}
	_, want := s.ReleaseTime(b.Context(), d, "5.0")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.ReleaseTime(b.Context(), d, "5.0"); !errors.Is(err, want) {
			b.Fatalf("err = %v, want the cached %v", err, want)
		}
	}
}
