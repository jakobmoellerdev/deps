package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadProblemLines(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Problem
	}{
		{
			name: "validation",
			src:  "pin: keep\nrules:\n  - manager: gomod\n  - manager: nope\n    schedule:\n      - \"0 5 * * *\"\n      - x\nlockFileMaintenance:\n  enabled: true\n",
			want: []Problem{
				{Line: 4, Msg: `rules[1].manager: must be one of ` + list(Managers) + `, got "nope"`},
				{Line: 7, Msg: `rules[1].schedule[1]: "x": unknown word "x" (want before, after, every, on or a cron)`},
			},
		},
		{
			name: "top-level key",
			src:  "pin: keep\nversion: 2\n",
			want: []Problem{{Line: 2, Msg: "version: must be 1, got 2"}},
		},
		{
			name: "unknown fields",
			src:  "rules:\n  - manager: gomod\n    bogus: 1\nnope: 2\n",
			want: []Problem{{Line: 3, Msg: "field bogus not found in type config.Rule"}, {Line: 4, Msg: "field nope not found in type config.Config"}},
		},
		{
			name: "syntax",
			src:  "rules:\n  - manager: gomod\n   bad: [\n",
			want: nil, // checked below: one problem with a line
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, ".deps.yaml")
			if err := os.WriteFile(p, []byte(tt.src), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir, "")
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v", err)
			}
			if ce.File != p {
				t.Errorf("File = %q, want %q", ce.File, p)
			}
			if tt.want == nil {
				if len(ce.Problems) != 1 || ce.Problems[0].Line == 0 {
					t.Errorf("problems = %+v", ce.Problems)
				}
				return
			}
			// Validation may report more than the problem under test; the tested ones must be there.
			for _, w := range tt.want {
				if !slices.Contains(ce.Problems, w) {
					t.Errorf("problems %+v lack %+v", ce.Problems, w)
				}
			}
		})
	}
}
