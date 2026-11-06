package deb

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ulikunitz/xz"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestParseRegistry(t *testing.T) {
	for raw, want := range map[string][]string{
		"https://deb.debian.org/debian?suite=stable&components=main,contrib&binaryArch=amd64": {
			"https://deb.debian.org/debian/dists/stable/main/binary-amd64",
			"https://deb.debian.org/debian/dists/stable/contrib/binary-amd64",
		},
		"https://deb.debian.org/debian/?release=bookworm&components=main&binaryArch=arm64": {
			"https://deb.debian.org/debian/dists/bookworm/main/binary-arm64",
		},
		"https://a.example/artifactory/debian?suite=bookworm&components=main&binaryArch=amd64&token=x": {
			"https://a.example/artifactory/debian/dists/bookworm/main/binary-amd64?token=x",
		},
		"http://archive.ubuntu.com/ubuntu?suite=noble-updates&components=main,%20universe,&binaryArch=amd64": {
			"http://archive.ubuntu.com/ubuntu/dists/noble-updates/main/binary-amd64",
			"http://archive.ubuntu.com/ubuntu/dists/noble-updates/universe/binary-amd64",
		},
	} {
		got, err := ParseRegistry(raw)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("ParseRegistry(%s) = %q, %v, want %q", raw, got, err, want)
		}
	}
	for raw, want := range map[string]string{
		"deb.debian.org/debian?suite=stable&components=main&binaryArch=amd64":  "want an http(s) URL",
		"https://deb.debian.org/debian?components=main&binaryArch=amd64":       "missing query parameter suite",
		"https://deb.debian.org/debian?suite=stable&binaryArch=amd64":          "missing query parameter components",
		"https://deb.debian.org/debian?suite=stable&components=,&binaryArch=a": "missing query parameter components",
		"https://deb.debian.org/debian?suite=stable&components=main":           "missing query parameter binaryArch",
	} {
		if _, err := ParseRegistry(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseRegistry(%s) = %v, want %q", raw, err, want)
		}
	}
}

// repo serves the testdata indexes, excerpts of real repositories: Debian bookworm main as
// Packages.gz (and .xz, unused), bookworm-security main as Packages.xz only (as
// security.debian.org serves it) and Docker's bookworm stable as plain Packages.
func repo(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	gz := func(b []byte) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Bytes()
	}
	xzc := func(b []byte) []byte {
		var buf bytes.Buffer
		w, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Bytes()
	}
	main, sec := read("bookworm-main-Packages"), read("bookworm-security-main-Packages")
	files := map[string][]byte{
		"/debian/dists/bookworm/main/binary-amd64/Packages.gz":                   gz(main),
		"/debian/dists/bookworm/main/binary-amd64/Packages.xz":                   xzc(main),
		"/debian-security/dists/bookworm-security/main/binary-amd64/Packages.xz": xzc(sec),
		"/linux/debian/dists/bookworm/stable/binary-amd64/Packages":              read("docker-bookworm-stable-Packages"),
		"/broken/dists/s/main/binary-amd64/Packages.gz":                          []byte("this is not a gzip stream"),
		"/crlf/dists/s/main/binary-amd64/Packages":                               []byte("Package: a\r\nVersion: 1.0-1\r\n\r\nVersion: 2.0-1\r\nPackage: a\r\n\r\nPackage: b\nVersion: 3\nPackage: a\nVersion: 1.0-1\n\n\n\nPackage: c\n"),
	}
	var hits sync.Map // path → *atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		if strings.HasPrefix(r.URL.Path, "/private/") {
			if u, p, ok := r.BasicAuth(); !ok || u != "x-access-token" || p != "tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/private")
		}
		if strings.HasPrefix(r.URL.Path, "/err/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
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

func hitCount(hits *sync.Map, path string) int32 {
	n, ok := hits.Load(path)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	srv, hits := repo(t)
	reg := func(path, suite, comps string) string {
		return srv.URL + path + "?suite=" + suite + "&components=" + comps + "&binaryArch=amd64"
	}
	main := reg("/debian", "bookworm", "main")
	sec := reg("/debian-security", "bookworm-security", "main")
	docker := reg("/linux/debian", "bookworm", "stable")
	s := New(srv.Client(), nil, nil)
	deps := []dep.Dep{
		{Datasource: Name, Name: "curl", Source: main + " " + sec},
		{Datasource: Name, Name: "openssl", Source: main + "\n" + sec},
		{Datasource: Name, Name: "git", Source: main},
		{Datasource: Name, Name: "docker-ce-cli", Source: docker},
		{Datasource: Name, Name: "nope", Source: "not a url"},
	}
	s.Plan(deps)
	ctx := context.Background()
	for _, tt := range []struct {
		d    dep.Dep
		want []string
	}{
		{deps[0], []string{"7.88.1-10+deb12u15", "7.88.1-10+deb12u5"}},
		{deps[1], []string{"3.0.20-1~deb12u2", "3.0.22-1~deb12u2"}},
		{deps[2], []string{"1:2.39.5-0+deb12u3"}},
		{deps[3], []string{"5:23.0.0-1~debian.12~bookworm", "5:23.0.1-1~debian.12~bookworm", "5:23.0.2-1~debian.12~bookworm", "5:23.0.3-1~debian.12~bookworm", "5:23.0.4-1~debian.12~bookworm", "5:23.0.5-1~debian.12~bookworm"}},
		// not planned: a scan of its own
		{dep.Dep{Datasource: Name, Name: "tzdata", Source: main}, []string{"2026b-0+deb12u1"}},
	} {
		rels, err := s.Releases(ctx, tt.d, "")
		if err != nil || !slices.Equal(versions(rels), tt.want) {
			t.Errorf("Releases(%s) = %q, %v, want %q", tt.d.Name, versions(rels), err, tt.want)
		}
	}
	if n := hitCount(hits, "/debian/dists/bookworm/main/binary-amd64/Packages.gz"); n != 2 {
		t.Errorf("main index fetched %d times, want 2 (one planned scan, one for tzdata)", n)
	}
	if n := hitCount(hits, "/debian/dists/bookworm/main/binary-amd64/Packages.xz"); n != 0 {
		t.Errorf("main Packages.xz fetched %d times, want 0", n)
	}
	if n := hitCount(hits, "/debian-security/dists/bookworm-security/main/binary-amd64/Packages.gz"); n != 1 {
		t.Errorf("security Packages.gz tried %d times, want 1 (404, then .xz)", n)
	}
	if _, err := s.Releases(ctx, deps[4], ""); err == nil || !strings.Contains(err.Error(), "want an http(s) URL") {
		t.Errorf("invalid registry: %v", err)
	}
	if _, err := s.Releases(ctx, dep.Dep{Datasource: Name, Name: "nope", Source: main}, ""); err == nil || !strings.Contains(err.Error(), "deb package nope not found in "+main) {
		t.Errorf("missing package: %v", err)
	}
}

func TestReleasesErrors(t *testing.T) {
	srv, _ := repo(t)
	reg := func(path string) string { return srv.URL + path + "?suite=s&components=main&binaryArch=amd64" }
	s := New(srv.Client(), nil, nil)
	ctx := context.Background()
	for path, want := range map[string]string{
		"/err":     "HTTP 500",
		"/missing": "no Packages.gz, Packages.xz, Packages",
		"/broken":  "gzip: invalid header",
	} {
		if _, err := s.Releases(ctx, dep.Dep{Name: "a", Source: reg(path)}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
	}
	// A failing registry is skipped when another lists the package.
	rels, err := s.Releases(ctx, dep.Dep{Name: "a", Source: reg("/err") + " " + reg("/crlf")}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0-1", "2.0-1"}) {
		t.Errorf("merged = %q, %v", versions(rels), err)
	}
	if _, err := s.Releases(ctx, dep.Dep{Name: "a", Source: "https://u:secret@127.0.0.1:1/x?suite=s&components=c&binaryArch=a"}, ""); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("unreachable with userinfo: %v", err)
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: "a"}, "1"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestReleasesAuth(t *testing.T) {
	srv, _ := repo(t)
	reg := srv.URL + "/private/linux/debian?suite=bookworm&components=stable&binaryArch=amd64"
	d := dep.Dep{Name: "docker-ce-cli", Source: reg}
	if _, err := New(srv.Client(), nil, nil).Releases(context.Background(), d, ""); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("anonymous: %v", err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	host = host[:strings.LastIndexByte(host, ':')]
	auth := httpx.NewAuth(platform.Info{Host: host}, "tok", func(string) string { return "" })
	if rels, err := New(srv.Client(), nil, auth).Releases(context.Background(), d, ""); err != nil || len(rels) != 6 {
		t.Errorf("forge token: %d, %v", len(rels), err)
	}
	d.Source = strings.Replace(reg, "http://", "http://x-access-token:tok@", 1)
	if rels, err := New(srv.Client(), nil, nil).Releases(context.Background(), d, ""); err != nil || len(rels) != 6 {
		t.Errorf("userinfo: %d, %v", len(rels), err)
	}
}

func TestScanIndexLongLine(t *testing.T) {
	src := "Package: a\nDescription: " + strings.Repeat("x", 200<<10) + "\nVersion: 1.0\n\nPackage: b\nVersion: 2"
	got, err := scanIndex(strings.NewReader(src), map[string]bool{"a": true, "b": true})
	if err != nil || !slices.Equal(got["a"], []string{"1.0"}) || !slices.Equal(got["b"], []string{"2"}) {
		t.Errorf("scanIndex = %v, %v", got, err)
	}
}

func TestDefaultRegistry(t *testing.T) {
	if got := registries(dep.Dep{}); !slices.Equal(got, []string{DefaultRegistry}) {
		t.Errorf("registries = %q", got)
	}
	if _, err := ParseRegistry(DefaultRegistry); err != nil {
		t.Error(err)
	}
}
