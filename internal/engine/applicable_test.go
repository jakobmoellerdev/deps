package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BuildApplicable leaves out the updates a post-processor finds inapplicable instead of
// failing the other updates of their files; real errors stay errors.
func TestBuildApplicable(t *testing.T) {
	root, _ := setup(t, "", map[string]string{
		".github/workflows/a.yml": "jobs:\n  a:\n    steps:\n      - uses: a/x@v1\n      - uses: b/y@v1\n      - uses: c/z@v1\n",
	})
	const wf = ".github/workflows/a.yml"
	up := func(name string, line int) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: "github-actions", File: wf, Line: line, Name: name, Version: "v1",
			RefSpan: refSpan(t, root, wf, name+"@v1")}, Type: dep.Major, NewVersion: "v2"}
	}
	ups := []dep.Update{up("a/x", 4), up("b/y", 5), up("c/z", 6)}
	for _, tt := range []struct {
		name    string
		process func(src string) error
		want    string // the patched workflow's uses lines
		skipped []string
		errs    int
	}{
		{"one inapplicable", func(src string) error {
			if strings.Contains(src, "a/x@v2") {
				return &dep.SkipError{Reason: "a/x cannot move"}
			}
			return nil
		}, "a/x@v1 b/y@v2 c/z@v2", []string{"a/x: a/x cannot move"}, 0},
		{"inapplicable together", func(src string) error {
			if strings.Contains(src, "a/x@v2") && strings.Contains(src, "b/y@v2") {
				return &dep.SkipError{Reason: "a/x and b/y conflict"}
			}
			return nil
		}, "a/x@v2 b/y@v1 c/z@v2", []string{"b/y: a/x and b/y conflict"}, 0},
		{"real error", func(src string) error {
			switch {
			case strings.Contains(src, "a/x@v2"):
				return &dep.SkipError{Reason: "a/x cannot move"}
			case strings.Contains(src, "c/z@v2"):
				return errors.New("boom")
			}
			return nil
		}, "", []string{"a/x: a/x cannot move"}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			post := map[string]PostProcessor{"github-actions": postFunc{f: func(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
				return nil, tt.process(string(manifests["a.yml"]))
			}, scope: func(string, string) (string, bool) { return ".github/workflows", true }}}
			changes, skipped, errs := BuildApplicable(t.Context(), root, managers, ups, post)
			var got []string
			for _, s := range skipped {
				got = append(got, s.Update.Dep.Name+": "+s.Reason)
			}
			if !slices.Equal(got, tt.skipped) || len(errs) != tt.errs {
				t.Fatalf("skipped %q, errs %v", got, errs)
			}
			var uses []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					if _, u, ok := strings.Cut(l, "uses: "); ok {
						uses = append(uses, u)
					}
				}
			}
			if strings.Join(uses, " ") != tt.want {
				t.Errorf("uses = %q, want %q", uses, tt.want)
			}
		})
	}
}

// refSpan returns the span of the ref after '@' of the first occurrence of uses in file.
func refSpan(t *testing.T, root, file, uses string) dep.Span {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(b, []byte(uses))
	if i < 0 {
		t.Fatalf("%s not in %s", uses, file)
	}
	at := i + strings.IndexByte(uses, '@') + 1
	return dep.Span{Start: at, End: i + len(uses)}
}
