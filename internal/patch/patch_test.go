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
		// An insertion at the start of a replaced span does not overlap it, whichever order
		// the two edits come in.
		{"insert at replaced span", []Edit{{8, 10, "v1", "v3"}, {8, 8, "", "# "}}, "uses: a@# v3\nuses: b@v2\n", ""},
		{"insert at replaced span reversed", []Edit{{8, 8, "", "# "}, {8, 10, "v1", "v3"}}, "uses: a@# v3\nuses: b@v2\n", ""},
		{"none", nil, string(src), ""},
		{"identical", []Edit{{8, 10, "v1", "v3"}, {19, 21, "v2", "v4"}, {8, 10, "v1", "v3"}}, "uses: a@v3\nuses: b@v4\n", ""},
		{"same span differs", []Edit{{8, 10, "v1", "v3"}, {8, 10, "v1", "v4"}}, "", "edits overlap at 8 and 8"},
		{"overlap", []Edit{{6, 10, "a@v1", "x"}, {8, 10, "v1", "y"}}, "", "edits overlap at 6 and 8"},
		{"mismatch", []Edit{{8, 10, "v9", "x"}}, "", `edit at 8: expected "v9", found "v1"`},
		{"past end", []Edit{{20, 30, "v2\n", "x"}}, "", "edit at 20: out of range"},
		{"negative start", []Edit{{-1, 2, "", "x"}}, "", "edit at -1: out of range"},
		{"end before start", []Edit{{8, 7, "", "x"}}, "", "edit at 8: out of range"},
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

// TestApplyOrderIndependent covers two zero-width insertions at the same offset: neither is
// in the span of the other, so both apply, and the sort has to order them by their text
// instead of by the order the managers happened to produce them in.
func TestApplyOrderIndependent(t *testing.T) {
	src := []byte("uses: a@v1\n")
	a, b := Edit{5, 5, "", "a"}, Edit{5, 5, "", "b"}
	const want = "uses:ab a@v1\n"
	for _, edits := range [][]Edit{{a, b}, {b, a}} {
		got, err := Apply(src, edits)
		if err != nil || string(got) != want {
			t.Errorf("Apply(%v) = %q, %v; want %q", edits, got, err, want)
		}
	}
}
