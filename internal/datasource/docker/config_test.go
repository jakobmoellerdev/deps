package dockerds

import (
	"strings"
	"testing"
	"time"

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
	if _, err := timer.ReleaseTime(t.Context(), dep.Dep{Name: "Bad Name"}, "1"); err == nil {
		t.Error("invalid image: no error")
	}
}
