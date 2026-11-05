package terraform

import (
	"fmt"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// TestLineIncrementalVersionBeforeSource pins dep lines when a block's
// version precedes its source, so the line cache has to move backward.
func TestLineIncrementalVersionBeforeSource(t *testing.T) {
	src := "module \"a\" {\n  version = \"1.0.0\"\n\n  source  = \"hashicorp/consul/aws\"\n}\n\n" +
		"module \"b\" {\n  source  = \"hashicorp/vpc/aws\"\n  version = \"2.0.0\"\n}\n" +
		"terraform {\n  required_providers {\n    aws = {\n      version = \"5.0.0\"\n      source = \"hashicorp/aws\"\n    }\n  }\n}\n"
	deps, err := New().Extract("main.tf", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"1.0.0": 2, "2.0.0": 9, "5.0.0": 13}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps: %+v", len(deps), deps)
	}
	for _, d := range deps {
		if d.Line != want[d.Version] {
			t.Errorf("%s %s: line %d, want %d", d.Name, d.Version, d.Line, want[d.Version])
		}
	}
}

func BenchmarkLineIncrementalModules(b *testing.B) {
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "module \"m%d\" {\n  source  = \"hashicorp/m%d/aws\"\n  version = \"1.%d.0\"\n  name    = \"x\"\n  tags    = { a = \"b\" }\n}\n\n", i, i, i)
	}
	src := []byte(sb.String())
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Extract("main.tf", src, manager.Env{}); err != nil {
			b.Fatal(err)
		}
	}
}
