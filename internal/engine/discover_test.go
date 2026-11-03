package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// suffixManager matches every file named go.mod; only Match is used by discover.
type suffixManager struct{ manager.Manager }

func (suffixManager) Match(p string) bool { return strings.HasSuffix(p, "go.mod") }

func TestDiscoverIgnorePaths(t *testing.T) {
	files := []string{
		"go.mod", "test/go.mod", "a/test/go.mod", "a/test/deep/go.mod", "a/tests/go.mod", "testx/go.mod",
		"a/testing/go.mod", "examples/x/go.mod", "pkg/__tests__/go.mod", "pkg/__fixtures__/go.mod",
		"web/bower_components/go.mod", "lib/go.mod",
	}
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		ignore []string
		want   []string
	}{
		{"defaults prune dirs at any depth", config.DefaultIgnorePaths, []string{"a/testing/go.mod", "go.mod", "lib/go.mod", "testx/go.mod"}},
		{"explicit empty list scans everything", []string{}, slices.Sorted(slices.Values(files))},
		{"custom dir glob", []string{"a/**"}, []string{"examples/x/go.mod", "go.mod", "lib/go.mod", "pkg/__fixtures__/go.mod", "pkg/__tests__/go.mod", "test/go.mod", "testx/go.mod", "web/bower_components/go.mod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := discover(root, tc.ignore, []manager.Manager{suffixManager{}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got[0], tc.want) {
				t.Errorf("discovered %v, want %v", got[0], tc.want)
			}
		})
	}
}
