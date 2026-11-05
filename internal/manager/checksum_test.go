package manager

import (
	"reflect"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestIsSHA256(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("aB3", 21) + "f": true,
		strings.Repeat("a", 63):         false,
		strings.Repeat("g", 64):         false,
		strings.Repeat("a", 64) + "\n":  false,
		"":                              false,
	} {
		if got := IsSHA256(s); got != want {
			t.Errorf("IsSHA256(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestChecksumEdits(t *testing.T) {
	oldSum, newSum := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pinned := dep.Dep{Version: "1", RefSpan: dep.Span{Start: 0, End: 1}, Checksum: oldSum, ChecksumSpan: dep.Span{Start: 2, End: 66}}
	version := patch.Edit{Start: 0, End: 1, Old: "1", New: "2"}
	sum := patch.Edit{Start: 2, End: 66, Old: oldSum, New: newSum}
	for _, tc := range []struct {
		name string
		u    dep.Update
		want []patch.Edit
		err  string
	}{
		{name: "version and checksum", u: dep.Update{Dep: pinned, NewVersion: "2", NewChecksum: newSum}, want: []patch.Edit{version, sum}},
		{name: "unchanged checksum", u: dep.Update{Dep: pinned, NewVersion: "2", NewChecksum: oldSum}, want: []patch.Edit{version}},
		{name: "same version without checksum", u: dep.Update{Dep: pinned, NewVersion: "1"}},
		{name: "no checksum pinned", u: dep.Update{Dep: dep.Dep{Version: "1"}, NewVersion: "2"}, want: []patch.Edit{version}},
		{name: "missing checksum", u: dep.Update{Dep: pinned, NewVersion: "2"},
			err: "checksum must be updated together with the version, but no checksum was resolved"},
		{name: "no checksum span", u: dep.Update{Dep: dep.Dep{Version: "1"}, NewVersion: "2", NewChecksum: newSum},
			err: "dependency has no checksum to update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var edits []patch.Edit
			if tc.u.NewVersion != tc.u.Dep.Version {
				edits = []patch.Edit{version}
			}
			got, err := ChecksumEdits(edits, tc.u)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err || got != nil {
					t.Fatalf("got %v, %v; want error %q", got, err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
