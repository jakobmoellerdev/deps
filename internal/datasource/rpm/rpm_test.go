package rpm

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// testdata/tailscale/repodata is https://pkgs.tailscale.com/stable/fedora/x86_64/repodata
// (repomd.xml and its gzip primary.xml) and testdata/docker/repodata
// https://download.docker.com/linux/fedora/43/x86_64/stable/repodata (zstd primary.xml),
// verbatim, fetched 2026-11-06. tailscale-primary.xml.{xz,bz2} are the tailscale primary.xml
// recompressed with xz and bzip2 (EPEL serves xz).
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	files := http.FileServer(http.Dir("testdata"))
	plain := gunzip(t, "testdata/tailscale/repodata/e0b83e63fb5ec497f31e8732faec634c3841565066205570016adc92975a852f-primary.xml.gz")
	repomd := func(href, base string) string {
		if base != "" {
			base = ` xml:base="` + base + `"`
		}
		return `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
  <data type="filelists"><location href="repodata/filelists.xml.gz"/></data>
  <data type="primary"><location` + base + ` href="` + href + `"/></data>
</repomd>`
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/xz/repodata/repomd.xml":
			io.WriteString(w, repomd("repodata/p.xml.xz", ""))
		case "/xz/repodata/p.xml.xz":
			http.ServeFile(w, r, "testdata/tailscale-primary.xml.xz")
		case "/bz2/repodata/repomd.xml":
			io.WriteString(w, repomd("repodata/p.xml.bz2", ""))
		case "/bz2/repodata/p.xml.bz2":
			http.ServeFile(w, r, "testdata/tailscale-primary.xml.bz2")
		case "/plain/repodata/repomd.xml": // a server decompressing transparently
			io.WriteString(w, repomd("repodata/p.xml.gz", ""))
		case "/plain/repodata/p.xml.gz":
			w.Write(plain)
		case "/mirror/repodata/repomd.xml": // primary on another host via xml:base
			io.WriteString(w, repomd("repodata/p.xml.xz", srv.URL+"/xz"))
		case "/noprimary/repodata/repomd.xml":
			io.WriteString(w, `<repomd><data type="other"><location href="repodata/o.xml.gz"/></data></repomd>`)
		case "/badrepomd/repodata/repomd.xml":
			io.WriteString(w, `<html>`)
		case "/truncated/repodata/repomd.xml":
			io.WriteString(w, repomd("repodata/p.xml", ""))
		case "/truncated/repodata/p.xml":
			w.Write(plain[:len(plain)/2])
		case "/badzstd/repodata/repomd.xml":
			io.WriteString(w, repomd("repodata/p.xml.zst", ""))
		case "/badzstd/repodata/p.xml.zst":
			w.Write([]byte{0x28, 0xb5, 0x2f, 0xfd, 0xff, 0xff})
		case "/badgzip/repodata/repomd.xml":
			io.WriteString(w, repomd("repodata/p.xml.gz", ""))
		case "/badgzip/repodata/p.xml.gz":
			w.Write([]byte{0x1f, 0x8b, 0, 0})
		case "/auth/repodata/repomd.xml":
			if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "s3cret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, repomd("repodata/p.xml.gz", ""))
		case "/auth/repodata/p.xml.gz":
			if _, p, _ := r.BasicAuth(); p != "s3cret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write(plain)
		default:
			files.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil)
	ctx := context.Background()

	// Renovate's registryUrl form (the repodata directory, with or without "/"), and the root.
	for _, src := range []string{srv.URL + "/tailscale/repodata/", srv.URL + "/tailscale/repodata", srv.URL + "/tailscale"} {
		rels, err := s.Releases(ctx, dep.Dep{Name: "tailscale", Source: src}, "")
		if err != nil {
			t.Fatal(err)
		}
		vs := versions(rels)
		if len(vs) != 155 || vs[0] != "0.100.0-107" || vs[len(vs)-1] != "1.98.9-1" {
			t.Errorf("%s: %d versions %v … %v", src, len(vs), vs[0], vs[len(vs)-1])
		}
	}
	if n := hits.Load(); n != 2 { // repomd.xml and primary.xml, once for all three URLs
		t.Errorf("requests = %d, want 2", n)
	}

	// zstd; versions omit the epoch (docker-ce has epoch 3), as Renovate writes them.
	rels, err := s.Releases(ctx, dep.Dep{Name: "docker-ce", Source: srv.URL + "/docker/repodata/"}, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rels)
	if len(vs) != 40 || vs[0] != "28.5.0-1.fc43" || !slices.Contains(vs, "29.0.0-1.fc43") {
		t.Errorf("docker-ce = %v", vs)
	}
	scheme, _ := versioning.Get(Versioning)
	if best, ok := scheme.Best("28.5.0-1.fc43", vs, nil, nil); !ok || best != slices.MaxFunc(vs, scheme.Compare) {
		t.Errorf("Best = %s", best)
	}
	at, err := s.ReleaseTime(ctx, dep.Dep{Name: "docker-ce", Source: srv.URL + "/docker/repodata/"}, "28.5.1-1.fc43")
	if err != nil || !at.Equal(time.Unix(1759927578, 0)) {
		t.Errorf("ReleaseTime = %v, %v", at, err)
	}
	if at, err := s.ReleaseTime(ctx, dep.Dep{Name: "docker-ce", Source: srv.URL + "/docker"}, "1.0-1"); err != nil || !at.IsZero() {
		t.Errorf("ReleaseTime of an unknown version = %v, %v", at, err)
	}

	for _, dir := range []string{"xz", "bz2", "plain", "mirror"} {
		rels, err := s.Releases(ctx, dep.Dep{Name: "tailscale", Source: srv.URL + "/" + dir + "/repodata/"}, "")
		if err != nil || len(rels) != 155 {
			t.Errorf("%s: %d releases, %v", dir, len(rels), err)
		}
	}
}

func TestReleasesMerge(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil)
	// The same package in two repositories (release and updates): versions once, the
	// earliest time.
	d := dep.Dep{Name: "tailscale", Source: srv.URL + "/tailscale/repodata/\n" + srv.URL + "/xz/repodata/ " + srv.URL + "/docker/repodata"}
	rels, err := s.Releases(context.Background(), d, "")
	if err != nil || len(rels) != 155 {
		t.Fatalf("merged: %d releases, %v", len(rels), err)
	}
	at, err := s.ReleaseTime(context.Background(), d, "0.100.0-107")
	if err != nil || !at.Equal(time.Unix(1683854002, 0)) {
		t.Errorf("ReleaseTime = %v, %v", at, err)
	}

	p := &pkg{}
	p.add("1.0-1", time.Time{})
	p.add("1.0-1", time.Unix(20, 0))
	p.add("1.0-1", time.Unix(10, 0))
	p.add("1.0-1", time.Unix(30, 0))
	p.add("1.1-1", time.Unix(5, 0))
	if len(p.rels) != 2 || !p.times[0].Equal(time.Unix(10, 0)) {
		t.Errorf("add = %+v %v", p.rels, p.times)
	}
}

func TestReleasesErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil)
	ctx := context.Background()
	authURL := strings.Replace(srv.URL, "http://", "http://user:s3cret@", 1) + "/auth/repodata/"
	if rels, err := s.Releases(ctx, dep.Dep{Name: "tailscale", Source: authURL}, ""); err != nil || len(rels) != 155 {
		t.Errorf("basic auth: %d releases, %v", len(rels), err)
	}
	wrongAuth := strings.Replace(srv.URL, "http://", "http://user:wrong@", 1) + "/auth/repodata/"
	for _, tt := range []struct{ name, src, want string }{
		{"tailscale", "", "rpm package tailscale: no repository: set registryUrl"},
		{"nope", srv.URL + "/tailscale/repodata/", "rpm package nope not found in " + srv.URL + "/tailscale/repodata/"},
		{"x", srv.URL + "/missing/repodata/", "/missing/repodata/repomd.xml: HTTP 404"},
		{"x", srv.URL + "/noprimary", "/noprimary/repodata/repomd.xml: no primary metadata"},
		{"x", srv.URL + "/badrepomd", "/badrepomd/repodata/repomd.xml: malformed metadata"},
		{"x", srv.URL + "/truncated", "/truncated/repodata/p.xml: malformed metadata: unexpected EOF"},
		{"x", srv.URL + "/badgzip", "/badgzip/repodata/p.xml.gz: malformed metadata"},
		{"x", srv.URL + "/badzstd", "/badzstd/repodata/p.xml.zst: malformed metadata"},
		{"x", wrongAuth, "http://user:xxxxx@"},
		{"x", "http://[::1", "invalid URL"},
	} {
		_, err := s.Releases(ctx, dep.Dep{Name: tt.name, Source: tt.src}, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q: err = %v, want %q", tt.name, tt.src, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") || err != nil && strings.Contains(err.Error(), "wrong") {
			t.Errorf("credentials leaked: %v", err)
		}
	}
	if _, err := s.ReleaseTime(ctx, dep.Dep{Name: "x"}, "1"); err == nil {
		t.Error("ReleaseTime without repository")
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: "x"}, "1"); err == nil {
		t.Error("Digest is unsupported")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(srv.Client(), make(chan struct{})).Releases(canceled, dep.Dep{Name: "x", Source: srv.URL + "/x"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

func TestReleasesConcurrent(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), make(chan struct{}, 2))
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.Releases(context.Background(), dep.Dep{Name: "docker-ce-cli", Source: srv.URL + "/docker/repodata/"}, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestScanPrimary(t *testing.T) {
	type entry struct {
		name, version string
		t             int64
	}
	for _, tt := range []struct {
		in   string
		want []entry
		err  bool
	}{
		{in: `<?xml version="1.0"?><!-- c --><metadata packages="4">` +
			`<package type="rpm"><name> a&amp;b </name><version epoch="1" ver='1.0' rel="2.el9"/><time file="7" build="3"/></package>` +
			`<package type="rpm"><name>noRel</name><version  ver = "2.0" /></package>` +
			`<package type="rpm"><name>emptyRel</name><version ver="3.0" rel=""/><time file="x"/></package>` +
			`<package type="rpm"><name>noVer</name><version epoch="0"/></package>` +
			`<package type="rpm"><name/><version ver="9"/></package>` +
			`<package type="other"><name>skip</name><version ver="1"/></package>` +
			`<package type="rpm"><name>esc</name><version ver="1&#46;0" rel="a&amp;b"/>` +
			`<format><rpm:entry name="ignored" ver="9" rel="9"/><file>/x</file></format></package>` +
			`</metadata>`,
			want: []entry{{"a&b", "1.0-2.el9", 7}, {"noRel", "2.0", 0}, {"emptyRel", "3.0", 0}, {"esc", "1.0-a&b", 0}}},
		{in: "", err: true},
		{in: "<metadata><package type=\"rpm\"><name>a", err: true},
		{in: "<metadata><package type=\"rpm\"><name>a</name><version ver=\"1\"/></package>", err: true},
		{in: "<metadata><package type=\"rpm\"><name>a</name><version ver=\"1", err: true},
		{in: "<metadata><desc>" + strings.Repeat("x", 200<<10) + "</desc><package type=\"rpm\"><name>" + strings.Repeat("n", 100<<10) + "</name><version ver=\"1\"/></package></metadata>",
			want: []entry{{strings.Repeat("n", 100<<10), "1", 0}}},
	} {
		var got []entry
		err := scanPrimary(strings.NewReader(tt.in), func(name []byte, v string, at time.Time) {
			var u int64
			if !at.IsZero() {
				u = at.Unix()
			}
			got = append(got, entry{string(name), v, u})
		})
		if (err != nil) != tt.err || !tt.err && !slices.Equal(got, tt.want) {
			t.Errorf("scanPrimary(%.60q) = %v, %v; want %v (err %v)", tt.in, got, err, tt.want, tt.err)
		}
	}
	if v, ok := attrOK([]byte(`version ver="1" rel="2`), "rel"); ok || v != "" {
		t.Errorf("unterminated attribute = %q, %v", v, ok)
	}
	if v := attr([]byte(`version xver="1" ver`), "ver"); v != "" {
		t.Errorf("attribute without value = %q", v)
	}
	if got := string(element([]byte("/package"))); got != "/package" {
		t.Errorf("element = %q", got)
	}
}

func TestDecompressPlainShort(t *testing.T) {
	r, closeFn, err := decompress(bytes.NewReader([]byte("<")))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if b, _ := io.ReadAll(r); string(b) != "<" {
		t.Errorf("plain = %q", b)
	}
	for _, magic := range [][]byte{xzMagic} { // zstd fails on read, as badzstd shows
		if _, _, err := decompress(bytes.NewReader(append(slices.Clone(magic), 0xff, 0xff))); err == nil {
			t.Errorf("corrupt %x: no error", magic)
		}
	}
}

func TestResolve(t *testing.T) {
	for _, tt := range []struct{ base, xmlBase, href, want string }{
		{"https://h/r/", "", "repodata/p.xml.gz", "https://h/r/repodata/p.xml.gz"},
		{"https://h/r/", "https://m/x", "repodata/p.xml.gz", "https://m/x/repodata/p.xml.gz"},
		{"https://h/r/", "", "https://cdn/p.xml.gz", "https://cdn/p.xml.gz"},
	} {
		if got, err := resolve(tt.base, tt.xmlBase, tt.href); err != nil || got != tt.want {
			t.Errorf("resolve(%s, %s, %s) = %s, %v", tt.base, tt.xmlBase, tt.href, got, err)
		}
	}
	if _, err := resolve("https://h/r/", "", "%zz"); err == nil {
		t.Error("invalid href")
	}
	if _, err := resolve("https://h/r/", "http://[::1", "x"); err == nil {
		t.Error("invalid xml:base")
	}
	if got := redact("%zz://"); got != "<invalid URL>" {
		t.Errorf("redact = %s", got)
	}
}
