package checksum

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

var (
	h1 = strings.Repeat("a", 64)
	h2 = strings.Repeat("b", 64)
	h5 = strings.Repeat("c", 128)
)

type fakeAssets struct{ name, version, asset string }

func (f *fakeAssets) AssetDigest(_ context.Context, name, version, asset string) (string, error) {
	f.name, f.version, f.asset = name, version, asset
	if asset == "none" {
		return "", errors.New("release v1 has no digest for asset none")
	}
	return h1, nil
}

// AssetByDigest knows one asset of release v3.1.4+ocm.6: cosign_checksums.txt with sha256 h2.
func (f *fakeAssets) AssetByDigest(_ context.Context, _, version, sum string) (string, error) {
	if version == "v3.1.4+ocm.6" && sum == h2 {
		return "cosign_checksums.txt", nil
	}
	return "", nil
}

func TestChecksumURL(t *testing.T) {
	var reqs sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := reqs.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		switch r.URL.Path {
		case "/v1.2.0/sums.txt":
			_, _ = w.Write([]byte("# comment\r\n" + strings.ToUpper(h1) + "  tool_1.2.0_linux.tgz\r\n" + h2 + " *tool_1.2.0_darwin.tgz\n" + h5 + "  tool.sha512\n" + h2 + "  tool_1.2.0_linux.tgz\n"))
		case "/bare":
			_, _ = w.Write([]byte("\n  " + h2 + "\n"))
		case "/empty":
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", maxBody+10)))
		case "/biglines":
			_, _ = w.Write([]byte(strings.Repeat(strings.Repeat("y", 99)+"\n", maxBody/100+10)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(srv.Client(), make(chan struct{}, 1), nil, nil)
	ctx := context.Background()
	d := func(url, file string) dep.Dep {
		return dep.Dep{Name: "o/r", ChecksumSource: &dep.ChecksumSource{URL: srv.URL + url, File: file}}
	}
	for _, c := range []struct {
		name string
		d    dep.Dep
		want string
		err  string
	}{
		{"two-space entry, uppercase", d("/v{{version}}/sums.txt", "tool_{{version}}_linux.tgz"), h1, ""},
		{"binary entry", d("/v{{version}}/sums.txt", "tool_{{version}}_darwin.tgz"), h2, ""},
		{"sha512", d("/v{{version}}/sums.txt", "tool.sha512"), h5, ""},
		{"first token", d("/v{{version}}/sums.txt", ""), h1, ""},
		{"bare digest", d("/bare", ""), h2, ""},
		{"missing entry", d("/v{{version}}/sums.txt", "nope_{{version}}"), "", "no checksum for nope_1.2.0 in " + srv.URL + "/v1.2.0/sums.txt"},
		{"empty body", d("/empty", ""), "", "no checksum in " + srv.URL + "/empty"},
		{"404", d("/gone", ""), "", "checksums " + srv.URL + "/gone: HTTP 404"},
		{"too large", d("/big", ""), "", "checksums " + srv.URL + "/big: larger than 1048576 bytes"},
		{"too large lines", d("/biglines", ""), "", "checksums " + srv.URL + "/biglines: larger than 1048576 bytes"},
		{"bad url", dep.Dep{ChecksumSource: &dep.ChecksumSource{URL: "http://[::1"}}, "", "checksums http://[::1: parse"},
		{"no source", dep.Dep{}, "", "dependency has no checksum source"},
		{"empty source", dep.Dep{ChecksumSource: &dep.ChecksumSource{}}, "", "checksum source has neither asset, url nor artifact"},
		{"asset without releases", dep.Dep{Datasource: "github-releases", ChecksumSource: &dep.ChecksumSource{Asset: "a"}}, "", "checksum asset requires the github-releases datasource"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Checksum(ctx, c.d, "1.2.0", "1.2.0")
			if got != c.want || (err == nil) != (c.err == "") || err != nil && !strings.HasPrefix(err.Error(), c.err) {
				t.Errorf("got %q, %v; want %q, %q", got, err, c.want, c.err)
			}
		})
	}
	if n, _ := reqs.Load("/v1.2.0/sums.txt"); n.(*atomic.Int32).Load() != 1 {
		t.Errorf("sums.txt fetched %d times, want 1", n.(*atomic.Int32).Load())
	}
}

func TestChecksumConcurrentDedupe(t *testing.T) {
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		_, _ = w.Write([]byte(h1 + "  f\n"))
	}))
	defer srv.Close()
	s := New(srv.Client(), nil, nil, nil)
	d := dep.Dep{ChecksumSource: &dep.ChecksumSource{URL: srv.URL, File: "f"}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := s.Checksum(context.Background(), d, "1", "1"); got != h1 || err != nil {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
	wg.Wait()
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1", reqs.Load())
	}
}

func TestChecksumAsset(t *testing.T) {
	a := &fakeAssets{}
	s := New(http.DefaultClient, nil, a, nil)
	d := dep.Dep{Datasource: "github-releases", Name: "cli/cli", ChecksumSource: &dep.ChecksumSource{Asset: "gh_{{version}}_linux_amd64.tar.gz"}}
	got, err := s.Checksum(context.Background(), d, "2.61.0", "2.61.0")
	if got != h1 || err != nil || *a != (fakeAssets{"cli/cli", "2.61.0", "gh_2.61.0_linux_amd64.tar.gz"}) {
		t.Errorf("got %q, %v, %+v", got, err, *a)
	}
	d.ChecksumSource.Asset = "none"
	if _, err := s.Checksum(context.Background(), d, "1", "1"); err == nil || err.Error() != "release v1 has no digest for asset none" {
		t.Errorf("missing: %v", err)
	}
	d.Datasource = "git-tags"
	if _, err := s.Checksum(context.Background(), d, "1", "1"); err == nil {
		t.Error("asset with git-tags: no error")
	}
}

func TestChecksumCanceled(t *testing.T) {
	s := New(http.DefaultClient, make(chan struct{}), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := dep.Dep{ChecksumSource: &dep.ChecksumSource{URL: "http://127.0.0.1:1/x"}}
	if _, err := s.Checksum(ctx, d, "1", "1"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	s2 := New(http.DefaultClient, nil, nil, nil)
	if _, err := s2.Checksum(context.Background(), d, "1", "1"); err == nil {
		t.Error("connection refused: no error")
	}
}

func TestChecksumArtifact(t *testing.T) {
	const body = "artifact 1.2.0\n"
	s256 := sha256.Sum256([]byte(body))
	s512 := sha512.Sum512([]byte(body))
	s224 := sha256.Sum224([]byte(body))
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch r.URL.Path {
		case "/a/1.2.0/a-1.2.0.zip":
			_, _ = w.Write([]byte(body))
		case "/big":
			chunk := make([]byte, 1<<20)
			for range maxArtifact>>20 + 1 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(srv.Client(), make(chan struct{}, 1), nil, map[string]ArtifactOpener{"other": &fakeOpener{}})
	art := func(path, pinned string) dep.Dep {
		return dep.Dep{Checksum: pinned, ChecksumSource: &dep.ChecksumSource{Artifact: srv.URL + path}}
	}
	for _, c := range []struct {
		name string
		d    dep.Dep
		want string
		err  string
	}{
		{"sha256", art("/a/{{version}}/a-{{version}}.zip", h1), hex.EncodeToString(s256[:]), ""},
		{"sha256 cached", art("/a/{{version}}/a-{{version}}.zip", h2), hex.EncodeToString(s256[:]), ""},
		{"sha512", art("/a/{{version}}/a-{{version}}.zip", h5), hex.EncodeToString(s512[:]), ""},
		{"sha224", art("/a/{{version}}/a-{{version}}.zip", strings.Repeat("c", 56)), hex.EncodeToString(s224[:]), ""},
		{"unknown length", art("/a/{{version}}/a-{{version}}.zip", "abc"), "", "artifact " + srv.URL + "/a/1.2.0/a-1.2.0.zip: pinned checksum is neither sha224, sha256 nor sha512 hex"},
		{"404", art("/missing", h1), "", "artifact " + srv.URL + "/missing: HTTP 404"},
		{"too large", art("/big", h1), "", "artifact " + srv.URL + "/big: larger than 268435456 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Checksum(t.Context(), c.d, "1.2.0", "1.2.0")
			if got != c.want || (err == nil) != (c.err == "") || err != nil && err.Error() != c.err {
				t.Errorf("got %q, %v; want %q, %q", got, err, c.want, c.err)
			}
		})
	}
	if n := reqs.Load(); n != 5 {
		t.Errorf("requests = %d, want 5 (each artifact once per algorithm)", n)
	}
}

// fakeOpener serves body for every URL ending in .zip and records its calls.
type fakeOpener struct {
	body   string
	calls  atomic.Int32
	closed atomic.Int32
	last   atomic.Value // "<dep name> <url>"
}

func (f *fakeOpener) OpenArtifact(_ context.Context, d dep.Dep, url string) (io.ReadCloser, error) {
	f.calls.Add(1)
	f.last.Store(d.Name + " " + url)
	if !strings.HasSuffix(url, ".zip") {
		return nil, errors.New("HTTP 401")
	}
	return closer{strings.NewReader(f.body), &f.closed}, nil
}

type closer struct {
	io.Reader
	n *atomic.Int32
}

func (c closer) Close() error { c.n.Add(1); return nil }

func TestChecksumArtifactOpener(t *testing.T) {
	o := &fakeOpener{body: "maven 3.9.15\n"}
	want := sha256.Sum256([]byte(o.body))
	// The client must not be used for deps of a datasource with an opener.
	s := New(&http.Client{Transport: failTransport{t}}, make(chan struct{}, 1), nil, map[string]ArtifactOpener{"maven": o})
	d := dep.Dep{Datasource: "maven", Name: "org.apache.maven:apache-maven", Checksum: h1,
		ChecksumSource: &dep.ChecksumSource{Artifact: "https://repo.example.com/m/{{version}}/m-{{version}}.zip"}}
	for range 3 {
		got, err := s.Checksum(t.Context(), d, "3.9.15", "3.9.15")
		if err != nil || got != hex.EncodeToString(want[:]) {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if o.calls.Load() != 1 || o.closed.Load() != 1 || o.last.Load() != "org.apache.maven:apache-maven https://repo.example.com/m/3.9.15/m-3.9.15.zip" {
		t.Errorf("calls %d, closed %d, last %v", o.calls.Load(), o.closed.Load(), o.last.Load())
	}
	d.ChecksumSource = &dep.ChecksumSource{Artifact: "https://repo.example.com/m/{{version}}/m.jar"}
	if _, err := s.Checksum(t.Context(), d, "1", "1"); err == nil || err.Error() != "artifact https://repo.example.com/m/1/m.jar: HTTP 401" {
		t.Errorf("err = %v", err)
	}
	if _, err := s.Checksum(t.Context(), d, "2", "2"); err == nil || err.Error() != "artifact https://repo.example.com/m/2/m.jar: HTTP 401" {
		t.Errorf("semaphore not released after an opener error: %v", err)
	}
}

type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("client used for %s", r.URL)
	return nil, errors.New("client used")
}

// Renovate's github-release-attachments digest: the asset of the current release with the
// current checksum is looked up, then the same asset of the new release.
func TestChecksumAttachment(t *testing.T) {
	a := &fakeAssets{}
	s := New(http.DefaultClient, nil, a, nil)
	d := dep.Dep{Datasource: "github-releases", Name: "o/cosign", Version: "v3.1.4+ocm.6", Checksum: strings.ToUpper(h2), ChecksumSource: &dep.ChecksumSource{Attachment: true}}
	got, err := s.Checksum(context.Background(), d, "v3.2.0+ocm.1", "v3.2.0+ocm.1")
	if got != h1 || err != nil || *a != (fakeAssets{"o/cosign", "v3.2.0+ocm.1", "cosign_checksums.txt"}) {
		t.Errorf("got %q, %v, %+v", got, err, *a)
	}
	d.Checksum = h1
	if _, err := s.Checksum(context.Background(), d, "v3.2.0+ocm.1", "v3.2.0+ocm.1"); err == nil || err.Error() != "no asset of release v3.1.4+ocm.6 has sha256 "+h1 {
		t.Errorf("unknown digest: %v", err)
	}
}

func TestRenameAsset(t *testing.T) {
	for _, tt := range []struct{ asset, cur, next, want string }{
		{"cosign_checksums.txt", "v3.1.4", "v3.2.0", "cosign_checksums.txt"},
		{"gh_v2.60.0_linux.tar.gz", "v2.60.0", "v2.61.0", "gh_v2.61.0_linux.tar.gz"},
		{"gh_2.60.0_linux.tar.gz", "v2.60.0", "v2.61.0", "gh_2.61.0_linux.tar.gz"},
	} {
		if got := renameAsset(tt.asset, tt.cur, tt.next); got != tt.want {
			t.Errorf("renameAsset(%q) = %q, want %q", tt.asset, got, tt.want)
		}
	}
}
