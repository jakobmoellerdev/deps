package main

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// File headers win over added/removed lines; context and the no-newline marker stay plain;
// without colors the diff is unchanged, so `deps plan > x.diff` still applies.
func TestColorDiff(t *testing.T) {
	d := "--- a/go.mod\n+++ b/go.mod\n@@ -1,2 +1,2 @@\n module x\n-require a v1\n+require a v2\n\\ No newline at end of file\n"
	if got := colorDiff(logx.NewStyle(false), d); got != d {
		t.Errorf("plain = %q", got)
	}
	want := "\x1b[1m--- a/go.mod\x1b[0m\n\x1b[1m+++ b/go.mod\x1b[0m\n\x1b[36m@@ -1,2 +1,2 @@\x1b[0m\n module x\n" +
		"\x1b[31m-require a v1\x1b[0m\n\x1b[32m+require a v2\x1b[0m\n\\ No newline at end of file\n"
	if got := colorDiff(logx.NewStyle(true), d); got != want {
		t.Errorf("colored:\n%q\nwant\n%q", got, want)
	}
}
