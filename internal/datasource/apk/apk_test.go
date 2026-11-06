package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// archive builds an APKINDEX.tar.gz laid out as apk-tools writes it: a gzip stream holding the
// signature tar member, then one holding DESCRIPTION and APKINDEX, neither tar terminated.
func archive(t *testing.T, apkindex []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	stream := func(files ...string) {
		zw := gzip.NewWriter(&out)
		var tb bytes.Buffer
		tw := tar.NewWriter(&tb)
		for i := 0; i < len(files); i += 2 {
			if err := tw.WriteHeader(&tar.Header{Name: files[i], Mode: 0o644, Size: int64(len(files[i+1]))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(files[i+1])); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Flush(); err != nil { // no Close: apk strips the end-of-archive blocks
			t.Fatal(err)
		}
		if _, err := zw.Write(tb.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stream(".SIGN.RSA.alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub", strings.Repeat("\x01", 512))
	stream("DESCRIPTION", "v3.22.2", "APKINDEX", string(apkindex))
	return out.Bytes()
}

// fixture reads a testdata index (real records of the Alpine repository).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func server(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestReleases(t *testing.T) {
	srv, hits := server(t, map[string][]byte{
		"/alpine/v3.22/main/x86_64/APKINDEX.tar.gz": archive(t, fixture(t, "v3.22-main.APKINDEX")),
		"/alpine/edge/main/x86_64/APKINDEX.tar.gz":  archive(t, fixture(t, "edge-main.APKINDEX")),
	})
	s := New(srv.Client(), nil)
	reg := srv.URL + "/alpine?branch=v3.22&components=main&arch=x86_64 " + srv.URL + "/alpine/edge/main/x86_64"
	d := dep.Dep{Name: "bash", Datasource: Name, Source: reg}
	ctx := context.Background()
	rels, err := s.Releases(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []datasource.Release{{Version: "5.2.37-r0"}, {Version: "5.3.9-r2"}}
	if !slices.Equal(rels, want) {
		t.Errorf("Releases = %v, want %v", rels, want)
	}
	tm, err := s.ReleaseTime(ctx, d, "5.2.37-r0")
	if err != nil || !tm.Equal(time.Unix(1727144961, 0)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if tm, err := s.ReleaseTime(ctx, d, "1.0-r0"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime(unknown) = %v, %v", tm, err)
	}
	// openssl is only in v3.22; indexes are fetched once per run.
	if rels, err := s.Releases(ctx, dep.Dep{Name: "openssl", Source: reg}, ""); err != nil || len(rels) != 1 || rels[0].Version != "3.5.9-r0" {
		t.Errorf("openssl = %v, %v", rels, err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if _, err := s.Releases(ctx, dep.Dep{Name: "nope", Source: reg}, ""); err == nil || !strings.Contains(err.Error(), `package "nope" not found`) {
		t.Errorf("unknown package err = %v", err)
	}
	if _, err := s.Digest(ctx, d, "5.2.37-r0"); err == nil {
		t.Error("Digest: want error")
	}
}

func TestReleasesMergesDuplicates(t *testing.T) {
	idx := []byte("P:x\nV:1.0-r0\nt:200\n\nP:x\nV:1.0-r1\n\n")
	other := []byte("P:x\nV:1.0-r0\nt:100\n\nP:x\nV:1.0-r1\nt:50\n")
	srv, _ := server(t, map[string][]byte{"/a/APKINDEX.tar.gz": archive(t, idx), "/b/APKINDEX.tar.gz": archive(t, other)})
	s := New(srv.Client(), nil)
	d := dep.Dep{Name: "x", Source: srv.URL + "/a " + srv.URL + "/b/APKINDEX.tar.gz"}
	rels, err := s.Releases(context.Background(), d, "")
	if err != nil || !slices.Equal(rels, []datasource.Release{{Version: "1.0-r0"}, {Version: "1.0-r1"}}) {
		t.Fatalf("Releases = %v, %v", rels, err)
	}
	for v, ts := range map[string]int64{"1.0-r0": 100, "1.0-r1": 50} {
		if tm, _ := s.ReleaseTime(context.Background(), d, v); !tm.Equal(time.Unix(ts, 0)) {
			t.Errorf("ReleaseTime(%s) = %v, want %d", v, tm, ts)
		}
	}
}

func TestReleasesErrors(t *testing.T) {
	srv, _ := server(t, map[string][]byte{
		"/plain/APKINDEX.tar.gz": []byte("<html>not a gzip stream</html>"),
		"/noindex/APKINDEX.tar.gz": func() []byte {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			tw := tar.NewWriter(zw)
			_ = tw.WriteHeader(&tar.Header{Name: "DESCRIPTION", Mode: 0o644})
			_ = tw.Close()
			_ = zw.Close()
			return b.Bytes()
		}(),
	})
	s := New(srv.Client(), nil)
	for reg, want := range map[string]string{
		srv.URL + "/missing":                "HTTP 404",
		srv.URL + "/plain":                  "gzip",
		srv.URL + "/noindex":                "no APKINDEX in archive",
		"":                                  "apk needs a registry URL",
		srv.URL + "/x?arch=x86_64&brnch=v3": `unknown parameter "brnch"`,
		srv.URL + "/x?branch=v3.22":         "missing arch parameter",
		"ftp://example.com/alpine":          "invalid apk registry URL",
	} {
		_, err := s.Releases(context.Background(), dep.Dep{Name: "bash", Source: reg}, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Releases(%q) err = %v, want %q", reg, err, want)
		}
	}
}

func TestIndexURLs(t *testing.T) {
	for reg, want := range map[string][]string{
		"https://dl-cdn.alpinelinux.org/alpine?branch=v3.19&components=main,community&arch=x86_64": {
			"https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64/APKINDEX.tar.gz",
			"https://dl-cdn.alpinelinux.org/alpine/v3.19/community/x86_64/APKINDEX.tar.gz",
		},
		"https://packages.wolfi.dev/os?arch=aarch64":                    {"https://packages.wolfi.dev/os/aarch64/APKINDEX.tar.gz"},
		"https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64/":      {"https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64/APKINDEX.tar.gz"},
		"https://example.com/repo/x86_64/APKINDEX.tar.gz":               {"https://example.com/repo/x86_64/APKINDEX.tar.gz"},
		"https://example.com/a?arch=x86_64&components=main\nhttp://b/c": {"https://example.com/a/main/x86_64/APKINDEX.tar.gz", "http://b/c/APKINDEX.tar.gz"},
	} {
		got, err := IndexURLs(reg)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("IndexURLs(%q) = %v, %v, want %v", reg, got, err, want)
		}
	}
}

func TestParseIndex(t *testing.T) {
	idx, err := parseIndex(strings.NewReader("P:a\nV:1-r0\nt:bad\n\n\nC:x\nP:b\nV:2-r0\nnoise\n\nP:c\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 || idx["a"][0].version != "1-r0" || !idx["a"][0].built.IsZero() || idx["b"][0].version != "2-r0" {
		t.Errorf("parseIndex = %v", idx)
	}
}
