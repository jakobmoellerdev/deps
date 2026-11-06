package dockerds

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestReleaseTimeAndSourceRepo(t *testing.T) {
	f := newFixture(t)
	imgTime := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	f.reg.tag("img", "3.0", f.reg.image(t, imgTime, map[string]string{sourceLabel: "https://github.com/o/img"}))
	// An index whose first entry is an attestation manifest (platform unknown/unknown).
	att := withPlatform(f.reg.image(t, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), map[string]string{sourceLabel: "attestation"}), "unknown", "unknown")
	amd := withPlatform(f.reg.image(t, imgTime.Add(time.Hour), nil), "linux", "amd64")
	f.reg.tag("img", "4.0", f.reg.index(t, att, amd))
	f.reg.tag("img", "5.0", f.reg.index(t, att))
	// A Helm chart: no image config, so the manifest annotations answer without a config GET.
	chartCfg := f.reg.put(t, "application/vnd.cncf.helm.config.v1+json", map[string]string{"name": "img", "version": "6.0"})
	f.reg.tag("img", "6.0", f.reg.put(t, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: chartCfg, Layers: []ocispec.Descriptor{},
		Annotations: map[string]string{ocispec.AnnotationCreated: imgTime.Add(2 * time.Hour).Format(time.RFC3339), sourceLabel: "https://github.com/o/chart"},
	}))
	// An image config without created falls back to the manifest annotation.
	noCreated := f.reg.put(t, ocispec.MediaTypeImageConfig, ocispec.Image{OS: "linux", Architecture: "amd64", RootFS: ocispec.RootFS{Type: "layers"}})
	f.reg.tag("img", "7.0", f.reg.put(t, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: noCreated, Layers: []ocispec.Descriptor{},
		Annotations: map[string]string{ocispec.AnnotationCreated: imgTime.Add(3 * time.Hour).Format(time.RFC3339)},
	}))
	// Helm charts without the created annotation (as bitnamicharts push them) are dated by the
	// first entry of their chart archive; an unreadable archive or an epoch stamp is unknown.
	chartLayer := func(b []byte) []ocispec.Descriptor {
		return []ocispec.Descriptor{f.reg.putRaw(mediaTypeHelmChart, b)}
	}
	for tag, layers := range map[string][]ocispec.Descriptor{
		"6.1": chartLayer(chartArchive(t, imgTime.Add(4*time.Hour))),
		"6.2": chartLayer([]byte("not gzip")),
		"6.3": chartLayer(chartArchive(t, time.Unix(0, 0))),
		"6.4": {},
	} {
		f.reg.tag("img", tag, f.reg.put(t, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			SchemaVersion: 2, MediaType: ocispec.MediaTypeImageManifest, Config: chartCfg, Layers: layers,
		}))
	}

	s := newSource(f.srv.Client(), make(chan struct{}, 1), anon)
	var (
		timer  datasource.Timer      = s
		repoer datasource.SourceRepo = s
	)
	d := dep.Dep{Name: f.host + "/img"}
	tests := []struct {
		version, wantSource, wantErr string
		want                         time.Time
	}{
		{version: "3.0", want: imgTime, wantSource: "https://github.com/o/img"},
		{version: "4.0", want: imgTime.Add(time.Hour)},
		{version: "5.0", wantErr: "index has no platform image"},
		{version: "6.0", want: imgTime.Add(2 * time.Hour), wantSource: "https://github.com/o/chart"},
		{version: "6.1", want: imgTime.Add(4 * time.Hour)},
		{version: "6.2"},
		{version: "6.3"},
		{version: "6.4"},
		{version: "7.0", want: imgTime.Add(3 * time.Hour)},
		{version: "9.9", wantErr: "image config " + f.host + "/img:9.9"},
	}
	for _, tt := range tests {
		for range 2 { // deduped per (repository, tag)
			got, err := timer.ReleaseTime(t.Context(), d, tt.version)
			src, srcErr := repoer.SourceRepo(t.Context(), d, tt.version)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || srcErr == nil {
					t.Fatalf("%s: err = %v, %v; want %q", tt.version, err, srcErr, tt.wantErr)
				}
				continue
			}
			if err != nil || !got.Equal(tt.want) || srcErr != nil || src != tt.wantSource {
				t.Fatalf("%s: = %v, %v, %q, %v", tt.version, got, err, src, srcErr)
			}
		}
	}
	if n := f.count("GET /v2/img/manifests/3.0"); n != 1 {
		t.Errorf("manifest GETs for 3.0 = %d, want 1", n)
	}
	if n := f.count("GET /v2/img/blobs/" + chartCfg.Digest.String()); n != 0 {
		t.Errorf("chart config GETs = %d, want 0", n)
	}
	if _, err := timer.ReleaseTime(t.Context(), dep.Dep{Name: "Bad Name"}, "1"); err == nil {
		t.Error("invalid image: no error")
	}
}

// chartArchive returns a chart archive as `helm package` writes it: a gzipped tar whose
// entries are stamped with the packaging time.
func chartArchive(t *testing.T, at time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range []string{"nginx/Chart.yaml", "nginx/values.yaml"} {
		body := []byte("name: nginx\n")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestConfigPullLimited checks that on the pull-limited registry (Docker Hub) configs are
// fetched by the digest of the tag's HEAD, which Digest shares, once per digest: two tags of one
// image cost one GET of each manifest.
func TestConfigPullLimited(t *testing.T) {
	f := newFixture(t)
	imgTime := time.Date(2026, 10, 28, 1, 38, 6, 0, time.UTC)
	amd := withPlatform(f.reg.image(t, imgTime, map[string]string{sourceLabel: "https://github.com/o/img"}), "linux", "amd64")
	idx := f.reg.index(t, amd)
	f.reg.tag("img", "18", idx)
	f.reg.tag("img", "18.6", idx)
	single := f.reg.image(t, imgTime.Add(time.Hour), nil)
	f.reg.tag("img", "17", single)

	s := newSource(f.srv.Client(), nil, anon)
	s.pullLimited = f.host
	d := dep.Dep{Name: f.host + "/img"}
	for _, tt := range []struct {
		version, source string
		want            time.Time
	}{
		{"18", "https://github.com/o/img", imgTime},
		{"18.6", "https://github.com/o/img", imgTime},
		{"17", "", imgTime.Add(time.Hour)},
	} {
		got, err := s.ReleaseTime(t.Context(), d, tt.version)
		src, srcErr := s.SourceRepo(t.Context(), d, tt.version)
		if err != nil || !got.Equal(tt.want) || srcErr != nil || src != tt.source {
			t.Errorf("%s: = %v, %v, %q, %v; want %v, %q", tt.version, got, err, src, srcErr, tt.want, tt.source)
		}
	}
	if dg, err := s.Digest(t.Context(), d, "18"); err != nil || dg != idx.Digest.String() {
		t.Errorf("Digest(18) = %q, %v", dg, err)
	}
	for key, want := range map[string]int{
		"HEAD /v2/img/manifests/18":                       1, // shared by config and Digest
		"HEAD /v2/img/manifests/18.6":                     1,
		"HEAD /v2/img/manifests/17":                       1,
		"GET /v2/img/manifests/18":                        0,
		"GET /v2/img/manifests/18.6":                      0,
		"GET /v2/img/manifests/17":                        0,
		"GET /v2/img/manifests/" + idx.Digest.String():    1, // 18 and 18.6 share one pull
		"GET /v2/img/manifests/" + amd.Digest.String():    1,
		"GET /v2/img/manifests/" + single.Digest.String(): 1,
	} {
		if n := f.count(key); n != want {
			t.Errorf("%s: %d requests, want %d", key, n, want)
		}
	}
	if _, err := s.ReleaseTime(t.Context(), d, "9.9"); err == nil || err.Error() != "image config "+f.host+"/img:9.9: tag 9.9 not found upstream" {
		t.Errorf("missing tag err = %v", err)
	}
}
