package dockerds

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// configImage returns a random image whose config has created and the source label.
func configImage(t *testing.T, created time.Time, source string) v1.Image {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.Created = v1.Time{Time: created}
	if source != "" {
		cf.Config.Labels = map[string]string{sourceLabel: source}
	}
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		t.Fatal(err)
	}
	return img
}

func TestReleaseTimeAndSourceRepo(t *testing.T) {
	f := newFixture(t)
	repo, err := name.NewRepository(f.host + "/img")
	if err != nil {
		t.Fatal(err)
	}
	imgTime := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := remote.Write(repo.Tag("3.0"), configImage(t, imgTime, "https://github.com/o/img")); err != nil {
		t.Fatal(err)
	}
	// An index whose first entry is an attestation manifest (platform unknown/unknown).
	att := configImage(t, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), "attestation")
	amd := configImage(t, imgTime.Add(time.Hour), "")
	idx := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex),
		mutate.IndexAddendum{Add: att, MediaType: types.OCIManifestSchema1, Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"}},
		mutate.IndexAddendum{Add: amd, MediaType: types.OCIManifestSchema1, Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}},
	)
	if err := remote.WriteIndex(repo.Tag("4.0"), idx); err != nil {
		t.Fatal(err)
	}
	onlyAtt := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: att, MediaType: types.OCIManifestSchema1, Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"}})
	if err := remote.WriteIndex(repo.Tag("5.0"), onlyAtt); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.hits = map[string]int{} })

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
