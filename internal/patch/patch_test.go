package patch

import (
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	src := []byte("uses: a@v1\nuses: b@v2\n")
	for _, tc := range []struct {
		name  string
		edits []Edit
		want  string
		err   string
	}{
		{"multi", []Edit{{19, 21, "v2", "v3"}, {8, 10, "v1", "v10"}}, "uses: a@v10\nuses: b@v3\n", ""},
		{"insert", []Edit{{10, 10, "", " # v1"}}, "uses: a@v1 # v1\nuses: b@v2\n", ""},
		{"none", nil, string(src), ""},
		{"overlap", []Edit{{6, 10, "a@v1", "x"}, {8, 10, "v1", "y"}}, "", "edits overlap at 6 and 8"},
		{"mismatch", []Edit{{8, 10, "v9", "x"}}, "", `edit at 8: expected "v9", found "v1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(src, tc.edits)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
