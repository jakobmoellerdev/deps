package releasenotes

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkBlockParseTagsOnce renders one block over a cached page of 100 releases.
func BenchmarkBlockParseTagsOnce(b *testing.B) {
	n := New(nil, nil, nil, nil, nil)
	d := dep.Dep{Datasource: dsGitHubReleases, Name: "acme/tool", Source: "https://github.com/acme/tool"}
	r, ok := n.resolve(b.Context(), d, "v1.99.0")
	if !ok {
		b.Fatal("resolve")
	}
	e := &listEntry{}
	e.once.Do(func() {})
	for i := 99; i >= 0; i-- {
		e.rels = append(e.rels, release{tag: fmt.Sprintf("v1.%d.0", i), body: "notes", url: "https://github.com/acme/tool/releases/v1." + strconv.Itoa(i) + ".0"})
	}
	n.lists[strconv.Itoa(int(r.kind))+" "+strings.ToLower(r.web())] = e
	it := item{d: d, from: "v1.90.0", to: "v1.99.0"}
	b.ReportAllocs()
	for b.Loop() {
		if blk := n.block(b.Context(), it); len(blk.entries) != 9 || blk.compare == "" {
			b.Fatalf("block: %d entries, compare %q", len(blk.entries), blk.compare)
		}
	}
}
