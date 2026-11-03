package checksum

import (
	"context"
	"errors"
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
	s := New(srv.Client(), make(chan struct{}, 1), nil)
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
		{"empty source", dep.Dep{ChecksumSource: &dep.ChecksumSource{}}, "", "checksum source has neither asset nor url"},
		{"asset without releases", dep.Dep{Datasource: "github-releases", ChecksumSource: &dep.ChecksumSource{Asset: "a"}}, "", "checksum asset requires the github-releases datasource"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Checksum(ctx, c.d, "1.2.0")
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
	s := New(srv.Client(), nil, nil)
	d := dep.Dep{ChecksumSource: &dep.ChecksumSource{URL: srv.URL, File: "f"}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := s.Checksum(context.Background(), d, "1"); got != h1 || err != nil {
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
	s := New(http.DefaultClient, nil, a)
	d := dep.Dep{Datasource: "github-releases", Name: "cli/cli", ChecksumSource: &dep.ChecksumSource{Asset: "gh_{{version}}_linux_amd64.tar.gz"}}
	got, err := s.Checksum(context.Background(), d, "2.61.0")
	if got != h1 || err != nil || *a != (fakeAssets{"cli/cli", "2.61.0", "gh_2.61.0_linux_amd64.tar.gz"}) {
		t.Errorf("got %q, %v, %+v", got, err, *a)
	}
	d.ChecksumSource.Asset = "none"
	if _, err := s.Checksum(context.Background(), d, "1"); err == nil || err.Error() != "release v1 has no digest for asset none" {
		t.Errorf("missing: %v", err)
	}
	d.Datasource = "git-tags"
	if _, err := s.Checksum(context.Background(), d, "1"); err == nil {
		t.Error("asset with git-tags: no error")
	}
}

func TestChecksumCanceled(t *testing.T) {
	s := New(http.DefaultClient, make(chan struct{}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := dep.Dep{ChecksumSource: &dep.ChecksumSource{URL: "http://127.0.0.1:1/x"}}
	if _, err := s.Checksum(ctx, d, "1"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	s2 := New(http.DefaultClient, nil, nil)
	if _, err := s2.Checksum(context.Background(), d, "1"); err == nil {
		t.Error("connection refused: no error")
	}
}
