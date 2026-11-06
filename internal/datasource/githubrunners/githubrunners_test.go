package githubrunners

import (
	"context"
	"slices"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

var (
	_ datasource.Datasource        = Source{}
	_ datasource.Timer             = Source{}
	_ datasource.DeprecationSource = Source{}
)

func versions(t *testing.T, name string) []string {
	t.Helper()
	rels, err := New().Releases(context.Background(), dep.Dep{Name: name}, "")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleasesDropDeprecated(t *testing.T) {
	ubuntu := versions(t, "ubuntu")
	for _, v := range []string{"26.04", "26.04-arm", "24.04", "24.04-arm", "22.04", "22.04-arm"} {
		if !slices.Contains(ubuntu, v) {
			t.Errorf("ubuntu lacks %s: %v", v, ubuntu)
		}
	}
	for _, v := range []string{"20.04", "18.04", "16.04"} {
		if slices.Contains(ubuntu, v) {
			t.Errorf("ubuntu lists deprecated %s: %v", v, ubuntu)
		}
	}
	if w := versions(t, "windows"); slices.Contains(w, "2019") || !slices.Contains(w, "2025") || !slices.Contains(w, "11-arm") {
		t.Errorf("windows = %v", w)
	}
	if _, err := New().Releases(context.Background(), dep.Dep{Name: "debian"}, ""); err == nil {
		t.Error("unknown runner: want an error")
	}
}

// TestUpdateTargets checks the table against docker versioning: the newest supported image of
// the current one's line (same suffix and precision).
func TestUpdateTargets(t *testing.T) {
	s, ok := versioning.Get("docker")
	if !ok {
		t.Fatal("no docker versioning")
	}
	for _, tt := range []struct{ name, cur, want string }{
		{"ubuntu", "22.04", "26.04"},
		{"ubuntu", "20.04", "26.04"},
		{"ubuntu", "22.04-arm", "26.04-arm"},
		{"macos", "14", "26"},
		{"macos", "13-large", "15-large"},
		{"macos", "14-xlarge", "26-xlarge"},
		{"macos", "15-intel", "26-intel"},
		{"windows", "2019", "2025"},
		{"windows", "11-arm", "11-arm"},
		{"xcode", "27", "27"},
	} {
		best := tt.cur
		for _, v := range versions(t, tt.name) {
			if s.Compatible(tt.cur, v) && s.Compare(v, best) > 0 {
				best = v
			}
		}
		if best != tt.want {
			t.Errorf("%s-%s: newest = %s, want %s", tt.name, tt.cur, best, tt.want)
		}
	}
}

func TestReleaseTime(t *testing.T) {
	ctx := context.Background()
	got, err := New().ReleaseTime(ctx, dep.Dep{Name: "ubuntu"}, "24.04")
	if err != nil || !got.Equal(time.Date(2024, 9, 25, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ubuntu-24.04 = %v, %v", got, err)
	}
	if got, err := New().ReleaseTime(ctx, dep.Dep{Name: "ubuntu"}, "20.04"); err != nil || got.IsZero() {
		t.Errorf("deprecated ubuntu-20.04 = %v, %v; want its date", got, err)
	}
	if got, err := New().ReleaseTime(ctx, dep.Dep{Name: "ubuntu"}, "99.04"); err != nil || !got.IsZero() {
		t.Errorf("unknown = %v, %v", got, err)
	}
}

func TestTableDates(t *testing.T) {
	for name, imgs := range images {
		for _, img := range imgs {
			if _, err := time.Parse(time.DateOnly, img.released); err != nil {
				t.Errorf("%s-%s: %v", name, img.version, err)
			}
		}
	}
}

func TestDeprecation(t *testing.T) {
	s := New()
	if got := s.Deprecation(dep.Dep{Name: "macos"}, "13"); got != "runner image macos-13 deprecated" {
		t.Errorf("macos-13 = %q", got)
	}
	for _, v := range []string{"15", "latest", "99"} {
		if got := s.Deprecation(dep.Dep{Name: "macos"}, v); got != "" {
			t.Errorf("macos-%s = %q, want none", v, got)
		}
	}
}

func TestKnown(t *testing.T) {
	for _, tt := range []struct {
		name, version string
		want          bool
	}{
		{"ubuntu", "24.04", true},
		{"ubuntu", "latest", true},
		{"ubuntu", "20.04", true},
		{"windows", "11-arm", true},
		{"ubuntu", "slim", false},
		{"ubuntu", "24.04-4", false},
		{"ubuntu", "latest-16-core-oss", false},
		{"depot", "ubuntu-24.04-4", false},
		{"Ubuntu", "24.04", false},
	} {
		if got := Known(tt.name, tt.version); got != tt.want {
			t.Errorf("Known(%q, %q) = %v, want %v", tt.name, tt.version, got, tt.want)
		}
	}
}

func TestDigestUnsupported(t *testing.T) {
	if _, err := New().Digest(context.Background(), dep.Dep{Name: "ubuntu"}, "24.04"); err == nil {
		t.Error("want an error")
	}
}
